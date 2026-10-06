package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Shaj2x/devassist/services/indexer/internal/search"
	"github.com/Shaj2x/devassist/services/indexer/internal/store"
)

type fakeSearcher struct {
	gotK, gotLimit int
	gotQuery       string
	err            error
}

func (f *fakeSearcher) Search(_ context.Context, repoID, q string, k int) (search.Response, error) {
	f.gotK, f.gotQuery = k, q
	return search.Response{RepoID: repoID, Results: []store.Hit{{FilePath: "a.py", StartLine: 3, EndLine: 9}}}, f.err
}

func (f *fakeSearcher) Symbols(_ context.Context, repoID, _ string, limit int) (search.Response, error) {
	f.gotLimit = limit
	return search.Response{RepoID: repoID}, f.err
}

func (f *fakeSearcher) LatestSnapshot(context.Context, string) (store.Snapshot, error) {
	return store.Snapshot{ID: "s1", Status: "ready", ChunkCount: 42}, f.err
}

func serve(t *testing.T, f *fakeSearcher, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	Register(mux, f, slog.New(slog.NewTextHandler(io.Discard, nil)))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
	return rec
}

func TestSearch(t *testing.T) {
	f := &fakeSearcher{}
	rec := serve(t, f, http.MethodPost, "/v1/search", `{"repo_id":"r1","query":"  leap year  ","top_k":500}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d: %s", rec.Code, rec.Body)
	}
	if f.gotK != maxTopK || f.gotQuery != "leap year" {
		t.Errorf("k=%d query=%q", f.gotK, f.gotQuery)
	}
	var resp search.Response
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if len(resp.Results) != 1 || resp.Results[0].StartLine != 3 {
		t.Errorf("body = %s", rec.Body)
	}
}

func TestSearchValidation(t *testing.T) {
	for _, body := range []string{`not json`, `{"query":"x"}`, `{"repo_id":"r","query":"   "}`} {
		if rec := serve(t, &fakeSearcher{}, http.MethodPost, "/v1/search", body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: code = %d", body, rec.Code)
		}
	}
}

func TestErrorMapping(t *testing.T) {
	rec := serve(t, &fakeSearcher{err: search.ErrNotIndexed}, http.MethodPost, "/v1/search", `{"repo_id":"r","query":"q"}`)
	if rec.Code != http.StatusNotFound {
		t.Errorf("not indexed: code = %d", rec.Code)
	}
	rec = serve(t, &fakeSearcher{err: errors.New("db down: password=secret")}, http.MethodPost, "/v1/search", `{"repo_id":"r","query":"q"}`)
	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "secret") {
		t.Errorf("internal error leaked details: %d %s", rec.Code, rec.Body)
	}
}

func TestSymbolsAndIndex(t *testing.T) {
	f := &fakeSearcher{}
	if rec := serve(t, f, http.MethodGet, "/v1/repos/r1/symbols?name=parse&limit=3", ""); rec.Code != http.StatusOK || f.gotLimit != 3 {
		t.Errorf("symbols: %d limit=%d", rec.Code, f.gotLimit)
	}
	if rec := serve(t, f, http.MethodGet, "/v1/repos/r1/symbols", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("symbols without name: %d", rec.Code)
	}
	rec := serve(t, f, http.MethodGet, "/v1/repos/r1/index", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"chunk_count":42`) {
		t.Errorf("index: %d %s", rec.Code, rec.Body)
	}
}
