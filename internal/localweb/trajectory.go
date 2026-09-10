package localweb

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/Yangyang96/chora/internal/domain"
)

type trajectoryDetailView struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

type trajectoryView struct {
	Sequence    int64                  `json:"sequence"`
	EndSequence int64                  `json:"endSequence,omitempty"`
	Source      string                 `json:"source"`
	Kind        string                 `json:"kind"`
	Title       string                 `json:"title"`
	Summary     string                 `json:"summary"`
	Status      string                 `json:"status"`
	StartedAt   string                 `json:"startedAt"`
	EndedAt     string                 `json:"endedAt,omitempty"`
	DurationMS  *int64                 `json:"durationMs,omitempty"`
	Details     []trajectoryDetailView `json:"details"`
}

type normalizedTrajectoryEvent struct {
	ToolName string `json:"tool_name"`
	Path     string `json:"workspace_relative_path"`
	Command  string `json:"redacted_command_summary"`
	Error    string `json:"error_class"`
	Outcome  string `json:"outcome"`
	Terminal string `json:"terminal_kind"`
	ExitCode *int   `json:"exit_code"`
}

var safeTrajectoryTokenPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)

var safeTrajectoryToolNames = map[string]struct{}{
	"read": {}, "view": {}, "edit": {}, "write": {}, "str_replace_editor": {},
	"bash": {}, "shell": {}, "exec": {}, "exec_command": {},
}

var safeTrajectoryErrorClasses = map[string]struct{}{
	"runtime_error": {}, "undeclared_path": {}, "undeclared_command": {},
}

func projectTrajectory(events []domain.RunEvent, runState domain.RunState) []trajectoryView {
	records := make([]trajectoryView, 0, len(events))
	pendingTools := make(map[string][]int)
	for _, event := range events {
		var payload normalizedTrajectoryEvent
		_ = json.Unmarshal(event.NormalizedJSON(), &payload)
		if event.Type() == "tool_end" {
			indexes := pendingTools[payload.ToolName]
			if len(indexes) == 0 {
				continue
			}
			index := indexes[0]
			pendingTools[payload.ToolName] = indexes[1:]
			completeTrajectoryTool(&records[index], event, payload)
			continue
		}
		if event.Type() == "exit_code" {
			for index := len(records) - 1; index >= 0; index-- {
				if records[index].Kind == "command" && records[index].Status == "running" {
					completeTrajectoryTool(&records[index], event, payload)
					break
				}
			}
			continue
		}
		record, ok := trajectoryRecord(event, payload)
		if !ok {
			continue
		}
		records = append(records, record)
		if event.Type() == "tool_start" {
			pendingTools[payload.ToolName] = append(pendingTools[payload.ToolName], len(records)-1)
		}
	}
	if runState != domain.RunStateRunning && runState != domain.RunStateStopping {
		for indexes := range pendingTools {
			for _, index := range pendingTools[indexes] {
				if records[index].Status == "running" {
					records[index].Status = "interrupted"
				}
			}
		}
	}
	return records
}

