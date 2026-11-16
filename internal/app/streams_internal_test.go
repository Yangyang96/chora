package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
	"github.com/Yangyang96/chora/internal/speccoding"
)

func TestRuntimePolicyViolationRecognizesUndeclaredCommand(t *testing.T) {
	for _, errorClass := range []string{"undeclared_path", "undeclared_command"} {
		normalized, err := json.Marshal(map[string]string{"event_type": "error", "error_class": errorClass})
		if err != nil {
			t.Fatal(err)
		}
		if !hasRuntimePolicyViolation([]execution.NormalizedEvent{{Type: "error", NormalizedJSON: normalized}}) {
			t.Fatalf("policy violation %q was ignored", errorClass)
		}
	}
}

func TestCurrentLocalModesDoNotUseLegacyDeclaredCommandWhitelist(t *testing.T) {
	if choraEnforcesDeclaredPiCommands(domain.CapabilityEnvelope{speccoding.LocalConnectedNoSandboxCapability: true}) {
		t.Fatal("Local Connected must use Pi's native command/file permission policy")
	}
	if choraEnforcesDeclaredPiCommands(domain.CapabilityEnvelope{speccoding.IsolatedLocalCapability: true}) {
		t.Fatal("Isolated Local auto checks must not be rejected by the legacy direct argv whitelist")
	}
	for _, capabilities := range []domain.CapabilityEnvelope{
		{"runtime.command_execution": true},
		{},
	} {
		if !choraEnforcesDeclaredPiCommands(capabilities) {
			t.Fatalf("unrelated managed capabilities must retain the legacy declared-command boundary: %#v", capabilities)
		}
	}
}

func TestAgentReportedWithoutVerifierIncludesBothLocalModes(t *testing.T) {
	if !agentReportedWithoutVerifier(domain.CapabilityEnvelope{speccoding.LocalConnectedNoSandboxCapability: true}) {
		t.Fatal("Local Connected must retain direct human review semantics")
	}
	if !agentReportedWithoutVerifier(domain.CapabilityEnvelope{speccoding.IsolatedLocalCapability: true}) {
		t.Fatal("Isolated Local must use agent-reported review without an independent verifier")
	}
	if agentReportedWithoutVerifier(domain.CapabilityEnvelope{"runtime.command_execution": true}) {
		t.Fatal("unrelated managed capability must not bypass verification")
	}
}

func TestProjectDeclaredCommandsAllowsOnlyExactOrBoundedDerivedArgv(t *testing.T) {
	policy := declaredCommandPolicy{
		exact:         map[string]struct{}{"go test ./...": {}},
		gofmtFiles:    map[string]struct{}{"internal/domain/task.go": {}, "internal/domain/task_test.go": {}},
		focusedGoTest: true,
	}
	input := []execution.NormalizedEvent{
		{Type: "tool_start", NormalizedJSON: []byte(`{"event_type":"tool_start","tool_name":"bash","redacted_command_summary":"go test ./internal/domain"}`)},
		{Type: "tool_start", NormalizedJSON: []byte(`{"event_type":"tool_start","tool_name":"bash","redacted_command_summary":"gofmt -w internal/domain/task.go"}`)},
		{Type: "tool_start", NormalizedJSON: []byte(`{"event_type":"tool_start","tool_name":"bash","redacted_command_summary":"git status"}`)},
	}
	projected, violated := projectDeclaredCommands(input, policy)
	if !violated || projected[0].Type != "tool_start" || projected[1].Type != "tool_start" || projected[2].Type != "error" ||
		!hasRuntimePolicyViolation(projected[2:]) {
		t.Fatalf("projected=%#v violated=%v", projected, violated)
	}
}

func TestDeclaredCommandPolicyRejectsShellAndBoundaryExpansion(t *testing.T) {
	policy := declaredCommandPolicy{
		exact:         map[string]struct{}{"go test ./...": {}},
		gofmtFiles:    map[string]struct{}{"internal/domain/task.go": {}},
		focusedGoTest: true,
	}
	for _, command := range []string{
		"gofmt -w internal/localweb/server.go",
		"gofmt -w internal/domain/task.go && git status",
		"go test -run TestTask ./internal/domain",
		"go test ../outside",
		"go test ./internal/domain;git status",
		"go test  ./internal/domain",
	} {
		if declaredCommandAllowed(command, policy) {
			t.Fatalf("unsafe derived command passed: %q", command)
		}
	}
}

