package httpkit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"
)

// Platform HTTP server defaults. They are deliberately not tunable per service:
// a service that needs a different write timeout has an endpoint that should be
// asynchronous instead.
const (
	// DefaultReadTimeout bounds reading the request, headers and body. Requests
	// are small commands and queries; a slow client must not hold a connection.
	DefaultReadTimeout = 10 * time.Second
	// DefaultWriteTimeout bounds writing the response, including the handler's
	// own work — the longest legitimate request (a paged report) fits inside it.
	DefaultWriteTimeout = 30 * time.Second
	// DefaultIdleTimeout bounds a kept-alive connection between requests.
	DefaultIdleTimeout = 120 * time.Second
	// DefaultShutdownTimeout bounds graceful shutdown: in-flight requests get
	// this long to finish before the process stops waiting. It is under the
	// Kubernetes default terminationGracePeriodSeconds of 30s, so the pod is
	// never SIGKILLed mid-drain.
	DefaultShutdownTimeout = 20 * time.Second
	// DefaultMaxHeaderBytes caps request headers.
	DefaultMaxHeaderBytes = 1 << 20 // 1 MiB
)

// ServerConfig configures a Server. Only Addr and Handler are required.
type ServerConfig struct {
	// Addr is the listen address, e.g. ":8080" from config.Base.HTTPAddr.
	Addr string
	// Handler serves requests — normally a mux wrapped in the standard chain.
	Handler http.Handler
	// Logger receives lifecycle lines (listening, shutting down). Defaults to
	// slog.Default().
	Logger *slog.Logger
	// ReadTimeout, WriteTimeout, IdleTimeout and ShutdownTimeout override the
	// platform defaults above. Zero means "use the default".
	ReadTimeout     time.Duration
	WriteTimeout    time.Duration
	IdleTimeout     time.Duration
	ShutdownTimeout time.Duration
}

// Server is an http.Server with the platform's timeouts and graceful shutdown.
type Server struct {
	http            *http.Server
	logger          *slog.Logger
	addr            string
	shutdownTimeout time.Duration
}

// NewServer builds a Server. It does not bind a port — Run does, so a failure to
// listen is reported by the call that owns the goroutine.
func NewServer(cfg ServerConfig) (*Server, error) {
	if cfg.Handler == nil {
		return nil, errors.New("building an HTTP server: no handler supplied")
	}
	if cfg.Addr == "" {
		return nil, errors.New("building an HTTP server: no listen address supplied")
	}

	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	readTimeout := orDefault(cfg.ReadTimeout, DefaultReadTimeout)

	return &Server{
		http: &http.Server{
			Addr:        cfg.Addr,
			Handler:     cfg.Handler,
			ReadTimeout: readTimeout,
			// Header reading is bounded separately so a client that opens a
			// connection and never completes its headers cannot hold a slot.
			ReadHeaderTimeout: readTimeout,
			WriteTimeout:      orDefault(cfg.WriteTimeout, DefaultWriteTimeout),
			IdleTimeout:       orDefault(cfg.IdleTimeout, DefaultIdleTimeout),
			MaxHeaderBytes:    DefaultMaxHeaderBytes,
			ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
		},
		logger:          logger,
		addr:            cfg.Addr,
		shutdownTimeout: orDefault(cfg.ShutdownTimeout, DefaultShutdownTimeout),
	}, nil
}

// orDefault substitutes def for a zero duration.
func orDefault(d, def time.Duration) time.Duration {
	if d <= 0 {
		return def
	}
	return d
}

// Run binds the configured address and serves until ctx is cancelled, then
// drains in-flight requests. It returns nil on a clean shutdown.
func (s *Server) Run(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", s.addr, err)
	}
	return s.Serve(ctx, ln)
}

// Serve serves on an already-bound listener until ctx is cancelled, then drains
// in-flight requests. Tests bind 127.0.0.1:0 and pass the listener so they never
// need a fixed port.
//
// Shutdown order matters: cancelling ctx stops accepting new connections and
// lets in-flight requests finish, up to the shutdown timeout. A request still
// running when that expires is closed — the returned error says so, which is
// what a readiness probe and an operator need to see.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	serveErr := make(chan error, 1)
	go func() {
		s.logger.InfoContext(ctx, "http server listening", slog.String("addr", ln.Addr().String()))
		err := s.http.Serve(ln)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		serveErr <- err
	}()

	select {
	case err := <-serveErr:
		if err != nil {
			return fmt.Errorf("serving HTTP on %s: %w", ln.Addr(), err)
		}
		return nil

	case <-ctx.Done():
		s.logger.InfoContext(ctx, "http server shutting down",
			slog.String("addr", ln.Addr().String()),
			slog.Duration("grace", s.shutdownTimeout))

		// A fresh context: ctx is already cancelled, and the drain needs its
		// own deadline.
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.shutdownTimeout)
		defer cancel()

		if err := s.http.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("draining HTTP connections within %s: %w", s.shutdownTimeout, err)
		}
		// Serve always returns once Shutdown completes; collect it so the
		// goroutine cannot outlive this call.
		if err := <-serveErr; err != nil {
			return fmt.Errorf("serving HTTP on %s: %w", ln.Addr(), err)
		}
		return nil
	}
}