func trajectoryRecord(event domain.RunEvent, payload normalizedTrajectoryEvent) (trajectoryView, bool) {
	record := trajectoryView{
		Sequence: event.Sequence(), Source: trajectorySource(event.Source()), Status: "completed",
		StartedAt: event.OccurredAt().Format(time.RFC3339Nano), Details: []trajectoryDetailView{{Label: "Event", Value: event.Type()}},
	}
	switch event.Type() {
	case "run.prepared":
		record.Kind, record.Title, record.Summary = "system", "Context and acceptance frozen", "Chora bound the Task, Plan, writable files, and checks before execution."
	case "run.started":
		record.Kind, record.Title, record.Summary = "system", "Run started", "Execution entered the declared reversible boundary."
	case "attempt.started":
		record.Kind, record.Title, record.Summary = "agent", "Attempt created", "Chora created the managed Attempt and bound it to this Run."
	case "agent_start":
		record.Kind, record.Title, record.Summary = "agent", "Agent execution started", "The Agent Runtime began the model and tool loop."
	case "thread.started":
		record.Kind, record.Title, record.Summary = "agent", "Agent thread started", "The adopted Agent Runtime opened its execution thread."
	case "tool_start":
		return trajectoryToolStart(record, payload), true
	case "file_path":
		if payload.Path == "" {
			return trajectoryView{}, false
		}
		record.Kind, record.Title, record.Summary = "file", "File observed", payload.Path
		record.Details = append(record.Details, trajectoryDetailView{Label: "File", Value: payload.Path})
	case "command_summary":
		if payload.Command == "" {
			return trajectoryView{}, false
		}
		record.Kind, record.Title, record.Summary = "command", "Command recorded", payload.Command
		record.Details = append(record.Details, trajectoryDetailView{Label: "Command", Value: payload.Command})
	case "agent_end":
		record.Kind, record.Title, record.Summary = "agent", "Agent finished", "The model loop stopped and Chora began collecting bounded output."
	case "run.awaiting_verification":
		record.Kind, record.Title = "result", "Implementation finished"
		record.Summary = "The Agent returned bounded output for independent verification."
		if terminal := safeTrajectoryToken(payload.Terminal, ""); terminal != "" {
			record.Details = append(record.Details, trajectoryDetailView{Label: "Agent outcome", Value: terminal})
		}
	case "verification.started", "verification.retried":
		record.Kind, record.Title, record.Summary = "check", "Independent verification started", "A fresh verifier workspace is running the frozen acceptance commands."
	case "verification.review_ready":
		record.Kind, record.Title, record.Summary = "check", "Independent verification passed", "All criterion-bound checks passed; the exact Patch is ready for review."
	case "verification.needs_revision":
		record.Kind, record.Title, record.Summary, record.Status = "check", "Independent verification found a gap", "A failed or unknown check preserved the Patch for revision.", "failed"
	case "verification.cancelled":
		record.Kind, record.Title, record.Summary, record.Status = "check", "Verification cancelled", "Verifier process death and cleanup were proven.", "cancelled"
	case "verification.recovery_required":
		record.Kind, record.Title, record.Summary, record.Status = "error", "Verification recovery required", "Verification continuity or cleanup could not be proven.", "failed"
	case "decision.requested":
		record.Kind, record.Title, record.Summary, record.Status = "decision", "Decision requested", "The Agent paused for a bounded human decision.", "waiting"
	case "decision.resolved":
		record.Kind, record.Title, record.Summary = "decision", "Decision recorded", "Execution resumed from the persisted human choice."
	case "review.accepted":
		record.Kind, record.Title, record.Summary = "decision", "Result accepted", "The human accepted the exact reviewed Patch."
	case "review.rejected":
		record.Kind, record.Title, record.Summary, record.Status = "decision", "Revision requested", "The result was returned to the Agent with bounded instructions.", "waiting"
	case "patch_application.started":
		record.Kind, record.Title, record.Summary, record.Status = "apply", "Applying accepted Patch", "Chora persisted the exact Patch and target binding before writing.", "running"
	case "patch_application.applied":
		record.Kind, record.Title, record.Summary = "apply", "Patch applied", "The accepted Patch is present in the local working tree and remains uncommitted."
	case "patch_application.conflict", "patch_application.recovery_required", "patch_inspection.conflict", "patch_inspection.recovery_required":
		title, detail := eventCopy(event.Type())
		record.Kind, record.Title, record.Summary, record.Status = "error", title, detail, "failed"
	case "attempt.failed", "attempt.start_failed", "attempt.reconciliation_required", "run.recovery_required", "error", "adapter.parse_error":
		title, detail := eventCopy(event.Type())
		record.Kind, record.Title, record.Summary, record.Status = "error", title, detail, "failed"
		if payload.Error != "" {
			errorClass := safeTrajectoryErrorClass(payload.Error)
			record.Summary = "Execution stopped safely: " + strings.ReplaceAll(errorClass, "_", " ") + "."
			record.Details = append(record.Details, trajectoryDetailView{Label: "Failure class", Value: errorClass})
		}
	case "run.stopping":
		record.Kind, record.Title, record.Summary, record.Status = "system", "Stopping Agent", "Chora requested termination and resource cleanup.", "running"
	case "run.stop_confirmed":
		record.Kind, record.Title, record.Summary = "system", "Agent stopped", "Termination and cleanup were confirmed."
	default:
		return trajectoryView{}, false
	}
	if outcome := safeTrajectoryToken(payload.Outcome, ""); outcome != "" {
		record.Details = append(record.Details, trajectoryDetailView{Label: "Outcome", Value: outcome})
	}
	return record, true
}

func trajectoryToolStart(record trajectoryView, payload normalizedTrajectoryEvent) trajectoryView {
	record.Status = "running"
	toolName := safeTrajectoryToolName(payload.ToolName)
	tool := strings.ToLower(toolName)
	switch tool {
	case "read", "view":
		record.Kind, record.Title = "read", "Read file"
	case "edit", "write", "str_replace_editor":
		record.Kind, record.Title = "edit", "Edited file"
	case "bash", "shell", "exec", "exec_command":
		record.Kind, record.Title = "command", "Ran command"
	default:
		record.Kind, record.Title = "tool", "Used "+nonEmpty(toolName, "tool")
	}
	record.Summary = nonEmpty(payload.Path, payload.Command, nonEmpty(toolName, "Bounded tool call"))
	if payload.Path != "" {
		record.Details = append(record.Details, trajectoryDetailView{Label: "File", Value: payload.Path})
	}
	if payload.Command != "" {
		record.Details = append(record.Details, trajectoryDetailView{Label: "Command", Value: payload.Command})
	}
	if toolName != "" {
		record.Details = append(record.Details, trajectoryDetailView{Label: "Tool", Value: toolName})
	}
	return record
}

func completeTrajectoryTool(record *trajectoryView, event domain.RunEvent, payload normalizedTrajectoryEvent) {
	if record == nil {
		return
	}
	record.EndSequence = event.Sequence()
	record.EndedAt = event.OccurredAt().Format(time.RFC3339Nano)
	record.Status = "completed"
	if payload.ExitCode != nil {
		record.Details = append(record.Details, trajectoryDetailView{Label: "Exit code", Value: fmt.Sprintf("%d", *payload.ExitCode)})
		if *payload.ExitCode != 0 {
			record.Status = "failed"
		}
	}
	started, startErr := time.Parse(time.RFC3339Nano, record.StartedAt)
	if startErr == nil && !event.OccurredAt().Before(started) {
		duration := event.OccurredAt().Sub(started).Milliseconds()
		record.DurationMS = &duration
	}
}

func trajectorySource(source string) string {
	switch source {
	case "app":
		return "Chora"
	case "adapter":
		return "Agent"
	case "verifier":
		return "Verifier"
	case "human":
		return "You"
	default:
		return "Chora"
	}
}

func nonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func safeTrajectoryToken(value, fallback string) string {
	value = strings.TrimSpace(value)
	if !safeTrajectoryTokenPattern.MatchString(value) {
		return fallback
	}
	return value
}

func safeTrajectoryToolName(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if _, ok := safeTrajectoryToolNames[value]; !ok {
		return ""
	}
	return value
}

func safeTrajectoryErrorClass(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if _, ok := safeTrajectoryErrorClasses[value]; !ok {
		return "runtime_error"
	}
	return value
}
