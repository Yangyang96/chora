package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
	"strings"
)

// Only typed Provider hook frames establish check-time observations. Ordinary
// tool output and assistant messages can never supply this evidence.
func resourceObserverFingerprints(attemptID domain.AttemptID, events []execution.NormalizedEvent, patches []ResourceReviewPatch, observerSHA string, runtimeFingerprint [32]byte) []ResourceContentFingerprintObservation {
	if observerSHA == "" || runtimeFingerprint == ([32]byte{}) {
		return nil
	}
	type wire struct {
		ToolCallID    string          `json:"tool_call_id"`
		CommandDigest string          `json:"resource_command_digest"`
		ErrorClass    string          `json:"error_class"`
		Observation   json.RawMessage `json:"resource_check_observation"`
	}
	starts := map[string]wire{}
	counts := map[string]int{}
	endCounts := map[string]int{}
	final := map[string]string{}
	for _, patch := range patches {
		sum := sha256.Sum256(patch.Patch.Raw)
		final[patch.RepoID] = hex.EncodeToString(sum[:])
	}
	for _, event := range events {
		var value wire
		if json.Unmarshal(event.NormalizedJSON, &value) != nil {
			continue
		}
		if event.Type == "error" || strings.Contains(value.ErrorClass, "extension") {
			return nil
		}
		if event.Type == "tool_start" && value.ToolCallID != "" {
			counts[value.ToolCallID]++
			starts[value.ToolCallID] = value
		}
		if event.Type == "tool_end" && value.ToolCallID != "" {
			endCounts[value.ToolCallID]++
		}
	}
	observations := []ResourceContentFingerprintObservation{}
	for index, event := range events {
		if event.Type != "tool_end" {
			continue
		}
		var value wire
		if json.Unmarshal(event.NormalizedJSON, &value) != nil || counts[value.ToolCallID] != 1 || endCounts[value.ToolCallID] != 1 {
			continue
		}
		proof, err := execution.DecodeResourceCheckObservation(value.Observation, observerSHA)
		if err != nil || proof.AttemptID != attemptID.String() || proof.RuntimeFingerprint != hex.EncodeToString(runtimeFingerprint[:]) || proof.ToolCallID != value.ToolCallID || starts[value.ToolCallID].CommandDigest != proof.CommandDigest || final[proof.RepoID] == "" {
			continue
		}
		if index+1 == len(events) && proof.EndFingerprint != final[proof.RepoID] {
			continue
		}
		observations = append(observations, ResourceContentFingerprintObservation{AfterEvent: index + 1, RepositoryID: proof.RepoID, Fingerprint: proof.EndFingerprint})
	}
	if len(observations) == 0 {
		return nil
	}
	for repo, digest := range final {
		observations = append(observations, ResourceContentFingerprintObservation{AfterEvent: len(events), RepositoryID: repo, Fingerprint: digest})
	}
	return observations
}
