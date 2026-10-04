// Package events defines the Kafka event envelope and payloads shared with
// the Python services. The canonical contract is proto/events/*.schema.json;
// events_test.go decodes the fixtures in proto/events/examples to keep these
// structs in sync with it.
package events

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"time"
)

// Type is an event type. The Kafka topic has the same name.
type Type string

const (
	RepoRegistered      Type = "repo.registered"
	RepoIndexed         Type = "repo.indexed"
	JobCreated          Type = "job.created"
	PatchGenerated      Type = "patch.generated"
	ValidationCompleted Type = "validation.completed"
	JobCompleted        Type = "job.completed"
)

// AllTypes lists every event type (and therefore every topic).
var AllTypes = []Type{RepoRegistered, RepoIndexed, JobCreated, PatchGenerated, ValidationCompleted, JobCompleted}

// TopicNames returns every topic name, for readiness checks and topic setup.
func TopicNames() []string {
	names := make([]string, len(AllTypes))
	for i, t := range AllTypes {
		names[i] = string(t)
	}
	return names
}

// DLQTopic is the dead-letter topic for a topic.
func DLQTopic(topic string) string { return topic + ".dlq" }

// Envelope wraps every event. Payload stays raw until the consumer knows
// which type to decode it into.
type Envelope struct {
	EventID   string          `json:"event_id"`
	Type      Type            `json:"type"`
	Version   int             `json:"version"`
	Timestamp time.Time       `json:"timestamp"`
	Source    string          `json:"source"`
	TraceID   *string         `json:"trace_id"`
	Payload   json.RawMessage `json:"payload"`
}

// New builds an envelope around payload.
func New(t Type, source, traceID string, payload any) (Envelope, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return Envelope{}, fmt.Errorf("marshal %s payload: %w", t, err)
	}
	var trace *string
	if traceID != "" {
		trace = &traceID
	}
	return Envelope{
		EventID:   newUUID(),
		Type:      t,
		Version:   1,
		Timestamp: time.Now().UTC(),
		Source:    source,
		TraceID:   trace,
		Payload:   raw,
	}, nil
}

// Decode parses an envelope and checks the required fields.
func Decode(data []byte) (Envelope, error) {
	var e Envelope
	if err := json.Unmarshal(data, &e); err != nil {
		return Envelope{}, fmt.Errorf("decode envelope: %w", err)
	}
	if e.EventID == "" || e.Type == "" || e.Source == "" || len(e.Payload) == 0 {
		return Envelope{}, fmt.Errorf("decode envelope: missing required field")
	}
	return e, nil
}

// DecodePayload unmarshals the payload into out.
func (e Envelope) DecodePayload(out any) error {
	if err := json.Unmarshal(e.Payload, out); err != nil {
		return fmt.Errorf("decode %s payload: %w", e.Type, err)
	}
	return nil
}

// Trace returns the trace id or "".
func (e Envelope) Trace() string {
	if e.TraceID == nil {
		return ""
	}
	return *e.TraceID
}

// newUUID returns a random (version 4) UUID string.
func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read never fails on supported platforms
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// ---------------------------------------------------------------------------
// Payloads
// ---------------------------------------------------------------------------

type RepoRegisteredPayload struct {
	RepoID        string  `json:"repo_id"`
	FullName      string  `json:"full_name"`
	CloneURL      string  `json:"clone_url"`
	DefaultBranch string  `json:"default_branch"`
	CommitSHA     *string `json:"commit_sha"`
}

type RepoIndexedPayload struct {
	RepoID     string  `json:"repo_id"`
	SnapshotID string  `json:"snapshot_id"`
	CommitSHA  string  `json:"commit_sha"`
	Status     string  `json:"status"` // ready | failed
	FileCount  int     `json:"file_count"`
	ChunkCount int     `json:"chunk_count"`
	Error      *string `json:"error"`
}

type JobCreatedPayload struct {
	JobID         string  `json:"job_id"`
	RepoID        string  `json:"repo_id"`
	Task          string  `json:"task"`
	BaseCommitSHA *string `json:"base_commit_sha"`
	MaxIterations int     `json:"max_iterations"`
}

type ValidationConfig struct {
	Language       *string `json:"language"`
	InstallCommand *string `json:"install_command"`
	TestCommand    *string `json:"test_command"`
	TimeoutSeconds *int    `json:"timeout_seconds"`
}

type PatchGeneratedPayload struct {
	JobID     string           `json:"job_id"`
	PatchID   string           `json:"patch_id"`
	Iteration int              `json:"iteration"`
	RepoID    string           `json:"repo_id"`
	CloneURL  string           `json:"clone_url"`
	CommitSHA string           `json:"commit_sha"`
	Diff      string           `json:"diff"`
	Config    ValidationConfig `json:"config"`
}

// Check statuses for one validation section.
const (
	CheckPassed  = "passed"
	CheckFailed  = "failed"
	CheckSkipped = "skipped"
	CheckError   = "error"
)

type Finding struct {
	Tool     *string `json:"tool"`
	RuleID   *string `json:"rule_id"`
	Severity *string `json:"severity"`
	File     *string `json:"file"`
	Line     *int    `json:"line"`
	Message  string  `json:"message"`
}

type CheckResult struct {
	Status     string    `json:"status"`
	Tool       *string   `json:"tool"`
	Summary    *string   `json:"summary"`
	Passed     int       `json:"passed"`
	Failed     int       `json:"failed"`
	Skipped    int       `json:"skipped"`
	Findings   []Finding `json:"findings"`
	Log        *string   `json:"log"`
	DurationMS int64     `json:"duration_ms"`
}

type ValidationCompletedPayload struct {
	JobID          string      `json:"job_id"`
	PatchID        string      `json:"patch_id"`
	Iteration      int         `json:"iteration"`
	Status         string      `json:"status"` // passed | failed | error | timeout
	Tests          CheckResult `json:"tests"`
	Security       CheckResult `json:"security"`
	StaticAnalysis CheckResult `json:"static_analysis"`
	DurationMS     int64       `json:"duration_ms"`
	Error          *string     `json:"error"`
}

type JobCompletedPayload struct {
	JobID        string  `json:"job_id"`
	Status       string  `json:"status"` // awaiting_review | failed | cancelled
	FinalPatchID *string `json:"final_patch_id"`
	Iterations   int     `json:"iterations"`
	Error        *string `json:"error"`
}
