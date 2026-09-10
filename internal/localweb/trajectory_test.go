package localweb

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/domain"
)

func TestProjectTrajectoryExplainsSafeAgentStepsAndPairsTools(t *testing.T) {
	start := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	events := []domain.RunEvent{
		trajectoryEvent(t, 1, "run.prepared", "app", start, `{}`),
		trajectoryEvent(t, 2, "tool_start", "adapter", start.Add(time.Second), `{"tool_name":"read","workspace_relative_path":"internal/domain/task.go"}`),
		trajectoryEvent(t, 3, "tool_end", "adapter", start.Add(1500*time.Millisecond), `{"tool_name":"read"}`),
		trajectoryEvent(t, 4, "tool_start", "adapter", start.Add(2*time.Second), `{"tool_name":"bash","redacted_command_summary":"go test ./internal/domain"}`),
		trajectoryEvent(t, 5, "tool_end", "adapter", start.Add(4*time.Second), `{"tool_name":"bash","exit_code":0}`),
		trajectoryEvent(t, 6, "run.awaiting_verification", "adapter", start.Add(5*time.Second), `{"summary":"Implemented Hello with a focused test.","terminal_kind":"review_ready"}`),
		trajectoryEvent(t, 7, "verification.started", "app", start.Add(6*time.Second), `{}`),
		trajectoryEvent(t, 8, "verification.review_ready", "verifier", start.Add(8*time.Second), `{"outcome":"review_ready"}`),
	}

	records := projectTrajectory(events, domain.RunStateAwaitingReview)
	if len(records) != 6 {
		t.Fatalf("trajectory records = %#v", records)
	}
	read := records[1]
	if read.Kind != "read" || read.Summary != "internal/domain/task.go" || read.Status != "completed" || read.EndSequence != 3 || read.DurationMS == nil || *read.DurationMS != 500 {
		t.Fatalf("read record = %#v", read)
	}
	command := records[2]
	if command.Kind != "command" || command.Summary != "go test ./internal/domain" || command.Status != "completed" || command.DurationMS == nil || *command.DurationMS != 2000 {
		t.Fatalf("command record = %#v", command)
	}
	if records[3].Title != "Implementation finished" || records[4].Title != "Independent verification started" || records[5].Title != "Independent verification passed" {
		t.Fatalf("result/verification records = %#v", records[3:])
	}
}

func TestProjectTrajectoryDistinguishesAttemptAndRuntimeStarts(t *testing.T) {
	at := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	events := []domain.RunEvent{
		trajectoryEvent(t, 1, "attempt.started", "app", at, `{}`),
		trajectoryEvent(t, 2, "agent_start", "adapter", at.Add(time.Second), `{}`),
		trajectoryEvent(t, 3, "thread.started", "adapter", at.Add(2*time.Second), `{}`),
	}

	records := projectTrajectory(events, domain.RunStateRunning)
	if len(records) != 3 || records[0].Title != "Attempt created" || records[1].Title != "Agent execution started" || records[2].Title != "Agent thread started" {
		t.Fatalf("start records = %#v", records)
	}
}

func TestProjectTrajectoryKeepsExitCodeCompletedCommandAtTerminalState(t *testing.T) {
	at := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	events := []domain.RunEvent{
		trajectoryEvent(t, 1, "tool_start", "adapter", at, `{"tool_name":"bash","redacted_command_summary":"go test ./internal/domain"}`),
		trajectoryEvent(t, 2, "exit_code", "adapter", at.Add(time.Second), `{"exit_code":0}`),
	}

	records := projectTrajectory(events, domain.RunStateAwaitingReview)
	if len(records) != 1 || records[0].Status != "completed" || records[0].EndSequence != 2 || records[0].DurationMS == nil || *records[0].DurationMS != 1000 {
		t.Fatalf("command record = %#v", records)
	}
}

func TestProjectTrajectoryIgnoresRawSecretsAndUnapprovedNormalizedFields(t *testing.T) {
	at := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	event, err := domain.NewRunEvent(domain.RunEventParams{
		ID: domain.NewEventID(), RunID: domain.NewRunID(), Sequence: 1, Type: "tool_start", Source: "adapter",
		OccurredAt: at, RecordedAt: at, NormalizedJSON: []byte(`{"tool_name":"bash","redacted_command_summary":"[redacted sensitive command]","prompt":"do not expose"}`),
		RawJSON: []byte(`{"password":"swordfish","path":"/Users/alice/private"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	records := projectTrajectory([]domain.RunEvent{event}, domain.RunStateRunning)
	if len(records) != 1 {
		t.Fatalf("records = %#v", records)
	}
	visible := records[0].Title + records[0].Summary
	for _, detail := range records[0].Details {
		visible += detail.Label + detail.Value
	}
	for _, forbidden := range []string{"swordfish", "/Users/alice", "do not expose", "password", "prompt"} {
		if strings.Contains(visible, forbidden) {
			t.Fatalf("trajectory exposed %q: %s", forbidden, visible)
		}
	}
	if records[0].Status != "running" || records[0].Summary != "[redacted sensitive command]" {
		t.Fatalf("redacted command record = %#v", records[0])
	}
}

func TestProjectTrajectorySanitizesApprovedTokenValues(t *testing.T) {
	at := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	events := []domain.RunEvent{
		trajectoryEvent(t, 1, "error", "adapter", at, `{"error_class":"swordfish"}`),
		trajectoryEvent(t, 2, "tool_start", "adapter", at.Add(time.Second), `{"tool_name":"secret"}`),
		trajectoryEvent(t, 3, "run.awaiting_verification", "adapter", at.Add(2*time.Second), `{"summary":"secret /Users/alice/private"}`),
	}

	records := projectTrajectory(events, domain.RunStateRunning)
	if len(records) != 3 {
		t.Fatalf("records = %#v", records)
	}
	visible := fmt.Sprintf("%#v", records)
	for _, forbidden := range []string{"swordfish", "secret", "/Users/alice"} {
		if strings.Contains(visible, forbidden) {
			t.Fatalf("trajectory exposed %q: %s", forbidden, visible)
		}
	}
	if records[0].Summary != "Execution stopped safely: runtime error." || records[1].Title != "Used tool" {
		t.Fatalf("sanitized records = %#v", records)
	}
}

func trajectoryEvent(t *testing.T, sequence int64, eventType, source string, at time.Time, normalized string) domain.RunEvent {
	t.Helper()
	event, err := domain.NewRunEvent(domain.RunEventParams{
		ID: domain.NewEventID(), RunID: domain.NewRunID(), Sequence: sequence, Type: eventType, Source: source,
		OccurredAt: at, RecordedAt: at, NormalizedJSON: []byte(normalized),
	})
	if err != nil {
		t.Fatal(err)
	}
	return event
}