func TestDerivePiAgentReportedChecksCorrelatesDeclaredCommandsByToolCallID(t *testing.T) {
	document := claimTestDocument(
		[]speccoding.BoundedCommand{
			{ID: "verify-app", Argv: []string{"go", "test", "./internal/app"}},
			{ID: "verify-pi", Argv: []string{"go", "test", "./internal/agent/pi"}},
		},
		[]speccoding.AcceptanceCriterionContract{
			{ID: "criterion-app", VerificationCommandIDs: []string{"verify-app"}},
			{ID: "criterion-pi", VerificationCommandIDs: []string{"verify-pi"}},
		},
	)
	events := []agentClaimEvent{
		claimEvent("tool_start", `{"tool_name":"bash","tool_call_id":"app-call","redacted_command_summary":"go test ./internal/app"}`),
		claimEvent("tool_start", `{"tool_name":"bash","tool_call_id":"pi-call","redacted_command_summary":"go test ./internal/agent/pi"}`),
		// Reverse completion order proves correlation is by Pi's call identity,
		// not adjacency or command completion order.
		claimEvent("tool_end", `{"tool_name":"bash","tool_call_id":"pi-call","exit_code":7}`),
		claimEvent("tool_end", `{"tool_name":"bash","tool_call_id":"app-call","exit_code":0}`),
	}

	checks := derivePiAgentReportedChecks(document, events)
	if len(checks) != 2 || checks[0].Status != execution.CheckPass || checks[1].Status != execution.CheckFail {
		t.Fatalf("checks = %#v", checks)
	}
	if !strings.Contains(checks[0].Evidence, "Agent-reported, non-authoritative") ||
		!strings.Contains(checks[0].Evidence, "verify-app exited 0") ||
		!strings.Contains(checks[1].Evidence, "verify-pi exited 7") {
		t.Fatalf("evidence = %#v", checks)
	}
}

func TestDerivePiAgentReportedChecksLeavesMissingMalformedAndNonVerificationToolsUnknown(t *testing.T) {
	commands := []speccoding.BoundedCommand{
		{ID: "missing-end", Argv: []string{"test", "missing"}},
		{ID: "malformed-end", Argv: []string{"test", "malformed"}},
		{ID: "missing-id", Argv: []string{"test", "missing-id"}},
		{ID: "read-tool", Argv: []string{"test", "read-tool"}},
		{ID: "not-exact", Argv: []string{"test", "exact"}},
	}
	criteria := make([]speccoding.AcceptanceCriterionContract, 0, len(commands))
	for _, command := range commands {
		criteria = append(criteria, speccoding.AcceptanceCriterionContract{ID: "criterion-" + command.ID, VerificationCommandIDs: []string{command.ID}})
	}
	document := claimTestDocument(commands, criteria)
	events := []agentClaimEvent{
		claimEvent("tool_start", `{"tool_name":"bash","tool_call_id":"missing","redacted_command_summary":"test missing"}`),
		claimEvent("tool_start", `{"tool_name":"bash","tool_call_id":"malformed","redacted_command_summary":"test malformed"}`),
		claimEvent("tool_end", `{"tool_name":"bash","tool_call_id":"malformed"}`),
		claimEvent("tool_start", `{"tool_name":"bash","redacted_command_summary":"test missing-id"}`),
		claimEvent("tool_end", `{"tool_name":"bash","tool_call_id":"invented","exit_code":0}`),
		claimEvent("tool_start", `{"tool_name":"read","tool_call_id":"read","redacted_command_summary":"test read-tool"}`),
		claimEvent("tool_end", `{"tool_name":"read","tool_call_id":"read","exit_code":0}`),
		claimEvent("tool_start", `{"tool_name":"bash","tool_call_id":"suffix","redacted_command_summary":"test exact --extra"}`),
		claimEvent("tool_end", `{"tool_name":"bash","tool_call_id":"suffix","exit_code":0}`),
		{typ: "tool_end", normalized: []byte(`{"tool_call_id":`)},
	}

	checks := derivePiAgentReportedChecks(document, events)
	statuses := make([]execution.CheckStatus, 0, len(checks))
	for _, check := range checks {
		statuses = append(statuses, check.Status)
		if !strings.Contains(check.Evidence, "Agent-reported, non-authoritative") {
			t.Fatalf("claim lacks trust disclosure: %#v", check)
		}
	}
	want := []execution.CheckStatus{
		execution.CheckUnknown, execution.CheckUnknown, execution.CheckUnknown,
		execution.CheckUnknown, execution.CheckUnknown,
	}
	if !reflect.DeepEqual(statuses, want) {
		t.Fatalf("statuses = %#v want %#v", statuses, want)
	}
}

