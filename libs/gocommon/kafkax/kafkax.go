// Package kafkax wraps segmentio/kafka-go with DevAssist's event conventions:
// JSON envelopes, topic == event type, keys == trace id, bounded retries with
// backoff, and a dead-letter topic for messages that keep failing.
//
// Delivery is at-least-once: an offset is committed only after the handler
// succeeds or the message has been dead-lettered. Handlers must therefore be
// idempotent (see the processed_events table and the per-entity unique keys).
package kafkax

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/Shaj2x/devassist/libs/gocommon/events"
	"github.com/Shaj2x/devassist/libs/gocommon/logging"
	"github.com/Shaj2x/devassist/libs/gocommon/metrics"
	"github.com/segmentio/kafka-go"
)

// Handler processes one event. Returning an error triggers a retry unless it
// is wrapped with Permanent.
type Handler func(ctx context.Context, env events.Envelope) error

type permanentError struct{ err error }

func (p permanentError) Error() string { return p.err.Error() }
func (p permanentError) Unwrap() error { return p.err }

// Permanent marks an error as not worth retrying (bad payload, unknown repo).
// The message goes straight to the dead-letter topic.
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return permanentError{err}
}

// IsPermanent reports whether err was wrapped with Permanent.
func IsPermanent(err error) bool {
	var p permanentError
	return errors.As(err, &p)
}

// Reader is the subset of *kafka.Reader the consumer uses (fakeable in tests).
type Reader interface {
	FetchMessage(ctx context.Context) (kafka.Message, error)
	CommitMessages(ctx context.Context, msgs ...kafka.Message) error
	Close() error
}

// Writer is the subset of *kafka.Writer the publisher and DLQ use.
type Writer interface {
	WriteMessages(ctx context.Context, msgs ...kafka.Message) error
	Close() error
}

// ConsumerConfig controls retry behaviour.
type ConsumerConfig struct {
	MaxAttempts    int           // total tries before dead-lettering (default 5)
	InitialBackoff time.Duration // doubled after each failure (default 1s)
	MaxBackoff     time.Duration // cap (default 30s)
}

func (c ConsumerConfig) withDefaults() ConsumerConfig {
	if c.MaxAttempts <= 0 {
		c.MaxAttempts = 5
	}
	if c.InitialBackoff <= 0 {
		c.InitialBackoff = time.Second
	}
	if c.MaxBackoff <= 0 {
		c.MaxBackoff = 30 * time.Second
	}
	return c
}

// Consumer runs a Handler over one topic.
type Consumer struct {
	reader  Reader
	dlq     Writer
	handler Handler
	log     *slog.Logger
	cfg     ConsumerConfig
}

// NewReader returns a consumer-group reader for topic. It joins the group
// lazily, on the first FetchMessage, and only once the topic exists: a
// reader that joins before its topic is created (every pod starting at once
// in Kubernetes while the topic Job is still running) is otherwise assigned
// no partitions, and kafka-go's partition watcher then rejoins in a tight
// loop instead of recovering.
func NewReader(brokers []string, groupID, topic string) Reader {
	return &lazyReader{brokers: brokers, groupID: groupID, topic: topic}
}

type lazyReader struct {
	brokers []string
	groupID string
	topic   string

	mu sync.Mutex
	r  *kafka.Reader
}

func (l *lazyReader) reader(ctx context.Context) (*kafka.Reader, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.r != nil {
		return l.r, nil
	}
	if err := WaitForTopic(ctx, l.brokers, l.topic, topicPollInterval); err != nil {
		return nil, err
	}
	l.r = kafka.NewReader(kafka.ReaderConfig{
		Brokers:        l.brokers,
		GroupID:        l.groupID,
		Topic:          l.topic,
		MinBytes:       1,
		MaxBytes:       10 << 20,
		MaxWait:        time.Second,
		CommitInterval: 0, // commit synchronously, only after handling
		StartOffset:    kafka.FirstOffset,
		// Rebalance when partitions are added later. Safe now that the
		// group is only joined once the topic exists.
		WatchPartitionChanges:  true,
		PartitionWatchInterval: 30 * time.Second,
	})
	return l.r, nil
}

func (l *lazyReader) FetchMessage(ctx context.Context) (kafka.Message, error) {
	r, err := l.reader(ctx)
	if err != nil {
		return kafka.Message{}, err
	}
	return r.FetchMessage(ctx)
}

func (l *lazyReader) CommitMessages(ctx context.Context, msgs ...kafka.Message) error {
	l.mu.Lock()
	r := l.r
	l.mu.Unlock()
	if r == nil {
		return errors.New("commit before any message was fetched")
	}
	return r.CommitMessages(ctx, msgs...)
}

func (l *lazyReader) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.r == nil {
		return nil
	}
	return l.r.Close()
}

const topicPollInterval = 2 * time.Second

// WaitForTopic blocks until topic exists with at least one partition, or
// ctx ends. It logs while it waits so a missing topic is visible.
func WaitForTopic(ctx context.Context, brokers []string, topic string, every time.Duration) error {
	for attempt := 0; ; attempt++ {
		if topicExists(ctx, brokers, topic) {
			return nil
		}
		if attempt%15 == 0 {
			slog.InfoContext(ctx, "waiting for kafka topic", "topic", topic)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(every):
		}
	}
}

