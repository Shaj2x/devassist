package kafkax

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/Shaj2x/devassist/libs/gocommon/events"
	"github.com/Shaj2x/devassist/libs/gocommon/logging"
	"github.com/segmentio/kafka-go"
)

// fakeReader yields queued messages, then blocks until ctx is cancelled.
type fakeReader struct {
	mu        sync.Mutex
	queue     []kafka.Message
	committed []int64
}

func (r *fakeReader) FetchMessage(ctx context.Context) (kafka.Message, error) {
	r.mu.Lock()
	if len(r.queue) > 0 {
		m := r.queue[0]
		r.queue = r.queue[1:]
		r.mu.Unlock()
		return m, nil
	}
	r.mu.Unlock()
	<-ctx.Done()
	return kafka.Message{}, ctx.Err()
}

func (r *fakeReader) CommitMessages(_ context.Context, msgs ...kafka.Message) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, m := range msgs {
		r.committed = append(r.committed, m.Offset)
	}
	return nil
}

func (r *fakeReader) Close() error { return nil }

type fakeWriter struct {
	mu   sync.Mutex
	msgs []kafka.Message
}

func (w *fakeWriter) WriteMessages(_ context.Context, msgs ...kafka.Message) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.msgs = append(w.msgs, msgs...)
	return nil
}

func (w *fakeWriter) Close() error { return nil }

func envelopeMsg(t *testing.T, offset int64) kafka.Message {
	t.Helper()
	env, err := events.New(events.RepoRegistered, "test", "repo-1", events.RepoRegisteredPayload{RepoID: "repo-1"})
	if err != nil {
		t.Fatal(err)
	}
	value, _ := env.Marshal()
	return kafka.Message{Topic: string(events.RepoRegistered), Offset: offset, Value: value, Key: []byte("repo-1")}
}

func runUntilDrained(t *testing.T, c *Consumer, r *fakeReader, want int) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	deadline := time.After(2 * time.Second)
	for {
		r.mu.Lock()
		n := len(r.committed)
		r.mu.Unlock()
		if n >= want {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("only %d of %d messages committed", n, want)
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run returned %v", err)
	}
}

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))
var fast = ConsumerConfig{MaxAttempts: 3, InitialBackoff: time.Millisecond, MaxBackoff: 2 * time.Millisecond}

func TestSuccessCommitsAndPropagatesTraceID(t *testing.T) {
	r := &fakeReader{queue: []kafka.Message{envelopeMsg(t, 7)}}
	dlq := &fakeWriter{}
	var gotTrace string
	c := NewConsumer(r, dlq, func(ctx context.Context, _ events.Envelope) error {
		gotTrace = logging.TraceID(ctx)
		return nil
	}, quiet, fast)

	runUntilDrained(t, c, r, 1)

	if gotTrace != "repo-1" {
		t.Errorf("trace id = %q", gotTrace)
	}
	if len(dlq.msgs) != 0 || r.committed[0] != 7 {
		t.Errorf("committed=%v dlq=%d", r.committed, len(dlq.msgs))
	}
}

func TestTransientFailureIsRetried(t *testing.T) {
	r := &fakeReader{queue: []kafka.Message{envelopeMsg(t, 1)}}
	dlq := &fakeWriter{}
	calls := 0
	c := NewConsumer(r, dlq, func(context.Context, events.Envelope) error {
		calls++
		if calls < 3 {
			return errors.New("database busy")
		}
		return nil
	}, quiet, fast)

	runUntilDrained(t, c, r, 1)

	if calls != 3 || len(dlq.msgs) != 0 {
		t.Fatalf("calls=%d dlq=%d", calls, len(dlq.msgs))
	}
}

func TestExhaustedRetriesGoToDLQ(t *testing.T) {
	r := &fakeReader{queue: []kafka.Message{envelopeMsg(t, 3)}}
	dlq := &fakeWriter{}
	calls := 0
	c := NewConsumer(r, dlq, func(context.Context, events.Envelope) error {
		calls++
		return errors.New("still broken")
	}, quiet, fast)

	runUntilDrained(t, c, r, 1)

	if calls != fast.MaxAttempts {
		t.Errorf("calls = %d, want %d", calls, fast.MaxAttempts)
	}
	if len(dlq.msgs) != 1 || dlq.msgs[0].Topic != "repo.registered.dlq" {
		t.Fatalf("dlq = %+v", dlq.msgs)
	}
	headers := map[string]string{}
	for _, h := range dlq.msgs[0].Headers {
		headers[h.Key] = string(h.Value)
	}
	if headers["x-error"] != "still broken" || headers["x-attempts"] != "3" || headers["x-original-offset"] != "3" {
		t.Errorf("headers = %v", headers)
	}
}

func TestPermanentErrorSkipsRetries(t *testing.T) {
	r := &fakeReader{queue: []kafka.Message{envelopeMsg(t, 1), {Topic: "job.created", Offset: 2, Value: []byte("garbage")}}}
	dlq := &fakeWriter{}
	calls := 0
	c := NewConsumer(r, dlq, func(context.Context, events.Envelope) error {
		calls++
		return Permanent(errors.New("unknown repository"))
	}, quiet, fast)

	runUntilDrained(t, c, r, 2)

	if calls != 1 {
		t.Errorf("permanent error retried: calls=%d", calls)
	}
	if len(dlq.msgs) != 2 || dlq.msgs[1].Topic != "job.created.dlq" {
		t.Fatalf("both the permanent failure and the undecodable message should be dead-lettered: %+v", dlq.msgs)
	}
}

func TestPublisherRoutesByTypeAndKeysByTrace(t *testing.T) {
	w := &fakeWriter{}
	p := NewPublisher(w, "indexer")
	env, err := p.Publish(context.Background(), events.RepoIndexed, "repo-9", events.RepoIndexedPayload{RepoID: "repo-9", Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	if len(w.msgs) != 1 || w.msgs[0].Topic != "repo.indexed" || string(w.msgs[0].Key) != "repo-9" {
		t.Fatalf("message = %+v", w.msgs)
	}
	decoded, err := events.Decode(w.msgs[0].Value)
	if err != nil || decoded.EventID != env.EventID || decoded.Source != "indexer" {
		t.Fatalf("decoded = %+v, %v", decoded, err)
	}
}
