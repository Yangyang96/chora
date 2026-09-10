package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

func (reader *reader) GetTaskAgentExecutionProfilePreference(ctx context.Context, taskID domain.TaskID) (domain.AgentExecutionProfilePreference, error) {
	var profile, actorID, sessionID, selectedAt string
	var version uint64
	err := reader.q.QueryRowContext(ctx, `SELECT version,profile,actor_id,session_id,selected_at
		FROM agent_execution_profile_preferences WHERE task_id=? ORDER BY version DESC LIMIT 1`, taskID.String()).
		Scan(&version, &profile, &actorID, &sessionID, &selectedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.AgentExecutionProfilePreference{}, storecontract.ErrNotFound
	}
	if err != nil {
		return domain.AgentExecutionProfilePreference{}, err
	}
	at, err := parseTime(selectedAt)
	if err != nil {
		return domain.AgentExecutionProfilePreference{}, err
	}
	return domain.NewAgentExecutionProfilePreference(domain.AgentExecutionProfilePreferenceRecord{
		TaskID: taskID, Version: version, Profile: domain.AgentExecutionProfile(profile),
		ActorID: actorID, SessionID: sessionID, SelectedAt: at,
	})
}

func (tx *writeTx) InsertTaskAgentExecutionProfilePreference(ctx context.Context, preference domain.AgentExecutionProfilePreference) error {
	restored, err := domain.NewAgentExecutionProfilePreference(preference.Record())
	if err != nil || restored.Record() != preference.Record() {
		return fmt.Errorf("%w: invalid preference", storecontract.ErrExecutionProfileConflict)
	}
	if err := tx.requireActiveRoomForTask(ctx, preference.TaskID()); err != nil {
		return err
	}
	_, err = tx.tx.ExecContext(ctx, `INSERT INTO agent_execution_profile_preferences(task_id,version,profile,actor_id,session_id,selected_at)
		VALUES(?,?,?,?,?,?)`, preference.TaskID().String(), preference.Version(), string(preference.Profile()),
		preference.ActorID(), preference.SessionID(), timeText(preference.SelectedAt()))
	return mapExecutionProfileWriteError(err)
}

func (reader *reader) GetTrustedLocalAcknowledgement(ctx context.Context, actorID, policyVersion string) (domain.TrustedLocalAcknowledgement, error) {
	var sessionID, acknowledgedAt string
	err := reader.q.QueryRowContext(ctx, `SELECT session_id,acknowledged_at FROM trusted_local_acknowledgements
		WHERE actor_id=? AND policy_version=?`, actorID, policyVersion).Scan(&sessionID, &acknowledgedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.TrustedLocalAcknowledgement{}, storecontract.ErrNotFound
	}
	if err != nil {
		return domain.TrustedLocalAcknowledgement{}, err
	}
	at, err := parseTime(acknowledgedAt)
	if err != nil {
		return domain.TrustedLocalAcknowledgement{}, err
	}
	return domain.NewTrustedLocalAcknowledgement(domain.TrustedLocalAcknowledgementRecord{
		PolicyVersion: policyVersion, ActorID: actorID, SessionID: sessionID, AcknowledgedAt: at,
	})
}

func (tx *writeTx) InsertTrustedLocalAcknowledgement(ctx context.Context, acknowledgement domain.TrustedLocalAcknowledgement) error {
	restored, err := domain.NewTrustedLocalAcknowledgement(acknowledgement.Record())
	if err != nil || restored.Record() != acknowledgement.Record() {
		return fmt.Errorf("%w: invalid acknowledgement", storecontract.ErrExecutionProfileConflict)
	}
	_, err = tx.tx.ExecContext(ctx, `INSERT INTO trusted_local_acknowledgements(actor_id,policy_version,session_id,acknowledged_at)
		VALUES(?,?,?,?)`, acknowledgement.ActorID(), acknowledgement.PolicyVersion(), acknowledgement.SessionID(),
		timeText(acknowledgement.AcknowledgedAt()))
	return mapExecutionProfileWriteError(err)
}

func mapExecutionProfileWriteError(err error) error {
	if err == nil {
		return nil
	}
	message := err.Error()
	if strings.Contains(message, "agent_execution_profile_preferences") ||
		strings.Contains(message, "Agent execution profile preference") ||
		strings.Contains(message, "trusted_local_acknowledgements") ||
		strings.Contains(message, "Trusted Local acknowledgement") {
		return fmt.Errorf("%w: %v", storecontract.ErrExecutionProfileConflict, err)
	}
	return mapWriteError(err)
}
