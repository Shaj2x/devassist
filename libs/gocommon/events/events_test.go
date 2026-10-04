package events

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"testing"
)

const protoDir = "../../../proto/events"

// payloadFor maps each type to a fresh payload struct.
func payloadFor(t Type) any {
	switch t {
	case RepoRegistered:
		return &RepoRegisteredPayload{}
	case RepoIndexed:
		return &RepoIndexedPayload{}
	case JobCreated:
		return &JobCreatedPayload{}
	case PatchGenerated:
		return &PatchGeneratedPayload{}
	case ValidationCompleted:
		return &ValidationCompletedPayload{}
	case JobCompleted:
		return &JobCompletedPayload{}
	}
	return nil
}

func TestExamplesDecodeStrictly(t *testing.T) {
	for _, typ := range AllTypes {
		t.Run(string(typ), func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(protoDir, "examples", string(typ)+".json"))
			if err != nil {
				t.Fatal(err)
			}
			env, err := Decode(data)
			if err != nil {
				t.Fatal(err)
			}
			if env.Type != typ {
				t.Fatalf("type = %s", env.Type)
			}
			// Strict decoding catches schema fields the struct forgot.
			payload := payloadFor(typ)
			dec := json.NewDecoder(bytesReader(env.Payload))
			dec.DisallowUnknownFields()
			if err := dec.Decode(payload); err != nil {
				t.Fatalf("payload does not match struct: %v", err)
			}
		})
	}
}

// Every property in each payload schema must have a struct field, and vice
// versa, so a schema change without a Go change fails here.
func TestStructFieldsMatchSchemaProperties(t *testing.T) {
	for _, typ := range AllTypes {
		t.Run(string(typ), func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(protoDir, "payloads", string(typ)+".schema.json"))
			if err != nil {
				t.Fatal(err)
			}
			var schema struct {
				Properties map[string]json.RawMessage `json:"properties"`
			}
			if err := json.Unmarshal(data, &schema); err != nil {
				t.Fatal(err)
			}
			var want []string
			for k := range schema.Properties {
				want = append(want, k)
			}
			got := jsonFieldNames(reflect.TypeOf(payloadFor(typ)).Elem())
			slices.Sort(want)
			slices.Sort(got)
			if !slices.Equal(want, got) {
				t.Fatalf("schema properties %v != struct fields %v", want, got)
			}
		})
	}
}

func TestEnvelopeSchemaListsAllTypes(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(protoDir, "envelope.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Properties struct {
			Type struct {
				Enum []string `json:"enum"`
			} `json:"type"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	var ours []string
	for _, typ := range AllTypes {
		ours = append(ours, string(typ))
	}
	slices.Sort(ours)
	theirs := schema.Properties.Type.Enum
	slices.Sort(theirs)
	if !slices.Equal(ours, theirs) {
		t.Fatalf("Go types %v != schema enum %v", ours, theirs)
	}
}

func TestNewRoundTrip(t *testing.T) {
	sha := "abc"
	in := RepoRegisteredPayload{RepoID: "r1", FullName: "a/b", CloneURL: "u", DefaultBranch: "main", CommitSHA: &sha}
	env, err := New(RepoRegistered, "indexer", "r1", in)
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(env.EventID) {
		t.Errorf("event id %q is not a v4 uuid", env.EventID)
	}
	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	back, err := Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	var out RepoRegisteredPayload
	if err := back.DecodePayload(&out); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(in, out) || back.Trace() != "r1" {
		t.Fatalf("round trip mismatch: %+v vs %+v", in, out)
	}
}

func TestDecodeRejectsIncompleteEnvelope(t *testing.T) {
	if _, err := Decode([]byte(`{"type":"job.created"}`)); err == nil {
		t.Fatal("expected error")
	}
	if _, err := Decode([]byte(`not json`)); err == nil {
		t.Fatal("expected error")
	}
}

func TestDLQTopic(t *testing.T) {
	if DLQTopic("patch.generated") != "patch.generated.dlq" {
		t.Fatal("bad dlq name")
	}
}

func jsonFieldNames(t reflect.Type) []string {
	var names []string
	for i := 0; i < t.NumField(); i++ {
		tag := t.Field(i).Tag.Get("json")
		if tag != "" && tag != "-" {
			names = append(names, regexp.MustCompile(`,.*$`).ReplaceAllString(tag, ""))
		}
	}
	return names
}
