package health

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func ok(context.Context) error { return nil }

func TestRunReportsEachCheck(t *testing.T) {
	checks := map[string]Check{
		"postgres": ok,
		"redis":    func(context.Context) error { return errors.New("connection refused") },
		"kafka": func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		},
	}
	report := Run(context.Background(), checks, 20*time.Millisecond)

	if report.Status != "unavailable" {
		t.Errorf("status = %s", report.Status)
	}
	want := map[string]string{"postgres": "ok", "redis": "connection refused", "kafka": "timeout after 20ms"}
	for k, v := range want {
		if report.Checks[k] != v {
			t.Errorf("%s = %q, want %q", k, report.Checks[k], v)
		}
	}
}

func TestEndpoints(t *testing.T) {
	healthy := map[string]Check{"postgres": ok}
	failing := map[string]Check{"postgres": func(context.Context) error { return errors.New("down") }}

	for _, tc := range []struct {
		name   string
		checks map[string]Check
		path   string
		code   int
	}{
		{"liveness ignores deps", failing, "/healthz", http.StatusOK},
		{"ready", healthy, "/readyz", http.StatusOK},
		{"not ready", failing, "/readyz", http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mux := http.NewServeMux()
			Register(mux, tc.checks, time.Second)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
			if rec.Code != tc.code {
				t.Fatalf("code = %d, want %d (%s)", rec.Code, tc.code, rec.Body.String())
			}
			var body map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestProbe(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	if err := Probe(context.Background(), srv.URL+"/healthz"); err != nil {
		t.Errorf("healthy probe failed: %v", err)
	}
	if err := Probe(context.Background(), srv.URL+"/readyz"); err == nil {
		t.Error("expected probe error for 503")
	}
}
