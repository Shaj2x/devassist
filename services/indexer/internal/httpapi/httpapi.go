// Package httpapi exposes search over HTTP for the orchestrator's retrieval
// tool (and for curl during demos).
//
//	POST /v1/search                      {"repo_id", "query", "top_k"}
//	GET  /v1/repos/{repo_id}/symbols     ?name=parse_date&limit=10
//	GET  /v1/repos/{repo_id}/index       latest ready snapshot
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/Shaj2x/devassist/services/indexer/internal/search"
	"github.com/Shaj2x/devassist/services/indexer/internal/store"
)

const (
	defaultTopK = 8
	maxTopK     = 50
	maxQueryLen = 2000
)

// Searcher is implemented by *search.Service.
type Searcher interface {
	Search(ctx context.Context, repoID, query string, k int) (search.Response, error)
	Symbols(ctx context.Context, repoID, name string, limit int) (search.Response, error)
	LatestSnapshot(ctx context.Context, repoID string) (store.Snapshot, error)
}

type handler struct {
	svc Searcher
	log *slog.Logger
}

// Register mounts the routes on mux.
func Register(mux *http.ServeMux, svc Searcher, log *slog.Logger) {
	h := &handler{svc: svc, log: log}
	mux.HandleFunc("POST /v1/search", h.search)
	mux.HandleFunc("GET /v1/repos/{repo_id}/symbols", h.symbols)
	mux.HandleFunc("GET /v1/repos/{repo_id}/index", h.index)
}

type searchRequest struct {
	RepoID string `json:"repo_id"`
	Query  string `json:"query"`
	TopK   int    `json:"top_k"`
}

func (h *handler) search(w http.ResponseWriter, r *http.Request) {
	var req searchRequest
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	req.Query = strings.TrimSpace(req.Query)
	switch {
	case req.RepoID == "":
		writeError(w, http.StatusBadRequest, "repo_id is required")
		return
	case req.Query == "" || len(req.Query) > maxQueryLen:
		writeError(w, http.StatusBadRequest, "query must be 1-2000 characters")
		return
	}
	if req.TopK <= 0 {
		req.TopK = defaultTopK
	}
	req.TopK = min(req.TopK, maxTopK)

	resp, err := h.svc.Search(r.Context(), req.RepoID, req.Query, req.TopK)
	h.respond(w, r, resp, err)
}

func (h *handler) symbols(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	limit, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || limit <= 0 {
		limit = 10
	}
	resp, err := h.svc.Symbols(r.Context(), r.PathValue("repo_id"), name, min(limit, maxTopK))
	h.respond(w, r, resp, err)
}

func (h *handler) index(w http.ResponseWriter, r *http.Request) {
	snap, err := h.svc.LatestSnapshot(r.Context(), r.PathValue("repo_id"))
	h.respond(w, r, map[string]any{
		"snapshot_id": snap.ID, "commit_sha": snap.CommitSHA, "status": snap.Status,
		"embedding_model": snap.EmbeddingModel, "file_count": snap.FileCount,
		"chunk_count": snap.ChunkCount, "finished_at": snap.FinishedAt,
	}, err)
}

func (h *handler) respond(w http.ResponseWriter, r *http.Request, body any, err error) {
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, body)
	case errors.Is(err, search.ErrNotIndexed):
		writeError(w, http.StatusNotFound, "repository not indexed yet")
	default:
		h.log.ErrorContext(r.Context(), "request failed", "path", r.URL.Path, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
