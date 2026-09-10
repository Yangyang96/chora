package sqlite

import (
	"context"

	"github.com/Yangyang96/chora/internal/domain"
)

// Retained files alone do not prevent archive: their cleanup is a separate
// explicit operation. Unfinished delivery and uncertain writes do prevent it.
func (r *reader) taskHasExternalActivity(ctx context.Context, taskID domain.TaskID, protectResults bool) (bool, error) {
	var blocked bool
	err := r.q.QueryRowContext(ctx, `SELECT EXISTS(
 SELECT 1 FROM runs run WHERE run.task_id=? AND (
   run.state IN ('ready','running','stopping','awaiting_verification','verifying','verification_recovery_required')
   OR (run.state='recovery_required' AND NOT EXISTS(SELECT 1 FROM result_closures c WHERE c.run_id=run.id)))
 UNION ALL SELECT 1 FROM verification_runs verification JOIN runs run ON run.id=verification.run_id WHERE run.task_id=? AND verification.state IN ('awaiting','verifying','recovery_required')
 UNION ALL SELECT 1 FROM task_delivery_operations WHERE task_id=? AND state IN ('writing','recovery_required')
 UNION ALL SELECT 1 FROM patch_applications apply JOIN runs run ON run.id=apply.run_id WHERE run.task_id=? AND apply.state IN ('applying','recovery_required')
 UNION ALL SELECT 1 FROM apply_repository_steps step JOIN apply_operations operation ON operation.operation_id=step.operation_id JOIN resource_result_groups result ON result.result_id=operation.result_id
 WHERE result.task_id=? AND step.status IN ('writing','uncertain') AND step.sequence=(SELECT max(latest.sequence) FROM apply_repository_steps latest WHERE latest.operation_id=step.operation_id AND latest.repo_id=step.repo_id)
)`, taskID.String(), taskID.String(), taskID.String(), taskID.String(), taskID.String()).Scan(&blocked)
	if err != nil || blocked || !protectResults {
		return blocked, err
	}
	err = r.q.QueryRowContext(ctx, `SELECT EXISTS(
 SELECT 1 FROM resource_result_groups result JOIN result_repository_changes change ON change.result_id=result.result_id
 WHERE result.task_id=? AND json_array_length(change.canonical_json,'$.changedPaths')>0
 AND NOT EXISTS(SELECT 1 FROM result_closures closure, json_each(closure.repository_ids) entry WHERE closure.result_id=result.result_id AND entry.value=change.repo_id)
 AND NOT EXISTS(SELECT 1 FROM apply_operations operation JOIN apply_repository_steps step ON step.operation_id=operation.operation_id WHERE operation.result_id=result.result_id AND step.repo_id=change.repo_id AND step.status='applied')
 AND NOT EXISTS(SELECT 1 FROM task_delivery_operations delivery WHERE delivery.task_id=result.task_id AND delivery.repo_id=change.repo_id AND delivery.state='succeeded' AND (delivery.kind='cleanup' OR json_extract(delivery.outcome_json,'$.PR.State')='merged'))
 UNION ALL SELECT 1 FROM runs run WHERE run.task_id=? AND run.state IN ('awaiting_review','accepted','revision_required')
 AND NOT EXISTS(SELECT 1 FROM resource_result_groups result WHERE result.run_id=run.id)
 AND NOT EXISTS(SELECT 1 FROM result_closures closure WHERE closure.run_id=run.id)
 AND NOT EXISTS(SELECT 1 FROM patch_applications apply WHERE apply.run_id=run.id AND apply.state='applied')
 UNION ALL SELECT 1 FROM task_delivery_operations delivery WHERE delivery.task_id=? AND delivery.state='succeeded' AND delivery.kind IN ('commit','push','pr')
 AND NOT EXISTS(SELECT 1 FROM task_delivery_operations done WHERE done.task_id=delivery.task_id AND done.repo_id=delivery.repo_id AND done.state='succeeded' AND (done.kind='cleanup' OR json_extract(done.outcome_json,'$.PR.State')='merged'))
)`, taskID.String(), taskID.String(), taskID.String()).Scan(&blocked)
	return blocked, err
}
