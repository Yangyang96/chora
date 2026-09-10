package domain

import (
	"fmt"
	"strings"
	"time"
)

// AgentExecutionProfile is the user-selected execution boundary for a real
// Agent Attempt. It is deliberately separate from the application's historical
// task-kind selector (diagnostic Fake versus real Spec Coding).
type AgentExecutionProfile string

const (
	AgentExecutionProfileMinimal      AgentExecutionProfile = "minimal"
	AgentExecutionProfileStandard     AgentExecutionProfile = "standard"
	AgentExecutionProfileTrustedLocal AgentExecutionProfile = "trusted_local"

	ManagedPiRuntimeSource       = "managed_pi_image"
	LocalPiRuntimeSource         = "local_pi"
	DockerExecutionProvider      = "docker"
	TrustedHostExecutionProvider = "trusted_host"

	MinimalCapabilityPolicy  = "chora.minimal.v1"
	StandardCapabilityPolicy = "chora.standard.v1"
	NativeCapabilityPolicy   = "pi.native"

	TrustedLocalDisclosurePolicy = "chora.trusted-local-disclosure.v1"
)

// AgentExecutionProfilePreference is one append-only Task-level selection.
// Version 1 is captured before a real Task can create execution authority;
// later versions are created only by an explicit successor-Attempt switch.
type AgentExecutionProfilePreference struct {
	taskID     TaskID
	version    uint64
	profile    AgentExecutionProfile
	actorID    string
	sessionID  string
	selectedAt time.Time
}

type AgentExecutionProfilePreferenceRecord struct {
	TaskID     TaskID
	Version    uint64
	Profile    AgentExecutionProfile
	ActorID    string
	SessionID  string
	SelectedAt time.Time
}

func NewAgentExecutionProfilePreference(record AgentExecutionProfilePreferenceRecord) (AgentExecutionProfilePreference, error) {
	if !record.TaskID.Valid() || record.Version == 0 || !validExecutionProfileActor(record.ActorID) ||
		!validExecutionProfileActor(record.SessionID) || record.SelectedAt.IsZero() {
		return AgentExecutionProfilePreference{}, fmt.Errorf("%w: invalid Agent execution profile preference", ErrInvalidArgument)
	}
	if _, err := AgentExecutionProfileContractFor(record.Profile); err != nil {
		return AgentExecutionProfilePreference{}, err
	}
	return AgentExecutionProfilePreference{
		taskID: record.TaskID, version: record.Version, profile: record.Profile,
		actorID: record.ActorID, sessionID: record.SessionID, selectedAt: record.SelectedAt,
	}, nil
}

func (preference AgentExecutionProfilePreference) TaskID() TaskID  { return preference.taskID }
func (preference AgentExecutionProfilePreference) Version() uint64 { return preference.version }
func (preference AgentExecutionProfilePreference) Profile() AgentExecutionProfile {
	return preference.profile
}
func (preference AgentExecutionProfilePreference) ActorID() string   { return preference.actorID }
func (preference AgentExecutionProfilePreference) SessionID() string { return preference.sessionID }
func (preference AgentExecutionProfilePreference) SelectedAt() time.Time {
	return preference.selectedAt
}
func (preference AgentExecutionProfilePreference) Record() AgentExecutionProfilePreferenceRecord {
	return AgentExecutionProfilePreferenceRecord{
		TaskID: preference.taskID, Version: preference.version, Profile: preference.profile,
		ActorID: preference.actorID, SessionID: preference.sessionID, SelectedAt: preference.selectedAt,
	}
}

// TrustedLocalAcknowledgement proves one human explicitly accepted one exact
// disclosure-policy version. Records are append-only; a newer policy therefore
// fails closed until it has its own acknowledgement.
type TrustedLocalAcknowledgement struct {
	policyVersion  string
	actorID        string
	sessionID      string
	acknowledgedAt time.Time
}

type TrustedLocalAcknowledgementRecord struct {
	PolicyVersion  string
	ActorID        string
	SessionID      string
	AcknowledgedAt time.Time
}

