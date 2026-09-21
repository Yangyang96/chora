package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

type ProjectDocumentSourceView struct {
	RunID            string `json:"runId"`
	AttemptID        string `json:"attemptId"`
	ResultID         string `json:"resultId"`
	AgentReportID    string `json:"agentReportId"`
	EventID          string `json:"eventId"`
	EventSequence    int64  `json:"eventSequence"`
	ResultDigest     string `json:"resultDigest"`
	SourceTextDigest string `json:"sourceTextDigest"`
}
type ProjectDocumentRevisionView struct {
	ID                        string                             `json:"id"`
	Number                    uint64                             `json:"number"`
	Kind                      domain.ProjectDocumentRevisionKind `json:"kind"`
	Body                      string                             `json:"body"`
	BodyDigest                string                             `json:"bodyDigest"`
	Source                    ProjectDocumentSourceView          `json:"source"`
	EditNote                  string                             `json:"editNote"`
	ActorID                   string                             `json:"actorId"`
	CreatedAt                 string                             `json:"createdAt"`
	Status                    string                             `json:"status"`
	AcceptedContextRevisionID string                             `json:"acceptedContextRevisionId,omitempty"`
}
type ProjectDocumentReviewView struct {
	ID                string                           `json:"id"`
	RevisionID        string                           `json:"revisionId"`
	ExpectedVersion   uint64                           `json:"expectedVersion"`
	Kind              domain.ProjectDocumentReviewKind `json:"kind"`
	Note              string                           `json:"note"`
	ActorID           string                           `json:"actorId"`
	SessionID         string                           `json:"sessionId"`
	ContextRevisionID string                           `json:"contextRevisionId,omitempty"`
	DecidedAt         string                           `json:"decidedAt"`
}
type ProjectDocumentView struct {
	TaskID    string                        `json:"taskId"`
	RoomID    string                        `json:"roomId,omitempty"`
	Version   uint64                        `json:"version"`
	Status    string                        `json:"status"`
	Revisions []ProjectDocumentRevisionView `json:"revisions"`
	Reviews   []ProjectDocumentReviewView   `json:"reviews"`
}
type SaveProjectDocumentRequest struct {
	CommandMeta
	TaskID          domain.TaskID
	ExpectedVersion uint64
	Body, Note      string
	SourceAttemptID domain.AttemptID
}
type SaveProjectDocumentResult struct {
	Document ProjectDocumentView `json:"document"`
	Replayed bool                `json:"replayed"`
}
type ReviewProjectDocumentRequest struct {
	CommandMeta
	TaskID          domain.TaskID
	ExpectedVersion uint64
	Kind            domain.ProjectDocumentReviewKind
	Note            string
}
type ReviewProjectDocumentResult struct {
	Document                  ProjectDocumentView `json:"document"`
	AcceptedContextRevisionID string              `json:"acceptedContextRevisionId,omitempty"`
	Replayed                  bool                `json:"replayed"`
}

func (s *Service) GetProjectDocument(ctx context.Context, taskID domain.TaskID) (ProjectDocumentView, error) {
	return loadProjectDocumentView(ctx, s.deps.Store.Reader(), taskID)
}

