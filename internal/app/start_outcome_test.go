package app

import (
	"crypto/sha256"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/execution"
	"github.com/Yangyang96/chora/internal/speccoding"
)

func TestRegisteredExecutionSchemaIncludesLocalConnectedV11(t *testing.T) {
	for _, schema := range []string{
		speccoding.CoreContractSchemaVersionV5,
		speccoding.CoreContractSchemaVersionV6,
		speccoding.CoreContractSchemaVersionV7,
		speccoding.CoreContractSchemaVersionV8,
		speccoding.CoreContractSchemaVersionV9,
		speccoding.CoreContractSchemaVersionV10,
		speccoding.CoreContractSchemaVersionV11, speccoding.CoreContractSchemaVersionV12,
	} {
		if !registeredExecutionSchema(schema) {
			t.Fatalf("registered execution schema %q was rejected", schema)
		}
	}
	if registeredExecutionSchema("chora.spec-coding-core.v13") {
		t.Fatal("unknown registered execution schema was accepted")
	}
}

func TestValidStartOutcomeRequiresExclusiveMatchingLaunchProof(t *testing.T) {
	launch := execution.LaunchToken{Value: "launch-1"}
	other := execution.LaunchToken{Value: "launch-2"}
	tests := []struct {
		name    string
		outcome execution.StartOutcome
		valid   bool
	}{
		{name: "started", outcome: execution.StartOutcome{Kind: execution.Started, Handle: execution.RuntimeHandle{Value: "handle"}, Identity: execution.ProcessIdentity{Value: "pid:1"}, LaunchToken: launch}, valid: true},
		{name: "started missing handle", outcome: execution.StartOutcome{Kind: execution.Started, Identity: execution.ProcessIdentity{Value: "pid:1"}, LaunchToken: launch}},
		{name: "started mismatched launch", outcome: execution.StartOutcome{Kind: execution.Started, Handle: execution.RuntimeHandle{Value: "handle"}, Identity: execution.ProcessIdentity{Value: "pid:1"}, LaunchToken: other}},
		{name: "proven no child", outcome: execution.StartOutcome{Kind: execution.StartProvenNoChild, LaunchToken: launch}, valid: true},
		{name: "proven no child has handle", outcome: execution.StartOutcome{Kind: execution.StartProvenNoChild, Handle: execution.RuntimeHandle{Value: "handle"}, LaunchToken: launch}},
		{name: "proven no child has identity", outcome: execution.StartOutcome{Kind: execution.StartProvenNoChild, Identity: execution.ProcessIdentity{Value: "pid:1"}, LaunchToken: launch}},
		{name: "reconcile by identity", outcome: execution.StartOutcome{Kind: execution.StartReconciliationRequired, Identity: execution.ProcessIdentity{Value: "pid:1"}, LaunchToken: launch}, valid: true},
		{name: "reconcile by launch token", outcome: execution.StartOutcome{Kind: execution.StartReconciliationRequired, LaunchToken: launch}, valid: true},
		{name: "reconcile diagnostic only", outcome: execution.StartOutcome{Kind: execution.StartReconciliationRequired, Diagnostic: "unknown"}},
		{name: "reconcile mismatched launch", outcome: execution.StartOutcome{Kind: execution.StartReconciliationRequired, Identity: execution.ProcessIdentity{Value: "pid:1"}, LaunchToken: other}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := validStartOutcome(test.outcome, launch); got != test.valid {
				t.Fatalf("valid=%v want=%v outcome=%#v", got, test.valid, test.outcome)
			}
		})
	}
}

func TestCommandKeyUsesCanonicalTerminalSemanticsAndIgnoresCallerDigest(t *testing.T) {
	service := &Service{}
	meta := CommandMeta{ActorID: "owner", SessionID: "session", IdempotencyKey: "same", RequestDigest: sha256.Sum256([]byte("caller-a"))}
	first, err := service.commandKey(meta, "submit_terminal", "run-1", 4, execution.TerminalResult{Kind: execution.TerminalFailed, FailureReason: "first"}, time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	changed, err := service.commandKey(meta, "submit_terminal", "run-1", 4, execution.TerminalResult{Kind: execution.TerminalFailed, FailureReason: "second"}, time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	if first.RequestDigest == changed.RequestDigest {
		t.Fatal("terminal payload change did not change server digest")
	}
	meta.RequestDigest = sha256.Sum256([]byte("caller-b"))
	sameSemantic, err := service.commandKey(meta, "submit_terminal", "run-1", 4, execution.TerminalResult{Kind: execution.TerminalFailed, FailureReason: "first"}, time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	if first.RequestDigest != sameSemantic.RequestDigest {
		t.Fatal("caller digest changed server semantic digest")
	}
}

func TestCommandKeyFailsClosedWhenSemanticsCannotBeMarshaled(t *testing.T) {
	service := &Service{}
	meta := CommandMeta{ActorID: "owner", SessionID: "session", IdempotencyKey: "same"}
	if _, err := service.commandKey(meta, "unsupported", "resource", 1, make(chan int), time.Unix(1, 0)); err == nil {
		t.Fatal("unsupported command semantics were accepted")
	}
}