func NewTrustedLocalAcknowledgement(record TrustedLocalAcknowledgementRecord) (TrustedLocalAcknowledgement, error) {
	if !validTrustedLocalDisclosureVersion(record.PolicyVersion) || !validExecutionProfileActor(record.ActorID) ||
		!validExecutionProfileActor(record.SessionID) || record.AcknowledgedAt.IsZero() {
		return TrustedLocalAcknowledgement{}, fmt.Errorf("%w: invalid Trusted Local acknowledgement", ErrInvalidArgument)
	}
	return TrustedLocalAcknowledgement{
		policyVersion: record.PolicyVersion, actorID: record.ActorID,
		sessionID: record.SessionID, acknowledgedAt: record.AcknowledgedAt,
	}, nil
}

func (acknowledgement TrustedLocalAcknowledgement) PolicyVersion() string {
	return acknowledgement.policyVersion
}
func (acknowledgement TrustedLocalAcknowledgement) ActorID() string { return acknowledgement.actorID }
func (acknowledgement TrustedLocalAcknowledgement) SessionID() string {
	return acknowledgement.sessionID
}
func (acknowledgement TrustedLocalAcknowledgement) AcknowledgedAt() time.Time {
	return acknowledgement.acknowledgedAt
}
func (acknowledgement TrustedLocalAcknowledgement) Record() TrustedLocalAcknowledgementRecord {
	return TrustedLocalAcknowledgementRecord{
		PolicyVersion: acknowledgement.policyVersion, ActorID: acknowledgement.actorID,
		SessionID: acknowledgement.sessionID, AcknowledgedAt: acknowledgement.acknowledgedAt,
	}
}

func validExecutionProfileActor(value string) bool {
	return value != "" && len(value) <= 256 && value == strings.TrimSpace(value) && !strings.ContainsRune(value, '\x00')
}