func topicExists(ctx context.Context, brokers []string, topic string) bool {
	for _, b := range brokers {
		conn, err := kafka.DialContext(ctx, "tcp", b)
		if err != nil {
			continue
		}
		partitions, err := conn.ReadPartitions(topic)
		_ = conn.Close()
		if err == nil && len(partitions) > 0 {
			return true
		}
	}
	return false
}

// NewWriter builds a writer that routes each message to its own Topic field,
// partitioning by key so one job's events stay ordered.
func NewWriter(brokers []string) *kafka.Writer {
	return &kafka.Writer{
		Addr:                   kafka.TCP(brokers...),
		Balancer:               &kafka.Hash{},
		RequiredAcks:           kafka.RequireAll,
		AllowAutoTopicCreation: false,
		BatchTimeout:           10 * time.Millisecond,
	}
}

// NewConsumer wires a reader, a dead-letter writer and a handler.
func NewConsumer(reader Reader, dlq Writer, handler Handler, log *slog.Logger, cfg ConsumerConfig) *Consumer {
	return &Consumer{reader: reader, dlq: dlq, handler: handler, log: log, cfg: cfg.withDefaults()}
}

// Run consumes until ctx is cancelled. It returns nil on clean shutdown.
func (c *Consumer) Run(ctx context.Context) error {
	for {
		msg, err := c.reader.FetchMessage(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("fetch message: %w", err)
		}
		if err := c.process(ctx, msg); err != nil {
			if ctx.Err() != nil {
				return nil // shutting down mid-retry; message will be redelivered
			}
			return err
		}
		if err := c.reader.CommitMessages(ctx, msg); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("commit offset: %w", err)
		}
	}
}

// process handles one message, retrying with backoff, and dead-letters it if
// it cannot be handled. A nil return means "safe to commit".
func (c *Consumer) process(ctx context.Context, msg kafka.Message) error {
	env, err := events.Decode(msg.Value)
	if err != nil {
		c.log.ErrorContext(ctx, "undecodable message", "topic", msg.Topic, "offset", msg.Offset, "error", err)
		return c.deadLetter(ctx, msg, err, 0)
	}
	ctx = logging.WithTraceID(ctx, env.Trace())
	start := time.Now()
	defer func() { metrics.KafkaHandled(msg.Topic, time.Since(start)) }()

	backoff := c.cfg.InitialBackoff
	for attempt := 1; ; attempt++ {
		err := c.handler(ctx, env)
		if err == nil {
			metrics.KafkaOutcome(msg.Topic, "processed")
			return nil
		}
		if IsPermanent(err) || attempt >= c.cfg.MaxAttempts {
			c.log.ErrorContext(ctx, "event handling failed; dead-lettering",
				"type", env.Type, "event_id", env.EventID, "attempts", attempt, "error", err)
			return c.deadLetter(ctx, msg, err, attempt)
		}
		metrics.KafkaOutcome(msg.Topic, "retried")
		c.log.WarnContext(ctx, "event handling failed; retrying",
			"type", env.Type, "event_id", env.EventID, "attempt", attempt, "backoff", backoff.String(), "error", err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, c.cfg.MaxBackoff)
	}
}

func (c *Consumer) deadLetter(ctx context.Context, msg kafka.Message, cause error, attempts int) error {
	dlqMsg := kafka.Message{
		Topic: events.DLQTopic(msg.Topic),
		Key:   msg.Key,
		Value: msg.Value,
		Headers: append(msg.Headers,
			kafka.Header{Key: "x-error", Value: []byte(cause.Error())},
			kafka.Header{Key: "x-attempts", Value: []byte(strconv.Itoa(attempts))},
			kafka.Header{Key: "x-original-offset", Value: []byte(strconv.FormatInt(msg.Offset, 10))},
		),
	}
	if err := c.dlq.WriteMessages(ctx, dlqMsg); err != nil {
		return fmt.Errorf("write to dead-letter topic: %w", err)
	}
	metrics.KafkaOutcome(msg.Topic, "dead_lettered")
	return nil
}

// Publisher sends envelopes to the topic named by their type.
type Publisher struct {
	writer Writer
	source string
}

// NewPublisher returns a publisher that stamps events with source.
func NewPublisher(writer Writer, source string) *Publisher {
	return &Publisher{writer: writer, source: source}
}

// Publish wraps payload in an envelope and sends it, keyed by traceID.
func (p *Publisher) Publish(ctx context.Context, t events.Type, traceID string, payload any) (events.Envelope, error) {
	env, err := events.New(t, p.source, traceID, payload)
	if err != nil {
		return events.Envelope{}, err
	}
	value, err := env.Marshal()
	if err != nil {
		return events.Envelope{}, err
	}
	err = p.writer.WriteMessages(ctx, kafka.Message{Topic: string(t), Key: []byte(traceID), Value: value})
	if err != nil {
		return events.Envelope{}, fmt.Errorf("publish %s: %w", t, err)
	}
	return env, nil
}
