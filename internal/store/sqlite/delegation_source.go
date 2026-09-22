package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

func (tx *writeTx) InsertDelegationProposalSource(ctx context.Context, s domain.DelegationProposalSource) error {
	if err := s.Validate(); err != nil {
		return err
	}
	d, err := tx.GetTaskDelegation(ctx, s.ParentTaskID)
	if err != nil {
		return err
	}
	if domain.DelegationPlanDigest(d.Assignments) != s.PlanDigest || !s.CreatedAt.Equal(d.CreatedAt) {
		return storecontract.ErrVerificationConflict
	}
	_, err = tx.tx.ExecContext(ctx, `INSERT INTO delegation_proposal_sources(parent_task_id,run_id,attempt_id,result_id,agent_report_id,event_id,event_sequence,result_digest,source_text_digest,plan_digest,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, s.ParentTaskID.String(), s.RunID.String(), s.AttemptID.String(), s.ResultID.String(), s.AgentReportID.String(), s.EventID.String(), s.EventSequence, s.ResultDigest[:], s.SourceTextDigest[:], s.PlanDigest[:], timeText(s.CreatedAt))
	return mapWriteError(err)
}

func (r *reader) GetDelegationProposalSource(ctx context.Context, id domain.TaskID) (domain.DelegationProposalSource, error) {
	var s domain.DelegationProposalSource
	var run, attempt, result, report, event, created string
	var rd, td, pd []byte
	err := r.q.QueryRowContext(ctx, `SELECT run_id,attempt_id,result_id,agent_report_id,event_id,event_sequence,result_digest,source_text_digest,plan_digest,created_at FROM delegation_proposal_sources WHERE parent_task_id=?`, id.String()).Scan(&run, &attempt, &result, &report, &event, &s.EventSequence, &rd, &td, &pd, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return s, storecontract.ErrNotFound
	}
	if err != nil {
		return s, err
	}
	s.ParentTaskID = id
	if s.RunID, err = domain.ParseRunID(run); err != nil {
		return s, err
	}
	if s.AttemptID, err = domain.ParseAttemptID(attempt); err != nil {
		return s, err
	}
	if s.ResultID, err = domain.ParseResultID(result); err != nil {
		return s, err
	}
	if s.AgentReportID, err = domain.ParseAgentReportID(report); err != nil {
		return s, err
	}
	if s.EventID, err = domain.ParseEventID(event); err != nil {
		return s, err
	}
	if s.CreatedAt, err = time.Parse(time.RFC3339Nano, created); err != nil {
		return s, err
	}
	if len(rd) != 32 || len(td) != 32 || len(pd) != 32 {
		return s, storecontract.ErrVerificationConflict
	}
	copy(s.ResultDigest[:], rd)
	copy(s.SourceTextDigest[:], td)
	copy(s.PlanDigest[:], pd)
	return s, s.Validate()
}
