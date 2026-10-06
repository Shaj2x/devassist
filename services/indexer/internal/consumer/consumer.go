// Package consumer turns repo.registered events into index runs and
// publishes repo.indexed when each run finishes.
package consumer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/Shaj2x/devassist/libs/gocommon/events"
	"github.com/Shaj2x/devassist/libs/gocommon/kafkax"
	"github.com/Shaj2x/devassist/services/indexer/internal/pipeline"
)

// Name identifies this consumer in the processed_events ledger.
const Name = "indexer"

type Indexer interface {
	Index(ctx context.Context, req pipeline.Request) (pipeline.Result, error)
}

type Ledger interface {
	EventProcessed(ctx context.Context, consumer, eventID string) (bool, error)
	MarkEventProcessed(ctx context.Context, consumer, eventID, eventType string) error
	RepositoryExists(ctx context.Context, repoID string) (bool, error)
}

type Publisher interface {
	Publish(ctx context.Context, t events.Type, traceID string, payload any) (events.Envelope, error)
}

type Handler struct {
	indexer   Indexer
	ledger    Ledger
	publisher Publisher
	log       *slog.Logger
}

func NewHandler(i Indexer, l Ledger, p Publisher, log *slog.Logger) *Handler {
	return &Handler{indexer: i, ledger: l, publisher: p, log: log}
}

// Handle implements kafkax.Handler. Returning an error means "retry";
// kafkax.Permanent errors are dead-lettered immediately.
func (h *Handler) Handle(ctx context.Context, env events.Envelope) error {
	if env.Type != events.RepoRegistered {
		return kafkax.Permanent(fmt.Errorf("unexpected event type %s", env.Type))
	}
	done, err := h.ledger.EventProcessed(ctx, Name, env.EventID)
	if err != nil {
		return err
	}
	if done {
		h.log.InfoContext(ctx, "duplicate event skipped", "event_id", env.EventID)
		return nil
	}

	var p events.RepoRegisteredPayload
	if err := env.DecodePayload(&p); err != nil {
		return kafkax.Permanent(err)
	}
	if p.RepoID == "" || p.CloneURL == "" {
		return kafkax.Permanent(errors.New("repo_id and clone_url are required"))
	}
	exists, err := h.ledger.RepositoryExists(ctx, p.RepoID)
	if err != nil {
		return err
	}
	if !exists {
		return kafkax.Permanent(fmt.Errorf("repository %s does not exist", p.RepoID))
	}

	req := pipeline.Request{RepoID: p.RepoID, CloneURL: p.CloneURL, Branch: p.DefaultBranch}
	if p.CommitSHA != nil {
		req.CommitSHA = *p.CommitSHA
	}
	res, err := h.indexer.Index(ctx, req)
	if err != nil && res.Snapshot.ID == "" {
		// Nothing recorded yet (lock busy, clone failed): let kafkax retry.
		return err
	}

	out := events.RepoIndexedPayload{
		RepoID: p.RepoID, SnapshotID: res.Snapshot.ID, CommitSHA: res.Snapshot.CommitSHA,
		Status: "ready", FileCount: res.Files, ChunkCount: res.Chunks,
	}
	if err != nil {
		// The snapshot is marked failed in the database; report and move on
		// rather than retrying a deterministic failure forever.
		msg := err.Error()
		out.Status, out.Error = "failed", &msg
		h.log.ErrorContext(ctx, "indexing failed", "snapshot_id", res.Snapshot.ID, "error", err)
	}
	if _, err := h.publisher.Publish(ctx, events.RepoIndexed, p.RepoID, out); err != nil {
		return err
	}
	return h.ledger.MarkEventProcessed(ctx, Name, env.EventID, string(env.Type))
}
