package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSchemaAndDecoderFixturesStaySynchronized(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "schemas", "chora.agent-result.v1.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	want := []string{"schema_version", "summary", "review_ready", "outputs", "artifact_candidates", "checks", "unknowns", "handoff"}
	if len(schema.Required) != len(want) {
		t.Fatalf("required=%v", schema.Required)
	}
	for index, name := range want {
		if schema.Required[index] != name {
			t.Fatalf("required=%v", schema.Required)
		}
		if _, ok := schema.Properties[name]; !ok {
			t.Fatalf("missing property %s", name)
		}
	}
	for _, fixture := range []string{"result-success.json", "result-needs-revision.json"} {
		if _, err := DecodeResult(readFixture(t, fixture)); err != nil {
			t.Fatalf("fixture %s: %v", fixture, err)
		}
	}
}
