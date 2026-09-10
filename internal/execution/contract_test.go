package execution

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/Yangyang96/chora/internal/domain"
)

func TestFrozenExecutionPromptRequiresInAttemptRepairAndCarriesExactAuthorities(t *testing.T) {
	snapshotDocument := []byte(`{"task":{"id":"task-1"}}`)
	contractDocument := []byte(`{"schema_version":"chora.spec-coding-core.v8","task":{"id":"task-1"},"execution":{"boundary":{"writable_files":["internal/domain/task.go"]}}}`)
	prompt, err := WrapFrozenExecutionPrompt(snapshotDocument, contractDocument)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"Never read credential, authentication, environment, trust, or unrelated host files",
		"Never disclose secrets, personal information, vendor metadata, non-Chora content, or enterprise-project code",
		"Vendor exists only for the offline compiler and must not be inspected",
		"If a declared test fails",
		"read only Chora repository files named by the failing test output when needed to understand compatibility constraints",
		"repair only within the writable-file boundary",
		"rerun the exact declared commands until they pass or no compliant repair remains",
	} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("frozen prompt missing %q: %q", required, prompt)
		}
	}
	snapshot, contract, ok := UnwrapFrozenExecutionPrompt(prompt)
	if !ok || string(snapshot) != string(snapshotDocument) || string(contract) != string(contractDocument) {
		t.Fatalf("unwrapped execution input = snapshot %q contract %q valid %v", snapshot, contract, ok)
	}
}

func TestFrozenExecutionPromptRejectsMissingOrInvalidAuthority(t *testing.T) {
	valid := []byte(`{"valid":true}`)
	for name, input := range map[string][2][]byte{
		"missing snapshot": {nil, valid},
		"invalid snapshot": {[]byte(`{`), valid},
		"missing contract": {valid, nil},
		"invalid contract": {valid, []byte(`{`)},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := WrapFrozenExecutionPrompt(input[0], input[1]); err == nil {
				t.Fatal("accepted incomplete execution authority")
			}
		})
	}
}

