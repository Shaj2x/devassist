package consumer

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/Shaj2x/devassist/libs/gocommon/events"
	"github.com/Shaj2x/devassist/libs/gocommon/kafkax"
	"github.com/Shaj2x/devassist/services/indexer/internal/pipeline"
	"github.com/Shaj2x/devassist/services/indexer/internal/store"
)

type fakeIndexer struct {
	calls int
	res   pipeline.Result
	err   error
	got   pipeline.Request
}

func (f *fakeIndexer) Index(_ context.Context, req pipeline.Request) (pipeline.Result, error) {
	f.calls++
	f.got = req
	return f.res, f.err
}

type fakeLedger struct {
	processed map[string]bool
	repos     map[string]bool
}

func (f *fakeLedger) EventProcessed(_ context.Context, _, id string) (bool, error) {
	return f.processed[id], nil
}

func (f *fakeLedger) MarkEventProcessed(_ context.Context, _, id, _ string) error {
	f.processed[id] = true
	return nil
}

func (f *fakeLedger) RepositoryExists(_ context.Context, id string) (bool, error) {
	return f.repos[id], nil
}

type fakePublisher struct{ sent []events.RepoIndexedPayload }

func (f *fakePublisher) Publish(_ context.Context, _ events.Type, _ string, payload any) (events.Envelope, error) {
	f.sent = append(f.sent, payload.(events.RepoIndexedPayload))
	return events.Envelope{}, nil
}

func setup(idx *fakeIndexer) (*Handler, *fakeLedger, *fakePublisher) {
	ledger := &fakeLedger{processed: map[string]bool{}, repos: map[string]bool{"repo-1": true}}
	pub := &fakePublisher{}
	return NewHandler(idx, ledger, pub, slog.New(slog.NewTextHandler(io.Discard, nil))), ledger, pub
}

func registered(t *testing.T, repoID string) events.Envelope {
	t.Helper()
	sha := "abc1234"
	env, err := events.New(events.RepoRegistered, "api", repoID, events.RepoRegisteredPayload{
		RepoID: repoID, FullName: "o/r", CloneURL: "file:///r.git", DefaultBranch: "main", CommitSHA: &sha,
	})
	if err != nil {
		t.Fatal(err)
	}
	return env
}

func TestIndexesPublishesAndIsIdempotent(t *testing.T) {
	idx := &fakeIndexer{res: pipeline.Result{Snapshot: store.Snapshot{ID: "snap", CommitSHA: "abc1234"}, Files: 3, Chunks: 9}}
	h, _, pub := setup(idx)
	env := registered(t, "repo-1")

	if err := h.Handle(context.Background(), env); err != nil {
		t.Fatal(err)
	}
	if err := h.Handle(context.Background(), env); err != nil { // redelivery
		t.Fatal(err)
	}
	if idx.calls != 1 {
		t.Errorf("redelivered event re-indexed: calls=%d", idx.calls)
	}
	if idx.got.CommitSHA != "abc1234" || idx.got.Branch != "main" {
		t.Errorf("request = %+v", idx.got)
	}
	if len(pub.sent) != 1 || pub.sent[0].Status != "ready" || pub.sent[0].ChunkCount != 9 {
		t.Errorf("published = %+v", pub.sent)
	}
}

func TestFailedSnapshotIsReportedNotRetried(t *testing.T) {
	idx := &fakeIndexer{res: pipeline.Result{Snapshot: store.Snapshot{ID: "snap", CommitSHA: "abc"}}, err: errors.New("embedding quota exceeded")}
	h, ledger, pub := setup(idx)
	env := registered(t, "repo-1")

	if err := h.Handle(context.Background(), env); err != nil {
		t.Fatalf("a recorded failure should not be retried: %v", err)
	}
	if len(pub.sent) != 1 || pub.sent[0].Status != "failed" || *pub.sent[0].Error != "embedding quota exceeded" {
		t.Errorf("published = %+v", pub.sent)
	}
	if !ledger.processed[env.EventID] {
		t.Error("event not marked processed")
	}
}

func TestTransientFailureIsRetried(t *testing.T) {
	h, _, pub := setup(&fakeIndexer{err: pipeline.ErrBusy})
	err := h.Handle(context.Background(), registered(t, "repo-1"))
	if !errors.Is(err, pipeline.ErrBusy) || kafkax.IsPermanent(err) {
		t.Fatalf("got %v, want retryable ErrBusy", err)
	}
	if len(pub.sent) != 0 {
		t.Error("published despite failure")
	}
}

func TestUnknownRepositoryIsPermanent(t *testing.T) {
	h, _, _ := setup(&fakeIndexer{})
	if err := h.Handle(context.Background(), registered(t, "nope")); !kafkax.IsPermanent(err) {
		t.Fatalf("got %v, want permanent", err)
	}
}