func (s *Service) SaveProjectDocument(ctx context.Context, req SaveProjectDocumentRequest) (SaveProjectDocumentResult, error) {
	if err := s.authorize(ctx, req.CommandMeta, "save_project_document", req.TaskID.String(), req.ExpectedVersion); err != nil {
		return SaveProjectDocumentResult{}, err
	}
	now := s.deps.Clock.Now()
	key, err := s.commandKey(req.CommandMeta, "save_project_document", req.TaskID.String(), req.ExpectedVersion, struct{ Body, Note, Attempt string }{req.Body, req.Note, req.SourceAttemptID.String()}, now)
	if err != nil {
		return SaveProjectDocumentResult{}, err
	}
	var result SaveProjectDocumentResult
	err = s.deps.Store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		response, replay, e := tx.LookupCommand(ctx, key)
		if e != nil {
			return e
		}
		if replay {
			if e = decodeResponse(response, &result); e == nil {
				result.Replayed = true
			}
			return e
		}
		task, e := activeDocumentTask(ctx, tx, req.TaskID)
		if e != nil {
			return e
		}
		existing, e := tx.ListProjectDocumentRevisions(ctx, req.TaskID)
		if e != nil {
			return e
		}
		if uint64(len(existing)) != req.ExpectedVersion {
			return storecontract.ErrVersionConflict
		}
		var source domain.ProjectDocumentSource
		body := req.Body
		kind := domain.ProjectDocumentHumanEdit
		if req.SourceAttemptID.Valid() {
			kind = domain.ProjectDocumentAgentInitial
			group, e := tx.GetResourceResultGroup(ctx, req.SourceAttemptID)
			if e != nil {
				return e
			}
			if group.TaskID != req.TaskID.String() {
				return fmt.Errorf("%w: result belongs to another Task", ErrInvalidCommand)
			}
			if group.OutcomeKind != "document" || group.Outcome != "review_ready" {
				return fmt.Errorf("%w: result is not a review-ready document", ErrInvalidCommand)
			}
			report, e := tx.GetAgentReportForAttempt(ctx, req.SourceAttemptID)
			if e != nil {
				return e
			}
			if report.ID().String() != group.AgentReportID || report.RunID().String() != group.RunID {
				return fmt.Errorf("%w: result source lineage mismatch", ErrInvalidCommand)
			}
			history, historyErr := tx.ListTaskRunHistory(ctx, task.RoomID(), task.ID())
			if historyErr != nil {
				return historyErr
			}
			if len(history) == 0 || history[0].Run.ID() != report.RunID() {
				return fmt.Errorf("%w: document source is superseded by a newer Run", ErrReviewEvidenceUnavailable)
			}
			if !projectDocumentSourceStateAllowed(history[0].Run.State()) {
				return fmt.Errorf("%w: Run state %q forbids document mutation", ErrInvalidCommand, history[0].Run.State())
			}
			currentAttempt, currentErr := tx.GetCurrentAttempt(ctx, report.RunID())
			if currentErr != nil {
				return currentErr
			}
			if currentAttempt.ID() != req.SourceAttemptID {
				return fmt.Errorf("%w: document source is superseded by a newer Attempt", ErrReviewEvidenceUnavailable)
			}
			_, digest, e := group.CanonicalJSON()
			if e != nil {
				return e
			}
			resultID, e := domain.ParseResultID(group.ID)
			if e != nil {
				return e
			}
			runID, e := domain.ParseRunID(group.RunID)
			if e != nil {
				return e
			}
			eventID, e := domain.ParseEventID(group.FinalAssistant.EventID)
			if e != nil {
				return e
			}
			if report.FinalText() != group.Markdown {
				return fmt.Errorf("%w: frozen Markdown differs from Agent report", ErrInvalidCommand)
			}
			textDigest := sha256.Sum256([]byte(report.FinalText()))
			if hex.EncodeToString(textDigest[:]) != group.FinalAssistant.TextDigest {
				return fmt.Errorf("%w: source text digest mismatch", ErrInvalidCommand)
			}
			source = domain.ProjectDocumentSource{RunID: runID, AttemptID: req.SourceAttemptID, ResultID: resultID, AgentReportID: report.ID(), EventID: eventID, EventSequence: group.FinalAssistant.Sequence, ResultDigest: digest, SourceTextDigest: textDigest}
			if body == "" {
				body = report.FinalText()
			}
			for _, v := range existing {
				if v.Source.AttemptID == source.AttemptID {
					return fmt.Errorf("%w: source Attempt already imported", ErrInvalidCommand)
				}
			}
		} else {
			if len(existing) == 0 {
				return fmt.Errorf("%w: initial document requires source Attempt", ErrInvalidCommand)
			}
			source = existing[len(existing)-1].Source
			history, historyErr := tx.ListTaskRunHistory(ctx, task.RoomID(), task.ID())
			if historyErr != nil {
				return historyErr
			}
			if len(history) == 0 || history[0].Run.ID() != source.RunID || !projectDocumentSourceStateAllowed(history[0].Run.State()) {
				return fmt.Errorf("%w: document source Run is not editable", ErrInvalidCommand)
			}
			currentAttempt, currentErr := tx.GetCurrentAttempt(ctx, source.RunID)
			if currentErr != nil {
				return currentErr
			}
			if currentAttempt.ID() != source.AttemptID {
				return fmt.Errorf("%w: document source is superseded by a newer Attempt", ErrReviewEvidenceUnavailable)
			}
		}
		v, e := domain.NewProjectDocumentRevision(domain.NewProjectDocumentRevisionID(), task.ID(), task.RoomID(), req.ExpectedVersion+1, kind, body, source, req.Note, req.ActorID, now)
		if e != nil {
			return e
		}
		if e = tx.InsertProjectDocumentRevision(ctx, v); e != nil {
			return e
		}
		view, e := loadProjectDocumentView(ctx, tx, req.TaskID)
		if e != nil {
			return e
		}
		result = SaveProjectDocumentResult{Document: view}
		response, e = jsonResponse(result)
		if e != nil {
			return e
		}
		return tx.SaveCommand(ctx, key, response)
	})
	return result, err
}

