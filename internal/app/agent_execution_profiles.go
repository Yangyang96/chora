package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

func normalizedAgentExecutionProfile(profile domain.AgentExecutionProfile) (domain.AgentExecutionProfile, error) {
	if profile == "" {
		return domain.DefaultAgentExecutionProfile(), nil
	}
	return domain.ParseAgentExecutionProfile(string(profile))
}

func agentExecutionProfileForAdapter(adapterID string, requested domain.AgentExecutionProfile) (domain.AgentExecutionProfile, error) {
	if adapterID != "pi" {
		if requested != "" {
			return "", fmt.Errorf("%w: Agent execution profile is available only for Pi", ErrInvalidCommand)
		}
		return "", nil
	}
	return normalizedAgentExecutionProfile(requested)
}

func newTaskAgentExecutionProfilePreference(taskID domain.TaskID, version uint64, profile domain.AgentExecutionProfile, meta CommandMeta, at time.Time) (domain.AgentExecutionProfilePreference, error) {
	return domain.NewAgentExecutionProfilePreference(domain.AgentExecutionProfilePreferenceRecord{
		TaskID: taskID, Version: version, Profile: profile, ActorID: meta.ActorID, SessionID: meta.SessionID, SelectedAt: at,
	})
}

func requireCurrentTrustedLocalAcknowledgement(ctx context.Context, reader storecontract.Reader, actorID string) error {
	acknowledgement, err := reader.GetTrustedLocalAcknowledgement(ctx, actorID, domain.TrustedLocalDisclosurePolicy)
	if errors.Is(err, storecontract.ErrNotFound) {
		return fmt.Errorf("%w: Trusted Local requires acknowledgement of %s", ErrUnauthorizedCommand, domain.TrustedLocalDisclosurePolicy)
	}
	if err != nil {
		return err
	}
	restored, err := domain.NewTrustedLocalAcknowledgement(acknowledgement.Record())
	if err != nil || restored.Record() != acknowledgement.Record() || acknowledgement.ActorID() != actorID ||
		acknowledgement.PolicyVersion() != domain.TrustedLocalDisclosurePolicy {
		return fmt.Errorf("%w: current Trusted Local acknowledgement is invalid", ErrUnauthorizedCommand)
	}
	return nil
}

func requireProfileAcknowledgement(ctx context.Context, reader storecontract.Reader, profile domain.AgentExecutionProfile, actorID string) error {
	if profile != domain.AgentExecutionProfileTrustedLocal {
		return nil
	}
	return requireCurrentTrustedLocalAcknowledgement(ctx, reader, actorID)
}

func taskAgentExecutionProfileBinding(ctx context.Context, reader storecontract.Reader, taskID domain.TaskID) (domain.AgentExecutionProfilePreference, domain.AgentExecutionProfileBinding, error) {
	preference, err := reader.GetTaskAgentExecutionProfilePreference(ctx, taskID)
	if err != nil {
		return domain.AgentExecutionProfilePreference{}, domain.AgentExecutionProfileBinding{}, err
	}
	binding, err := domain.NewAgentExecutionProfileBinding(preference.Profile())
	if err != nil {
		return domain.AgentExecutionProfilePreference{}, domain.AgentExecutionProfileBinding{}, err
	}
	return preference, binding, nil
}

func sandboxModeForAgentExecutionProfile(binding domain.AgentExecutionProfileBinding) string {
	if binding.ExecutionProvider() == domain.TrustedHostExecutionProvider {
		return "trusted-host"
	}
	return "colima-docker"
}

type trustedLocalAcknowledgementWire struct {
	PolicyVersion  string
	ActorID        string
	SessionID      string
	AcknowledgedAt time.Time
}

func (s *Service) AcknowledgeTrustedLocal(ctx context.Context, request AcknowledgeTrustedLocalRequest) (AcknowledgeTrustedLocalResult, error) {
	if s == nil || s.deps.Store == nil || request.PolicyVersion != domain.TrustedLocalDisclosurePolicy {
		return AcknowledgeTrustedLocalResult{}, fmt.Errorf("%w: acknowledgement must name the current Trusted Local disclosure policy", ErrInvalidCommand)
	}
	if err := s.authorize(ctx, request.CommandMeta, "acknowledge_trusted_local", request.PolicyVersion, 0); err != nil {
		return AcknowledgeTrustedLocalResult{}, err
	}
	now := s.deps.Clock.Now()
	key, err := s.commandKey(request.CommandMeta, "acknowledge_trusted_local", request.PolicyVersion, 0, request.PolicyVersion, now)
	if err != nil {
		return AcknowledgeTrustedLocalResult{}, err
	}
	var result AcknowledgeTrustedLocalResult
	err = s.deps.Store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		response, replayed, err := tx.LookupCommand(ctx, key)
		if err != nil {
			return err
		}
		if replayed {
			var wire trustedLocalAcknowledgementWire
			if err := json.Unmarshal(response.Body, &wire); err != nil || wire.PolicyVersion != request.PolicyVersion || wire.ActorID != request.ActorID {
				return fmt.Errorf("%w: invalid Trusted Local acknowledgement replay", ErrInvalidCommand)
			}
			result.Acknowledgement, err = tx.GetTrustedLocalAcknowledgement(ctx, request.ActorID, request.PolicyVersion)
			result.Replayed = err == nil
			return err
		}
		existing, err := tx.GetTrustedLocalAcknowledgement(ctx, request.ActorID, request.PolicyVersion)
		if err == nil {
			result.Acknowledgement = existing
		} else if !errors.Is(err, storecontract.ErrNotFound) {
			return err
		} else {
			acknowledgement, err := domain.NewTrustedLocalAcknowledgement(domain.TrustedLocalAcknowledgementRecord{
				PolicyVersion: request.PolicyVersion, ActorID: request.ActorID,
				SessionID: request.SessionID, AcknowledgedAt: now,
			})
			if err != nil {
				return err
			}
			if err := tx.InsertTrustedLocalAcknowledgement(ctx, acknowledgement); err != nil {
				return err
			}
			result.Acknowledgement = acknowledgement
		}
		wire := trustedLocalAcknowledgementWire{
			PolicyVersion: result.Acknowledgement.PolicyVersion(), ActorID: result.Acknowledgement.ActorID(),
			SessionID: result.Acknowledgement.SessionID(), AcknowledgedAt: result.Acknowledgement.AcknowledgedAt(),
		}
		response, err = jsonResponse(wire)
		if err != nil {
			return err
		}
		return tx.SaveCommand(ctx, key, response)
	})
	return result, err
}

func profileSwitchReason(reason string) (string, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" || len(reason) > 2048 || strings.ContainsRune(reason, '\x00') {
		return "", fmt.Errorf("%w: profile switch reason is required", ErrInvalidCommand)
	}
	return reason, nil
}
