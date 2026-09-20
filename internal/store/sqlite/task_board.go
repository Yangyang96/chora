package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
	"strings"
)

// GetTaskBoardSnapshot uses a read transaction and a fixed set of summary
// queries regardless of task/room count. No external resources are inspected.
func (r *reader) GetTaskBoardSnapshot(ctx context.Context, id domain.ProjectID) (storecontract.TaskBoardSnapshot, error) {
	if db, ok := r.q.(*sql.DB); ok {
		tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			return storecontract.TaskBoardSnapshot{}, err
		}
		defer tx.Rollback()
		return (&reader{q: tx}).GetTaskBoardSnapshot(ctx, id)
	}
	var out storecontract.TaskBoardSnapshot
	var err error
	if out.Project, err = r.GetProject(ctx, id); err != nil {
		return out, err
	}
	if out.Rooms, err = r.ListProjectRooms(ctx, id); err != nil {
		return out, err
	}
	out.Repositories = []storecontract.TaskBoardRepository{}
	rows, err := r.q.QueryContext(ctx, `SELECT r.repo_id,r.name FROM project_repositories p JOIN repositories r ON r.repo_id=p.repo_id WHERE p.project_id=? ORDER BY r.repo_id`, id.String())
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var v storecontract.TaskBoardRepository
		if err = rows.Scan(&v.RepoID, &v.Name); err != nil {
			rows.Close()
			return out, err
		}
		out.Repositories = append(out.Repositories, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	query := `WITH room_tasks AS (SELECT task.* FROM tasks task JOIN rooms room ON room.id=task.room_id WHERE room.project_id=?)` + workspaceTasksQueryTail
	// Activity is summary metadata; never fan out over full event history.
	query = strings.ReplaceAll(query, "  UNION ALL SELECT run.task_id,event.occurred_at FROM run_events event JOIN runs run ON run.id=event.run_id JOIN room_tasks task ON task.id=run.task_id\n", "")
	rows, err = r.q.QueryContext(ctx, query, id.String())
	if err != nil {
		return out, err
	}
	var current *workspaceTaskRow
	var criteria []domain.AcceptanceCriterion
	add := func() {
		if current == nil {
			return
		}
		v := storecontract.TaskBoardFacts{TaskID: current.idText, RoomID: current.roomText, Title: current.title, State: current.stateText, Archived: current.archived, Repositories: []storecontract.TaskBoardRepositoryFacts{}}
		v.Activity, _ = parseTime(current.lastActivityAt)
		v.Item, err = current.restore(criteria)
		if err != nil {
			v.Invalid = "invalid_task_summary"
		}
		out.Tasks = append(out.Tasks, v)
	}
	for rows.Next() {
		row, c, e := scanWorkspaceTaskRow(rows)
		if e != nil {
			rows.Close()
			return out, e
		}
		if current != nil && current.idText != row.idText {
			add()
			criteria = nil
		}
		if current == nil || current.idText != row.idText {
			current = &row
		}
		if c != nil {
			criteria = append(criteria, *c)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	add()
	index := map[string]*storecontract.TaskBoardFacts{}
	for i := range out.Tasks {
		v := &out.Tasks[i]
		if _, ok := index[v.TaskID]; ok {
			v.Invalid = "duplicate_task_summary"
		}
		index[v.TaskID] = v
	}
	// Scope by latest Run and its current Attempt, never a historical result.
	const scope = `WITH ranked AS (SELECT r.*,row_number() OVER(PARTITION BY r.task_id ORDER BY r.created_at DESC,r.id ASC) n FROM runs r JOIN tasks t ON t.id=r.task_id JOIN rooms room ON room.id=t.room_id WHERE room.project_id=?), current_runs AS (SELECT * FROM ranked WHERE n=1) `
	rows, err = r.q.QueryContext(ctx, scope+`SELECT r.task_id,coalesce(a.id,''),coalesce(a.state,''),coalesce(g.result_id,''),coalesce(json_extract((CASE WHEN json_valid(g.canonical_json) THEN g.canonical_json ELSE '{}' END),'$.outcome'),''),coalesce(review.kind,''),EXISTS(SELECT 1 FROM result_closures c WHERE c.run_id=r.id AND c.attempt_id=a.id AND c.task_id=r.task_id AND (g.result_id IS NULL OR (c.result_id=g.result_id AND c.result_digest=g.digest))),coalesce((SELECT gate.id FROM execution_decision_gates gate WHERE gate.run_id=r.id AND gate.attempt_id=a.id AND gate.status='open' ORDER BY gate.requested_at DESC,gate.id LIMIT 1),''),coalesce((SELECT substr(gate.question,1,500) FROM execution_decision_gates gate WHERE gate.run_id=r.id AND gate.attempt_id=a.id AND gate.status='open' ORDER BY gate.requested_at DESC,gate.id LIMIT 1),'') FROM current_runs r LEFT JOIN attempts a ON a.run_id=r.id AND a.sequence=r.current_attempt_number LEFT JOIN resource_result_groups g ON g.attempt_id=a.id AND g.run_id=r.id AND g.task_id=r.task_id LEFT JOIN resource_result_reviews rr ON rr.result_id=g.result_id AND rr.result_digest=g.digest LEFT JOIN review_decisions review ON review.id=rr.review_id AND review.run_id=r.id`, id.String())
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var task, a, as, res, outcome, review, gate, gateQuestion string
		var closed bool
		if err = rows.Scan(&task, &a, &as, &res, &outcome, &review, &closed, &gate, &gateQuestion); err != nil {
			break
		}
		if v := index[task]; v != nil {
			v.AttemptID = a
			v.AttemptState = as
			v.ResourceResultID = res
			v.ResourceOutcome = outcome
			v.ResourceReviewKind = review
			v.Closure = closed
			v.GateID = gate
			v.GateQuestion = gateQuestion
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return out, err
	}
	rows, err = r.q.QueryContext(ctx, scope+`SELECT s.task_id,coalesce(json_extract((CASE WHEN json_valid(entry.value) THEN entry.value ELSE '{}' END),'$.repoId'),''),coalesce(json_extract((CASE WHEN json_valid(entry.value) THEN entry.value ELSE '{}' END),'$.name'),''),coalesce(json_extract((CASE WHEN json_valid(entry.value) THEN entry.value ELSE '{}' END),'$.role'),''),coalesce(json_extract((CASE WHEN json_valid(entry.value) THEN entry.value ELSE '{}' END),'$.deliveryMode'),''),coalesce(w.state,''),change.repo_id IS NOT NULL,coalesce(json_array_length((CASE WHEN json_valid(change.canonical_json) THEN change.canonical_json ELSE '{}' END),'$.changedPaths'),0),coalesce(json_extract((CASE WHEN json_valid(change.canonical_json) THEN change.canonical_json ELSE '{}' END),'$.checks.Status'),''),coalesce(json_extract((CASE WHEN json_valid(entry.value) THEN entry.value ELSE '{}' END),'$.checks.mode'),''),coalesce(json_extract((CASE WHEN json_valid(change.canonical_json) THEN change.canonical_json ELSE '{}' END),'$.checks.NoApplicableChecks'),0),EXISTS(SELECT 1 FROM result_closures c,json_each((CASE WHEN json_valid(c.repository_ids) THEN c.repository_ids ELSE '[]' END)) e WHERE c.result_id=g.result_id AND c.run_id=r.id AND c.task_id=task.id AND c.attempt_id=a.id AND c.result_digest=g.digest AND e.value=change.repo_id),coalesce((SELECT step.status FROM apply_operations op JOIN apply_repository_steps step ON step.operation_id=op.operation_id WHERE op.result_id=g.result_id AND step.repo_id=json_extract((CASE WHEN json_valid(entry.value) THEN entry.value ELSE '{}' END),'$.repoId') ORDER BY step.sequence DESC LIMIT 1),'') FROM task_resource_snapshots s JOIN tasks task ON task.id=s.task_id JOIN rooms room ON room.id=task.room_id JOIN json_each((CASE WHEN json_valid(s.canonical_json) THEN s.canonical_json ELSE '{}' END),'$.resources') entry LEFT JOIN current_runs r ON r.task_id=task.id LEFT JOIN attempts a ON a.run_id=r.id AND a.sequence=r.current_attempt_number LEFT JOIN resource_result_groups g ON g.attempt_id=a.id AND g.run_id=r.id LEFT JOIN result_repository_changes change ON change.result_id=g.result_id AND change.repo_id=json_extract((CASE WHEN json_valid(entry.value) THEN entry.value ELSE '{}' END),'$.repoId') LEFT JOIN task_repository_worktrees w ON w.task_id=task.id AND w.repo_id=json_extract((CASE WHEN json_valid(entry.value) THEN entry.value ELSE '{}' END),'$.repoId') WHERE room.project_id=? ORDER BY task.id,entry.key`, id.String(), id.String())
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var task string
		var v storecontract.TaskBoardRepositoryFacts
		if err = rows.Scan(&task, &v.RepoID, &v.Name, &v.Role, &v.Mode, &v.Preparation, &v.ResultPresent, &v.Changed, &v.ChecksStatus, &v.ChecksMode, &v.ChecksNotApplicable, &v.Closed, &v.ApplyState); err != nil {
			break
		}
		if t := index[task]; t != nil {
			t.ResourceSnapshot = true
			t.Repositories = append(t.Repositories, v)
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return out, err
	}
	// Validate frozen identity, result cardinality, digest references and delivery
	// intent binding inside SQL, extracting no patches or filesystem paths.
	rows, err = r.q.QueryContext(ctx, scope+`SELECT task.id,s.task_id IS NOT NULL,
 CASE WHEN EXISTS(SELECT 1 FROM result_closures c WHERE c.run_id=r.id AND (c.task_id IS NOT task.id OR c.attempt_id IS NOT a.id OR (g.result_id IS NOT NULL AND (c.result_id IS NOT g.result_id OR c.result_digest IS NOT g.digest)) OR NOT json_valid(c.repository_ids) OR json_type((CASE WHEN json_valid(c.repository_ids) THEN c.repository_ids ELSE '{}' END)) IS NOT 'array')) OR EXISTS(SELECT 1 FROM task_delivery_operations o WHERE o.run_id=r.id AND (NOT json_valid(o.preview_json) OR NOT json_valid(o.outcome_json) OR json_type((CASE WHEN json_valid(o.preview_json) THEN o.preview_json ELSE '{}' END)) IS NOT 'object' OR json_type((CASE WHEN json_valid(o.outcome_json) THEN o.outcome_json ELSE '{}' END)) IS NOT 'object')) THEN 1
 WHEN s.task_id IS NOT NULL AND (NOT json_valid(s.canonical_json) OR json_type((CASE WHEN json_valid(s.canonical_json) THEN s.canonical_json ELSE '{}' END),'$.resources') IS NOT 'array' OR EXISTS(SELECT 1 FROM json_each((CASE WHEN json_valid(s.canonical_json) THEN s.canonical_json ELSE '{}' END),'$.resources') child WHERE json_type((CASE WHEN json_valid(child.value) THEN child.value ELSE '{}' END),'$.repoId') IS NOT 'text' OR json_type((CASE WHEN json_valid(child.value) THEN child.value ELSE '{}' END),'$.baseCommit') IS NOT 'text' OR json_type((CASE WHEN json_valid(child.value) THEN child.value ELSE '{}' END),'$.baseTree') IS NOT 'text') OR coalesce(json_extract((CASE WHEN json_valid(s.canonical_json) THEN s.canonical_json ELSE '{}' END),'$.schemaVersion'),'') IS NOT 'chora.task-resources.v2' OR json_extract((CASE WHEN json_valid(s.canonical_json) THEN s.canonical_json ELSE '{}' END),'$.taskId') IS NOT task.id OR json_extract((CASE WHEN json_valid(s.canonical_json) THEN s.canonical_json ELSE '{}' END),'$.roomId') IS NOT task.room_id OR json_extract((CASE WHEN json_valid(s.canonical_json) THEN s.canonical_json ELSE '{}' END),'$.projectId') IS NOT room.project_id OR coalesce(json_array_length((CASE WHEN json_valid(s.canonical_json) THEN s.canonical_json ELSE '{}' END),'$.resources'),0)=0) THEN 1
 WHEN g.result_id IS NOT NULL AND (NOT json_valid(g.canonical_json) OR json_extract((CASE WHEN json_valid(g.canonical_json) THEN g.canonical_json ELSE '{}' END),'$.schemaVersion') IS NOT 'chora.result-group.v2' OR json_extract((CASE WHEN json_valid(g.canonical_json) THEN g.canonical_json ELSE '{}' END),'$.outcome') IS NULL OR json_extract((CASE WHEN json_valid(g.canonical_json) THEN g.canonical_json ELSE '{}' END),'$.outcome') NOT IN ('review_ready','checks_failed','checks_incomplete','completed_no_change') OR json_extract((CASE WHEN json_valid(g.canonical_json) THEN g.canonical_json ELSE '{}' END),'$.id') IS NOT g.result_id OR json_type((CASE WHEN json_valid(g.canonical_json) THEN g.canonical_json ELSE '{}' END),'$.repositories') IS NOT 'array' OR s.task_id IS NULL OR json_extract((CASE WHEN json_valid(g.canonical_json) THEN g.canonical_json ELSE '{}' END),'$.resourceSnapshotDigest') IS NOT lower(hex(s.digest)) OR json_extract((CASE WHEN json_valid(g.canonical_json) THEN g.canonical_json ELSE '{}' END),'$.taskId') IS NOT task.id OR json_extract((CASE WHEN json_valid(g.canonical_json) THEN g.canonical_json ELSE '{}' END),'$.runId') IS NOT r.id OR json_extract((CASE WHEN json_valid(g.canonical_json) THEN g.canonical_json ELSE '{}' END),'$.attemptId') IS NOT a.id OR json_array_length((CASE WHEN json_valid(g.canonical_json) THEN g.canonical_json ELSE '{}' END),'$.repositories') IS NOT json_array_length((CASE WHEN json_valid(s.canonical_json) THEN s.canonical_json ELSE '{}' END),'$.resources') OR (SELECT count(*) FROM result_repository_changes c WHERE c.result_id=g.result_id) IS NOT json_array_length((CASE WHEN json_valid(s.canonical_json) THEN s.canonical_json ELSE '{}' END),'$.resources') OR EXISTS(SELECT 1 FROM result_repository_changes c WHERE c.result_id=g.result_id AND NOT EXISTS(SELECT 1 FROM json_each((CASE WHEN json_valid(s.canonical_json) THEN s.canonical_json ELSE '{}' END),'$.resources') e WHERE json_extract((CASE WHEN json_valid(e.value) THEN e.value ELSE '{}' END),'$.repoId')=c.repo_id AND json_extract((CASE WHEN json_valid(e.value) THEN e.value ELSE '{}' END),'$.baseCommit')=json_extract((CASE WHEN json_valid(c.canonical_json) THEN c.canonical_json ELSE '{}' END),'$.baseCommit') AND json_extract((CASE WHEN json_valid(e.value) THEN e.value ELSE '{}' END),'$.baseTree')=json_extract((CASE WHEN json_valid(c.canonical_json) THEN c.canonical_json ELSE '{}' END),'$.baseTree')))) THEN 1
 WHEN EXISTS(SELECT 1 FROM task_delivery_operations o WHERE o.run_id=r.id AND o.task_id=task.id AND o.kind IS NOT 'cleanup' AND (g.result_id IS NULL OR coalesce(json_extract((CASE WHEN json_valid(o.preview_json) THEN o.preview_json ELSE '{}' END),'$.ResultDigest'),'') IS NOT lower(hex(g.digest)) OR NOT EXISTS(SELECT 1 FROM resource_result_reviews rv WHERE rv.result_id=g.result_id AND rv.review_id=json_extract((CASE WHEN json_valid(o.preview_json) THEN o.preview_json ELSE '{}' END),'$.ReviewID') AND rv.result_digest=g.digest) OR NOT EXISTS(SELECT 1 FROM json_each((CASE WHEN json_valid(s.canonical_json) THEN s.canonical_json ELSE '{}' END),'$.resources') e WHERE json_extract((CASE WHEN json_valid(e.value) THEN e.value ELSE '{}' END),'$.repoId')=o.repo_id AND json_extract((CASE WHEN json_valid(e.value) THEN e.value ELSE '{}' END),'$.baseCommit')=json_extract((CASE WHEN json_valid(o.preview_json) THEN o.preview_json ELSE '{}' END),'$.Binding.BaseCommit') AND json_extract((CASE WHEN json_valid(e.value) THEN e.value ELSE '{}' END),'$.baseTree')=json_extract((CASE WHEN json_valid(o.preview_json) THEN o.preview_json ELSE '{}' END),'$.Binding.BaseTree') AND json_extract((CASE WHEN json_valid(e.value) THEN e.value ELSE '{}' END),'$.baseRef')=json_extract((CASE WHEN json_valid(o.preview_json) THEN o.preview_json ELSE '{}' END),'$.Binding.TargetRef') AND json_extract((CASE WHEN json_valid(e.value) THEN e.value ELSE '{}' END),'$.taskBranch')=json_extract((CASE WHEN json_valid(o.preview_json) THEN o.preview_json ELSE '{}' END),'$.Binding.Branch')))) THEN 1 ELSE 0 END
 FROM tasks task JOIN rooms room ON room.id=task.room_id LEFT JOIN task_resource_snapshots s ON s.task_id=task.id LEFT JOIN current_runs r ON r.task_id=task.id LEFT JOIN attempts a ON a.run_id=r.id AND a.sequence=r.current_attempt_number LEFT JOIN resource_result_groups g ON g.attempt_id=a.id WHERE room.project_id=?`, id.String(), id.String())
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var task string
		var snapshot, invalid bool
		if err = rows.Scan(&task, &snapshot, &invalid); err != nil {
			break
		}
		if f := index[task]; f != nil {
			f.ResourceSnapshot = snapshot
			if invalid {
				f.Invalid = "invalid_resource_lineage"
			}
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return out, err
	}
	// Keep the newest observation for each operation/state kind plus retained
	// successful achievements; output is bounded by repository and operation kinds.
	rows, err = r.q.QueryContext(ctx, scope+`, ops AS (SELECT o.*,row_number() OVER(PARTITION BY o.task_id,o.repo_id,o.kind,o.state ORDER BY o.updated_at DESC,o.operation_id DESC) rank FROM task_delivery_operations o JOIN current_runs r ON r.id=o.run_id AND r.task_id=o.task_id) SELECT task_id,repo_id,operation_id,kind,state,version,coalesce(json_extract((CASE WHEN json_valid(outcome_json) THEN outcome_json ELSE '{}' END),'$.PR.State'),''),coalesce(json_extract((CASE WHEN json_valid(outcome_json) THEN outcome_json ELSE '{}' END),'$.PR.MergeCommit'),''),coalesce(json_extract((CASE WHEN json_valid(outcome_json) THEN outcome_json ELSE '{}' END),'$.Commit'),''),updated_at FROM ops WHERE rank=1 ORDER BY updated_at,operation_id`, id.String())
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var task, repo, at string
		var op storecontract.TaskBoardOperation
		if err = rows.Scan(&task, &repo, &op.ID, &op.Kind, &op.State, &op.Version, &op.PRState, &op.MergeCommit, &op.Commit, &at); err != nil {
			break
		}
		op.UpdatedAt, err = parseTime(at)
		if err != nil {
			break
		}
		if t := index[task]; t != nil {
			found := false
			for i := range t.Repositories {
				if t.Repositories[i].RepoID == repo {
					t.Repositories[i].Operations = append(t.Repositories[i].Operations, op)
					found = true
				}
			}
			if !found {
				t.Invalid = "delivery_scope_mismatch"
			}
			if op.UpdatedAt.After(t.Activity) {
				t.Activity = op.UpdatedAt
			}
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return out, err
	}
	// Persisted hosting refreshes are separate append-only observations. Select
	// one current observation per successful PR intent without loading Run logs.
	rows, err = r.q.QueryContext(ctx, scope+`, observations AS (SELECT r.task_id,o.repo_id,o.operation_id,o.version,e.normalized_json,e.occurred_at,row_number() OVER(PARTITION BY o.operation_id ORDER BY e.sequence DESC) n,o.preview_json FROM current_runs r JOIN task_delivery_operations o ON o.run_id=r.id AND o.task_id=r.task_id AND o.kind='pr' AND o.state='succeeded' JOIN run_events e ON e.run_id=r.id AND e.type='task_delivery.github.observed' AND json_extract(e.normalized_json,'$.OperationID')=o.operation_id AND json_extract(e.normalized_json,'$.RepoID')=o.repo_id) SELECT task_id,repo_id,operation_id,version,coalesce(json_extract(normalized_json,'$.PR.State'),''),coalesce(json_extract(normalized_json,'$.PR.MergeCommit'),''),occurred_at,coalesce(json_extract(normalized_json,'$.PR.Head'),'')=coalesce(json_extract((CASE WHEN json_valid(preview_json) THEN preview_json ELSE '{}' END),'$.Hosting.Head'),'') AND coalesce(json_extract(normalized_json,'$.PR.HeadBranch'),'')=coalesce(json_extract((CASE WHEN json_valid(preview_json) THEN preview_json ELSE '{}' END),'$.Hosting.HeadBranch'),'') AND coalesce(json_extract(normalized_json,'$.PR.BaseBranch'),'')=coalesce(json_extract((CASE WHEN json_valid(preview_json) THEN preview_json ELSE '{}' END),'$.Hosting.BaseBranch'),'') AND coalesce(json_extract(normalized_json,'$.PR.Repository'),'')=coalesce(json_extract((CASE WHEN json_valid(preview_json) THEN preview_json ELSE '{}' END),'$.Hosting.Repository'),'') FROM observations WHERE n=1`, id.String())
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var task, repo, at string
		var valid bool
		var o storecontract.TaskBoardOperation
		o.Kind = "pr"
		o.State = "succeeded"
		if err = rows.Scan(&task, &repo, &o.ID, &o.Version, &o.PRState, &o.MergeCommit, &at, &valid); err != nil {
			break
		}
		o.UpdatedAt, err = parseTime(at)
		if err != nil {
			break
		}
		if f := index[task]; f != nil {
			if !valid {
				f.Invalid = "invalid_hosting_observation"
			}
			for i := range f.Repositories {
				if f.Repositories[i].RepoID == repo {
					for j := range f.Repositories[i].Operations {
						old := &f.Repositories[i].Operations[j]
						if old.ID == o.ID {
							*old = o
						}
					}
				}
			}
			if o.UpdatedAt.After(f.Activity) {
				f.Activity = o.UpdatedAt
			}
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return out, err
	}
	rows, err = r.q.QueryContext(ctx, scope+`SELECT r.task_id,coalesce((SELECT json_extract(e.normalized_json,'$.failure_reason') FROM run_events e WHERE e.run_id=r.id AND e.type IN ('attempt.failed','attempt.reconciled_dead') AND json_extract(e.normalized_json,'$.attempt_id')=a.id ORDER BY e.sequence DESC LIMIT 1),''),EXISTS(SELECT 1 FROM runtime_sessions sess WHERE sess.attempt_id=a.id AND sess.terminal_at IS NOT NULL AND sess.finalized_at IS NOT NULL AND a.state='failed' AND ((sess.state='failed' AND sess.process_identity='' AND sess.started_at IS NULL) OR (sess.state='stopped' AND sess.process_identity<>'' AND sess.started_at IS NOT NULL))) FROM current_runs r LEFT JOIN attempts a ON a.run_id=r.id AND a.sequence=r.current_attempt_number`, id.String())
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var task, failure string
		var finalized bool
		if err = rows.Scan(&task, &failure, &finalized); err != nil {
			break
		}
		if t := index[task]; t != nil {
			t.FailureReason = failure
			t.RetryFinalized = finalized
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return out, err
	}
	// Automatic retry uses durable control events, not Agent logs. Restrict to the
	// latest reset window and a fixed suffix; no transcript payloads are returned.
	rows, err = r.q.QueryContext(ctx, scope+`, retry AS (SELECT r.task_id,e.type,e.normalized_json,e.sequence,row_number() OVER(PARTITION BY r.id ORDER BY e.sequence DESC) n FROM current_runs r JOIN run_events e ON e.run_id=r.id WHERE e.type LIKE 'automatic_retry.%' OR e.type='run.started') SELECT task_id,type,normalized_json,sequence FROM retry WHERE n<=8 ORDER BY task_id,sequence`, id.String())
	if err != nil {
		return out, fmt.Errorf("board retry summaries: %w", err)
	}
	for rows.Next() {
		var task string
		var e storecontract.TaskBoardRetryEvent
		if err = rows.Scan(&task, &e.Type, &e.Payload, &e.Sequence); err != nil {
			break
		}
		if t := index[task]; t != nil {
			t.RetryEvents = append(t.RetryEvents, e)
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	return out, err
}
