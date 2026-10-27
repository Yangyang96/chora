package execution

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/Yangyang96/chora/internal/domain"
)

var (
	ErrUnknownRuntime        = errors.New("unknown_runtime")
	ErrUnsupportedCapability = errors.New("unsupported_capability")
	ErrInvalidRuntimeConfig  = errors.New("invalid_runtime_config")
)

// ProviderDescriptor describes protocol support, never installation readiness.
// Lists are explicit: an absent capability is unsupported.
type ProviderDescriptor struct {
	ID                string     `json:"runtimeId"`
	Name              string     `json:"name"`
	ProtocolVersion   string     `json:"protocolVersion"`
	Environments      []string   `json:"environments"`
	TaskKinds         []string   `json:"taskKinds"`
	Features          []string   `json:"features"`
	ResumeMode        ResumeMode `json:"resumeMode"`
	ShutdownMode      string     `json:"shutdownMode"`
	ApprovalMode      string     `json:"approvalMode"`
	ModelMode         string     `json:"modelMode"`
	CheckEvidenceMode string     `json:"checkEvidenceMode"`
}

func (d ProviderDescriptor) Validate() error {
	if _, err := NewExecutionTarget(d.ID, d.ID); err != nil || d.Name == "" || d.ProtocolVersion == "" || len(d.Environments) == 0 || len(d.TaskKinds) == 0 {
		return ErrInvalidRuntimeConfig
	}
	if d.ResumeMode != ResumeUnsupported && d.ResumeMode != ResumeExplicitSession {
		return ErrInvalidRuntimeConfig
	}
	if d.ShutdownMode != "stdin_after_settled" && d.ShutdownMode != "exit_after_input" {
		return ErrInvalidRuntimeConfig
	}
	if d.ApprovalMode != "native_noninteractive" && d.ApprovalMode != "unsupported" {
		return ErrInvalidRuntimeConfig
	}
	if d.ModelMode != "native_default" && d.ModelMode != "explicit" && d.ModelMode != "catalog" {
		return ErrInvalidRuntimeConfig
	}
	if d.CheckEvidenceMode != "none" && d.CheckEvidenceMode != "agent_reported" && d.CheckEvidenceMode != "content_bound" {
		return ErrInvalidRuntimeConfig
	}
	for _, values := range [][]string{d.Environments, d.TaskKinds, d.Features} {
		seen := map[string]bool{}
		for _, value := range values {
			if value == "" || seen[value] {
				return ErrInvalidRuntimeConfig
			}
			seen[value] = true
		}
	}
	return nil
}
func (d ProviderDescriptor) Clone() ProviderDescriptor {
	d.Environments = slices.Clone(d.Environments)
	d.TaskKinds = slices.Clone(d.TaskKinds)
	d.Features = slices.Clone(d.Features)
	return d
}
func (d ProviderDescriptor) Digest() [32]byte { raw, _ := json.Marshal(d); return sha256.Sum256(raw) }
func (d ProviderDescriptor) Require(environment, taskKind string, features ...string) error {
	if err := d.Validate(); err != nil {
		return err
	}
	if !slices.Contains(d.Environments, environment) || !slices.Contains(d.TaskKinds, taskKind) {
		return fmt.Errorf("%w: runtime %s environment/task", ErrUnsupportedCapability, d.ID)
	}
	for _, feature := range features {
		if !slices.Contains(d.Features, feature) {
			return fmt.Errorf("%w: %s", ErrUnsupportedCapability, feature)
		}
	}
	return nil
}

// Provider extends the existing execution ports. Completion and recovery are
// pure policy; only the application and supervisor may mutate or signal.
type Provider interface {
	AgentAdapter
	Describe() ProviderDescriptor
	Complete(CompletionRequest) (TerminalResult, error)
	Recover(RecoveryRequest) RecoveryPolicy
}
type CompletionRequest struct {
	Attempt domain.Attempt
	Files   TerminalFiles
	Events  []domain.RunEvent
}
type RecoveryRequest struct {
	Binding      domain.AgentExecutionProfileBinding
	RunState     domain.RunState
	AttemptState domain.AttemptState
}
type RecoveryPolicy struct {
	FreshRetry            bool
	StopOnRestart         bool
	MissingRouteUncertain bool
}

// Readiness fingerprints the explicitly configured installation without
// installing, logging in, or making a model request.
type Readiness struct {
	RuntimeID   string
	Ready       bool
	Fingerprint RuntimeFingerprint
	Failure     string
}

func Inspect(ctx context.Context, adapter AgentAdapter) Readiness {
	result := Readiness{RuntimeID: adapter.ID()}
	fingerprint, err := adapter.Fingerprint(ctx)
	if err != nil || !fingerprint.Valid() {
		result.Failure = "runtime_unavailable"
		return result
	}
	result.Ready = true
	result.Fingerprint = fingerprint
	return result
}

// AgentCheckReporter projects non-authoritative claims only. It cannot create
// independent verification results or human acceptance.
type AgentCheckReporter interface {
	AgentReportedChecks([]byte, []NormalizedEvent) ([]DeclaredCheck, error)
}
