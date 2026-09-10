package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

// GetRoomWorkspace loads the Room and all of its Task summary state with a
// fixed number of set queries. In particular, the Task query folds criteria
// into the same ordered row set instead of issuing one query per Task.
func (reader *reader) GetRoomWorkspace(ctx context.Context, roomID domain.RoomID) (storecontract.RoomWorkspaceProjection, error) {
	roomItem, err := reader.getRoomWorkspaceSummary(ctx, roomID)
	if err != nil {
		return storecontract.RoomWorkspaceProjection{}, err
	}

	result, err := reader.queryWorkspaceTasks(ctx, workspaceTasksQuery, roomID.String())
	if err != nil {
		return storecontract.RoomWorkspaceProjection{}, err
	}
	return storecontract.RoomWorkspaceProjection{Room: roomItem, Tasks: result}, nil
}

func (reader *reader) queryWorkspaceTasks(ctx context.Context, query string, argument any) ([]storecontract.TaskWorkspaceItem, error) {
	rows, err := reader.q.QueryContext(ctx, query, argument)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []storecontract.TaskWorkspaceItem
	var current *workspaceTaskRow
	var criteria []domain.AcceptanceCriterion
	for rows.Next() {
		row, criterion, err := scanWorkspaceTaskRow(rows)
		if err != nil {
			return nil, err
		}
		if current != nil && current.idText != row.idText {
			item, err := current.restore(criteria)
			if err != nil {
				return nil, err
			}
			result = append(result, item)
			criteria = nil
		}
		if current == nil || current.idText != row.idText {
			current = &row
		}
		if criterion != nil {
			criteria = append(criteria, *criterion)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if current != nil {
		item, err := current.restore(criteria)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, nil
}

func (reader *reader) getRoomWorkspaceSummary(ctx context.Context, roomID domain.RoomID) (storecontract.RoomDirectoryItem, error) {
	var idText, name, description, root, state, created, updated, activity string
	var version uint64
	var archived, project sql.NullString
	var ownership string
	var counts storecontract.RoomTaskCounts
	err := reader.q.QueryRowContext(ctx, `
WITH room_scope AS (
  SELECT * FROM rooms WHERE id=?
), activity(at) AS (
  SELECT updated_at FROM room_scope
  UNION ALL SELECT task.updated_at FROM tasks task JOIN room_scope room ON room.id=task.room_id
  UNION ALL SELECT draft.updated_at FROM technical_plan_drafts draft JOIN tasks task ON task.id=draft.task_id JOIN room_scope room ON room.id=task.room_id
  UNION ALL SELECT revision.submitted_at FROM technical_plan_revisions revision JOIN tasks task ON task.id=revision.task_id JOIN room_scope room ON room.id=task.room_id
  UNION ALL SELECT review.decided_at FROM technical_plan_reviews review JOIN tasks task ON task.id=review.task_id JOIN room_scope room ON room.id=task.room_id
  UNION ALL SELECT binding.bound_at FROM technical_plan_acceptance_bindings binding JOIN tasks task ON task.id=binding.task_id JOIN room_scope room ON room.id=task.room_id
  UNION ALL SELECT run.updated_at FROM runs run JOIN tasks task ON task.id=run.task_id JOIN room_scope room ON room.id=task.room_id
  UNION ALL SELECT event.occurred_at FROM run_events event JOIN runs run ON run.id=event.run_id JOIN tasks task ON task.id=run.task_id JOIN room_scope room ON room.id=task.room_id
  UNION ALL SELECT review.decided_at FROM review_decisions review JOIN runs run ON run.id=review.run_id JOIN tasks task ON task.id=run.task_id JOIN room_scope room ON room.id=task.room_id
  UNION ALL SELECT verification.updated_at FROM verification_runs verification JOIN runs run ON run.id=verification.run_id JOIN tasks task ON task.id=run.task_id JOIN room_scope room ON room.id=task.room_id
  UNION ALL SELECT result.created_at FROM local_review_results result JOIN runs run ON run.id=result.run_id JOIN tasks task ON task.id=run.task_id JOIN room_scope room ON room.id=task.room_id
  UNION ALL SELECT review.decided_at FROM verified_review_decisions review JOIN runs run ON run.id=review.run_id JOIN tasks task ON task.id=run.task_id JOIN room_scope room ON room.id=task.room_id
  UNION ALL SELECT review.decided_at FROM local_review_decisions review JOIN runs run ON run.id=review.run_id JOIN tasks task ON task.id=run.task_id JOIN room_scope room ON room.id=task.room_id
  UNION ALL SELECT application.updated_at FROM patch_applications application JOIN runs run ON run.id=application.run_id JOIN tasks task ON task.id=run.task_id JOIN room_scope room ON room.id=task.room_id
  UNION ALL SELECT failure.updated_at FROM patch_inspection_failures failure JOIN runs run ON run.id=failure.run_id JOIN tasks task ON task.id=run.task_id JOIN room_scope room ON room.id=task.room_id
), task_counts AS (
  SELECT COUNT(*) AS total,
    COALESCE(SUM(CASE WHEN task.state='open' THEN 1 ELSE 0 END),0) AS open_count,
    COALESCE(SUM(CASE WHEN task.state='closed' THEN 1 ELSE 0 END),0) AS terminal_count
  FROM tasks task JOIN room_scope room ON room.id=task.room_id
)
SELECT room.id,room.name,room.description,room.workspace_root,room.state,room.version,
       room.created_at,room.updated_at,room.archived_at,room.project_id,room.ownership_kind,MAX(activity.at),
       task_counts.total,task_counts.open_count,task_counts.terminal_count
FROM room_scope room CROSS JOIN activity CROSS JOIN task_counts
GROUP BY room.id`, roomID.String()).Scan(
		&idText, &name, &description, &root, &state, &version,
		&created, &updated, &archived, &project, &ownership, &activity,
		&counts.Total, &counts.Open, &counts.Terminal,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return storecontract.RoomDirectoryItem{}, storecontract.ErrNotFound
	}
	if err != nil {
		return storecontract.RoomDirectoryItem{}, err
	}
	room, err := restoreRoomWithOwnership(idText, name, description, root, state, version, created, updated, archived, project, ownership)
	if err != nil {
		return storecontract.RoomDirectoryItem{}, err
	}
	activityAt, err := parseTime(activity)
	if err != nil {
		return storecontract.RoomDirectoryItem{}, err
	}
	return storecontract.RoomDirectoryItem{Room: room, LastActivityAt: activityAt, TaskCounts: counts}, nil
}

const workspaceTasksQuery = `WITH room_tasks AS (
  SELECT task.* FROM tasks task WHERE task.room_id=?
)` + workspaceTasksQueryTail

const directoryTasksQuery = `WITH room_tasks AS (
  SELECT task.* FROM tasks task JOIN rooms room ON room.id=task.room_id WHERE room.state=?
)` + workspaceTasksQueryTail

const workspaceTasksQueryTail = `, activity(task_id,at) AS (
  SELECT id,updated_at FROM room_tasks
  UNION ALL SELECT draft.task_id,draft.updated_at FROM technical_plan_drafts draft JOIN room_tasks task ON task.id=draft.task_id
  UNION ALL SELECT revision.task_id,revision.submitted_at FROM technical_plan_revisions revision JOIN room_tasks task ON task.id=revision.task_id
  UNION ALL SELECT review.task_id,review.decided_at FROM technical_plan_reviews review JOIN room_tasks task ON task.id=review.task_id
  UNION ALL SELECT binding.task_id,binding.bound_at FROM technical_plan_acceptance_bindings binding JOIN room_tasks task ON task.id=binding.task_id
  UNION ALL SELECT run.task_id,run.updated_at FROM runs run JOIN room_tasks task ON task.id=run.task_id
  UNION ALL SELECT run.task_id,event.occurred_at FROM run_events event JOIN runs run ON run.id=event.run_id JOIN room_tasks task ON task.id=run.task_id
  UNION ALL SELECT run.task_id,review.decided_at FROM review_decisions review JOIN runs run ON run.id=review.run_id JOIN room_tasks task ON task.id=run.task_id
  UNION ALL SELECT run.task_id,verification.updated_at FROM verification_runs verification JOIN runs run ON run.id=verification.run_id JOIN room_tasks task ON task.id=run.task_id
  UNION ALL SELECT run.task_id,result.created_at FROM local_review_results result JOIN runs run ON run.id=result.run_id JOIN room_tasks task ON task.id=run.task_id
  UNION ALL SELECT run.task_id,review.decided_at FROM verified_review_decisions review JOIN runs run ON run.id=review.run_id JOIN room_tasks task ON task.id=run.task_id
  UNION ALL SELECT run.task_id,review.decided_at FROM local_review_decisions review JOIN runs run ON run.id=review.run_id JOIN room_tasks task ON task.id=run.task_id
  UNION ALL SELECT run.task_id,application.updated_at FROM patch_applications application JOIN runs run ON run.id=application.run_id JOIN room_tasks task ON task.id=run.task_id
  UNION ALL SELECT run.task_id,failure.updated_at FROM patch_inspection_failures failure JOIN runs run ON run.id=failure.run_id JOIN room_tasks task ON task.id=run.task_id
), latest_activity AS (
  SELECT task_id,MAX(at) AS at FROM activity GROUP BY task_id
), run_counts AS (
  SELECT run.task_id,COUNT(*) AS run_count,
    SUM(CASE WHEN run.state NOT IN ('accepted','cancelled','completed') THEN 1 ELSE 0 END) AS active_count
  FROM runs run JOIN room_tasks task ON task.id=run.task_id GROUP BY run.task_id
), ranked_runs AS (
  SELECT run.*,ROW_NUMBER() OVER (PARTITION BY run.task_id ORDER BY run.created_at DESC,run.id ASC) AS rank
  FROM runs run JOIN room_tasks task ON task.id=run.task_id
), latest_runs AS (
  SELECT * FROM ranked_runs WHERE rank=1
), open_drafts AS (
  SELECT draft.* FROM technical_plan_drafts draft JOIN room_tasks task ON task.id=draft.task_id WHERE draft.closed_at IS NULL
), ranked_revisions AS (
  SELECT revision.*,ROW_NUMBER() OVER (PARTITION BY revision.task_id ORDER BY revision.revision_number DESC,revision.id ASC) AS rank
  FROM technical_plan_revisions revision JOIN room_tasks task ON task.id=revision.task_id
), latest_revisions AS (
  SELECT * FROM ranked_revisions WHERE rank=1
), latest_plan_reviews AS (
  SELECT review.*
  FROM technical_plan_reviews review
  JOIN latest_revisions revision ON revision.id=review.revision_id AND revision.task_id=review.task_id
), latest_verification_results AS (
  SELECT result.id,result.outcome,verification.run_id
  FROM verification_results result
  JOIN verification_runs verification ON verification.id=result.verification_run_id
  JOIN attempts agent_attempt ON agent_attempt.id=verification.agent_attempt_id
  JOIN latest_runs run ON run.id=verification.run_id
    AND agent_attempt.run_id=run.id AND agent_attempt.sequence=run.current_attempt_number
  UNION ALL
  SELECT result.id,result.outcome,result.run_id
  FROM local_review_results result
  JOIN attempts agent_attempt ON agent_attempt.id=result.agent_attempt_id
  JOIN latest_runs run ON run.id=result.run_id
    AND agent_attempt.run_id=run.id AND agent_attempt.sequence=run.current_attempt_number
), latest_verified_reviews AS (
  SELECT review.id,review.kind,review.expected_run_version,review.rejection_class,run.task_id
  FROM verified_review_decisions review
  JOIN latest_verification_results result ON result.id=review.result_id
  JOIN latest_runs run ON run.id=result.run_id
  UNION ALL
  SELECT review.id,review.kind,review.expected_run_version,review.rejection_class,run.task_id
  FROM local_review_decisions review
  JOIN latest_verification_results result ON result.id=review.result_id
  JOIN latest_runs run ON run.id=result.run_id
)
SELECT
  task.id,task.room_id,task.predecessor_task_id,task.title,task.goal,task.state,task.archived,task.archived_at,
  task.created_at,task.updated_at,latest_activity.at,
  COALESCE(run_counts.run_count,0),COALESCE(run_counts.active_count,0),
  draft.id,draft.predecessor_revision_id,draft.edit_version,
  revision.id,revision.revision_number,revision.submitted_at,
  plan_review.id,plan_review.revision_id,plan_review.kind,plan_review.decided_at,
  acceptance.revision_id,
  run.id,run.task_id,run.charter_id,run.state,run.version,run.current_attempt_number,
  run.created_at,run.updated_at,run.started_at,run.review_requested_at,run.terminal_at,
  run_binding.revision_id,
  verification_result.id,verification_result.outcome,
  verified_review.id,verified_review.kind,verified_review.expected_run_version,verified_review.rejection_class,
  COALESCE(application.state,inspection_failure.state),COALESCE(application.version,inspection_failure.version),
  route.rejection_class,route.source_run_id,route.source_task_id,route.planning_draft_id,
  route_draft.predecessor_revision_id,route.related_task_id,related_task.predecessor_task_id,
  EXISTS(SELECT 1 FROM task_repository_worktrees worktree WHERE worktree.task_id=task.id AND worktree.delivery_mode='task_branch'),
  criterion.criterion_id,criterion.title,criterion.description
FROM room_tasks task
JOIN latest_activity ON latest_activity.task_id=task.id
LEFT JOIN run_counts ON run_counts.task_id=task.id
LEFT JOIN open_drafts draft ON draft.task_id=task.id
LEFT JOIN latest_revisions revision ON revision.task_id=task.id
LEFT JOIN latest_plan_reviews plan_review ON plan_review.task_id=task.id
LEFT JOIN current_technical_plan_acceptances acceptance ON acceptance.task_id=task.id
LEFT JOIN latest_runs run ON run.task_id=task.id
LEFT JOIN technical_plan_run_bindings run_binding ON run_binding.run_id=run.id
LEFT JOIN latest_verification_results verification_result ON verification_result.run_id=run.id
LEFT JOIN latest_verified_reviews verified_review ON verified_review.task_id=task.id
LEFT JOIN patch_applications application ON application.run_id=run.id
LEFT JOIN patch_inspection_failures inspection_failure ON inspection_failure.run_id=run.id
LEFT JOIN verified_review_routes route ON route.decision_id=verified_review.id
LEFT JOIN technical_plan_drafts route_draft ON route_draft.id=route.planning_draft_id
LEFT JOIN tasks related_task ON related_task.id=route.related_task_id
LEFT JOIN task_criteria criterion ON criterion.task_id=task.id
ORDER BY latest_activity.at DESC,task.id ASC,criterion.position ASC`

type workspaceTaskRow struct {
	idText, roomText, title, goal, stateText string
	predecessor                              sql.NullString
	createdAt, updatedAt, lastActivityAt     string
	runCount, activeRunCount                 int
	archived                                 bool
	archivedAt                               sql.NullString

	openDraftID, openDraftPredecessor sql.NullString
	openDraftEditVersion              sql.NullInt64
	latestRevisionID                  sql.NullString
	latestRevisionNumber              sql.NullInt64
	latestRevisionSubmittedAt         sql.NullString
	latestPlanReviewID                sql.NullString
	latestPlanReviewRevisionID        sql.NullString
	latestPlanReviewKind              sql.NullString
	latestPlanReviewDecidedAt         sql.NullString
	acceptedRevisionID                sql.NullString

	runID, runTaskID, runCharterID, runState   sql.NullString
	runVersion, runAttemptNumber               sql.NullInt64
	runCreatedAt, runUpdatedAt                 sql.NullString
	runStartedAt, runReviewedAt, runTerminalAt sql.NullString
	latestRunBindingRevisionID                 sql.NullString
	latestVerificationResultID                 sql.NullString
	latestVerificationResultOutcome            sql.NullString

	latestVerifiedReviewID, latestVerifiedReviewKind  sql.NullString
	latestVerifiedReviewRunVersion                    sql.NullInt64
	latestRejectionClass                              sql.NullString
	latestPatchApplicationState                       sql.NullString
	latestPatchApplicationVersion                     sql.NullInt64
	routeRejectionClass, routeSourceRunID             sql.NullString
	routeSourceTaskID, routePlanningDraftID           sql.NullString
	routePlanningDraftPredecessorRevisionID           sql.NullString
	routeRelatedTaskID, routeRelatedTaskPredecessorID sql.NullString
	hasTaskBranchDelivery                             bool
}

func scanWorkspaceTaskRow(rows *sql.Rows) (workspaceTaskRow, *domain.AcceptanceCriterion, error) {
	var row workspaceTaskRow
	var criterionID, criterionTitle, criterionDescription sql.NullString
	err := rows.Scan(
		&row.idText, &row.roomText, &row.predecessor, &row.title, &row.goal, &row.stateText, &row.archived, &row.archivedAt,
		&row.createdAt, &row.updatedAt, &row.lastActivityAt,
		&row.runCount, &row.activeRunCount,
		&row.openDraftID, &row.openDraftPredecessor, &row.openDraftEditVersion,
		&row.latestRevisionID, &row.latestRevisionNumber, &row.latestRevisionSubmittedAt,
		&row.latestPlanReviewID, &row.latestPlanReviewRevisionID, &row.latestPlanReviewKind, &row.latestPlanReviewDecidedAt,
		&row.acceptedRevisionID,
		&row.runID, &row.runTaskID, &row.runCharterID, &row.runState, &row.runVersion, &row.runAttemptNumber,
		&row.runCreatedAt, &row.runUpdatedAt, &row.runStartedAt, &row.runReviewedAt, &row.runTerminalAt,
		&row.latestRunBindingRevisionID,
		&row.latestVerificationResultID, &row.latestVerificationResultOutcome,
		&row.latestVerifiedReviewID, &row.latestVerifiedReviewKind, &row.latestVerifiedReviewRunVersion, &row.latestRejectionClass,
		&row.latestPatchApplicationState, &row.latestPatchApplicationVersion,
		&row.routeRejectionClass, &row.routeSourceRunID, &row.routeSourceTaskID, &row.routePlanningDraftID,
		&row.routePlanningDraftPredecessorRevisionID, &row.routeRelatedTaskID, &row.routeRelatedTaskPredecessorID,
		&row.hasTaskBranchDelivery,
		&criterionID, &criterionTitle, &criterionDescription,
	)
	if err != nil {
		return workspaceTaskRow{}, nil, err
	}
	if !criterionID.Valid {
		return row, nil, nil
	}
	id, err := domain.ParseCriterionID(criterionID.String)
	if err != nil || !criterionTitle.Valid || !criterionDescription.Valid {
		return workspaceTaskRow{}, nil, fmt.Errorf("invalid persisted Task criterion")
	}
	criterion, err := domain.NewAcceptanceCriterion(id, criterionTitle.String, criterionDescription.String)
	if err != nil {
		return workspaceTaskRow{}, nil, err
	}
	return row, &criterion, nil
}

func (row workspaceTaskRow) restore(criteria []domain.AcceptanceCriterion) (storecontract.TaskWorkspaceItem, error) {
	taskID, err := domain.ParseTaskID(row.idText)
	if err != nil {
		return storecontract.TaskWorkspaceItem{}, err
	}
	roomID, err := domain.ParseRoomID(row.roomText)
	if err != nil {
		return storecontract.TaskWorkspaceItem{}, err
	}
	var predecessorID domain.TaskID
	if row.predecessor.Valid {
		predecessorID, err = domain.ParseTaskID(row.predecessor.String)
		if err != nil {
			return storecontract.TaskWorkspaceItem{}, err
		}
	}
	var archivedAt time.Time
	if row.archivedAt.Valid {
		archivedAt, err = parseTime(row.archivedAt.String)
		if err != nil {
			return storecontract.TaskWorkspaceItem{}, err
		}
	}
	task, err := domain.RestoreTask(domain.TaskRecord{ID: taskID, RoomID: roomID, PredecessorTaskID: predecessorID, Title: row.title, Goal: row.goal, Criteria: criteria, State: domain.TaskState(row.stateText), Archived: row.archived, ArchivedAt: archivedAt})
	if err != nil {
		return storecontract.TaskWorkspaceItem{}, err
	}
	createdAt, err := parseTime(row.createdAt)
	if err != nil {
		return storecontract.TaskWorkspaceItem{}, err
	}
	updatedAt, err := parseTime(row.updatedAt)
	if err != nil {
		return storecontract.TaskWorkspaceItem{}, err
	}
	lastActivityAt, err := parseTime(row.lastActivityAt)
	if err != nil {
		return storecontract.TaskWorkspaceItem{}, err
	}
	item := storecontract.TaskWorkspaceItem{Task: task, CreatedAt: createdAt, UpdatedAt: updatedAt, LastActivityAt: lastActivityAt, RunCount: row.runCount, ActiveRunCount: row.activeRunCount, HasTaskBranchDelivery: row.hasTaskBranchDelivery}

	if item.OpenDraftID, err = parseOptionalPlanDraftID(row.openDraftID); err != nil {
		return storecontract.TaskWorkspaceItem{}, err
	}
	if item.OpenDraftPredecessorRevisionID, err = parseOptionalPlanRevisionID(row.openDraftPredecessor); err != nil {
		return storecontract.TaskWorkspaceItem{}, err
	}
	item.OpenDraftEditVersion = optionalUint64(row.openDraftEditVersion)
	if item.LatestRevisionID, err = parseOptionalPlanRevisionID(row.latestRevisionID); err != nil {
		return storecontract.TaskWorkspaceItem{}, err
	}
	item.LatestRevisionNumber = optionalUint64(row.latestRevisionNumber)
	if item.LatestRevisionSubmittedAt, err = parseOptionalTime(row.latestRevisionSubmittedAt); err != nil {
		return storecontract.TaskWorkspaceItem{}, err
	}
	if item.LatestPlanReviewID, err = parseOptionalPlanReviewID(row.latestPlanReviewID); err != nil {
		return storecontract.TaskWorkspaceItem{}, err
	}
	if item.LatestPlanReviewRevisionID, err = parseOptionalPlanRevisionID(row.latestPlanReviewRevisionID); err != nil {
		return storecontract.TaskWorkspaceItem{}, err
	}
	if row.latestPlanReviewKind.Valid {
		item.LatestPlanReviewKind = domain.TechnicalPlanReviewKind(row.latestPlanReviewKind.String)
	}
	if item.LatestPlanReviewDecidedAt, err = parseOptionalTime(row.latestPlanReviewDecidedAt); err != nil {
		return storecontract.TaskWorkspaceItem{}, err
	}
	if item.AcceptedRevisionID, err = parseOptionalPlanRevisionID(row.acceptedRevisionID); err != nil {
		return storecontract.TaskWorkspaceItem{}, err
	}

	if row.runID.Valid {
		run, err := restoreAgentRunColumns(row.runID.String, row.runTaskID.String, row.runCharterID.String, row.runState.String,
			optionalUint64(row.runVersion), int(optionalUint64(row.runAttemptNumber)), row.runCreatedAt.String, row.runUpdatedAt.String,
			row.runStartedAt, row.runReviewedAt, row.runTerminalAt)
		if err != nil {
			return storecontract.TaskWorkspaceItem{}, err
		}
		item.LatestRun = &run
	}
	if item.LatestRunBindingRevisionID, err = parseOptionalPlanRevisionID(row.latestRunBindingRevisionID); err != nil {
		return storecontract.TaskWorkspaceItem{}, err
	}
	if item.LatestVerificationResultID, err = parseOptionalResultID(row.latestVerificationResultID); err != nil {
		return storecontract.TaskWorkspaceItem{}, err
	}
	if row.latestVerificationResultOutcome.Valid {
		item.LatestVerificationResultOutcome = domain.ResultOutcome(row.latestVerificationResultOutcome.String)
	}
	if item.LatestVerifiedReviewID, err = parseOptionalReviewDecisionID(row.latestVerifiedReviewID); err != nil {
		return storecontract.TaskWorkspaceItem{}, err
	}
	if row.latestVerifiedReviewKind.Valid {
		item.LatestVerifiedReviewKind = domain.ReviewDecisionKind(row.latestVerifiedReviewKind.String)
	}
	item.LatestVerifiedReviewRunVersion = optionalUint64(row.latestVerifiedReviewRunVersion)
	if row.latestRejectionClass.Valid {
		item.LatestRejectionClass = domain.ReviewRejectionClass(row.latestRejectionClass.String)
	}
	if row.latestPatchApplicationState.Valid {
		item.LatestPatchApplicationState = domain.PatchApplicationState(row.latestPatchApplicationState.String)
		item.LatestPatchApplicationVersion = optionalUint64(row.latestPatchApplicationVersion)
	}
	if row.routeRejectionClass.Valid {
		item.RouteRejectionClass = domain.ReviewRejectionClass(row.routeRejectionClass.String)
	}
	if item.RouteSourceRunID, err = parseOptionalRunID(row.routeSourceRunID); err != nil {
		return storecontract.TaskWorkspaceItem{}, err
	}
	if item.RouteSourceTaskID, err = parseOptionalTaskID(row.routeSourceTaskID); err != nil {
		return storecontract.TaskWorkspaceItem{}, err
	}
	if item.RoutePlanningDraftID, err = parseOptionalPlanDraftID(row.routePlanningDraftID); err != nil {
		return storecontract.TaskWorkspaceItem{}, err
	}
	if item.RoutePlanningDraftPredecessorRevisionID, err = parseOptionalPlanRevisionID(row.routePlanningDraftPredecessorRevisionID); err != nil {
		return storecontract.TaskWorkspaceItem{}, err
	}
	if item.RouteRelatedTaskID, err = parseOptionalTaskID(row.routeRelatedTaskID); err != nil {
		return storecontract.TaskWorkspaceItem{}, err
	}
	if item.RouteRelatedTaskPredecessorTaskID, err = parseOptionalTaskID(row.routeRelatedTaskPredecessorID); err != nil {
		return storecontract.TaskWorkspaceItem{}, err
	}
	return item, nil
}

// ListTaskRunHistory validates Room -> Task ownership and returns every Run and
// its event count in one query. A valid Task with no Runs produces an empty list;
// a missing or mismatched Task produces ErrNotFound.
func (reader *reader) ListTaskRunHistory(ctx context.Context, roomID domain.RoomID, taskID domain.TaskID) ([]storecontract.RunSummary, error) {
	rows, err := reader.q.QueryContext(ctx, `
WITH owned_task AS (
  SELECT task.id FROM tasks task WHERE task.id=? AND task.room_id=?
)
SELECT owned_task.id,run.id,run.task_id,run.charter_id,run.state,run.version,run.current_attempt_number,
       run.created_at,run.updated_at,run.started_at,run.review_requested_at,run.terminal_at,COUNT(event.id)
FROM owned_task
LEFT JOIN runs run ON run.task_id=owned_task.id
LEFT JOIN run_events event ON event.run_id=run.id
GROUP BY owned_task.id,run.id
ORDER BY run.created_at DESC,run.id ASC`, taskID.String(), roomID.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []storecontract.RunSummary
	found := false
	for rows.Next() {
		found = true
		var ownedTask string
		var id, runTask, charter, state, created, updated, started, reviewed, terminal sql.NullString
		var version, attempt sql.NullInt64
		var eventCount int
		if err := rows.Scan(&ownedTask, &id, &runTask, &charter, &state, &version, &attempt, &created, &updated, &started, &reviewed, &terminal, &eventCount); err != nil {
			return nil, err
		}
		if !id.Valid {
			continue
		}
		run, err := restoreAgentRunColumns(id.String, runTask.String, charter.String, state.String, uint64(version.Int64), int(attempt.Int64), created.String, updated.String, started, reviewed, terminal)
		if err != nil {
			return nil, err
		}
		result = append(result, storecontract.RunSummary{Run: run, EventCount: eventCount})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if !found {
		return nil, storecontract.ErrNotFound
	}
	return result, nil
}

func (reader *reader) GetTaskRun(ctx context.Context, roomID domain.RoomID, taskID domain.TaskID, runID domain.RunID) (domain.AgentRun, error) {
	var id, runTask, charter, state, created, updated string
	var version uint64
	var attempt int
	var started, reviewed, terminal sql.NullString
	err := reader.q.QueryRowContext(ctx, `
SELECT run.id,run.task_id,run.charter_id,run.state,run.version,run.current_attempt_number,
       run.created_at,run.updated_at,run.started_at,run.review_requested_at,run.terminal_at
FROM runs run
JOIN tasks task ON task.id=run.task_id
WHERE task.room_id=? AND task.id=? AND run.id=?`, roomID.String(), taskID.String(), runID.String()).Scan(
		&id, &runTask, &charter, &state, &version, &attempt,
		&created, &updated, &started, &reviewed, &terminal,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.AgentRun{}, storecontract.ErrNotFound
	}
	if err != nil {
		return domain.AgentRun{}, err
	}
	return restoreAgentRunColumns(id, runTask, charter, state, version, attempt, created, updated, started, reviewed, terminal)
}

func restoreAgentRunColumns(idText, taskText, charterText, state string, version uint64, attempt int, created, updated string, started, reviewed, terminal sql.NullString) (domain.AgentRun, error) {
	id, err := domain.ParseRunID(idText)
	if err != nil {
		return domain.AgentRun{}, err
	}
	taskID, err := domain.ParseTaskID(taskText)
	if err != nil {
		return domain.AgentRun{}, err
	}
	charterID, err := domain.ParseCharterID(charterText)
	if err != nil {
		return domain.AgentRun{}, err
	}
	createdAt, err := parseTime(created)
	if err != nil {
		return domain.AgentRun{}, err
	}
	updatedAt, err := parseTime(updated)
	if err != nil {
		return domain.AgentRun{}, err
	}
	startedAt, err := parseNullableTime(started)
	if err != nil {
		return domain.AgentRun{}, err
	}
	reviewedAt, err := parseNullableTime(reviewed)
	if err != nil {
		return domain.AgentRun{}, err
	}
	terminalAt, err := parseNullableTime(terminal)
	if err != nil {
		return domain.AgentRun{}, err
	}
	return domain.RestoreAgentRun(domain.AgentRunRecord{ID: id, TaskID: taskID, CharterID: charterID, State: domain.RunState(state), Version: version, CurrentAttemptNumber: attempt, CreatedAt: createdAt, UpdatedAt: updatedAt, StartedAt: startedAt, ReviewRequestedAt: reviewedAt, TerminalAt: terminalAt})
}

func optionalUint64(value sql.NullInt64) uint64 {
	if !value.Valid || value.Int64 < 0 {
		return 0
	}
	return uint64(value.Int64)
}

func parseOptionalTime(value sql.NullString) (time.Time, error) {
	if !value.Valid {
		return time.Time{}, nil
	}
	return parseTime(value.String)
}

func parseOptionalTaskID(value sql.NullString) (domain.TaskID, error) {
	if !value.Valid {
		return domain.TaskID{}, nil
	}
	return domain.ParseTaskID(value.String)
}

func parseOptionalRunID(value sql.NullString) (domain.RunID, error) {
	if !value.Valid {
		return domain.RunID{}, nil
	}
	return domain.ParseRunID(value.String)
}

func parseOptionalPlanDraftID(value sql.NullString) (domain.TechnicalPlanDraftID, error) {
	if !value.Valid {
		return domain.TechnicalPlanDraftID{}, nil
	}
	return domain.ParseTechnicalPlanDraftID(value.String)
}

func parseOptionalPlanRevisionID(value sql.NullString) (domain.TechnicalPlanRevisionID, error) {
	if !value.Valid {
		return domain.TechnicalPlanRevisionID{}, nil
	}
	return domain.ParseTechnicalPlanRevisionID(value.String)
}

func parseOptionalPlanReviewID(value sql.NullString) (domain.TechnicalPlanReviewID, error) {
	if !value.Valid {
		return domain.TechnicalPlanReviewID{}, nil
	}
	return domain.ParseTechnicalPlanReviewID(value.String)
}

func parseOptionalResultID(value sql.NullString) (domain.ResultID, error) {
	if !value.Valid {
		return domain.ResultID{}, nil
	}
	return domain.ParseResultID(value.String)
}

func parseOptionalReviewDecisionID(value sql.NullString) (domain.ReviewDecisionID, error) {
	if !value.Valid {
		return domain.ReviewDecisionID{}, nil
	}
	return domain.ParseReviewDecisionID(value.String)
}
