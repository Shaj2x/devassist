// Package httpapi exposes synchronous validation for demos and debugging:
//
//	POST /v1/validate {"clone_url","commit_sha","diff","config":{...},"timeout_seconds"}
//
// The production path is the patch.generated Kafka topic.
package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/Shaj2x/devassist/libs/gocommon/events"
	"github.com/Shaj2x/devassist/services/sandbox-runner/internal/toolchain"
	"github.com/Shaj2x/devassist/services/sandbox-runner/internal/validate"
)

type Validator interface {
	Validate(ctx context.Context, req validate.Request) events.ValidationCompletedPayload
}

type validateRequest struct {
	CloneURL       string                  `json:"clone_url"`
	CommitSHA      string                  `json:"commit_sha"`
	Diff           string                  `json:"diff"`
	Config         events.ValidationConfig `json:"config"`
	TimeoutSeconds int                     `json:"timeout_seconds"`
}

func Register(mux *http.ServeMux, v Validator) {
	mux.HandleFunc("POST /v1/validate", func(w http.ResponseWriter, r *http.Request) {
		var req validateRequest
		r.Body = http.MaxBytesReader(w, r.Body, 4<<20)
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
			return
		}
		if req.CloneURL == "" || req.CommitSHA == "" || req.Diff == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "clone_url, commit_sha and diff are required"})
			return
		}
		vr := validate.Request{JobID: "adhoc", PatchID: "adhoc", Iteration: 1, CloneURL: req.CloneURL,
			CommitSHA: req.CommitSHA, Diff: req.Diff, Timeout: time.Duration(req.TimeoutSeconds) * time.Second}
		if c := req.Config; c.Language != nil || c.TestCommand != nil || c.InstallCommand != nil {
			vr.Overrides = toolchain.Overrides{Language: deref(c.Language), InstallCommand: deref(c.InstallCommand), TestCommand: deref(c.TestCommand)}
		}
		writeJSON(w, http.StatusOK, v.Validate(r.Context(), vr))
	})
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
