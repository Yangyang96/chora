package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
	"github.com/Yangyang96/chora/internal/speccoding"
	"strings"
	"testing"
)

func TestResourceObserverBindsFinalPatchAndFailsClosed(t *testing.T) {
	for _, name := range []string{"matched", "final drift", "wrong attempt", "wrong tool", "wrong command", "wrong source", "parallel", "missing", "duplicate", "extension error"} {
		t.Run(name, func(t *testing.T) {
			attempt := domain.NewAttemptID()
			repo := domain.NewRepositoryID().String()
			source := strings.Repeat("a", 64)
			runtimeFingerprint := sha256.Sum256([]byte("runtime"))
			resource := speccoding.ExecutionRepositoryResource{RepoID: repo, Locator: repo, Role: "write", Checks: domain.TaskCheckPolicy{Mode: "named", Commands: []domain.TaskCheckCommand{{ID: "test", Version: 1, Name: "Test", Command: "npm test", Argv: []string{"npm", "test"}, WorkingDirectory: ".", Source: "custom"}}}}
			semantic, _ := json.Marshal(struct {
				Argv []string `json:"argv"`
				Cwd  string   `json:"cwd"`
			}{[]string{"npm", "test"}, repo})
			commandHash := sha256.Sum256(semantic)
			raw := []byte("exact final patch")
			contentHash := sha256.Sum256(raw)
			proof := execution.ResourceCheckObservation{Schema: execution.ResourceCheckObservationSchema, Algorithm: execution.ResourceCheckFingerprintAlgorithm, ObserverSHA256: source, HelperSHA256: strings.Repeat("e", 64), RuntimeFingerprint: hex.EncodeToString(runtimeFingerprint[:]), AttemptID: attempt.String(), RepoID: repo, ToolCallID: "tool-1", CommandDigest: hex.EncodeToString(commandHash[:]), WorkingDirectory: ".", StartFingerprint: hex.EncodeToString(contentHash[:]), EndFingerprint: hex.EncodeToString(contentHash[:]), SingleToolBatch: true}
			switch name {
			case "final drift":
				raw = []byte("different patch")
			case "wrong attempt":
				proof.AttemptID = domain.NewAttemptID().String()
			case "wrong tool":
				proof.ToolCallID = "other"
			case "wrong command":
				proof.CommandDigest = strings.Repeat("b", 64)
			case "wrong source":
				proof.ObserverSHA256 = strings.Repeat("b", 64)
			case "parallel":
				proof.SingleToolBatch = false
			}
			event := func(kind string, body map[string]any) execution.NormalizedEvent {
				body["event_type"] = kind
				b, _ := json.Marshal(body)
				return execution.NormalizedEvent{Type: kind, NormalizedJSON: b}
			}
			start := event("tool_start", map[string]any{"tool_name": "bash", "tool_call_id": "tool-1", "resource_command_digest": hex.EncodeToString(commandHash[:]), "command_digest": strings.Repeat("f", 64), "redacted_command_summary": "cd " + repo + " && npm test"})
			endBody := map[string]any{"tool_call_id": "tool-1", "tool_error": false, "resource_check_observation": proof}
			if name == "missing" {
				delete(endBody, "resource_check_observation")
			}
			events := []execution.NormalizedEvent{start, event("tool_end", endBody), event("assistant_message", map[string]any{"text": "Done", "terminal_complete": true})}
			if name == "duplicate" {
				events = append(events, start)
			}
			if name == "extension error" {
				events = append(events, event("error", map[string]any{"error_class": "runtime_error", "extension_path": "observer.mjs"}))
			}
			observations := resourceObserverFingerprints(attempt, events, []ResourceReviewPatch{{RepoID: repo, Patch: ReviewablePatch{Raw: raw}}}, source, runtimeFingerprint)
			derived, err := DeriveResourceChecks([]speccoding.ExecutionRepositoryResource{resource}, events, observations)
			if err != nil {
				t.Fatal(err)
			}
			if name == "matched" {
				if derived[0].Status != execution.CheckPass || !derived[0].FinalContentVerified {
					t.Fatalf("valid hook proof did not verify final patch: %+v", derived[0])
				}
			} else if derived[0].Status == execution.CheckPass {
				t.Fatalf("unproven hook evidence passed: %+v", derived[0])
			}
		})
	}
}
