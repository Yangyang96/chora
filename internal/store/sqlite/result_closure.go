package sqlite

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"

	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

const resultClosureColumns = `result_id,run_id,task_id,attempt_id,result_digest,preview_digest,review_id,repository_ids,actor_id,session_id,created_at`

func scanResultClosure(row interface{ Scan(...any) error }) (storecontract.ResultClosure, error) {
	var c storecontract.ResultClosure
	var result, run, task, attempt, created string
	var digest, preview, repos []byte
	err := row.Scan(&result, &run, &task, &attempt, &digest, &preview, &c.ReviewID, &repos, &c.ActorID, &c.SessionID, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return c, storecontract.ErrNotFound
	}
	if err != nil {
		return c, err
	}
	if c.ResultID, err = domain.ParseResultID(result); err != nil {
		return c, err
	}
	if c.RunID, err = domain.ParseRunID(run); err != nil {
		return c, err
	}
	if c.TaskID, err = domain.ParseTaskID(task); err != nil {
		return c, err
	}
	if c.AttemptID, err = domain.ParseAttemptID(attempt); err != nil {
		return c, err
	}
	if len(digest) != 32 || len(preview) != 32 {
		return c, storecontract.ErrVerificationConflict
	}
	copy(c.ResultDigest[:], digest)
	copy(c.PreviewDigest[:], preview)
	if err = json.Unmarshal(repos, &c.RepositoryIDs); err != nil {
		return c, err
	}
	c.CreatedAt, err = parseTime(created)
	return c, err
}

func (r *reader) GetResultClosure(ctx context.Context, id domain.ResultID) (storecontract.ResultClosure, error) {
	return scanResultClosure(r.q.QueryRowContext(ctx, `SELECT `+resultClosureColumns+` FROM result_closures WHERE result_id=?`, id.String()))
}

