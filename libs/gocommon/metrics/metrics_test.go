package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestInstrumentLabelsByPatternNotPath(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/repos/{id}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})
	mux.Handle("GET /metrics", Handler())
	h := Instrument(mux)

	for _, id := range []string{"a", "b", "c"} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/v1/repos/"+id, nil))
	}
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/nope", nil))

	if got := testutil.ToFloat64(httpRequests.WithLabelValues("GET /v1/repos/{id}", "GET", "418")); got != 3 {
		t.Fatalf("pattern counter = %v, want 3", got)
	}
	if got := testutil.ToFloat64(httpRequests.WithLabelValues("unmatched", "GET", "404")); got != 1 {
		t.Fatalf("unmatched counter = %v, want 1", got)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := rec.Body.String()
	if !strings.Contains(body, `http_requests_total{method="GET",route="GET /v1/repos/{id}",status="418"} 3`) {
		t.Fatalf("exposition missing request counter:\n%s", body)
	}
	if strings.Contains(body, `route="GET /metrics"`) {
		t.Fatal("scrapes must not be counted as traffic")
	}
	if !strings.Contains(body, "go_goroutines") {
		t.Fatal("runtime collectors missing")
	}
}

func TestStatusRecorderKeepsFlusher(t *testing.T) {
	var flushed bool
	h := Instrument(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		f, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("wrapped writer lost http.Flusher")
		}
		f.Flush()
		flushed = true
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	if !flushed {
		t.Fatal("handler did not run")
	}
}

func TestKafkaCounters(t *testing.T) {
	KafkaOutcome("repo.registered", "processed")
	KafkaOutcome("repo.registered", "processed")
	KafkaOutcome("repo.registered", "dead_lettered")
	if got := testutil.ToFloat64(kafkaMessages.WithLabelValues("repo.registered", "processed")); got != 2 {
		t.Fatalf("processed = %v", got)
	}
}