func (s *Service) ReviewProjectDocument(ctx context.Context, req ReviewProjectDocumentRequest) (ReviewProjectDocumentResult, error) {
	if err := s.authorize(ctx, req.CommandMeta, "review_project_document", req.TaskID.String(), req.ExpectedVersion); err != nil {
		return ReviewProjectDocumentResult{}, err
	}
	now := s.deps.Clock.Now()
	key, err := s.commandKey(req.CommandMeta, "review_project_document", req.TaskID.String(), req.ExpectedVersion, struct {
		Kind domain.ProjectDocumentReviewKind
		Note string
	}{req.Kind, req.Note}, now)
	if err != nil {
		return ReviewProjectDocumentResult{}, err
	}
	var result ReviewProjectDocumentResult
	err = s.deps.Store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		response, replay, e := tx.LookupCommand(ctx, key)
		if e != nil {
			return e
		}
		if replay {
			if e = decodeResponse(response, &result); e == nil {
				result.Replayed = true
			}
			return e
		}
		task, e := activeDocumentTask(ctx, tx, req.TaskID)
		if e != nil {
			return e
		}
		versions, e := tx.ListProjectDocumentRevisions(ctx, req.TaskID)
		if e != nil {
			return e
		}
		if len(versions) == 0 || uint64(len(versions)) != req.ExpectedVersion {
			return storecontract.ErrVersionConflict
		}
		reviews, e := tx.ListProjectDocumentReviews(ctx, req.TaskID)
		if e != nil {
			return e
		}
		for _, r := range reviews {
			if r.RevisionID == versions[len(versions)-1].ID {
				return storecontract.ErrVersionConflict
			}
		}
		latest := versions[len(versions)-1]
		history, e := tx.ListTaskRunHistory(ctx, task.RoomID(), task.ID())
		if e != nil {
			return e
		}
		if len(history) == 0 || history[0].Run.ID() != latest.Source.RunID {
			return fmt.Errorf("%w: document source is superseded by a newer Run", ErrReviewEvidenceUnavailable)
		}
		run := history[0].Run
		if !projectDocumentSourceStateAllowed(run.State()) {
			return fmt.Errorf("%w: Run state %q forbids document review", ErrInvalidCommand, run.State())
		}
		attempt, attemptErr := tx.GetCurrentAttempt(ctx, run.ID())
		if attemptErr != nil {
			return attemptErr
		}
		if attempt.ID() != latest.Source.AttemptID {
			return fmt.Errorf("%w: document source is superseded by a newer Attempt", ErrReviewEvidenceUnavailable)
		}
		if s.deps.Presence == nil {
			return domain.ErrUnauthorizedReview
		}
		pk := domain.ReviewDecisionReject
		if req.Kind == domain.ProjectDocumentReviewAccept {
			pk = domain.ReviewDecisionAccept
		}
		authorization, e := s.deps.Presence.AuthorizeReview(ctx, PresenceRequest{ActorID: req.ActorID, SessionID: req.SessionID, RunID: latest.Source.RunID, Kind: pk, ExpectedVersion: run.Version()})
		if e != nil {
			return e
		}
		var contextID domain.ContextRevisionID
		if req.Kind == domain.ProjectDocumentReviewAccept {
			contextID = domain.NewContextRevisionID()
			entryID := domain.NewContextEntryID()
			locator := domain.ProjectDocumentLocator(req.TaskID, latest.Number)
			revision, e := domain.NewRoomContextRevision(domain.RoomContextRevisionParams{EntryID: entryID, RevisionID: contextID, RoomID: task.RoomID(), Kind: domain.ContextKindSourceRef, RevisionNumber: 1, Title: task.Title(), Body: latest.Body, Locator: locator, CreatedAt: now, UpdatedAt: now})
			if e != nil {
				return e
			}
			confirmed, e := domain.NewRoomRevision(revision, domain.HumanRoomProvenance(req.ActorID), now)
			if e != nil {
				return e
			}
			if e = tx.InsertRevision(ctx, revision); e != nil {
				return e
			}
			if e = tx.InsertRoomRevision(ctx, confirmed); e != nil {
				return e
			}
		}
		review, e := domain.NewProjectDocumentReview(domain.NewProjectDocumentReviewID(), latest, req.ExpectedVersion, req.Kind, req.Note, req.ActorID, req.SessionID, contextID, now)
		if e != nil {
			return e
		}
		if e = tx.InsertProjectDocumentReview(ctx, review); e != nil {
			return e
		}
		// If this exact source is still the current review-ready Result, the
		// document decision is also the Run decision. Later human edits remain
		// independently reviewable after a rejection.
		if run.State() == domain.RunStateAwaitingReview {
			params := domain.ReviewDecisionParams{ID: s.deps.IDs.ReviewID(), RunID: run.ID(), ExpectedRunVersion: run.Version(), Comment: req.Note, DecidedAt: now}
			var runReview domain.ReviewDecision
			if req.Kind == domain.ProjectDocumentReviewAccept {
				runReview, e = domain.NewAcceptedReviewDecision(params, authorization)
			} else {
				runReview, e = domain.NewRejectedReviewDecision(params, authorization)
			}
			if e != nil {
				return e
			}
			next, applyErr := run.ApplyReview(runReview)
			if applyErr != nil {
				return applyErr
			}
			if e = tx.InsertReview(ctx, runReview); e != nil {
				return e
			}
			if e = tx.InsertResourceResultReview(ctx, latest.Source.ResultID, latest.Source.ResultDigest, runReview.ID()); e != nil {
				return e
			}
			if e = tx.SaveRunCAS(ctx, run.Version(), next); e != nil {
				return e
			}
			if _, e = tx.AppendRunEvent(ctx, run.ID(), storecontract.EventDraft{ID: s.deps.IDs.EventID(), Type: "document_review." + string(req.Kind), Source: "app", OccurredAt: now, RecordedAt: now, NormalizedJSON: responseBody(map[string]any{"result_id": latest.Source.ResultID.String(), "document_version": latest.Number})}); e != nil {
				return e
			}
		}
		if req.Kind == domain.ProjectDocumentReviewAccept {
			if e = tx.CloseTaskForAcceptedProjectDocument(ctx, task.ID()); e != nil {
				return e
			}
		}
		view, e := loadProjectDocumentView(ctx, tx, req.TaskID)
		if e != nil {
			return e
		}
		result = ReviewProjectDocumentResult{Document: view, AcceptedContextRevisionID: contextID.String()}
		response, e = jsonResponse(result)
		if e != nil {
			return e
		}
		return tx.SaveCommand(ctx, key, response)
	})
	return result, err
}

