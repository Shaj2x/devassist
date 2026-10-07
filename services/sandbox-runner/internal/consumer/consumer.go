// Package consumer validates patches from patch.generated and publishes
// validation.completed.
//
// The runner keeps no database. Idempotency comes from Redis: a finished
// result is cached per patch id, so a redelivered event republishes the
// cached result instead of running the sandbox again, and a per-patch lock
// stops two runners validating the same patch at once.
package consumer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/Shaj2x/devassist/libs/gocommon/events"
	"github.com/Shaj2x/devassist/libs/gocommon/kafkax"
	"github.com/Shaj2x/devassist/libs/gocommon/lock"
	"github.com/Shaj2x/devassist/services/sandbox-runner/internal/toolchain"
	"github.com/Shaj2x/devassist/services/sandbox-runner/internal/validate"
	"github.com/redis/go-redis/v9"
)

type Validator interface {
	Validate(ctx context.Context, req validate.Request) events.ValidationCompletedPayload
}

type Publisher interface {
	Publish(ctx context.Context, t events.Type, traceID string, payload any) (events.Envelope, error)
}

type Handler struct {
	validator Validator
	publisher Publisher
	redis     *redis.Client
	resultTTL time.Duration
	lockTTL   time.Duration
	log       *slog.Logger
}

func NewHandler(v Validator, p Publisher, rdb *redis.Client, resultTTL, lockTTL time.Duration, log *slog.Logger) *Handler {
	return &Handler{validator: v, publisher: p, redis: rdb, resultTTL: resultTTL, lockTTL: lockTTL, log: log}
}

func resultKey(patchID string) string { return "devassist:validation:" + patchID }

// Handle implements kafkax.Handler.
func (h *Handler) Handle(ctx context.Context, env events.Envelope) error {
	if env.Type != events.PatchGenerated {
		return kafkax.Permanent(fmt.Errorf("unexpected event type %s", env.Type))
	}
	var p events.PatchGeneratedPayload
	if err := env.DecodePayload(&p); err != nil {
		return kafkax.Permanent(err)
	}
	if p.PatchID == "" || p.JobID == "" || p.CloneURL == "" || p.CommitSHA == "" {
		return kafkax.Permanent(errors.New("job_id, patch_id, clone_url and commit_sha are required"))
	}

	// Already validated? Republish (the earlier publish may have been lost).
	if raw, err := h.redis.Get(ctx, resultKey(p.PatchID)).Bytes(); err == nil {
		var cached events.ValidationCompletedPayload
		if json.Unmarshal(raw, &cached) == nil {
			h.log.InfoContext(ctx, "patch already validated; republishing result", "patch_id", p.PatchID)
			return h.publish(ctx, cached)
		}
	} else if !errors.Is(err, redis.Nil) {
		return err
	}

	l, err := lock.Acquire(ctx, h.redis, "validate:patch:"+p.PatchID, h.lockTTL)
	if err != nil {
		return err // ErrNotAcquired included: retried with backoff
	}
	l.KeepAlive(ctx)
	defer func() { _ = l.Release(context.WithoutCancel(ctx)) }()

	req := validate.Request{JobID: p.JobID, PatchID: p.PatchID, Iteration: p.Iteration,
		CloneURL: p.CloneURL, CommitSHA: p.CommitSHA, Diff: p.Diff}
	c := p.Config
	req.Overrides = toolchain.Overrides{Language: deref(c.Language), InstallCommand: deref(c.InstallCommand), TestCommand: deref(c.TestCommand)}
	if c.TimeoutSeconds != nil {
		req.Timeout = time.Duration(*c.TimeoutSeconds) * time.Second
	}
	result := h.validator.Validate(ctx, req)
	if ctx.Err() != nil {
		return ctx.Err() // shutting down: let the event be redelivered
	}

	if raw, err := json.Marshal(result); err == nil {
		if err := h.redis.Set(ctx, resultKey(p.PatchID), raw, h.resultTTL).Err(); err != nil {
			h.log.WarnContext(ctx, "could not cache validation result", "error", err)
		}
	}
	return h.publish(ctx, result)
}

func (h *Handler) publish(ctx context.Context, r events.ValidationCompletedPayload) error {
	_, err := h.publisher.Publish(ctx, events.ValidationCompleted, r.JobID, r)
	return err
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
