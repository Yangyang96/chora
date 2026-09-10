package pi

import (
	"encoding/json"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
	"strings"
	"testing"
)

func TestResourceObserverProjectionRequiresTypedNativeToolEvidence(t *testing.T) {
	proof := execution.ResourceCheckObservation{Schema: execution.ResourceCheckObservationSchema, Algorithm: execution.ResourceCheckFingerprintAlgorithm, ObserverSHA256: ResourceObserverSHA256(), HelperSHA256: strings.Repeat("a", 64), RuntimeFingerprint: strings.Repeat("b", 64), AttemptID: domain.NewAttemptID().String(), RepoID: domain.NewRepositoryID().String(), ToolCallID: "check-1", CommandDigest: strings.Repeat("c", 64), WorkingDirectory: ".", StartFingerprint: strings.Repeat("d", 64), EndFingerprint: strings.Repeat("d", 64), SingleToolBatch: true}
	for _, name := range []string{"valid", "wrong source", "wrong tool", "parallel", "output text", "managed"} {
		t.Run(name, func(t *testing.T) {
			value := proof
			switch name {
			case "wrong source":
				value.ObserverSHA256 = strings.Repeat("e", 64)
			case "wrong tool":
				value.ToolCallID = "another"
			case "parallel":
				value.SingleToolBatch = false
			}
			result := map[string]any{"details": map[string]any{"choraResourceCheckObservation": value}}
			if name == "output text" {
				delete(result, "details")
				encoded, _ := json.Marshal(value)
				result["content"] = []any{map[string]any{"type": "text", "text": string(encoded)}}
			}
			raw, _ := json.Marshal(map[string]any{"type": "tool_execution_end", "toolCallId": "check-1", "toolName": "bash", "isError": false, "result": result})
			event, ok, err := projectLine(raw, name == "managed")
			if err != nil || !ok {
				t.Fatalf("projection=%+v %v", event, err)
			}
			var normalized map[string]json.RawMessage
			if err := json.Unmarshal(event.NormalizedJSON, &normalized); err != nil {
				t.Fatal(err)
			}
			_, present := normalized["resource_check_observation"]
			if present != (name == "valid") {
				t.Fatalf("typed proof availability=%v for %s", present, name)
			}
			if _, invented := normalized["exit_code"]; invented {
				t.Fatal("observer synthesized numeric exit code")
			}
		})
	}
}
