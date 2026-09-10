package app_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
	"github.com/Yangyang96/chora/internal/speccoding"
)

func TestDeriveResourceChecksKeepsSameCommandsRepositoryScoped(t *testing.T) {
	first := resourceCheckTestResource(t, "write", "named")
	second := resourceCheckTestResource(t, "reference", "named")
	first.Checks.Commands = []domain.TaskCheckCommand{resourceCheckTestCommand("test", []string{"go", "test", "./..."}, ".")}
	second.Checks.Commands = []domain.TaskCheckCommand{resourceCheckTestCommand("test", []string{"go", "test", "./..."}, ".")}
	events := []execution.NormalizedEvent{
		resourceCheckEvent("tool_start", map[string]any{"tool_name": "bash", "tool_call_id": "first", "redacted_command_summary": "cd " + first.RepoID + " && go test ./..."}),
		resourceCheckEvent("tool_end", map[string]any{"tool_call_id": "first", "exit_code": 0}),
		resourceCheckEvent("tool_start", map[string]any{"tool_name": "bash", "tool_call_id": "second", "redacted_command_summary": "cd " + second.RepoID + " && go test ./..."}),
		resourceCheckEvent("tool_end", map[string]any{"tool_call_id": "second", "exit_code": 0}),
	}
	firstFingerprint := strings.Repeat("a", 64)
	secondFingerprint := strings.Repeat("b", 64)
	derived, err := app.DeriveResourceChecks([]speccoding.ExecutionRepositoryResource{first, second}, events, []app.ResourceContentFingerprintObservation{
		{AfterEvent: 2, RepositoryID: first.RepoID, Fingerprint: firstFingerprint},
		{AfterEvent: 4, RepositoryID: first.RepoID, Fingerprint: firstFingerprint},
		{AfterEvent: 4, RepositoryID: second.RepoID, Fingerprint: secondFingerprint},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(derived) != 2 || derived[0].RepositoryID != first.RepoID || derived[1].RepositoryID != second.RepoID {
		t.Fatalf("repository ordering/identity lost: %#v", derived)
	}
	for _, repository := range derived {
		if repository.Status != execution.CheckPass || !repository.FinalContentVerified || len(repository.Checks) != 1 || repository.Checks[0].Status != execution.CheckPass {
			t.Fatalf("repository check did not pass: %#v", repository)
		}
	}
	if derived[0].Checks[0].ToolCallID != "first" || derived[1].Checks[0].ToolCallID != "second" {
		t.Fatalf("tool calls crossed repository boundary: %#v", derived)
	}
}

func TestDeriveResourceChecksPreservesProviderSuccessWithoutInventingExitZero(t *testing.T) {
	resource := resourceCheckTestResource(t, "write", "named")
	resource.Checks.Commands = []domain.TaskCheckCommand{resourceCheckTestCommand("custom", []string{"test", "-f", "same.txt"}, ".")}
	events := []execution.NormalizedEvent{
		resourceCheckEvent("tool_start", map[string]any{"tool_name": "bash", "tool_call_id": "call", "redacted_command_summary": "cd " + resource.RepoID + " && test -f same.txt"}),
		resourceCheckEvent("tool_end", map[string]any{"tool_call_id": "call", "tool_error": false}),
	}
	derived, err := app.DeriveResourceChecks([]speccoding.ExecutionRepositoryResource{resource}, events, nil)
	if err != nil {
		t.Fatal(err)
	}
	check := derived[0].Checks[0]
	if check.ObservedStatus != execution.CheckPass || check.Status != execution.CheckUnknown || check.ProviderSucceeded == nil || !*check.ProviderSucceeded || check.ExitCode != nil || check.FinalContentVerified {
		t.Fatalf("provider-only outcome distorted: %#v", check)
	}
	if !strings.Contains(check.Evidence, "numeric exit code unavailable") || !strings.Contains(check.Evidence, "final repository contents were not proven") {
		t.Fatalf("provider-only evidence=%q", check.Evidence)
	}
}

func TestDeriveResourceChecksPreservesNumericFailure(t *testing.T) {
	resource := resourceCheckTestResource(t, "reference", "named")
	resource.Checks.Commands = []domain.TaskCheckCommand{resourceCheckTestCommand("read-check", []string{"go", "test", "./reader"}, ".")}
	events := []execution.NormalizedEvent{
		resourceCheckEvent("tool_start", map[string]any{"tool_name": "exec_command", "tool_call_id": "failure", "redacted_command_summary": "cd " + resource.RepoID + " && go test ./reader"}),
		resourceCheckEvent("tool_end", map[string]any{"tool_call_id": "failure", "tool_error": false, "exit_code": 7}),
	}
	derived, err := app.DeriveResourceChecks([]speccoding.ExecutionRepositoryResource{resource}, events, nil)
	if err != nil {
		t.Fatal(err)
	}
	check := derived[0].Checks[0]
	if check.Status != execution.CheckFail || check.ObservedStatus != execution.CheckFail || check.ExitCode == nil || *check.ExitCode != 7 || check.ProviderSucceeded == nil || !*check.ProviderSucceeded {
		t.Fatalf("numeric failure lost: %#v", check)
	}
}

func TestDeriveResourceChecksRejectsAmbiguousObservedCommands(t *testing.T) {
	resource := resourceCheckTestResource(t, "write", "named")
	resource.Checks.Commands = []domain.TaskCheckCommand{resourceCheckTestCommand("test", []string{"go", "test", "./..."}, ".")}
	other := domain.NewRepositoryID().String()
	commands := []string{
		"go test ./...",
		"cd " + resource.RepoID + " && go test ./... && echo chained",
		"cd /tmp && go test ./...",
		"cd " + other + " && go test ./...",
	}
	for _, command := range commands {
		events := []execution.NormalizedEvent{
			resourceCheckEvent("tool_start", map[string]any{"tool_name": "bash", "tool_call_id": "call", "redacted_command_summary": command}),
			resourceCheckEvent("tool_end", map[string]any{"tool_call_id": "call", "exit_code": 0}),
		}
		derived, err := app.DeriveResourceChecks([]speccoding.ExecutionRepositoryResource{resource}, events, nil)
		if err != nil {
			t.Fatal(err)
		}
		if derived[0].Checks[0].ObservedStatus != execution.CheckUnknown || derived[0].Checks[0].ToolCallID != "" {
			t.Fatalf("ambiguous command %q correlated: %#v", command, derived)
		}
	}
}

func TestDeriveResourceChecksRequiresFinalFingerprintAfterPossibleWrites(t *testing.T) {
	resource := resourceCheckTestResource(t, "write", "named")
	resource.Checks.Commands = []domain.TaskCheckCommand{resourceCheckTestCommand("test", []string{"go", "test", "./..."}, ".")}
	events := []execution.NormalizedEvent{
		resourceCheckEvent("tool_start", map[string]any{"tool_name": "bash", "tool_call_id": "check", "redacted_command_summary": "cd " + resource.RepoID + " && go test ./..."}),
		resourceCheckEvent("tool_end", map[string]any{"tool_call_id": "check", "exit_code": 0}),
		resourceCheckEvent("tool_start", map[string]any{"tool_name": "bash", "tool_call_id": "write", "redacted_command_summary": "cd " + resource.RepoID + " && touch generated.txt"}),
		resourceCheckEvent("tool_end", map[string]any{"tool_call_id": "write", "exit_code": 0}),
	}
	checked := strings.Repeat("c", 64)
	changed := strings.Repeat("d", 64)
	derived, err := app.DeriveResourceChecks([]speccoding.ExecutionRepositoryResource{resource}, events, []app.ResourceContentFingerprintObservation{
		{AfterEvent: 2, RepositoryID: resource.RepoID, Fingerprint: checked},
		{AfterEvent: 4, RepositoryID: resource.RepoID, Fingerprint: changed},
	})
	if err != nil {
		t.Fatal(err)
	}
	if check := derived[0].Checks[0]; check.ObservedStatus != execution.CheckPass || check.Status != execution.CheckUnknown || !check.Stale || check.FinalContentVerified {
		t.Fatalf("later bash write did not stale check: %#v", derived)
	}

	proved, err := app.DeriveResourceChecks([]speccoding.ExecutionRepositoryResource{resource}, events, []app.ResourceContentFingerprintObservation{
		{AfterEvent: 2, RepositoryID: resource.RepoID, Fingerprint: checked},
		{AfterEvent: 4, RepositoryID: resource.RepoID, Fingerprint: checked},
	})
	if err != nil {
		t.Fatal(err)
	}
	if check := proved[0].Checks[0]; check.Status != execution.CheckPass || check.Stale || !check.FinalContentVerified {
		t.Fatalf("exact final content proof did not restore pass: %#v", proved)
	}
}

func TestDeriveResourceChecksMatchesRedactedCanonicalCommandByDigest(t *testing.T) {
	resource := resourceCheckTestResource(t, "write", "named")
	command := resourceCheckTestCommand("secret-safe-id", []string{"custom-check", "token-value"}, "tools")
	resource.Checks.Commands = []domain.TaskCheckCommand{command}
	argv, err := speccoding.CanonicalCheckCommand(command.Argv)
	if err != nil {
		t.Fatal(err)
	}
	directory, err := speccoding.CanonicalCheckCommand([]string{"cd", resource.RepoID + "/tools"})
	if err != nil {
		t.Fatal(err)
	}
	text := directory + " && " + argv
	digest := sha256.Sum256([]byte(text))
	events := []execution.NormalizedEvent{
		resourceCheckEvent("tool_start", map[string]any{"tool_name": "bash", "tool_call_id": "redacted", "redacted_command_summary": "[REDACTED]", "command_digest": hex.EncodeToString(digest[:])}),
		resourceCheckEvent("tool_end", map[string]any{"tool_call_id": "redacted", "exit_code": 0}),
	}
	fingerprint := strings.Repeat("e", 64)
	derived, err := app.DeriveResourceChecks([]speccoding.ExecutionRepositoryResource{resource}, events, []app.ResourceContentFingerprintObservation{
		{AfterEvent: 2, RepositoryID: resource.RepoID, Fingerprint: fingerprint},
	})
	if err != nil {
		t.Fatal(err)
	}
	if check := derived[0].Checks[0]; check.Status != execution.CheckPass || check.ToolCallID != "redacted" || check.CommandDigest != hex.EncodeToString(digest[:]) {
		t.Fatalf("redacted command digest did not correlate: %#v", derived)
	}
}

func TestDeriveResourceChecksKeepsNoneAndNoApplicableAutoUnknown(t *testing.T) {
	none := resourceCheckTestResource(t, "write", "none")
	auto := resourceCheckTestResource(t, "reference", "auto")
	derived, err := app.DeriveResourceChecks([]speccoding.ExecutionRepositoryResource{none, auto}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(derived) != 2 || derived[0].Status != execution.CheckUnknown || derived[1].Status != execution.CheckUnknown ||
		!strings.Contains(derived[0].Explanation, "no checks were selected") || derived[1].NoApplicableChecks || !strings.Contains(derived[1].Explanation, "no automatic check selection") {
		t.Fatalf("empty check policy became success: %#v", derived)
	}
}

func TestDeriveResourceChecksAutoCandidatesAreOptionalUntilActuallySelected(t *testing.T) {
	resource := resourceCheckTestResource(t, "write", "auto")
	resource.Checks.Commands = []domain.TaskCheckCommand{
		resourceCheckTestCommand("unit", []string{"go", "test", "./unit"}, "."),
		resourceCheckTestCommand("integration", []string{"go", "test", "./integration"}, "."),
	}
	for index := range resource.Checks.Commands {
		resource.Checks.Commands[index].Source = "automatic"
	}
	events := []execution.NormalizedEvent{
		resourceCheckEvent("tool_start", map[string]any{"tool_name": "bash", "tool_call_id": "selected", "redacted_command_summary": "cd " + resource.RepoID + " && go test ./unit"}),
		resourceCheckEvent("tool_end", map[string]any{"tool_call_id": "selected", "exit_code": 0}),
	}
	derived, err := app.DeriveResourceChecks([]speccoding.ExecutionRepositoryResource{resource}, events, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(derived[0].Checks) != 1 || derived[0].Checks[0].CheckID != "unit" || derived[0].Checks[0].ToolCallID != "selected" || derived[0].Checks[0].ObservedStatus != execution.CheckPass {
		t.Fatalf("actual candidate selection=%#v", derived[0])
	}
	if derived[0].Checks[0].Status != execution.CheckUnknown || derived[0].Checks[0].Source != "automatic" || derived[0].SelectionSource != "automatic_candidate_execution" {
		t.Fatalf("candidate evidence axes=%#v", derived[0])
	}
}

func TestDeriveResourceChecksMatchesExplicitAgentProposalToActualRedactedToolCall(t *testing.T) {
	resource := resourceCheckTestResource(t, "write", "auto")
	selection := resourceCheckSelectionEvent(t, []map[string]any{{
		"repoId": resource.RepoID, "checks": []map[string]any{{"name": "Focused test", "command": "npm test -- foo", "workingDirectory": "."}},
		"noApplicableChecks": false, "explanation": "The changed component is covered by this focused test.",
	}})
	digest := resourceSemanticDigest(t, resource.RepoID, ".", []string{"npm", "test", "--", "foo"})
	events := []execution.NormalizedEvent{
		resourceCheckEvent("tool_start", map[string]any{"tool_name": "bash", "tool_call_id": "agent-check", "redacted_command_summary": "[REDACTED]", "command_digest": strings.Repeat("a", 64), "resource_command_digest": digest}),
		resourceCheckEvent("tool_end", map[string]any{"tool_call_id": "agent-check", "tool_error": false}),
		selection,
	}
	derived, err := app.DeriveResourceChecks([]speccoding.ExecutionRepositoryResource{resource}, events, nil)
	if err != nil {
		t.Fatal(err)
	}
	check := derived[0].Checks[0]
	if len(derived[0].Checks) != 1 || check.Source != "agent_proposed" || check.Version != 1 || !strings.HasPrefix(check.CheckID, "agent-") || check.ToolCallID != "agent-check" || check.ObservedStatus != execution.CheckPass || check.Status != execution.CheckUnknown || check.ExitCode != nil {
		t.Fatalf("agent proposal was not separately and truthfully derived: %#v", derived[0])
	}
	again, err := app.DeriveResourceChecks([]speccoding.ExecutionRepositoryResource{resource}, events, nil)
	if err != nil || again[0].Checks[0].CheckID != check.CheckID {
		t.Fatalf("agent proposal identity is unstable: %#v err=%v", again, err)
	}
}

func TestDeriveResourceChecksDoesNotGuessExplicitProposalWorkingDirectory(t *testing.T) {
	resource := resourceCheckTestResource(t, "write", "auto")
	selection := resourceCheckSelectionEvent(t, []map[string]any{{
		"repoId": resource.RepoID, "checks": []map[string]any{{"name": "Unit", "command": "go test ./...", "workingDirectory": "."}},
		"noApplicableChecks": false, "explanation": "Run repository tests.",
	}})
	events := []execution.NormalizedEvent{
		resourceCheckEvent("tool_start", map[string]any{"tool_name": "bash", "tool_call_id": "unknown-cwd", "redacted_command_summary": "go test ./..."}),
		resourceCheckEvent("tool_end", map[string]any{"tool_call_id": "unknown-cwd", "exit_code": 0}),
		selection,
	}
	derived, err := app.DeriveResourceChecks([]speccoding.ExecutionRepositoryResource{resource}, events, nil)
	if err != nil {
		t.Fatal(err)
	}
	if check := derived[0].Checks[0]; check.ToolCallID != "" || check.ObservedStatus != execution.CheckUnknown || !strings.Contains(check.Evidence, "not observed") {
		t.Fatalf("unknown cwd was guessed: %#v", derived[0])
	}
}

func TestDeriveResourceChecksRequiresExplicitNoApplicableSelection(t *testing.T) {
	resource := resourceCheckTestResource(t, "reference", "auto")
	events := []execution.NormalizedEvent{resourceCheckSelectionEvent(t, []map[string]any{{
		"repoId": resource.RepoID, "checks": []map[string]any{}, "noApplicableChecks": true, "explanation": "Documentation-only reference has no configured checks.",
	}})}
	derived, err := app.DeriveResourceChecks([]speccoding.ExecutionRepositoryResource{resource}, events, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !derived[0].NoApplicableChecks || derived[0].SelectionSource != "agent_proposed" || derived[0].SelectionExplanation == "" || derived[0].Status != execution.CheckUnknown || !strings.Contains(derived[0].Explanation, "explicitly reported") {
		t.Fatalf("explicit no-applicable evidence was lost: %#v", derived[0])
	}
}

func TestDeriveResourceChecksMalformedSelectionStaysIncomplete(t *testing.T) {
	resource := resourceCheckTestResource(t, "write", "auto")
	for name, repositories := range map[string][]map[string]any{
		"missing explanation": {{"repoId": resource.RepoID, "checks": []map[string]any{}, "noApplicableChecks": true, "explanation": ""}},
		"duplicate semantic command": {{
			"repoId":             resource.RepoID,
			"checks":             []map[string]any{{"name": "one", "command": "go test ./...", "workingDirectory": "."}, {"name": "two", "command": "go test ./...", "workingDirectory": "."}},
			"noApplicableChecks": false, "explanation": "duplicates are ambiguous",
		}},
	} {
		t.Run(name, func(t *testing.T) {
			derived, err := app.DeriveResourceChecks([]speccoding.ExecutionRepositoryResource{resource}, []execution.NormalizedEvent{resourceCheckSelectionEvent(t, repositories)}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if derived[0].Status != execution.CheckUnknown || derived[0].NoApplicableChecks || len(derived[0].Checks) != 0 || !strings.Contains(derived[0].Explanation, "selection is invalid") {
				t.Fatalf("malformed selection escaped as evidence: %#v", derived[0])
			}
		})
	}
}

func TestDeriveResourceChecksRetainsEveryCorrelatedInvocation(t *testing.T) {
	resource := resourceCheckTestResource(t, "write", "named")
	resource.Checks.Commands = []domain.TaskCheckCommand{resourceCheckTestCommand("unit", []string{"go", "test", "./unit"}, ".")}
	events := []execution.NormalizedEvent{
		resourceCheckEvent("tool_start", map[string]any{"tool_name": "bash", "tool_call_id": "first", "redacted_command_summary": "cd " + resource.RepoID + " && go test ./unit"}),
		resourceCheckEvent("tool_end", map[string]any{"tool_call_id": "first", "exit_code": 9}),
		resourceCheckEvent("tool_start", map[string]any{"tool_name": "bash", "tool_call_id": "second", "redacted_command_summary": "cd " + resource.RepoID + " && go test ./unit"}),
		resourceCheckEvent("tool_end", map[string]any{"tool_call_id": "second", "tool_error": false}),
	}
	derived, err := app.DeriveResourceChecks([]speccoding.ExecutionRepositoryResource{resource}, events, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(derived[0].Checks) != 1 || derived[0].Checks[0].ToolCallID != "second" || derived[0].Checks[0].ObservedStatus != execution.CheckPass {
		t.Fatalf("latest check summary=%#v", derived[0].Checks)
	}
	invocations := derived[0].Invocations
	if len(invocations) != 2 || invocations[0].ToolCallID != "first" || invocations[0].ObservedStatus != execution.CheckFail || invocations[0].StartEvent != 0 || invocations[0].EndEvent != 1 || invocations[1].ToolCallID != "second" || invocations[1].ObservedStatus != execution.CheckPass || invocations[1].StartEvent != 2 || invocations[1].EndEvent != 3 {
		t.Fatalf("invocation ledger=%#v", invocations)
	}
	if invocations[1].Status != execution.CheckUnknown || invocations[1].ExitCode != nil {
		t.Fatalf("provider success invented final proof or exit code: %#v", invocations[1])
	}
}

func TestDeriveResourceChecksRejectsDuplicateToolCallIdentityFromInvocationLedger(t *testing.T) {
	resource := resourceCheckTestResource(t, "write", "named")
	resource.Checks.Commands = []domain.TaskCheckCommand{resourceCheckTestCommand("unit", []string{"go", "test", "./unit"}, ".")}
	events := []execution.NormalizedEvent{
		resourceCheckEvent("tool_start", map[string]any{"tool_name": "bash", "tool_call_id": "duplicate", "redacted_command_summary": "cd " + resource.RepoID + " && go test ./unit"}),
		resourceCheckEvent("tool_end", map[string]any{"tool_call_id": "duplicate", "exit_code": 0}),
		resourceCheckEvent("tool_start", map[string]any{"tool_name": "bash", "tool_call_id": "duplicate", "redacted_command_summary": "cd " + resource.RepoID + " && go test ./unit"}),
		resourceCheckEvent("tool_end", map[string]any{"tool_call_id": "duplicate", "exit_code": 0}),
	}
	derived, err := app.DeriveResourceChecks([]speccoding.ExecutionRepositoryResource{resource}, events, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(derived[0].Invocations) != 0 || derived[0].Checks[0].ObservedStatus != execution.CheckUnknown {
		t.Fatalf("duplicate tool identity became evidence: %#v", derived[0])
	}
}

func resourceCheckTestResource(t *testing.T, role, mode string) speccoding.ExecutionRepositoryResource {
	t.Helper()
	id := domain.NewRepositoryID().String()
	return speccoding.ExecutionRepositoryResource{RepoID: id, Locator: id, Role: role, Checks: domain.TaskCheckPolicy{Mode: mode}}
}

func resourceCheckTestCommand(id string, argv []string, directory string) domain.TaskCheckCommand {
	return domain.TaskCheckCommand{ID: id, Name: id, Version: 1, Argv: argv, WorkingDirectory: directory, Source: "user"}
}

func resourceCheckEvent(eventType string, value map[string]any) execution.NormalizedEvent {
	value["event_type"] = eventType
	raw, _ := json.Marshal(value)
	return execution.NormalizedEvent{Type: eventType, NormalizedJSON: raw}
}

func resourceCheckSelectionEvent(t *testing.T, repositories []map[string]any) execution.NormalizedEvent {
	t.Helper()
	document, err := json.Marshal(map[string]any{"repositories": repositories})
	if err != nil {
		t.Fatal(err)
	}
	return resourceCheckEvent("assistant_message", map[string]any{
		"text":              "Result summary.\n\n```chora-check-selection\n" + string(document) + "\n```",
		"terminal_complete": true, "truncated": false,
	})
}

func resourceSemanticDigest(t *testing.T, repositoryID, workingDirectory string, argv []string) string {
	t.Helper()
	cwd := repositoryID
	if workingDirectory != "." {
		cwd += "/" + workingDirectory
	}
	canonical, err := json.Marshal(struct {
		Argv []string `json:"argv"`
		Cwd  string   `json:"cwd"`
	}{argv, cwd})
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(canonical)
	return hex.EncodeToString(digest[:])
}

func TestResourceSemanticDigestAttributesRedactedEquivalentQuoting(t *testing.T) {
	resource := resourceCheckTestResource(t, "write", "named")
	resource.Checks.Commands = []domain.TaskCheckCommand{resourceCheckTestCommand("quoted", []string{"custom-check", "token-value"}, "module space")}
	canonical, _ := json.Marshal(struct {
		Argv []string `json:"argv"`
		Cwd  string   `json:"cwd"`
	}{resource.Checks.Commands[0].Argv, resource.RepoID + "/module space"})
	identity := sha256.Sum256(canonical)
	events := []execution.NormalizedEvent{
		resourceCheckEvent("tool_start", map[string]any{"tool_name": "bash", "tool_call_id": "quoted-call", "redacted_command_summary": "[REDACTED]", "command_digest": strings.Repeat("a", 64), "resource_command_digest": hex.EncodeToString(identity[:])}),
		resourceCheckEvent("tool_end", map[string]any{"tool_call_id": "quoted-call", "tool_error": false}),
	}
	derived, err := app.DeriveResourceChecks([]speccoding.ExecutionRepositoryResource{resource}, events, nil)
	if err != nil {
		t.Fatal(err)
	}
	check := derived[0].Checks[0]
	if check.ToolCallID != "quoted-call" || check.ObservedStatus != execution.CheckPass || check.Status != execution.CheckUnknown || check.ExitCode != nil {
		t.Fatalf("incorrect evidence %+v", check)
	}
}

func TestDeriveResourceChecksRejectsUnprovenNestedProposalDirectories(t *testing.T) {
	for _, cwd := range []string{"linked", "ordinary/nested"} {
		t.Run(cwd, func(t *testing.T) {
			resource := resourceCheckTestResource(t, "write", "auto")
			selection := resourceCheckSelectionEvent(t, []map[string]any{{"repoId": resource.RepoID, "checks": []map[string]any{{"name": "Proposed", "command": "npm test", "workingDirectory": cwd}}, "noApplicableChecks": false, "explanation": "Agent selected this directory."}})
			events := []execution.NormalizedEvent{
				resourceCheckEvent("tool_start", map[string]any{"tool_name": "bash", "tool_call_id": "nested", "resource_command_digest": resourceSemanticDigest(t, resource.RepoID, cwd, []string{"npm", "test"})}),
				resourceCheckEvent("tool_end", map[string]any{"tool_call_id": "nested", "tool_error": false}), selection,
			}
			derived, err := app.DeriveResourceChecks([]speccoding.ExecutionRepositoryResource{resource}, events, nil)
			if err != nil {
				t.Fatal(err)
			}
			if derived[0].Status != execution.CheckUnknown || len(derived[0].Invocations) != 0 || !strings.Contains(derived[0].Explanation, "not proven") {
				t.Fatalf("unproven cwd attributed: %+v", derived[0])
			}
		})
	}
}

func TestDeriveResourceChecksShortLocatorsKeepRepositoryIdentity(t *testing.T) {
	first := resourceCheckTestResource(t, "write", "named")
	second := resourceCheckTestResource(t, "reference", "named")
	first.Checks.Commands = []domain.TaskCheckCommand{resourceCheckTestCommand("test", []string{"go", "test", "./..."}, ".")}
	second.Checks.Commands = []domain.TaskCheckCommand{resourceCheckTestCommand("test", []string{"go", "test", "./..."}, ".")}
	first.Locator = domain.ShortWorkspaceName(first.RepoID)
	second.Locator = domain.ShortWorkspaceName(second.RepoID)
	events := []execution.NormalizedEvent{
		resourceCheckEvent("tool_start", map[string]any{"tool_name": "bash", "tool_call_id": "first", "redacted_command_summary": "cd " + first.Locator + " && go test ./..."}),
		resourceCheckEvent("tool_end", map[string]any{"tool_call_id": "first", "exit_code": 0}),
		resourceCheckEvent("tool_start", map[string]any{"tool_name": "bash", "tool_call_id": "second", "redacted_command_summary": "cd " + second.Locator + " && go test ./..."}),
		resourceCheckEvent("tool_end", map[string]any{"tool_call_id": "second", "exit_code": 0}),
	}
	firstFingerprint := strings.Repeat("a", 64)
	secondFingerprint := strings.Repeat("b", 64)
	derived, err := app.DeriveResourceChecks([]speccoding.ExecutionRepositoryResource{first, second}, events, []app.ResourceContentFingerprintObservation{
		{AfterEvent: 2, RepositoryID: first.RepoID, Fingerprint: firstFingerprint},
		{AfterEvent: 4, RepositoryID: first.RepoID, Fingerprint: firstFingerprint},
		{AfterEvent: 4, RepositoryID: second.RepoID, Fingerprint: secondFingerprint},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(derived) != 2 || derived[0].RepositoryID != first.RepoID || derived[1].RepositoryID != second.RepoID {
		t.Fatalf("repository ordering/identity lost: %#v", derived)
	}
	for _, repository := range derived {
		if repository.Status != execution.CheckPass || !repository.FinalContentVerified || len(repository.Checks) != 1 || repository.Checks[0].Status != execution.CheckPass {
			t.Fatalf("repository check did not pass: %#v", repository)
		}
	}
	if derived[0].Checks[0].ToolCallID != "first" || derived[1].Checks[0].ToolCallID != "second" {
		t.Fatalf("tool calls crossed repository boundary: %#v", derived)
	}
}