func TestDerivePiAgentReportedChecksMergesCriterionOutcomes(t *testing.T) {
	document := claimTestDocument(
		[]speccoding.BoundedCommand{
			{ID: "pass", Argv: []string{"check", "pass"}},
			{ID: "fail", Argv: []string{"check", "fail"}},
			{ID: "unknown", Argv: []string{"check", "unknown"}},
		},
		[]speccoding.AcceptanceCriterionContract{
			{ID: "all-pass", VerificationCommandIDs: []string{"pass"}},
			{ID: "has-fail", VerificationCommandIDs: []string{"pass", "fail", "unknown"}},
			{ID: "has-unknown", VerificationCommandIDs: []string{"pass", "unknown"}},
		},
	)
	events := []agentClaimEvent{
		claimEvent("tool_start", `{"tool_name":"bash","tool_call_id":"pass","redacted_command_summary":"check pass"}`),
		claimEvent("tool_end", `{"tool_name":"bash","tool_call_id":"pass","exit_code":0}`),
		claimEvent("tool_start", `{"tool_name":"bash","tool_call_id":"fail","redacted_command_summary":"check fail"}`),
		claimEvent("tool_end", `{"tool_name":"bash","tool_call_id":"fail","tool_error":true}`),
	}

	checks := derivePiAgentReportedChecks(document, events)
	want := []execution.CheckStatus{execution.CheckPass, execution.CheckFail, execution.CheckUnknown}
	got := []execution.CheckStatus{checks[0].Status, checks[1].Status, checks[2].Status}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("statuses = %#v want %#v", got, want)
	}
}

func claimTestDocument(commands []speccoding.BoundedCommand, criteria []speccoding.AcceptanceCriterionContract) speccoding.CoreContractDocument {
	return speccoding.CoreContractDocument{Acceptance: speccoding.AcceptanceContract{VerificationCommands: commands, Criteria: criteria}}
}

func claimEvent(typ, normalized string) agentClaimEvent {
	return agentClaimEvent{typ: typ, normalized: []byte(normalized)}
}

func TestNoSelectedChecksNeverSynthesizePass(t *testing.T) {
	document := claimTestDocument(nil, []speccoding.AcceptanceCriterionContract{{ID: "criterion-no-check"}})
	checks := derivePiAgentReportedChecks(document, nil)
	if len(checks) != 1 || checks[0].Status != execution.CheckUnknown || !strings.Contains(checks[0].Evidence, "Not run / Unverified") {
		t.Fatalf("no-check result must remain unverified: %#v", checks)
	}
}

func TestProjectCheckWorkingDirectoryMustMatchObservedCommand(t *testing.T) {
	document := claimTestDocument([]speccoding.BoundedCommand{{ID: "custom", Argv: []string{"node", "check.js"}, WorkingDirectory: "tools"}}, []speccoding.AcceptanceCriterionContract{{ID: "criterion-custom", VerificationCommandIDs: []string{"custom"}}})
	wrong := []agentClaimEvent{claimEvent("tool_start", `{"tool_name":"bash","tool_call_id":"c","redacted_command_summary":"node check.js"}`), claimEvent("tool_end", `{"tool_call_id":"c","exit_code":0}`)}
	if checks := derivePiAgentReportedChecks(document, wrong); checks[0].Status != execution.CheckUnknown {
		t.Fatalf("root command claimed subdir check: %#v", checks)
	}
	exact := []agentClaimEvent{claimEvent("tool_start", `{"tool_name":"bash","tool_call_id":"c","redacted_command_summary":"cd tools && node check.js"}`), claimEvent("tool_end", `{"tool_call_id":"c","exit_code":0}`)}
	if checks := derivePiAgentReportedChecks(document, exact); checks[0].Status != execution.CheckPass {
		t.Fatalf("exact subdir check not observed: %#v", checks)
	}
}

