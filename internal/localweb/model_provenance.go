package localweb

import (
	"encoding/json"
	"strings"

	"github.com/Yangyang96/chora/internal/domain"
)

type modelIdentityView struct {
	Provider string `json:"provider"`
	ModelID  string `json:"modelId"`
}

type modelProvenanceView struct {
	Status     string              `json:"status"`
	Identities []modelIdentityView `json:"identities"`
	Reason     string              `json:"reason,omitempty"`
}

func unknownModelProvenance() modelProvenanceView {
	return modelProvenanceView{Status: "unknown", Identities: []modelIdentityView{}, Reason: "No model identity was observed for this Attempt."}
}

// modelProvenanceByAttempt projects only identities observed inside a successfully
// started Attempt interval. run.prepared is the interval boundary, including for
// Attempts which never started; this prevents a predecessor's identity from being
// attributed to a retry or resumed Attempt.
func modelProvenanceByAttempt(attempts []domain.Attempt, events []domain.RunEvent) map[string]modelProvenanceView {
	result := make(map[string]modelProvenanceView, len(attempts))
	byID := make(map[string]domain.Attempt, len(attempts))
	bySequence := make(map[int]domain.Attempt, len(attempts))
	for _, attempt := range attempts {
		result[attempt.ID().String()] = unknownModelProvenance()
		byID[attempt.ID().String()] = attempt
		bySequence[attempt.Sequence()] = attempt
	}
	preparedCount := 0
	legacyPreparedCount := 0
	for _, event := range events {
		if event.Type() != "run.prepared" {
			continue
		}
		preparedCount++
		var boundary struct {
			AttemptID       string `json:"attempt_id"`
			AttemptSequence int    `json:"attempt_sequence"`
		}
		if json.Unmarshal(event.NormalizedJSON(), &boundary) == nil && boundary.AttemptID == "" && boundary.AttemptSequence == 0 {
			legacyPreparedCount++
		}
	}
	legacyIntervalsUnambiguous := preparedCount == len(attempts) && legacyPreparedCount == preparedCount

	var current domain.Attempt
	legacySequence := 0
	started := false
	seen := map[string]bool{}
	for _, event := range events {
		if event.Type() == "run.prepared" {
			current = domain.Attempt{}
			started = false
			seen = map[string]bool{}
			var boundary struct {
				AttemptID       string `json:"attempt_id"`
				AttemptSequence int    `json:"attempt_sequence"`
			}
			if json.Unmarshal(event.NormalizedJSON(), &boundary) == nil && boundary.AttemptID != "" && boundary.AttemptSequence > 0 {
				candidate, ok := byID[boundary.AttemptID]
				if ok && candidate.Sequence() == boundary.AttemptSequence {
					current = candidate
					legacySequence = candidate.Sequence()
				}
			} else if legacyIntervalsUnambiguous {
				legacySequence++
				current = bySequence[legacySequence]
			}
			continue
		}
		if !current.ID().Valid() {
			continue
		}
		if event.Type() == "attempt.started" {
			started = boundaryMatchesAttempt(event.NormalizedJSON(), current)
			continue
		}
		// Native Pi 0.85.1 supplies the model on completed assistant messages;
		// agent_start itself may carry no model identity.
		if !started || event.Source() != "adapter" || (event.Type() != "agent_start" && event.Type() != "model_identity" && event.Type() != "assistant_message") {
			continue
		}
		var observed struct {
			ModelID  string `json:"model_id"`
			Provider string `json:"provider"`
		}
		if json.Unmarshal(event.NormalizedJSON(), &observed) != nil {
			continue
		}
		observed.ModelID = strings.TrimSpace(observed.ModelID)
		observed.Provider = strings.TrimSpace(observed.Provider)
		if observed.ModelID == "" {
			continue
		}
		if observed.Provider == "" {
			observed.Provider = "unknown"
		}
		key := observed.Provider + "\x00" + observed.ModelID
		if seen[key] {
			continue
		}
		seen[key] = true
		projection := result[current.ID().String()]
		projection.Status = "observed"
		projection.Reason = ""
		projection.Identities = append(projection.Identities, modelIdentityView{Provider: observed.Provider, ModelID: observed.ModelID})
		result[current.ID().String()] = projection
	}
	return result
}

func boundaryMatchesAttempt(payload []byte, attempt domain.Attempt) bool {
	var boundary struct {
		AttemptID       string `json:"attempt_id"`
		AttemptSequence int    `json:"attempt_sequence"`
	}
	if json.Unmarshal(payload, &boundary) != nil {
		return false
	}
	if boundary.AttemptID == "" && boundary.AttemptSequence == 0 {
		return true // conservative compatibility for an unambiguous legacy interval
	}
	return boundary.AttemptID == attempt.ID().String() && boundary.AttemptSequence == attempt.Sequence()
}