func projectDocumentSourceStateAllowed(state domain.RunState) bool {
	return state == domain.RunStateAwaitingReview || state == domain.RunStateAccepted || state == domain.RunStateRevisionRequired
}

func activeDocumentTask(ctx context.Context, r storecontract.Reader, id domain.TaskID) (domain.Task, error) {
	task, e := r.GetTask(ctx, id)
	if e != nil {
		return domain.Task{}, e
	}
	room, e := r.GetRoom(ctx, task.RoomID())
	if e != nil {
		return domain.Task{}, e
	}
	if task.Archived() || room.State() != domain.RoomStateActive {
		return domain.Task{}, storecontract.ErrRoomStateForbidden
	}
	return task, nil
}
func loadProjectDocumentView(ctx context.Context, r storecontract.Reader, id domain.TaskID) (ProjectDocumentView, error) {
	task, e := r.GetTask(ctx, id)
	if e != nil {
		return ProjectDocumentView{}, e
	}
	versions, e := r.ListProjectDocumentRevisions(ctx, id)
	if e != nil {
		return ProjectDocumentView{}, e
	}
	reviews, e := r.ListProjectDocumentReviews(ctx, id)
	if e != nil {
		return ProjectDocumentView{}, e
	}
	view := ProjectDocumentView{TaskID: id.String(), RoomID: task.RoomID().String(), Version: uint64(len(versions)), Status: "empty", Revisions: []ProjectDocumentRevisionView{}, Reviews: []ProjectDocumentReviewView{}}
	byRevision := map[string]domain.ProjectDocumentReview{}
	for _, rv := range reviews {
		byRevision[rv.RevisionID.String()] = rv
		view.Reviews = append(view.Reviews, ProjectDocumentReviewView{ID: rv.ID.String(), RevisionID: rv.RevisionID.String(), ExpectedVersion: rv.ExpectedVersion, Kind: rv.Kind, Note: rv.Note, ActorID: rv.ActorID, SessionID: rv.SessionID, ContextRevisionID: rv.ContextRevisionID.String(), DecidedAt: rv.DecidedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")})
	}
	for _, v := range versions {
		status := "pending"
		cid := ""
		if rv, ok := byRevision[v.ID.String()]; ok {
			status = string(rv.Kind) + "ed"
			cid = rv.ContextRevisionID.String()
		}
		view.Revisions = append(view.Revisions, ProjectDocumentRevisionView{ID: v.ID.String(), Number: v.Number, Kind: v.Kind, Body: v.Body, BodyDigest: hex.EncodeToString(v.BodyDigest[:]), Source: ProjectDocumentSourceView{RunID: v.Source.RunID.String(), AttemptID: v.Source.AttemptID.String(), ResultID: v.Source.ResultID.String(), AgentReportID: v.Source.AgentReportID.String(), EventID: v.Source.EventID.String(), EventSequence: v.Source.EventSequence, ResultDigest: hex.EncodeToString(v.Source.ResultDigest[:]), SourceTextDigest: hex.EncodeToString(v.Source.SourceTextDigest[:])}, EditNote: v.EditNote, ActorID: v.ActorID, CreatedAt: v.CreatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"), Status: status, AcceptedContextRevisionID: cid})
	}
	if len(view.Revisions) > 0 {
		view.Status = view.Revisions[len(view.Revisions)-1].Status
	}
	return view, nil
}
