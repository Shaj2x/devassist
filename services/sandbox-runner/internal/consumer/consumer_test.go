package consumer

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/Shaj2x/devassist/libs/gocommon/events"
	"github.com/Shaj2x/devassist/libs/gocommon/kafkax"
	"github.com/Shaj2x/devassist/libs/gocommon/lock"
	"github.com/Shaj2x/devassist/services/sandbox-runner/internal/validate"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

type fakeValidator struct {
	calls int
	got   validate.Request
}

func (f *fakeValidator) Validate(_ context.Context, r validate.Request) events.ValidationCompletedPayload {
	f.calls++
	f.got = r
	return events.ValidationCompletedPayload{JobID: r.JobID, PatchID: r.PatchID, Iteration: r.Iteration, Status: "passed"}
}

type fakePublisher struct {
	sent []events.ValidationCompletedPayload
}

func (f *fakePublisher) Publish(_ context.Context, t events.Type, key string, payload any) (events.Envelope, error) {
	if t != events.ValidationCompleted || key != "job-1" {
		return events.Envelope{}, errors.New("wrong topic or key")
	}
	f.sent = append(f.sent, payload.(events.ValidationCompletedPayload))
	return events.Envelope{}, nil
}

func setup(t *testing.T) (*Handler, *fakeValidator, *fakePublisher, *redis.Client) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	v, p := &fakeValidator{}, &fakePublisher{}
	return NewHandler(v, p, rdb, time.Hour, time.Minute, slog.New(slog.NewTextHandler(io.Discard, nil))), v, p, rdb
}

func patchEvent(t *testing.T) events.Envelope {
	t.Helper()
	timeout := 90
	lang := "python"
	env, err := events.New(events.PatchGenerated, "orchestrator", "job-1", events.PatchGeneratedPayload{
		JobID: "job-1", PatchID: "patch-1", Iteration: 2, RepoID: "r", CloneURL: "file:///r.git",
		CommitSHA: "abc1234", Diff: "--- a/x\n+++ b/x\n",
		Config: events.ValidationConfig{Language: &lang, TimeoutSeconds: &timeout},
	})
	if err != nil {
		t.Fatal(err)
	}
	return env
}

func TestValidatesOnceAndRepublishesOnRedelivery(t *testing.T) {
	h, v, p, _ := setup(t)
	env := patchEvent(t)

	for i := 0; i < 2; i++ {
		if err := h.Handle(context.Background(), env); err != nil {
			t.Fatal(err)
		}
	}
	if v.calls != 1 {
		t.Errorf("validated %d times, want 1", v.calls)
	}
	if len(p.sent) != 2 || p.sent[1].PatchID != "patch-1" || p.sent[1].Iteration != 2 {
		t.Errorf("published = %+v", p.sent)
	}
	if v.got.Overrides.Language != "python" || v.got.Timeout != 90*time.Second {
		t.Errorf("request = %+v", v.got)
	}
}

func TestPatchBeingValidatedElsewhereIsRetried(t *testing.T) {
	h, v, _, rdb := setup(t)
	held, err := lock.Acquire(context.Background(), rdb, "validate:patch:patch-1", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Release(context.Background()) }()

	err = h.Handle(context.Background(), patchEvent(t))
	if !errors.Is(err, lock.ErrNotAcquired) || kafkax.IsPermanent(err) {
		t.Fatalf("got %v, want retryable ErrNotAcquired", err)
	}
	if v.calls != 0 {
		t.Error("validated despite lock")
	}
}

func TestInvalidPayloadIsPermanent(t *testing.T) {
	h, _, _, _ := setup(t)
	env, _ := events.New(events.PatchGenerated, "x", "job-1", events.PatchGeneratedPayload{JobID: "job-1"})
	if err := h.Handle(context.Background(), env); !kafkax.IsPermanent(err) {
		t.Fatalf("got %v", err)
	}
}
