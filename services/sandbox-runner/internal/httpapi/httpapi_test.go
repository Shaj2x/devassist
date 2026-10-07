package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Shaj2x/devassist/libs/gocommon/events"
	"github.com/Shaj2x/devassist/services/sandbox-runner/internal/validate"
)

type fakeValidator struct{ got validate.Request }

func (f *fakeValidator) Validate(_ context.Context, r validate.Request) events.ValidationCompletedPayload {
	f.got = r
	return events.ValidationCompletedPayload{Status: "passed"}
}

func TestValidateEndpoint(t *testing.T) {
	f := &fakeValidator{}
	mux := http.NewServeMux()
	Register(mux, f)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/validate", strings.NewReader(
		`{"clone_url":"file:///r.git","commit_sha":"abc1234","diff":"d","config":{"language":"go"},"timeout_seconds":30}`)))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"passed"`) {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
	if f.got.Overrides.Language != "go" || f.got.Timeout.Seconds() != 30 {
		t.Errorf("request = %+v", f.got)
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/validate", strings.NewReader(`{"clone_url":"x"}`)))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("missing fields: code = %d", rec.Code)
	}
}
