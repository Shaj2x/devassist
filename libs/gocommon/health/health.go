// Package health implements /healthz (liveness) and /readyz (readiness).
//
// Liveness only says the process is serving. Readiness runs every dependency
// check concurrently, each bounded by a timeout, and returns 503 with the
// per-dependency result when any fails.
package health

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"
)

// Check returns nil when the dependency is healthy.
type Check func(ctx context.Context) error

// Report is the JSON body of /readyz.
type Report struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks"`
}

// Run executes checks concurrently and reports "ok" or the error per name.
func Run(ctx context.Context, checks map[string]Check, timeout time.Duration) Report {
	results := make(map[string]string, len(checks))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for name, check := range checks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			msg := "ok"
			if err := check(cctx); err != nil {
				if errors.Is(err, context.DeadlineExceeded) {
					msg = fmt.Sprintf("timeout after %s", timeout)
				} else {
					msg = err.Error()
				}
			}
			mu.Lock()
			results[name] = msg
			mu.Unlock()
		}()
	}
	wg.Wait()

	status := "ok"
	for _, r := range results {
		if r != "ok" {
			status = "unavailable"
			break
		}
	}
	return Report{Status: status, Checks: results}
}

// Register mounts /healthz and /readyz on mux.
func Register(mux *http.ServeMux, checks map[string]Check, timeout time.Duration) {
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		report := Run(r.Context(), checks, timeout)
		code := http.StatusOK
		if report.Status != "ok" {
			code = http.StatusServiceUnavailable
		}
		writeJSON(w, code, report)
	})
}

// Probe performs a GET against url and returns an error unless it answers
// 200. Distroless images have no curl, so each binary exposes
// `<binary> healthcheck` for Docker HEALTHCHECK, built on this.
func Probe(ctx context.Context, url string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s returned %d", url, resp.StatusCode)
	}
	return nil
}

// ProbeLocal probes this process's own /healthz on the given port and returns
// a process exit code. It backs the `<binary> healthcheck` subcommand.
func ProbeLocal(port string) int {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := Probe(ctx, "http://127.0.0.1:"+port+"/healthz"); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
