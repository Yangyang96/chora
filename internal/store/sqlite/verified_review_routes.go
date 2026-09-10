package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

func (tx *writeTx) InsertVerifiedReviewRoute(ctx context.Context, route storecontract.VerifiedReviewRoute) error {
	if err := tx.requireActiveRoomForRun(ctx, route.SourceRunID); err != nil {
		return err
	}
	var draftID, relatedTaskID any
	if route.PlanningDraftID.Valid() {
		draftID = route.PlanningDraftID.String()
	}
	if route.RelatedTaskID.Valid() {
		relatedTaskID = route.RelatedTaskID.String()
	}
	_, err := tx.tx.ExecContext(ctx, `INSERT INTO verified_review_routes(decision_id,source_run_id,source_task_id,rejection_class,planning_draft_id,related_task_id,created_at) VALUES(?,?,?,?,?,?,?)`,
		route.DecisionID.String(), route.SourceRunID.String(), route.SourceTaskID.String(), string(route.RejectionClass), draftID, relatedTaskID, timeText(route.CreatedAt))
	return mapWriteError(err)
}

func (reader *reader) GetVerifiedReviewRoute(ctx context.Context, decisionID domain.ReviewDecisionID) (storecontract.VerifiedReviewRoute, error) {
	var decisionText, runText, taskText, classText, createdText string
	var draftText, relatedText sql.NullString
	err := reader.q.QueryRowContext(ctx, `SELECT decision_id,source_run_id,source_task_id,rejection_class,planning_draft_id,related_task_id,created_at FROM verified_review_routes WHERE decision_id=?`, decisionID.String()).Scan(&decisionText, &runText, &taskText, &classText, &draftText, &relatedText, &createdText)
	if errors.Is(err, sql.ErrNoRows) {
		return storecontract.VerifiedReviewRoute{}, storecontract.ErrNotFound
	}
	if err != nil {
		return storecontract.VerifiedReviewRoute{}, err
	}
	parsedDecision, err := domain.ParseReviewDecisionID(decisionText)
	if err != nil {
		return storecontract.VerifiedReviewRoute{}, err
	}
	runID, err := domain.ParseRunID(runText)
	if err != nil {
		return storecontract.VerifiedReviewRoute{}, err
	}
	taskID, err := domain.ParseTaskID(taskText)
	if err != nil {
		return storecontract.VerifiedReviewRoute{}, err
	}
	createdAt, err := parseTime(createdText)
	if err != nil {
		return storecontract.VerifiedReviewRoute{}, err
	}
	route := storecontract.VerifiedReviewRoute{DecisionID: parsedDecision, SourceRunID: runID, SourceTaskID: taskID, RejectionClass: domain.ReviewRejectionClass(classText), CreatedAt: createdAt}
	if draftText.Valid {
		route.PlanningDraftID, err = domain.ParseTechnicalPlanDraftID(draftText.String)
		if err != nil {
			return storecontract.VerifiedReviewRoute{}, err
		}
	}
	if relatedText.Valid {
		route.RelatedTaskID, err = domain.ParseTaskID(relatedText.String)
		if err != nil {
			return storecontract.VerifiedReviewRoute{}, err
		}
	}
	return route, nil
}