func validTrustedLocalDisclosureVersion(value string) bool {
	const prefix = "chora.trusted-local-disclosure.v"
	if !strings.HasPrefix(value, prefix) || len(value) == len(prefix) || len(value) > 128 {
		return false
	}
	for _, character := range value[len(prefix):] {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

// AgentExecutionProfileContract freezes product-visible profile semantics
// without making a Docker Engine, image, or local executable into a profile.
type AgentExecutionProfileContract struct {
	Profile                   AgentExecutionProfile
	RuntimeAdapterID          string
	RuntimeSource             string
	ExecutionProvider         string
	CapabilityPolicy          string
	RequiresSandbox           bool
	InheritsUserConfiguration bool
	TrustDisclosurePolicy     string
}

// AgentExecutionProfileBinding is the exact, persisted execution contract of
// one Pi Charter or Attempt. The zero value means that no Pi profile is bound.
// Fields stay private so callers cannot mutate an accepted binding in place.
type AgentExecutionProfileBinding struct {
	profile               AgentExecutionProfile
	runtimeSource         string
	executionProvider     string
	capabilityPolicy      string
	trustDisclosurePolicy string
}

type AgentExecutionProfileBindingRecord struct {
	Profile               AgentExecutionProfile
	RuntimeSource         string
	ExecutionProvider     string
	CapabilityPolicy      string
	TrustDisclosurePolicy string
}

func DefaultAgentExecutionProfile() AgentExecutionProfile {
	return AgentExecutionProfileStandard
}

func SupportedAgentExecutionProfiles() []AgentExecutionProfile {
	return []AgentExecutionProfile{
		AgentExecutionProfileMinimal,
		AgentExecutionProfileStandard,
		AgentExecutionProfileTrustedLocal,
	}
}

func ParseAgentExecutionProfile(value string) (AgentExecutionProfile, error) {
	profile := AgentExecutionProfile(value)
	if _, err := AgentExecutionProfileContractFor(profile); err != nil {
		return "", err
	}
	return profile, nil
}

func AgentExecutionProfileContractFor(profile AgentExecutionProfile) (AgentExecutionProfileContract, error) {
	contract := AgentExecutionProfileContract{Profile: profile, RuntimeAdapterID: "pi"}
	switch profile {
	case AgentExecutionProfileMinimal:
		contract.RuntimeSource = ManagedPiRuntimeSource
		contract.ExecutionProvider = DockerExecutionProvider
		contract.CapabilityPolicy = MinimalCapabilityPolicy
		contract.RequiresSandbox = true
	case AgentExecutionProfileStandard:
		contract.RuntimeSource = ManagedPiRuntimeSource
		contract.ExecutionProvider = DockerExecutionProvider
		contract.CapabilityPolicy = StandardCapabilityPolicy
		contract.RequiresSandbox = true
	case AgentExecutionProfileTrustedLocal:
		contract.RuntimeSource = LocalPiRuntimeSource
		contract.ExecutionProvider = TrustedHostExecutionProvider
		contract.CapabilityPolicy = NativeCapabilityPolicy
		contract.InheritsUserConfiguration = true
		contract.TrustDisclosurePolicy = TrustedLocalDisclosurePolicy
	default:
		return AgentExecutionProfileContract{}, fmt.Errorf("%w: unsupported Agent execution profile %q", ErrInvalidArgument, profile)
	}
	return contract, nil
}

func NewAgentExecutionProfileBinding(profile AgentExecutionProfile) (AgentExecutionProfileBinding, error) {
	contract, err := AgentExecutionProfileContractFor(profile)
	if err != nil {
		return AgentExecutionProfileBinding{}, err
	}
	return AgentExecutionProfileBinding{
		profile:               contract.Profile,
		runtimeSource:         contract.RuntimeSource,
		executionProvider:     contract.ExecutionProvider,
		capabilityPolicy:      contract.CapabilityPolicy,
		trustDisclosurePolicy: contract.TrustDisclosurePolicy,
	}, nil
}

// RestoreAgentExecutionProfileBinding rejects partial, unknown, and drifted
// persisted contracts rather than silently replacing them with current defaults.
func RestoreAgentExecutionProfileBinding(record AgentExecutionProfileBindingRecord) (AgentExecutionProfileBinding, error) {
	if record == (AgentExecutionProfileBindingRecord{}) {
		return AgentExecutionProfileBinding{}, nil
	}
	want, err := NewAgentExecutionProfileBinding(record.Profile)
	if err != nil {
		return AgentExecutionProfileBinding{}, err
	}
	got := AgentExecutionProfileBinding{
		profile:               record.Profile,
		runtimeSource:         record.RuntimeSource,
		executionProvider:     record.ExecutionProvider,
		capabilityPolicy:      record.CapabilityPolicy,
		trustDisclosurePolicy: record.TrustDisclosurePolicy,
	}
	if got != want {
		return AgentExecutionProfileBinding{}, fmt.Errorf("%w: Agent execution profile binding drift", ErrInvalidArgument)
	}
	return got, nil
}

func (binding AgentExecutionProfileBinding) Bound() bool {
	return binding != (AgentExecutionProfileBinding{})
}

func (binding AgentExecutionProfileBinding) Profile() AgentExecutionProfile {
	return binding.profile
}

func (binding AgentExecutionProfileBinding) RuntimeSource() string {
	return binding.runtimeSource
}

func (binding AgentExecutionProfileBinding) ExecutionProvider() string {
	return binding.executionProvider
}

func (binding AgentExecutionProfileBinding) CapabilityPolicy() string {
	return binding.capabilityPolicy
}

func (binding AgentExecutionProfileBinding) TrustDisclosurePolicy() string {
	return binding.trustDisclosurePolicy
}

func (binding AgentExecutionProfileBinding) Record() AgentExecutionProfileBindingRecord {
	return AgentExecutionProfileBindingRecord{
		Profile:               binding.profile,
		RuntimeSource:         binding.runtimeSource,
		ExecutionProvider:     binding.executionProvider,
		CapabilityPolicy:      binding.capabilityPolicy,
		TrustDisclosurePolicy: binding.trustDisclosurePolicy,
	}
}

func validAgentExecutionProfileBinding(adapterID string, binding AgentExecutionProfileBinding) bool {
	if adapterID != "pi" {
		return !binding.Bound()
	}
	if !binding.Bound() {
		return false
	}
	restored, err := RestoreAgentExecutionProfileBinding(binding.Record())
	return err == nil && restored == binding
}
