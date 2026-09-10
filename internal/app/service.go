package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/Yangyang96/chora/internal/taskdelivery"
	"sync"
	"time"

	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
	"github.com/Yangyang96/chora/internal/speccoding"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

type Dependencies struct {
	Lifecycle                   context.Context
	Store                       Store
	Context                     ContextPort
	Agents                      AgentRegistry
	Supervisor                  execution.ProcessSupervisor
	Presence                    PresenceAuthorizer
	Authorizer                  CommandAuthorizer
	Clock                       Clock
	IDs                         IDGenerator
	Verifier                    VerificationExecutor
	PatchMaterializer           ReviewPatchMaterializer
	ResourcePatchMaterializer   ResourceReviewPatchMaterializer
	ResourceReviewVerifier      ResourceReviewVerifier
	ResourceCheckObserverSHA256 string
	ResourcePatchTarget         ResourcePatchApplicationTarget
	PatchSource                 ReviewPatchSource
	PatchTarget                 PatchApplicationTarget
	TaskWorktrees               TaskWorktreeResolver
	TaskResourceWorkspaces      TaskResourceWorkspaceResolver
	TaskDeliveryWorkspaces      TaskDeliveryWorkspaces
	TaskDeliveryGit             TaskDeliveryGit
	TaskDeliveryHosting         taskdelivery.Hosting
	SCM                         SCMAdapter
	InstalledSpecCodingEnvelope *speccoding.InstalledEnvelope
	SpecCodingEnvelopeResolver  SpecCodingEnvelopeResolver
	RepositorySource            RepositorySource
	DataRoot                    string
	RoomCreationMode            RoomCreationMode

	// AutoStartVerification starts independent verification automatically as
	// soon as the Agent completes and produces a reviewable result, instead of
	// waiting for an explicit human StartVerification command. Diagnostic Fake
	// tasks and Runs without a registered Spec Coding contract are never
	// auto-started. The explicit StartVerification command remains available for
	// Verifier retry and recovery.
	AutoStartVerification bool
}

type RoomCreationMode string

const (
	RoomCreationLegacyStandalone RoomCreationMode = ""
	RoomCreationProjectOnly      RoomCreationMode = "project_only"
)

type Service struct {
	deps                Dependencies
	locks               sync.Map
	candidateLocks      keyedLocks
	signals             sync.Map
	verificationSignals sync.Map
}

type keyedLocks struct {
	mu      sync.Mutex
	entries map[string]*keyedLock
}

type keyedLock struct {
	mu   sync.Mutex
	refs int
}

func (locks *keyedLocks) lock(key string) func() {
	locks.mu.Lock()
	if locks.entries == nil {
		locks.entries = make(map[string]*keyedLock)
	}
	entry := locks.entries[key]
	if entry == nil {
		entry = &keyedLock{}
		locks.entries[key] = entry
	}
	entry.refs++
	locks.mu.Unlock()
	entry.mu.Lock()
	return func() {
		entry.mu.Unlock()
		locks.mu.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(locks.entries, key)
		}
		locks.mu.Unlock()
	}
}

func NewService(deps Dependencies) *Service {
	if deps.Lifecycle == nil {
		deps.Lifecycle = context.Background()
	}
	if deps.Context == nil {
		deps.Context = ContextAssembler{}
	}
	if deps.Clock == nil {
		deps.Clock = systemClock{}
	}
	if deps.IDs == nil {
		deps.IDs = RandomIDs{}
	}
	return &Service{deps: deps}
}
func (s *Service) runLock(id domain.RunID) func() {
	value, _ := s.locks.LoadOrStore(id.String(), &sync.Mutex{})
	m := value.(*sync.Mutex)
	m.Lock()
	return m.Unlock
}
func responseBody(value any) []byte { body, _ := json.Marshal(value); return body }
func validateMeta(meta CommandMeta) error {
	if !meta.valid() {
		return ErrInvalidCommand
	}
	return nil
}

func (s *Service) authorize(ctx context.Context, meta CommandMeta, action, resource string, version uint64) error {
	if err := validateMeta(meta); err != nil {
		return err
	}
	if s.deps.Authorizer == nil {
		return ErrUnauthorizedCommand
	}
	return s.deps.Authorizer.Authorize(ctx, CommandAuthorizationRequest{ActorID: meta.ActorID, SessionID: meta.SessionID, Action: action, ResourceID: resource, ExpectedVersion: version})
}

func (s *Service) commandKey(meta CommandMeta, action, resource string, version uint64, semantic any, now time.Time) (storecontract.CommandKey, error) {
	binding := struct {
		Action, Resource, Actor, Session string
		Version                          uint64
		Semantic                         any
	}{Action: action, Resource: resource, Actor: meta.ActorID, Session: meta.SessionID, Version: version, Semantic: semantic}
	keyHash := sha256.Sum256([]byte(action + "\x00" + resource + "\x00" + meta.IdempotencyKey))
	body, err := json.Marshal(binding)
	if err != nil {
		return storecontract.CommandKey{}, fmt.Errorf("marshal command semantics: %w", err)
	}
	digest := sha256.Sum256(body)
	return storecontract.CommandKey{Command: action, ResourceID: resource, KeyHash: keyHash, RequestDigest: digest, CreatedAt: now}, nil
}
func (s *Service) event(eventType string, run domain.AgentRun) storeEvent {
	return storeEvent{Type: eventType, RunID: run.ID(), State: run.State(), Version: run.Version()}
}

func (s *Service) attemptEvent(eventType string, run domain.AgentRun, attempt domain.Attempt) storeEvent {
	return storeEvent{Type: eventType, RunID: run.ID(), State: run.State(), Version: run.Version(), AttemptID: attempt.ID().String(), AttemptSequence: attempt.Sequence()}
}

type storeEvent struct {
	Type            string          `json:"type"`
	RunID           domain.RunID    `json:"run_id"`
	State           domain.RunState `json:"state"`
	Version         uint64          `json:"version"`
	AttemptID       string          `json:"attempt_id,omitempty"`
	AttemptSequence int             `json:"attempt_sequence,omitempty"`
}

func validStartOutcome(out execution.StartOutcome, expected execution.LaunchToken) bool {
	if !expected.Valid() || out.LaunchToken != expected {
		return false
	}
	switch out.Kind {
	case execution.Started:
		return out.Handle.Valid() && out.Identity.Valid()
	case execution.StartProvenNoChild:
		return !out.Handle.Valid() && !out.Identity.Valid()
	case execution.StartReconciliationRequired:
		return out.Identity.Valid() || out.LaunchToken.Valid()
	default:
		return false
	}
}
func (s *Service) require() error {
	if s == nil || s.deps.Store == nil || s.deps.Agents == nil || s.deps.Supervisor == nil {
		return fmt.Errorf("%w: missing dependencies", ErrInvalidCommand)
	}
	return nil
}