func (r *reader) ListResultClosuresForRun(ctx context.Context, id domain.RunID) ([]storecontract.ResultClosure, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT `+resultClosureColumns+` FROM result_closures WHERE run_id=? ORDER BY created_at,result_id`, id.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []storecontract.ResultClosure{}
	for rows.Next() {
		item, err := scanResultClosure(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (tx *writeTx) InsertResultClosure(ctx context.Context, c storecontract.ResultClosure) error {
	if !c.ResultID.Valid() || !c.RunID.Valid() || !c.TaskID.Valid() || !c.AttemptID.Valid() || c.ResultDigest == ([32]byte{}) || c.PreviewDigest == ([32]byte{}) || c.ActorID == "" || c.SessionID == "" || c.CreatedAt.IsZero() || len(c.RepositoryIDs) == 0 || len(c.RepositoryIDs) > domain.TaskRepositoryLimit || !sort.StringsAreSorted(c.RepositoryIDs) {
		return domain.ErrInvalidArgument
	}
	for i, id := range c.RepositoryIDs {
		if i > 0 && id == c.RepositoryIDs[i-1] {
			return domain.ErrInvalidArgument
		}
		if id == "" && len(c.RepositoryIDs) != 1 {
			return domain.ErrInvalidArgument
		}
		if id != "" {
			if _, err := domain.ParseRepositoryID(id); err != nil {
				return err
			}
		}
	}
	run, err := tx.GetRun(ctx, c.RunID)
	if err != nil {
		return err
	}
	a, err := tx.GetCurrentAttempt(ctx, c.RunID)
	if err != nil {
		return err
	}
	if run.TaskID() != c.TaskID || a.ID() != c.AttemptID {
		return storecontract.ErrVerificationConflict
	}
	switch run.State() {
	case domain.RunStateAwaitingReview, domain.RunStateRevisionRequired, domain.RunStateAccepted, domain.RunStateCompleted, domain.RunStateCancelled, domain.RunStateRecoveryRequired:
	default:
		return storecontract.ErrVersionConflict
	}
	var belongs, blocked bool
	if err = tx.tx.QueryRowContext(ctx, `SELECT EXISTS(
 SELECT 1 FROM resource_result_groups WHERE result_id=? AND run_id=? AND attempt_id=? AND digest=?
 UNION ALL SELECT 1 FROM local_review_results WHERE id=? AND run_id=? AND agent_attempt_id=?
 UNION ALL SELECT 1 FROM verification_results result JOIN verification_runs verification ON verification.id=result.verification_run_id WHERE result.id=? AND verification.run_id=? AND verification.agent_attempt_id=?
)`, c.ResultID.String(), c.RunID.String(), c.AttemptID.String(), c.ResultDigest[:], c.ResultID.String(), c.RunID.String(), c.AttemptID.String(), c.ResultID.String(), c.RunID.String(), c.AttemptID.String()).Scan(&belongs); err != nil {
		return err
	}
	if !belongs {
		id, digest, terminalErr := storecontract.TerminalResultForClosure(ctx, tx, run, a)
		if terminalErr != nil || id != c.ResultID || digest != c.ResultDigest {
			return storecontract.ErrVerificationConflict
		}
	}
	group, groupErr := tx.GetResourceResultGroup(ctx, c.AttemptID)
	if run.State() == domain.RunStateRecoveryRequired {
		session, sessionErr := tx.GetRuntimeSessionForAttempt(ctx, c.AttemptID)
		finalized := sessionErr == nil && !session.TerminalAt.IsZero() && !session.FinalizedAt.IsZero() &&
			((session.State == "failed" && session.ProcessIdentity == "" && session.StartedAt.IsZero()) ||
				(session.State == "stopped" && session.ProcessIdentity != "" && !session.StartedAt.IsZero()))
		if (groupErr != nil && !errors.Is(groupErr, storecontract.ErrNotFound)) || (groupErr == nil && group.Outcome != "checks_failed" && group.Outcome != "checks_incomplete") || a.State() != domain.AttemptStateFailed || !finalized {
			return storecontract.ErrVersionConflict
		}
	}
	if groupErr == nil {
		if group.ID != c.ResultID.String() {
			return storecontract.ErrVerificationConflict
		}
		for _, repo := range c.RepositoryIDs {
			found := false
			for _, entry := range group.Repositories {
				if entry.RepoID == repo {
					found = true
					break
				}
			}
			if !found {
				return storecontract.ErrVerificationConflict
			}
			var retained bool
			if err = tx.tx.QueryRowContext(ctx, `SELECT EXISTS(
 SELECT 1 FROM task_delivery_operations WHERE task_id=? AND repo_id=? AND run_id=? AND state='succeeded' AND kind IN ('commit','push','pr','merge') AND json_extract(preview_json,'$.ResultDigest')=?
 UNION ALL SELECT 1 FROM apply_operations operation JOIN apply_repository_steps step ON step.operation_id=operation.operation_id WHERE operation.result_id=? AND step.repo_id=? AND step.status='applied'
)`, c.TaskID.String(), repo, c.RunID.String(), hex.EncodeToString(c.ResultDigest[:]), c.ResultID.String(), repo).Scan(&retained); err != nil {
				return err
			}
			if retained {
				return storecontract.ErrVerificationConflict
			}
		}
		review, digest, reviewErr := tx.GetResourceResultReview(ctx, c.ResultID)
		if reviewErr == nil {
			if review.ID().String() != c.ReviewID || review.RunID() != c.RunID || digest != c.ResultDigest {
				return storecontract.ErrVerificationConflict
			}
		} else if !errors.Is(reviewErr, storecontract.ErrNotFound) {
			return reviewErr
		} else if c.ReviewID != "" {
			return storecontract.ErrVerificationConflict
		}
	} else if !errors.Is(groupErr, storecontract.ErrNotFound) {
		return groupErr
	} else {
		if len(c.RepositoryIDs) != 1 || c.RepositoryIDs[0] != "" {
			return storecontract.ErrVerificationConflict
		}
		review, reviewErr := tx.GetVerifiedReviewForResult(ctx, c.ResultID)
		if reviewErr == nil {
			if review.ID().String() != c.ReviewID || review.RunID() != c.RunID || review.Binding().AgentAttemptID != c.AttemptID {
				return storecontract.ErrVerificationConflict
			}
		} else if !errors.Is(reviewErr, storecontract.ErrNotFound) {
			return reviewErr
		} else if c.ReviewID != "" {
			return storecontract.ErrVerificationConflict
		}
		if applied, applyErr := tx.GetPatchApplication(ctx, c.RunID); applyErr == nil {
			if applied.State() == domain.PatchApplicationApplied {
				return storecontract.ErrVerificationConflict
			}
		} else if !errors.Is(applyErr, storecontract.ErrNotFound) {
			return applyErr
		}
	}
	if err = tx.tx.QueryRowContext(ctx, `SELECT EXISTS(
 SELECT 1 FROM runs WHERE task_id=? AND id<>? AND state IN ('ready','running','stopping','awaiting_verification','verifying','verification_recovery_required','recovery_required')
 UNION ALL SELECT 1 FROM task_delivery_operations WHERE task_id=? AND state IN ('writing','recovery_required')
 UNION ALL SELECT 1 FROM apply_repository_steps step JOIN apply_operations operation ON operation.operation_id=step.operation_id WHERE operation.result_id=? AND step.status IN ('writing','uncertain') AND step.sequence=(SELECT max(latest.sequence) FROM apply_repository_steps latest WHERE latest.operation_id=step.operation_id AND latest.repo_id=step.repo_id)
 UNION ALL SELECT 1 FROM patch_applications WHERE run_id=? AND state IN ('applying','recovery_required')
)`, c.TaskID.String(), c.RunID.String(), c.TaskID.String(), c.ResultID.String(), c.RunID.String()).Scan(&blocked); err != nil {
		return err
	}
	if blocked {
		return storecontract.ErrVersionConflict
	}
	if err = tx.requireActiveRoomForRun(ctx, c.RunID); err != nil {
		return err
	}
	repos, err := json.Marshal(c.RepositoryIDs)
	if err != nil || len(repos) > 4096 {
		return domain.ErrInvalidArgument
	}
	_, err = tx.tx.ExecContext(ctx, `INSERT INTO result_closures(`+resultClosureColumns+`) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, c.ResultID.String(), c.RunID.String(), c.TaskID.String(), c.AttemptID.String(), c.ResultDigest[:], c.PreviewDigest[:], c.ReviewID, repos, c.ActorID, c.SessionID, timeText(c.CreatedAt))
	return mapWriteError(err)
}