func TestDerivePiAgentReportedChecksMatchesRedactedAndTruncatedCommandsByDigest(t *testing.T) {
	longTail := "LONG-COMMAND-TAIL-MUST-NOT-PERSIST"
	commands := []speccoding.BoundedCommand{
		{ID: "sensitive", Argv: []string{"custom-check", "token-marker"}},
		{ID: "long", Argv: []string{"custom-check", strings.Repeat("x", 320) + longTail}},
	}
	document := claimTestDocument(commands, []speccoding.AcceptanceCriterionContract{
		{ID: "criterion-sensitive", VerificationCommandIDs: []string{"sensitive"}},
		{ID: "criterion-long", VerificationCommandIDs: []string{"long"}},
	})
	sensitiveDigest := canonicalCommandDigestForTest(t, commands[0])
	longDigest := canonicalCommandDigestForTest(t, commands[1])
	startSensitive, _ := json.Marshal(map[string]any{
		"tool_name": "bash", "tool_call_id": "sensitive-call", "redacted_command_summary": "[REDACTED]", "command_digest": sensitiveDigest,
	})
	startLong, _ := json.Marshal(map[string]any{
		"tool_name": "bash", "tool_call_id": "long-call", "redacted_command_summary": strings.Repeat("x", 256), "command_digest": longDigest,
	})
	if bytesContainEither(startSensitive, []byte("token-marker"), startLong, []byte(longTail)) {
		t.Fatal("test event leaked command text that should have been projected away")
	}
	events := []agentClaimEvent{
		{typ: "tool_start", normalized: startSensitive},
		claimEvent("tool_end", `{"tool_call_id":"sensitive-call","exit_code":0}`),
		{typ: "tool_start", normalized: startLong},
		claimEvent("tool_end", `{"tool_call_id":"long-call","exit_code":0}`),
	}
	checks := derivePiAgentReportedChecks(document, events)
	if len(checks) != 2 || checks[0].Status != execution.CheckPass || checks[1].Status != execution.CheckPass {
		t.Fatalf("digest-correlated checks = %#v", checks)
	}
}

func TestDerivePiAgentReportedChecksDoesNotFallbackToSummaryWhenDigestPresent(t *testing.T) {
	command := speccoding.BoundedCommand{ID: "check", Argv: []string{"custom-check", "safe"}}
	document := claimTestDocument([]speccoding.BoundedCommand{command}, []speccoding.AcceptanceCriterionContract{{ID: "criterion", VerificationCommandIDs: []string{"check"}}})
	text, err := speccoding.CanonicalCommandText(command)
	if err != nil {
		t.Fatal(err)
	}
	start, _ := json.Marshal(map[string]any{
		"tool_name": "bash", "tool_call_id": "check-call", "redacted_command_summary": text, "command_digest": strings.Repeat("0", 64),
	})
	checks := derivePiAgentReportedChecks(document, []agentClaimEvent{
		{typ: "tool_start", normalized: start},
		claimEvent("tool_end", `{"tool_call_id":"check-call","exit_code":0}`),
	})
	if len(checks) != 1 || checks[0].Status != execution.CheckUnknown {
		t.Fatalf("mismatched digest fell back to summary: %#v", checks)
	}
}

func canonicalCommandDigestForTest(t *testing.T, command speccoding.BoundedCommand) string {
	t.Helper()
	text, err := speccoding.CanonicalCommandText(command)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(text))
	return hex.EncodeToString(digest[:])
}

func bytesContainEither(first, firstNeedle, second, secondNeedle []byte) bool {
	return strings.Contains(string(first), string(firstNeedle)) || strings.Contains(string(second), string(secondNeedle))
}

func TestPiNativeToolOutcomeDoesNotInventNumericExitCode(t *testing.T) {
	document := claimTestDocument([]speccoding.BoundedCommand{{ID: "custom", Argv: []string{"test", "-f", "file"}}}, []speccoding.AcceptanceCriterionContract{{ID: "criterion-custom", VerificationCommandIDs: []string{"custom"}}})
	events := []agentClaimEvent{claimEvent("tool_start", `{"tool_name":"bash","tool_call_id":"c","redacted_command_summary":"test -f file"}`), claimEvent("tool_end", `{"tool_call_id":"c","tool_error":false}`)}
	checks := derivePiAgentReportedChecks(document, events)
	if checks[0].Status != execution.CheckPass || !strings.Contains(checks[0].Evidence, "numeric exit code unavailable") {
		t.Fatalf("native outcome = %#v", checks)
	}
}

func TestNoChangeSuccessRequiresSelectedChecksToPass(t *testing.T) {
	for _, test := range []struct {
		status   execution.CheckStatus
		noChecks bool
		want     execution.TerminalResultKind
	}{
		{execution.CheckPass, false, execution.TerminalCompletedNoChange},
		{execution.CheckFail, false, execution.TerminalChecksFailed},
		{execution.CheckUnknown, false, execution.TerminalChecksIncomplete},
		{execution.CheckUnknown, true, execution.TerminalCompletedNoChange},
	} {
		if got := noChangeOutcome([]execution.DeclaredCheck{{Status: test.status}}, test.noChecks); got != test.want {
			t.Fatalf("%s/noChecks=%v => %s", test.status, test.noChecks, got)
		}
	}
	if noChangeOutcome(nil, false) != execution.TerminalChecksIncomplete {
		t.Fatal("missing checks became success")
	}
}
