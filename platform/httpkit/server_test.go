package httpkit_test

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/canhtoanptit/collection-platform/platform/httpkit"
)

// listen binds a loopback port the operating system chooses, so tests never
// collide and never need a fixed port.
func listen(t *testing.T) net.Listener {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("binding a test listener: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	return ln
}

func TestNewServerValidatesItsConfig(t *testing.T) {
	tests := []struct {
		name   string
		cfg    httpkit.ServerConfig
		wantIn string
	}{
		{"no handler", httpkit.ServerConfig{Addr: ":8080"}, "no handler"},
		{"no address", httpkit.ServerConfig{Handler: http.NotFoundHandler()}, "no listen address"},
		{"neither", httpkit.ServerConfig{}, "no handler"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv, err := httpkit.NewServer(tc.cfg)
			if err == nil {
				t.Fatal("NewServer accepted an incomplete config")
			}
			if srv != nil {
				t.Error("NewServer returned a server alongside the error")
			}
			if !strings.Contains(err.Error(), tc.wantIn) {
				t.Errorf("error = %q, want it to mention %q", err, tc.wantIn)
			}
		})
	}
}

func TestServerServesAndShutsDownGracefully(t *testing.T) {
	srv, err := httpkit.NewServer(httpkit.ServerConfig{
		Addr:    "127.0.0.1:0",
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") }),
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	ln := listen(t)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, ln) }()

	resp, err := http.Get("http://" + ln.Addr().String() + "/healthz")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if got := string(body); got != "ok" {
		t.Errorf("body = %q, want %q", got, "ok")
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Serve returned %v, want nil on a clean shutdown", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after the context was cancelled")
	}

	// The listener is closed, so a second request must fail.
	if _, err := http.Get("http://" + ln.Addr().String() + "/healthz"); err == nil {
		t.Error("the server still accepted a request after shutdown")
	}
}

// TestServerDrainsInFlightRequests is the property graceful shutdown exists for:
// a request already being served must complete, not be cut off.
func TestServerDrainsInFlightRequests(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})

	srv, err := httpkit.NewServer(httpkit.ServerConfig{
		Addr: "127.0.0.1:0",
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			close(started)
			<-release
			_, _ = io.WriteString(w, "finished")
		}),
		ShutdownTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	ln := listen(t)
	ctx, cancel := context.WithCancel(t.Context())
	serveDone := make(chan error, 1)
	go func() { serveDone <- srv.Serve(ctx, ln) }()

	type result struct {
		body string
		err  error
	}
	requestDone := make(chan result, 1)
	go func() {
		resp, err := http.Get("http://" + ln.Addr().String() + "/v1/slow")
		if err != nil {
			requestDone <- result{err: err}
			return
		}
		defer func() { _ = resp.Body.Close() }()
		b, err := io.ReadAll(resp.Body)
		requestDone <- result{body: string(b), err: err}
	}()

	<-started
	cancel() // shutdown begins while the request is in flight
	close(release)

	select {
	case got := <-requestDone:
		if got.err != nil {
			t.Fatalf("the in-flight request failed during shutdown: %v", got.err)
		}
		if got.body != "finished" {
			t.Errorf("body = %q, want %q — the request was cut off", got.body, "finished")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the in-flight request never completed")
	}

	select {
	case err := <-serveDone:
		if err != nil {
			t.Errorf("Serve returned %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return")
	}
}

// TestServerReportsAStalledDrain: a request that outlives the shutdown timeout
// must produce an error, because "we stopped waiting" is something an operator
// has to see.
func TestServerReportsAStalledDrain(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })

	srv, err := httpkit.NewServer(httpkit.ServerConfig{
		Addr: "127.0.0.1:0",
		Handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			close(started)
			<-release
		}),
		ShutdownTimeout: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	ln := listen(t)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, ln) }()

	go func() {
		resp, err := http.Get("http://" + ln.Addr().String() + "/v1/stuck")
		if err == nil {
			_ = resp.Body.Close()
		}
	}()

	<-started
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Serve returned nil after abandoning an in-flight request")
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("error = %v, want it to wrap context.DeadlineExceeded", err)
		}
		if !strings.Contains(err.Error(), "draining") {
			t.Errorf("error = %q, want it to say the drain timed out", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after the shutdown timeout expired")
	}
}

func TestServerRunReportsABindFailure(t *testing.T) {
	// Hold a port, then ask the server to bind the same one.
	ln := listen(t)

	srv, err := httpkit.NewServer(httpkit.ServerConfig{
		Addr:    ln.Addr().String(),
		Handler: http.NotFoundHandler(),
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	err = srv.Run(t.Context())
	if err == nil {
		t.Fatal("Run bound a port that was already in use")
	}
	if !strings.Contains(err.Error(), "listening on") {
		t.Errorf("error = %q, want it to name the bind failure", err)
	}
}

func TestServerRunBindsTheConfiguredAddress(t *testing.T) {
	srv, err := httpkit.NewServer(httpkit.ServerConfig{
		Addr:    "127.0.0.1:0",
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") }),
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()

	// Port 0 means the kernel picks one, so there is nothing to poll for here
	// beyond the server having started; cancel immediately and assert the clean
	// return, which is what Run adds over Serve.
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run returned %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after the context was cancelled")
	}
}

// TestServerAppliesTheReadTimeout proves the timeouts are wired rather than
// merely documented: a client that opens a connection and sends nothing must be
// dropped.
func TestServerAppliesTheReadTimeout(t *testing.T) {
	srv, err := httpkit.NewServer(httpkit.ServerConfig{
		Addr:        "127.0.0.1:0",
		Handler:     http.NotFoundHandler(),
		ReadTimeout: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	ln := listen(t)
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	go func() { _ = srv.Serve(ctx, ln) }()

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dialling: %v", err)
	}
	defer func() { _ = conn.Close() }()

	// Send a partial request line and never finish it.
	if _, err := conn.Write([]byte("GET /v1/cases HTTP/1.1\r\n")); err != nil {
		t.Fatalf("writing a partial request: %v", err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("setting a read deadline: %v", err)
	}
	if _, err := io.ReadAll(conn); err != nil {
		t.Fatalf("the server did not close the idle connection: %v", err)
	}
}
