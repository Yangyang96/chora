package domain

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestAgentExecutionProfilesPreserveHistoryAndAddPublicIsolation(t *testing.T) {
	want := []AgentExecutionProfile{
		AgentExecutionProfileMinimal,
		AgentExecutionProfileStandard,
		AgentExecutionProfileTrustedLocal,
		AgentExecutionProfileIsolatedLocal,
	}
	if got := SupportedAgentExecutionProfiles(); !reflect.DeepEqual(got, want) {
		t.Fatalf("profiles = %#v", got)
	}
	if DefaultAgentExecutionProfile() != AgentExecutionProfileStandard {
		t.Fatalf("default = %q", DefaultAgentExecutionProfile())
	}

	got := SupportedAgentExecutionProfiles()
	got[0] = "mutated"
	if SupportedAgentExecutionProfiles()[0] != AgentExecutionProfileMinimal {
		t.Fatal("supported profile list leaked mutable state")
	}
}

func TestAgentExecutionProfileContractsKeepRuntimeSandboxAndTrustSeparate(t *testing.T) {
	minimal, err := AgentExecutionProfileContractFor(AgentExecutionProfileMinimal)
	if err != nil {
		t.Fatal(err)
	}
	standard, err := AgentExecutionProfileContractFor(AgentExecutionProfileStandard)
	if err != nil {
		t.Fatal(err)
	}
	trusted, err := AgentExecutionProfileContractFor(AgentExecutionProfileTrustedLocal)
	if err != nil {
		t.Fatal(err)
	}

	for _, managed := range []AgentExecutionProfileContract{minimal, standard} {
		if managed.RuntimeAdapterID != "pi" || managed.RuntimeSource != ManagedPiRuntimeSource ||
			managed.ExecutionProvider != DockerExecutionProvider || !managed.RequiresSandbox ||
			managed.InheritsUserConfiguration || managed.TrustDisclosurePolicy != "" {
			t.Fatalf("managed contract = %#v", managed)
		}
	}
	if minimal.CapabilityPolicy != MinimalCapabilityPolicy || standard.CapabilityPolicy != StandardCapabilityPolicy {
		t.Fatalf("managed capability policies = %q / %q", minimal.CapabilityPolicy, standard.CapabilityPolicy)
	}
	if trusted.RuntimeAdapterID != "pi" || trusted.RuntimeSource != LocalPiRuntimeSource ||
		trusted.ExecutionProvider != TrustedHostExecutionProvider || trusted.RequiresSandbox ||
		!trusted.InheritsUserConfiguration || trusted.CapabilityPolicy != NativeCapabilityPolicy ||
		trusted.TrustDisclosurePolicy != TrustedLocalDisclosurePolicy {
		t.Fatalf("trusted contract = %#v", trusted)
	}
}

func TestAgentExecutionProfileParsingFailsClosed(t *testing.T) {
	for _, profile := range SupportedAgentExecutionProfiles() {
		parsed, err := ParseAgentExecutionProfile(string(profile))
		if err != nil || parsed != profile {
			t.Fatalf("parse %q = %q, %v", profile, parsed, err)
		}
	}
	for _, value := range []string{"", "real_spec_coding", "docker", "trusted", "Standard"} {
		if _, err := ParseAgentExecutionProfile(value); !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("parse %q error = %v", value, err)
		}
	}
}

func TestAgentExecutionProfileBindingRestoresOnlyExactContract(t *testing.T) {
	for _, profile := range SupportedAgentExecutionProfiles() {
		binding, err := NewAgentExecutionProfileBinding(profile)
		if err != nil {
			t.Fatal(err)
		}
		restored, err := RestoreAgentExecutionProfileBinding(binding.Record())
		if err != nil || restored != binding || !restored.Bound() {
			t.Fatalf("restore %q = %#v, %v", profile, restored, err)
		}
	}

	standard, err := NewAgentExecutionProfileBinding(AgentExecutionProfileStandard)
	if err != nil {
		t.Fatal(err)
	}
	drifted := standard.Record()
	drifted.ExecutionProvider = TrustedHostExecutionProvider
	if _, err := RestoreAgentExecutionProfileBinding(drifted); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("drifted binding error = %v", err)
	}
	if empty, err := RestoreAgentExecutionProfileBinding(AgentExecutionProfileBindingRecord{}); err != nil || empty.Bound() {
		t.Fatalf("empty binding = %#v, %v", empty, err)
	}
}

func TestAgentExecutionProfilePreferenceAndTrustedAcknowledgementAreExactImmutableValues(t *testing.T) {
	now := time.Date(2026, 8, 27, 9, 0, 0, 0, time.UTC)
	for _, profile := range SupportedAgentExecutionProfiles() {
		preference, err := NewAgentExecutionProfilePreference(AgentExecutionProfilePreferenceRecord{
			TaskID: NewTaskID(), Version: 1, Profile: profile, ActorID: "owner", SessionID: "session", SelectedAt: now,
		})
		if err != nil || preference.Profile() != profile || preference.Record().Profile != profile {
			t.Fatalf("profile %q preference=%#v err=%v", profile, preference, err)
		}
	}
	acknowledgement, err := NewTrustedLocalAcknowledgement(TrustedLocalAcknowledgementRecord{
		PolicyVersion: TrustedLocalDisclosurePolicy, ActorID: "owner", SessionID: "session", AcknowledgedAt: now,
	})
	if err != nil || acknowledgement.PolicyVersion() != TrustedLocalDisclosurePolicy {
		t.Fatalf("acknowledgement=%#v err=%v", acknowledgement, err)
	}
	for _, invalid := range []string{"", "chora.trusted-local-disclosure.v", "chora.trusted-local-disclosure.v1-drift", "other.v1"} {
		if _, err := NewTrustedLocalAcknowledgement(TrustedLocalAcknowledgementRecord{PolicyVersion: invalid, ActorID: "owner", SessionID: "session", AcknowledgedAt: now}); !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("policy %q error=%v", invalid, err)
		}
	}
}