func TestFrozenExecutionPromptRejectsTrailingInput(t *testing.T) {
	prompt, err := WrapFrozenExecutionPrompt([]byte(`{"snapshot":true}`), []byte(`{"contract":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, ok := UnwrapFrozenExecutionPrompt(prompt + ` true`); ok {
		t.Fatal("accepted trailing execution input")
	}
}

func TestInvocationClonesMutableInputsAndOutputs(t *testing.T) {
	args := []string{"run", "--json"}
	env := map[string]string{"HOME": "/tmp/home"}
	stdin := []byte("snapshot")
	invocation, err := NewInvocation(InvocationParams{
		AdapterID: "fake", Executable: "/usr/bin/fake", Arguments: args,
		Environment: env, WorkingRoot: "/workspace", Stdin: stdin,
		LaunchToken: LaunchToken{Value: "launch-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	args[0] = "mutated"
	env["HOME"] = "mutated"
	stdin[0] = 'X'
	if invocation.AdapterID() != "fake" || invocation.Executable() != "/usr/bin/fake" || invocation.WorkingRoot() != "/workspace" || invocation.LaunchToken().Value != "launch-1" {
		t.Fatalf("unexpected immutable fields: %#v", invocation)
	}
	if invocation.Target().AdapterID() != "fake" || invocation.Target().ProviderID() != "fake" {
		t.Fatalf("legacy target = %#v", invocation.Target())
	}
	gotArgs := invocation.Arguments()
	gotEnv := invocation.Environment()
	gotStdin := invocation.Stdin()
	if gotArgs[0] != "run" || gotEnv["HOME"] != "/tmp/home" || string(gotStdin) != "snapshot" {
		t.Fatalf("constructor did not clone inputs: %q %#v %q", gotArgs, gotEnv, gotStdin)
	}
	gotArgs[0] = "again"
	gotEnv["HOME"] = "again"
	gotStdin[0] = 'Y'
	if invocation.Arguments()[0] != "run" || invocation.Environment()["HOME"] != "/tmp/home" || string(invocation.Stdin()) != "snapshot" {
		t.Fatal("getters exposed mutable invocation state")
	}
}

func TestExecutionTargetIsStrictImmutableAndComparable(t *testing.T) {
	target, err := NewExecutionTarget("pi", domain.DockerExecutionProvider)
	if err != nil {
		t.Fatal(err)
	}
	if !target.Valid() || target.AdapterID() != "pi" || target.ProviderID() != domain.DockerExecutionProvider {
		t.Fatalf("target = %#v", target)
	}
	if reflect.TypeOf(target).Comparable() == false {
		t.Fatal("execution target is not comparable")
	}
	copy, err := NewExecutionTarget(target.AdapterID(), target.ProviderID())
	if err != nil || copy != target {
		t.Fatalf("copied target = %#v, %v", copy, err)
	}
	for _, ids := range [][2]string{{"", "docker"}, {"pi", ""}, {" pi", "docker"}, {"pi", "docker "}, {"p\ni", "docker"}, {"pi", "trusted host"}} {
		if got, err := NewExecutionTarget(ids[0], ids[1]); err == nil || got.Valid() {
			t.Fatalf("accepted invalid target %q/%q: %#v, %v", ids[0], ids[1], got, err)
		}
	}
}

func TestInvocationRequiresExactSuppliedTargetAdapter(t *testing.T) {
	target, err := NewExecutionTarget("pi", domain.DockerExecutionProvider)
	if err != nil {
		t.Fatal(err)
	}
	params := InvocationParams{
		AdapterID: "pi", Target: target, Executable: "/usr/bin/pi", WorkingRoot: "/workspace",
		LaunchToken: LaunchToken{Value: "launch-target"},
	}
	invocation, err := NewInvocation(params)
	if err != nil || invocation.Target() != target || !invocation.Valid() {
		t.Fatalf("targeted invocation = %#v, %v", invocation, err)
	}
	params.AdapterID = "fake"
	if _, err := NewInvocation(params); err == nil {
		t.Fatal("accepted invocation whose target adapter differs from AdapterID")
	}
}

func TestBoundStartPreparationIsStrictAndOptional(t *testing.T) {
	target, err := NewExecutionTarget("pi", domain.DockerExecutionProvider)
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := NewInvocation(InvocationParams{
		AdapterID: "pi", Target: target, Executable: "/usr/bin/pi", WorkingRoot: "/workspace",
		LaunchToken: LaunchToken{Value: "launch-bound"},
	})
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := RuntimeFingerprint{Digest: [32]byte{1}, Version: "pi-v1"}
	preparation, err := NewStartPreparation(invocation, fingerprint)
	if err != nil || !preparation.Valid() || preparation.Invocation.Target() != target || preparation.RuntimeFingerprint != fingerprint {
		t.Fatalf("preparation = %#v, %v", preparation, err)
	}
	if _, err := NewStartPreparation(Invocation{}, fingerprint); err == nil {
		t.Fatal("accepted preparation without invocation")
	}
	if _, err := NewStartPreparation(invocation, RuntimeFingerprint{}); err == nil {
		t.Fatal("accepted preparation without Runtime fingerprint")
	}
	typ := reflect.TypeOf((*AgentAdapter)(nil)).Elem()
	if _, found := typ.MethodByName("PrepareBoundStart"); found {
		t.Fatal("bound start preparation must remain an optional adapter extension")
	}
	type optionalBoundStart interface {
		PrepareBoundStart(context.Context, StartRequest) (StartPreparation, error)
	}
	var _ optionalBoundStart = (BoundStartPreparer)(nil)
}

func TestAgentAdapterContractSeparatesProcessSupervision(t *testing.T) {
	type adapterContract interface {
		ID() string
		Capabilities() AdapterCapabilities
		PrepareStart(context.Context, StartRequest) (Invocation, error)
		PrepareResume(context.Context, ResumeRequest) (Invocation, error)
		DecodeEvent(EventChunk) (DecodedChunk, error)
		DecodeTerminal(TerminalFiles) (TerminalResult, error)
		Fingerprint(context.Context) (RuntimeFingerprint, error)
	}
	var _ adapterContract = (AgentAdapter)(nil)
	typ := reflect.TypeOf((*AgentAdapter)(nil)).Elem()
	for _, forbidden := range []string{"Start", "Stop", "Kill", "Reconcile"} {
		if _, found := typ.MethodByName(forbidden); found {
			t.Fatalf("AgentAdapter unexpectedly exposes %s", forbidden)
		}
	}
	if _, found := typ.MethodByName("FingerprintForBinding"); found {
		t.Fatal("profile-bound fingerprinting must remain an optional adapter extension")
	}
	type optionalBindingFingerprint interface {
		FingerprintForBinding(context.Context, domain.AgentExecutionProfileBinding) (RuntimeFingerprint, error)
	}
	var _ optionalBindingFingerprint = (BindingFingerprinter)(nil)
}

func TestResumeModesAreExplicit(t *testing.T) {
	if ResumeUnsupported == ResumeExplicitSession || ResumeUnsupported == "" || ResumeExplicitSession == "" {
		t.Fatalf("invalid resume modes: %q %q", ResumeUnsupported, ResumeExplicitSession)
	}
}

func TestLocalConnectedPromptPreservesFrozenInputsAndExplicitCommands(t *testing.T) {
	snapshot, contract := []byte(`{"snapshot":true}`), []byte(`{"contract":true}`)
	prompt, err := WrapLocalConnectedExecutionPrompt(snapshot, contract, []string{"cd tools && node check.js"})
	if err != nil {
		t.Fatal(err)
	}
	restoredSnapshot, restoredContract, ok := UnwrapFrozenExecutionPrompt(prompt)
	if !ok || string(restoredSnapshot) != string(snapshot) || string(restoredContract) != string(contract) {
		t.Fatal("Local Connected prompt lost frozen authority")
	}
	if _, _, ok := UnwrapFrozenExecutionPrompt(prompt + " true"); ok {
		t.Fatal("accepted trailing Local Connected prompt")
	}
}
