// Package health serves the two probes every service exposes: /healthz and
// /readyz (CLAUDE.md §6).
//
// The distinction is the whole point, and getting it backwards causes outages:
//
//   - /healthz is liveness. It answers 200 as long as the process can serve
//     HTTP. It runs no dependency checks, because a database outage must not
//     make Kubernetes restart every pod — that turns a recoverable dependency
//     failure into a crash loop.
//   - /readyz is readiness. It runs every registered probe and answers 503 with
//     the failing names when any of them errors, so the pod is removed from the
//     Service until its dependencies are back.
//
// Probes come from the packages that own the dependency (LIB-B adds
// postgres.ReadyCheck and kafka.ReadyCheck), so this package never learns what
// a database is.
package health

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"sync"
)

// Paths served by Handler.
const (
	LivePath  = "/healthz"
	ReadyPath = "/readyz"
)

// Status values in the JSON body.
const (
	statusOK          = "ok"
	statusUnavailable = "unavailable"
)

// Check is one readiness dependency.
type Check struct {
	// Name identifies the dependency in the 503 body: "postgres", "kafka",
	// "model-provider". It is the only thing a failure exposes to the caller.
	Name string
	// Probe reports whether the dependency is usable. It must be cheap and
	// bounded — it runs on every readiness poll — and must respect ctx, which
	// carries the request's deadline.
	Probe func(ctx context.Context) error
}

// response is the JSON body of both probes.
type response struct {
	Status string `json:"status"`
	// Failed names the checks that failed, and is omitted when none did. Only
	// names: a probe's error text can carry a connection string or a hostname,
	// and this endpoint is reachable by anything in the cluster.
	Failed []string `json:"failed,omitempty"`
}

// Handler serves LivePath and ReadyPath. Any other path is 404.
//
// The returned handler is safe for concurrent use and is normally mounted on the
// service's main mux. Probes run in parallel, so readiness costs the slowest
// dependency rather than their sum.
func Handler(checks ...Check) http.Handler {
	// Copy: a caller that appends to its slice afterwards must not silently
	// change what this handler probes.
	registered := slices.Clone(checks)

	mux := http.NewServeMux()
	mux.HandleFunc("GET "+LivePath, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, response{Status: statusOK})
	})
	mux.HandleFunc("GET "+ReadyPath, func(w http.ResponseWriter, r *http.Request) {
		failed := runChecks(r.Context(), registered)
		if len(failed) == 0 {
			writeJSON(w, http.StatusOK, response{Status: statusOK})
			return
		}
		writeJSON(w, http.StatusServiceUnavailable, response{Status: statusUnavailable, Failed: failed})
	})
	return mux
}

// runChecks probes every dependency in parallel and returns the names that
// failed, in registration order so the body is stable across polls.
func runChecks(ctx context.Context, checks []Check) []string {
	if len(checks) == 0 {
		return nil
	}

	errs := make([]error, len(checks))

	var wg sync.WaitGroup
	for i, c := range checks {
		if c.Probe == nil {
			// A Check with no probe is a wiring bug. Readiness fails closed:
			// reporting ready because nobody looked is the worse answer.
			errs[i] = errors.New("no probe configured")
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = c.Probe(ctx)
		}()
	}
	wg.Wait()

	var failed []string
	for i, err := range errs {
		if err != nil {
			failed = append(failed, checks[i].Name)
		}
	}
	return failed
}

// writeJSON writes the probe body. Encoding cannot fail for these two fields,
// so there is nothing to report if it somehow did.
func writeJSON(w http.ResponseWriter, status int, body response) {
	encoded, err := json.Marshal(body)
	if err != nil {
		http.Error(w, `{"status":"unavailable"}`, http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(append(encoded, '\n'))
}
