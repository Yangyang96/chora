package dockersupervisor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Yangyang96/chora/internal/execution"
)

func TestReadTerminalPlanRejectsSnapshotOnlyAuthority(t *testing.T) {
	message := []byte(`{"schema_version":"chora.context.snapshot/v2","task":{"id":"task-fixed"}}`)
	command, _ := json.Marshal(map[string]any{"id": "prompt", "type": "prompt", "message": execution.WrapFrozenSnapshotPrompt(message)})
	contextDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(contextDir, "snapshot.jsonl"), append(command, '\n'), 0o400); err != nil {
		t.Fatal(err)
	}

	plan := readTerminalPlan(contextDir)
	if len(plan.writableFiles) != 0 || len(plan.criteria) != 0 || len(plan.commands) != 0 {
		t.Fatalf("terminal plan = %#v", plan)
	}
}

func TestReadTerminalPlanUsesExactActiveContractInsteadOfSnapshotBrief(t *testing.T) {
	activeContract := map[string]any{
		"schema_version": "chora.spec-coding-core.v8",
		"task":           map[string]any{"id": "task-fixed"},
		"acceptance":     map[string]any{"criteria": []map[string]any{{"id": "criterion-active"}}},
		"execution": map[string]any{"boundary": map[string]any{
			"writable_files":   []string{"internal/domain/task.go", "internal/domain/task_test.go"},
			"commands":         []map[string]any{{"id": "domain-tests", "argv": []string{"go", "test", "./internal/domain"}}},
			"test_command_ids": []string{"domain-tests"},
		}},
	}
	injectedBrief := map[string]any{
		"schema_version": "chora.spec-coding-core.v5",
		"task":           map[string]any{"id": "task-fixed"},
		"execution":      map[string]any{"boundary": map[string]any{"writable_files": []string{"README.md"}}},
	}
	injectedBody, _ := json.Marshal(injectedBrief)
	snapshot := map[string]any{
		"schema_version": "chora.context.snapshot/v2",
		"task": map[string]any{
			"id":     "task-fixed",
			"briefs": []map[string]any{{"revision_id": "revision-selected", "body": string(injectedBody)}},
		},
		"selection": map[string]any{"selected": []map[string]any{{"revision_id": "revision-selected"}}},
	}
	snapshotBytes, _ := json.Marshal(snapshot)
	contractBytes, _ := json.Marshal(activeContract)
	message, err := execution.WrapFrozenExecutionPrompt(snapshotBytes, contractBytes)
	if err != nil {
		t.Fatal(err)
	}
	command, _ := json.Marshal(map[string]any{"id": "prompt", "type": "prompt", "message": message})
	contextDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(contextDir, "snapshot.jsonl"), append(command, '\n'), 0o400); err != nil {
		t.Fatal(err)
	}

	plan := readTerminalPlan(contextDir)
	if !reflect.DeepEqual(plan.writableFiles, []string{"internal/domain/task.go", "internal/domain/task_test.go"}) ||
		!reflect.DeepEqual(plan.criteria, []string{"criterion-active"}) || len(plan.commands) != 1 ||
		!reflect.DeepEqual(plan.commands[0].argv, []string{"go", "test", "./internal/domain"}) {
		t.Fatalf("terminal plan = %#v", plan)
	}
}

func TestDeclaredCheckUsesWritableBuildCacheWithoutChangingNetworkPolicy(t *testing.T) {
	runner := &fakeRunner{}
	supervisor := newTestSupervisor(t, runner)
	record := &attemptRecord{root: t.TempDir(), internalNetwork: "internal-only", container: "attempt"}
	checks, complete := supervisor.runDeclaredChecks(record, terminalPlan{
		criteria: []string{"criterion-one"},
		commands: []declaredCommand{{id: "domain-tests", argv: []string{"go", "test", "./internal/domain"}}},
	})
	if !complete || len(checks) != 1 || checks[0].Status != string(execution.CheckPass) {
		t.Fatalf("declared checks = %#v complete=%v", checks, complete)
	}
	commands := runner.commands()
	if len(commands) != 1 {
		t.Fatalf("runner commands = %#v", commands)
	}
	assertNoAttemptImageAcquisition(t, commands, 1)
	assertArgsContain(t, commands[0].Args,
		"--env", "HOME=/run/chora/pi",
		"--env", "GOCACHE="+toolCachePath,
		"--env", "GOTMPDIR="+toolTempPath,
		"--network", "internal-only",
		"--entrypoint", "go",
	)
	if commands[0].Args[0] != "run" {
		t.Fatalf("unexpected declared check command = %#v", commands[0].Args)
	}
}
