package localweb

import (
	"crypto/sha256"
	"fmt"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/domain"
)

func TestModelProvenanceBindsSuccessfulAttemptIntervals(t *testing.T) {
	runID := domain.NewRunID()
	first := provenanceAttempt(t, runID, 1, nil)
	firstID := first.ID()
	second := provenanceAttempt(t, runID, 2, &firstID)
	thirdID := second.ID()
	third := provenanceAttempt(t, runID, 3, &thirdID)
	events := []domain.RunEvent{
		provenanceEvent(t, runID, 1, "run.prepared", "app", fmt.Sprintf(`{"attempt_id":%q,"attempt_sequence":1}`, first.ID().String())),
		provenanceEvent(t, runID, 2, "attempt.start_failed", "app", fmt.Sprintf(`{"attempt_id":%q,"attempt_sequence":1}`, first.ID().String())),
		provenanceEvent(t, runID, 3, "model_identity", "adapter", `{"model_id":"must-not-leak","provider":"old"}`),
		provenanceEvent(t, runID, 4, "run.prepared", "app", fmt.Sprintf(`{"attempt_id":%q,"attempt_sequence":2}`, second.ID().String())),
		provenanceEvent(t, runID, 5, "attempt.started", "app", fmt.Sprintf(`{"attempt_id":%q,"attempt_sequence":2}`, second.ID().String())),
		provenanceEvent(t, runID, 6, "agent_start", "adapter", `{"model_id":"gpt-a","provider":"openai"}`),
		provenanceEvent(t, runID, 7, "model_identity", "adapter", `{"model_id":"gpt-b"}`),
		provenanceEvent(t, runID, 8, "model_identity", "adapter", `{"model_id":"gpt-a","provider":"openai"}`),
		provenanceEvent(t, runID, 9, "run.prepared", "app", fmt.Sprintf(`{"attempt_id":%q,"attempt_sequence":3}`, third.ID().String())),
		provenanceEvent(t, runID, 10, "attempt.started", "app", `{"attempt_id":3}`),
		provenanceEvent(t, runID, 11, "model_identity", "adapter", `{"model_id":"must-not-bind"}`),
	}

	got := modelProvenanceByAttempt([]domain.Attempt{first, second, third}, events)
	if got[first.ID().String()].Status != "unknown" || got[third.ID().String()].Status != "unknown" {
		t.Fatalf("failed or malformed-start Attempts must remain unknown: %#v", got)
	}
	identities := got[second.ID().String()].Identities
	if len(identities) != 2 || identities[0] != (modelIdentityView{Provider: "openai", ModelID: "gpt-a"}) || identities[1] != (modelIdentityView{Provider: "unknown", ModelID: "gpt-b"}) {
		t.Fatalf("observed identities = %#v", identities)
	}
}

func TestModelProvenanceObservesNativeAssistantMessageWithoutStartupIdentity(t *testing.T) {
	runID := domain.NewRunID()
	first := provenanceAttempt(t, runID, 1, nil)
	firstID := first.ID()
	second := provenanceAttempt(t, runID, 2, &firstID)
	events := []domain.RunEvent{
		provenanceEvent(t, runID, 1, "run.prepared", "app", fmt.Sprintf(`{"attempt_id":%q,"attempt_sequence":1}`, first.ID().String())),
		provenanceEvent(t, runID, 2, "attempt.started", "app", fmt.Sprintf(`{"attempt_id":%q,"attempt_sequence":1}`, first.ID().String())),
		provenanceEvent(t, runID, 3, "agent_start", "adapter", `{}`),
		provenanceEvent(t, runID, 4, "assistant_message", "app", `{"model_id":"not-adapter","provider":"not-observed"}`),
		provenanceEvent(t, runID, 5, "assistant_message", "adapter", `{"model_id":"deepseek-v4-flash","provider":"deepseek"}`),
		provenanceEvent(t, runID, 6, "assistant_message", "adapter", `{"model_id":"deepseek-v4-flash","provider":"deepseek"}`),
		provenanceEvent(t, runID, 7, "run.prepared", "app", fmt.Sprintf(`{"attempt_id":%q,"attempt_sequence":2}`, second.ID().String())),
		provenanceEvent(t, runID, 8, "attempt.start_failed", "app", fmt.Sprintf(`{"attempt_id":%q,"attempt_sequence":2}`, second.ID().String())),
		provenanceEvent(t, runID, 9, "assistant_message", "adapter", `{"model_id":"must-not-leak","provider":"old"}`),
	}
	got := modelProvenanceByAttempt([]domain.Attempt{first, second}, events)
	observed := got[first.ID().String()]
	if observed.Status != "observed" || len(observed.Identities) != 1 || observed.Identities[0] != (modelIdentityView{Provider: "deepseek", ModelID: "deepseek-v4-flash"}) {
		t.Fatalf("native message identity = %#v", observed)
	}
	if got[second.ID().String()].Status != "unknown" {
		t.Fatalf("failed retry inherited model: %#v", got)
	}
}

func TestModelProvenanceSupportsUnambiguousLegacyPreparedIntervals(t *testing.T) {
	runID := domain.NewRunID()
	first := provenanceAttempt(t, runID, 1, nil)
	firstID := first.ID()
	second := provenanceAttempt(t, runID, 2, &firstID)
	events := []domain.RunEvent{
		provenanceEvent(t, runID, 1, "run.prepared", "app", `{"version":2}`),
		provenanceEvent(t, runID, 2, "attempt.start_failed", "app", `{"version":3}`),
		provenanceEvent(t, runID, 3, "run.prepared", "app", `{"version":4}`),
		provenanceEvent(t, runID, 4, "attempt.started", "app", `{"version":5}`),
		provenanceEvent(t, runID, 5, "model_identity", "adapter", `{"model_id":"legacy-model","provider":"legacy-provider"}`),
	}
	got := modelProvenanceByAttempt([]domain.Attempt{first, second}, events)
	if got[first.ID().String()].Status != "unknown" || len(got[second.ID().String()].Identities) != 1 {
		t.Fatalf("legacy projection = %#v", got)
	}
}

func provenanceAttempt(t *testing.T, runID domain.RunID, sequence int, predecessor *domain.AttemptID) domain.Attempt {
	t.Helper()
	attempt, err := domain.NewAttempt(domain.AttemptParams{ID: domain.NewAttemptID(), RunID: runID, Sequence: sequence, Predecessor: predecessor, ContextSnapshotID: domain.NewContextSnapshotID(), ContextDigest: sha256.Sum256([]byte(fmt.Sprint(sequence))), AdapterID: "fake", CreatedAt: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	return attempt
}

func provenanceEvent(t *testing.T, runID domain.RunID, sequence int64, kind, source, payload string) domain.RunEvent {
	t.Helper()
	event, err := domain.NewRunEvent(domain.RunEventParams{ID: domain.NewEventID(), RunID: runID, Sequence: sequence, Type: kind, Source: source, OccurredAt: time.Now().UTC(), RecordedAt: time.Now().UTC(), NormalizedJSON: []byte(payload)})
	if err != nil {
		t.Fatal(err)
	}
	return event
}
