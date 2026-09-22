package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

type DelegationPlanningView struct {
	State   domain.DelegationPlanningState `json:"state"`
	Version uint64                         `json:"version"`
	RunID   string                         `json:"runId"`
	Reason  string                         `json:"reason,omitempty"`
}
type StartDelegationPlanningRequest struct {
	CommandMeta
	ParentTaskID domain.TaskID
}

func (s *Service) StartDelegationPlanning(ctx context.Context, req StartDelegationPlanningRequest) (DelegationView, error) {
	_, err := s.CreateRun(ctx, CreateRunRequest{CommandMeta: req.CommandMeta, TaskID: req.ParentTaskID, delegationPlanning: true})
	if err != nil {
		return DelegationView{}, err
	}
	return s.GetDelegation(ctx, req.ParentTaskID)
}

// The planning instruction is part of the already registered contract. Nothing
// is appended to an accepted contract or to an Adapter invocation at launch.
func planningAuthority(ctx context.Context, r storecontract.Reader, id domain.TaskID) ([32]byte, error) {
	var zero [32]byte
	if _, _, err := delegationParent(ctx, r, id); err != nil {
		return zero, err
	}
	resources, err := r.GetTaskResourceSnapshot(ctx, id)
	if err != nil {
		return zero, err
	}
	binding, err := r.GetSpecCodingBinding(ctx, id)
	if err != nil {
		return zero, err
	}
	if sha256.Sum256(binding.ActiveContractJSON) != binding.ActiveContractDigest {
		return zero, ErrInvalidCommand
	}
	var contract struct {
		Requirement struct {
			Statement string `json:"statement"`
		} `json:"requirement"`
	}
	if err = json.Unmarshal(binding.ActiveContractJSON, &contract); err != nil {
		return zero, err
	}
	for _, marker := range []string{domain.DelegationPlanningMarker, "chora-delegation-plan", domain.DelegationProposalSchemaV1} {
		if !strings.Contains(contract.Requirement.Statement, marker) {
			return zero, fmt.Errorf("%w: planning requires a frozen research planning Task with %s", ErrInvalidCommand, marker)
		}
	}
	settings, err := r.GetTaskExecutionSettings(ctx, id)
	if err != nil {
		return zero, err
	}
	raw, err := json.Marshal(struct {
		Resource, Contract, Context [32]byte
		Profile                     domain.AgentExecutionProfile
		Model, Native               string
	}{resources.Digest, binding.ActiveContractDigest, binding.SnapshotDigest, settings.AgentExecutionProfile, settings.ModelBinding.JSON(), settings.NativeCapabilitiesJSON})
	if err != nil {
		return zero, err
	}
	return sha256.Sum256(raw), nil
}
func requirePlanningAuthority(ctx context.Context, r storecontract.Reader, p domain.DelegationPlanningIntent) error {
	digest, err := planningAuthority(ctx, r, p.ParentTaskID)
	if err != nil {
		return err
	}
	if digest != p.AuthorityDigest {
		return fmt.Errorf("%w: frozen planning authority changed", ErrInvalidCommand)
	}
	return nil
}
func (s *Service) ChangeDelegationPlanning(ctx context.Context, req ChangeDelegationRequest) (DelegationView, error) {
	if req.Action != "stop" && req.Action != "resume" {
		return DelegationView{}, ErrInvalidCommand
	}
	if err := s.authorize(ctx, req.CommandMeta, "change_delegation_planning", req.ParentTaskID.String(), req.ExpectedVersion); err != nil {
		return DelegationView{}, err
	}
	now := s.deps.Clock.Now()
	key, err := s.commandKey(req.CommandMeta, "change_delegation_planning", req.ParentTaskID.String(), req.ExpectedVersion, req.Action, now)
	if err != nil {
		return DelegationView{}, err
	}
	err = s.deps.Store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		if _, replay, e := tx.LookupCommand(ctx, key); e != nil {
			return e
		} else if replay {
			return nil
		}
		p, e := tx.GetDelegationPlanning(ctx, req.ParentTaskID)
		if e != nil {
			return e
		}
		if p.Version != req.ExpectedVersion {
			return storecontract.ErrVersionConflict
		}
		state := domain.DelegationPlanningStopping
		if req.Action == "resume" {
			if e = requirePlanningAuthority(ctx, tx, p); e != nil {
				return e
			}
			state = domain.DelegationPlanningRunning
		}
		next, e := p.Transition(state, "", now)
		if e != nil {
			return e
		}
		if e = tx.SaveDelegationPlanningCAS(ctx, p.Version, next); e != nil {
			return e
		}
		return tx.SaveCommand(ctx, key, storecontract.Response{Body: []byte(`{}`)})
	})
	if err != nil {
		return DelegationView{}, err
	}
	return s.GetDelegation(ctx, req.ParentTaskID)
}

// ImportPlanningDelegation consumes only this authorization's single Attempt.
// The source, fixed assignments and consumed authorization commit atomically.
func (s *Service) ImportPlanningDelegation(ctx context.Context, id domain.TaskID) error {
	return s.deps.Store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		p, err := tx.GetDelegationPlanning(ctx, id)
		if err != nil {
			return err
		}
		if p.State == domain.DelegationPlanningImported {
			return nil
		}
		if p.State != domain.DelegationPlanningRunning || !p.AttemptID.Valid() {
			return storecontract.ErrVersionConflict
		}
		if err = requirePlanningAuthority(ctx, tx, p); err != nil {
			return err
		}
		assignments, source, err := loadDelegationProposal(ctx, tx, id, p.AttemptID, "")
		if err != nil {
			return err
		}
		if source.RunID != p.RunID {
			return ErrReviewEvidenceUnavailable
		}
		now := s.deps.Clock.Now()
		next, err := p.Transition(domain.DelegationPlanningImported, "", now)
		if err != nil {
			return err
		}
		if err = tx.SaveDelegationPlanningCAS(ctx, p.Version, next); err != nil {
			return err
		}
		d := domain.TaskDelegation{ParentTaskID: id, Version: 1, State: domain.DelegationRunning, Assignments: assignments, ActorID: p.ActorID, SessionID: p.SessionID, CreatedAt: now, UpdatedAt: now}
		if err = tx.InsertTaskDelegation(ctx, d); err != nil {
			return err
		}
		source.CreatedAt = now
		return tx.InsertDelegationProposalSource(ctx, source)
	})
}

func (s *Service) CancelUnstartedPlanningRun(ctx context.Context, id domain.TaskID) error {
	p, err := s.deps.Store.Reader().GetDelegationPlanning(ctx, id)
	if err != nil {
		return err
	}
	return s.cancelUnstartedDelegationRun(ctx, p.RunID, id, true)
}

func planningForRun(ctx context.Context, r storecontract.Reader, run domain.AgentRun) (*domain.DelegationPlanningIntent, error) {
	p, err := r.GetDelegationPlanning(ctx, run.TaskID())
	if errors.Is(err, storecontract.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if p.RunID != run.ID() {
		return nil, fmt.Errorf("%w: planning authorization owns another Run", ErrInvalidCommand)
	}
	return &p, nil
}