// Recheck at the durable write boundary, including a different service process.
func (tx *writeTx) requireOpenResultEntry(ctx context.Context, runID domain.RunID, repoID string) error {
	closures, err := tx.ListResultClosuresForRun(ctx, runID)
	if err != nil {
		return err
	}
	for _, c := range closures {
		for _, id := range c.RepositoryIDs {
			if id == repoID {
				return storecontract.ErrVerificationConflict
			}
		}
	}
	return nil
}

// A closed entry permits only cleanup of that exact Result, never another write.
func (tx *writeTx) requireClosedResultCleanup(ctx context.Context, o storecontract.DeliveryOperation) error {
	if o.Kind != "cleanup" {
		return storecontract.ErrVerificationConflict
	}
	var intent struct {
		ClosedResultID, ResultDigest, ReviewID string
		ResultCleanup                          json.RawMessage
	}
	if err := json.Unmarshal(o.PreviewJSON, &intent); err != nil {
		return err
	}
	if len(intent.ResultCleanup) == 0 || string(intent.ResultCleanup) == "null" {
		return storecontract.ErrVerificationConflict
	}
	id, err := domain.ParseResultID(intent.ClosedResultID)
	if err != nil {
		return storecontract.ErrVerificationConflict
	}
	c, err := tx.GetResultClosure(ctx, id)
	if err != nil {
		return err
	}
	if c.RunID != o.RunID || c.TaskID != o.TaskID || hex.EncodeToString(c.ResultDigest[:]) != intent.ResultDigest || c.ReviewID != intent.ReviewID {
		return storecontract.ErrVerificationConflict
	}
	found := false
	for _, repo := range c.RepositoryIDs {
		if repo == o.RepositoryID.String() {
			found = true
		}
	}
	if !found {
		return storecontract.ErrVerificationConflict
	}
	var blocked bool
	err = tx.tx.QueryRowContext(ctx, `SELECT EXISTS(
 SELECT 1 FROM runs WHERE task_id=? AND state IN ('ready','running','stopping','awaiting_verification','verifying','verification_recovery_required')
 UNION ALL SELECT 1 FROM task_delivery_operations WHERE task_id=? AND operation_id<>? AND (state IN ('writing','recovery_required') OR (repo_id=? AND state='succeeded'))
 UNION ALL SELECT 1 FROM patch_applications apply JOIN runs run ON run.id=apply.run_id WHERE run.task_id=? AND apply.state IN ('applying','recovery_required')
 UNION ALL SELECT 1 FROM apply_repository_steps step JOIN apply_operations operation ON operation.operation_id=step.operation_id JOIN resource_result_groups result ON result.result_id=operation.result_id WHERE result.task_id=? AND step.status IN ('writing','uncertain') AND step.sequence=(SELECT max(latest.sequence) FROM apply_repository_steps latest WHERE latest.operation_id=step.operation_id AND latest.repo_id=step.repo_id)
 )`, o.TaskID.String(), o.TaskID.String(), o.ID, o.RepositoryID.String(), o.TaskID.String(), o.TaskID.String()).Scan(&blocked)
	if err != nil {
		return err
	}
	if blocked {
		return storecontract.ErrVersionConflict
	}
	return nil
}
