package embed

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync/atomic"
	"testing"
	"time"
)

func cosine(a, b []float32) float64 {
	var dot float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
	}
	return dot // inputs are unit vectors
}

func TestTokenize(t *testing.T) {
	got := Tokenize("def parseISODate(dateStr): return is_leap_year(y2k)")
	want := []string{"parse", "iso", "date", "date", "str", "is", "leap", "year", "y2k"}
	// "is" is a stopword, so it is dropped.
	want = slices.DeleteFunc(want, func(s string) bool { return s == "is" })
	if !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestLocalEmbedderIsDeterministicNormalizedAndLexical(t *testing.T) {
	e := NewLocal(1024)
	docs := []string{
		"def is_leap_year(year): return year % 4 == 0",
		"def format_money(cents): return f'${cents/100:.2f}'",
		"class RateLimiter: def allow(self, key): ...",
	}
	vecs, err := e.Embed(context.Background(), docs, Document)
	if err != nil {
		t.Fatal(err)
	}
	again, _ := e.Embed(context.Background(), docs, Document)
	for i, v := range vecs {
		if len(v) != 1024 {
			t.Fatalf("dims = %d", len(v))
		}
		if norm := math.Sqrt(cosine(v, v)); math.Abs(norm-1) > 1e-5 {
			t.Errorf("vector %d not unit length: %f", i, norm)
		}
		if !slices.Equal(v, again[i]) {
			t.Errorf("vector %d not deterministic", i)
		}
	}

	q, _ := e.Embed(context.Background(), []string{"leap year check"}, Query)
	scores := []float64{cosine(q[0], vecs[0]), cosine(q[0], vecs[1]), cosine(q[0], vecs[2])}
	if scores[0] <= scores[1] || scores[0] <= scores[2] {
		t.Fatalf("leap-year doc should rank first: %v", scores)
	}
}

// fakeAPI answers like OpenAI/Voyage and records requests.
func fakeAPI(t *testing.T, failFirst int, status int) (*httptest.Server, *atomic.Int32, *[]map[string]any) {
	t.Helper()
	var calls atomic.Int32
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer test-key" || r.URL.Path != "/embeddings" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if int(n) <= failFirst {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":"slow down"}`))
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		bodies = append(bodies, body)
		inputs := body["input"].([]any)
		type item struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		}
		var data []item
		for i := len(inputs) - 1; i >= 0; i-- { // out of order on purpose
			v := make([]float32, 4)
			v[i%4] = 2 // not unit length on purpose
			data = append(data, item{Index: i, Embedding: v})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	t.Cleanup(srv.Close)
	return srv, &calls, &bodies
}

var fastHTTP = HTTPOptions{Timeout: time.Second, MaxRetries: 3, InitialBackoff: time.Millisecond}

func TestOpenAIRequestShapeOrderingAndRetry(t *testing.T) {
	srv, calls, bodies := fakeAPI(t, 2, http.StatusTooManyRequests)
	e := NewOpenAI(srv.URL, "test-key", "text-embedding-3-small", 4, fastHTTP)

	vecs, err := e.Embed(context.Background(), []string{"a", "b"}, Document)
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 {
		t.Errorf("calls = %d, want 2 failures + 1 success", calls.Load())
	}
	if (*bodies)[0]["dimensions"] != float64(4) || (*bodies)[0]["model"] != "text-embedding-3-small" {
		t.Errorf("request body = %v", (*bodies)[0])
	}
	if vecs[0][0] != 1 || vecs[1][1] != 1 {
		t.Errorf("vectors not reordered by index / normalized: %v", vecs)
	}
	if e.Model() != "openai/text-embedding-3-small" {
		t.Errorf("model = %s", e.Model())
	}
}

func TestVoyageSendsInputType(t *testing.T) {
	srv, _, bodies := fakeAPI(t, 0, 0)
	e := NewVoyage(srv.URL, "test-key", "voyage-code-3", 4, fastHTTP)
	if _, err := e.Embed(context.Background(), []string{"q"}, Query); err != nil {
		t.Fatal(err)
	}
	b := (*bodies)[0]
	if b["input_type"] != "query" || b["output_dimension"] != float64(4) {
		t.Errorf("request body = %v", b)
	}
}

func TestClientErrorsAreNotRetried(t *testing.T) {
	srv, calls, _ := fakeAPI(t, 100, http.StatusBadRequest)
	e := NewOpenAI(srv.URL, "test-key", "m", 4, fastHTTP)
	if _, err := e.Embed(context.Background(), []string{"x"}, Document); err == nil {
		t.Fatal("expected error")
	}
	if calls.Load() != 1 {
		t.Errorf("400 was retried: calls = %d", calls.Load())
	}
}

func TestRetriesAreBounded(t *testing.T) {
	srv, calls, _ := fakeAPI(t, 100, http.StatusServiceUnavailable)
	e := NewOpenAI(srv.URL, "test-key", "m", 4, fastHTTP)
	if _, err := e.Embed(context.Background(), []string{"x"}, Document); err == nil {
		t.Fatal("expected error")
	}
	if calls.Load() != int32(fastHTTP.MaxRetries+1) {
		t.Errorf("calls = %d, want %d", calls.Load(), fastHTTP.MaxRetries+1)
	}
}

func TestNewValidatesProvider(t *testing.T) {
	if _, err := New(Options{Provider: "openai", Dimensions: 4}); err == nil {
		t.Error("openai without key should fail")
	}
	if _, err := New(Options{Provider: "cohere"}); err == nil {
		t.Error("unknown provider should fail")
	}
	e, err := New(Options{Provider: "local", Dimensions: 8})
	if err != nil || e.Dimensions() != 8 {
		t.Fatalf("local: %v %v", e, err)
	}
}

func TestRateLimiterSpacesCalls(t *testing.T) {
	limited := WithRateLimit(NewLocal(8), 20, 1) // one call per 50ms
	start := time.Now()
	for i := 0; i < 3; i++ {
		if _, err := limited.Embed(context.Background(), []string{"x"}, Document); err != nil {
			t.Fatal(err)
		}
	}
	if elapsed := time.Since(start); elapsed < 90*time.Millisecond {
		t.Errorf("3 calls at 20 rps took only %v", elapsed)
	}
}
