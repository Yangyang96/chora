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
	"github.com/google/uuid"
)

type SubmitWorkflowRequest struct {
	CommandMeta
	TaskID          domain.TaskID
	ProjectID       domain.ProjectID
	Question        string
	Configuration   json.RawMessage
	PiAuthorization *domain.WorkflowPiAuthorization
}
type DecideWorkflowRequest struct {
	CommandMeta
	ID                                          string
	ProjectID                                   domain.ProjectID
	ExpectedVersion                             uint64
	GateID, InputDigest, Decision, Instructions string
}

// workflowAuthority binds the original task's accepted plan, materials, model
// and execution settings. Later global defaults never rewrite this snapshot.
func workflowAuthority(ctx context.Context, r storecontract.Reader, id domain.TaskID) (domain.ProjectID, domain.TechnicalPlanRevisionID, string, error) {
	task, err := r.GetTask(ctx, id)
	if err != nil {
		return domain.ProjectID{}, domain.TechnicalPlanRevisionID{}, "", err
	}
	if task.Archived() || task.State() != domain.TaskStateOpen {
		return domain.ProjectID{}, domain.TechnicalPlanRevisionID{}, "", ErrInvalidCommand
	}
	room, err := r.GetRoom(ctx, task.RoomID())
	if err != nil {
		return domain.ProjectID{}, domain.TechnicalPlanRevisionID{}, "", err
	}
	p, err := r.GetProject(ctx, room.ProjectID())
	if err != nil {
		return domain.ProjectID{}, domain.TechnicalPlanRevisionID{}, "", err
	}
	if room.State() != domain.RoomStateActive || p.State() != domain.ProjectStateActive {
		return p.ID(), domain.TechnicalPlanRevisionID{}, "", storecontract.ErrRoomStateForbidden
	}
	a, err := r.GetCurrentTechnicalPlanAcceptance(ctx, id)
	if err != nil {
		return p.ID(), domain.TechnicalPlanRevisionID{}, "", err
	}
	settings, err := r.GetTaskExecutionSettings(ctx, id)
	if err != nil {
		return p.ID(), a.RevisionID(), "", err
	}
	snapshot, err := r.GetSnapshot(ctx, a.SnapshotID())
	if err != nil {
		return p.ID(), a.RevisionID(), "", err
	}
	charterID, _, err := specCodingSnapshotBoundary(snapshot)
	if err != nil {
		return p.ID(), a.RevisionID(), "", err
	}
	charter, err := r.GetCharter(ctx, charterID)
	if err != nil {
		return p.ID(), a.RevisionID(), "", err
	}
	if charter.AdapterID() != "pi" {
		return p.ID(), a.RevisionID(), "", ErrInvalidCommand
	}
	b := responseBody(map[string]any{"task": id.String(), "goal": task.Goal(), "revision": a.RevisionID().String(), "snapshot": fmt.Sprintf("%x", a.SnapshotDigest()), "settings": settings, "profile": settings.AgentExecutionProfile, "model": charter.ModelBinding().JSON(), "task_model": settings.ModelBinding.JSON()})
	return p.ID(), a.RevisionID(), domain.WorkflowDigest(b), nil
}
func requireWorkflowAuthority(ctx context.Context, r storecontract.Reader, w domain.WorkflowExecution) error {
	p, revision, digest, err := workflowAuthority(ctx, r, w.TaskID)
	if err != nil {
		return err
	}
	if p != w.ProjectID || revision != w.RevisionID || digest != w.AuthorityDigest {
		return fmt.Errorf("%w: frozen workflow input changed", storecontract.ErrVersionConflict)
	}
	return nil
}
func workflowCommand(w domain.WorkflowExecution, kind string, payload any) domain.WorkflowCommand {
	b := responseBody(payload)
	return domain.WorkflowCommand{WorkflowID: w.ID, OperationID: w.ID + ":" + kind, Kind: kind, InputDigest: domain.WorkflowDigest(b), Payload: string(b)}
}
func workflowMeta(w domain.WorkflowExecution, kind string) CommandMeta {
	return CommandMeta{ActorID: w.ActorID, SessionID: w.SessionID, IdempotencyKey: w.ID + ":" + kind}
}

func (s *Service) SubmitWorkflow(ctx context.Context, q SubmitWorkflowRequest) (w domain.WorkflowExecution, err error) {
	if err = s.authorize(ctx, q.CommandMeta, "submit_workflow", q.TaskID.String(), 0); err != nil {
		return
	}
	if len(strings.TrimSpace(q.Question)) == 0 || len(q.Question) > 8000 || !json.Valid(q.Configuration) {
		return w, ErrInvalidCommand
	}
	if q.PiAuthorization != nil && !q.PiAuthorization.Active(s.deps.Clock.Now()) {
		return w, ErrInvalidCommand
	}
	id := uuid.NewSHA1(uuid.NameSpaceURL, []byte("chora-workflow:"+q.ProjectID.String()+":"+q.TaskID.String()+":"+q.ActorID+":"+q.IdempotencyKey)).String()
	inputFields := map[string]any{"question": q.Question, "configuration": q.Configuration, "task": q.TaskID.String(), "project": q.ProjectID.String()}
	if q.PiAuthorization != nil {
		inputFields["pi_authorization"] = q.PiAuthorization
	}
	input := domain.WorkflowDigest(responseBody(inputFields))
	err = s.deps.Store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		if old, e := tx.GetTaskWorkflow(ctx, q.TaskID); e == nil {
			if old.ID != id || old.ActorID != q.ActorID || old.InputDigest != input {
				return storecontract.ErrIdempotencyConflict
			}
			w = old
			return requireWorkflowAuthority(ctx, tx, w)
		} else if !errors.Is(e, storecontract.ErrNotFound) {
			return e
		}
		p, revision, authority, e := workflowAuthority(ctx, tx, q.TaskID)
		if e != nil {
			return e
		}
		if p != q.ProjectID {
			return ErrUnauthorizedCommand
		}
		if q.PiAuthorization != nil {
			if e = requireWorkflowPiBinding(ctx, tx, q.TaskID, *q.PiAuthorization); e != nil {
				return e
			}
		}
		// Either dispatcher may own a Task, never both.
		if _, e = tx.GetTaskDelegation(ctx, q.TaskID); !errors.Is(e, storecontract.ErrNotFound) {
			return ErrInvalidCommand
		}
		if _, e = tx.GetDelegationPlanning(ctx, q.TaskID); !errors.Is(e, storecontract.ErrNotFound) {
			return ErrInvalidCommand
		}
		if _, e = tx.GetDelegationChild(ctx, q.TaskID); !errors.Is(e, storecontract.ErrNotFound) {
			return ErrInvalidCommand
		}
		if _, e = tx.GetSynthesisForTask(ctx, q.TaskID); !errors.Is(e, storecontract.ErrNotFound) {
			return ErrInvalidCommand
		}
		task, e := tx.GetTask(ctx, q.TaskID)
		if e != nil {
			return e
		}
		history, e := tx.ListTaskRunHistory(ctx, task.RoomID(), q.TaskID)
		if e != nil {
			return e
		}
		if len(history) != 0 {
			return ErrInvalidCommand
		}
		w = domain.WorkflowExecution{ID: id, TaskID: q.TaskID, ProjectID: p, ActorID: q.ActorID, SessionID: q.SessionID, Version: 1, GraphVersion: domain.ResearchWorkflowVersion, InputDigest: input, AuthorityDigest: authority, RevisionID: revision, Question: q.Question, Configuration: q.Configuration, Status: "researching", CreatedAt: s.deps.Clock.Now()}
		w.PiAuthorization = q.PiAuthorization
		if e = tx.InsertWorkflow(ctx, w); e != nil {
			return e
		}
		if e = tx.PutWorkflowCommand(ctx, workflowCommand(w, "submit", map[string]any{"question": w.Question, "configuration": w.Configuration, "input_digest": w.InputDigest})); e != nil {
			return e
		}
		return tx.AppendWorkflowEvent(ctx, w.ID, "submitted", responseBody(w))
	})
	return
}

func (s *Service) GetWorkflow(ctx context.Context, meta CommandMeta, id string, project domain.ProjectID) (domain.WorkflowExecution, error) {
	if err := s.authorize(ctx, meta, "read_workflow", id, 0); err != nil {
		return domain.WorkflowExecution{}, err
	}
	w, err := s.deps.Store.Reader().GetWorkflow(ctx, id)
	if err != nil {
		return w, err
	}
	if w.ProjectID != project || w.ActorID != meta.ActorID {
		return domain.WorkflowExecution{}, ErrUnauthorizedCommand
	}
	return w, nil
}
func (s *Service) DecideWorkflow(ctx context.Context, q DecideWorkflowRequest) (w domain.WorkflowExecution, err error) {
	if err = s.authorize(ctx, q.CommandMeta, "decide_workflow", q.ID, q.ExpectedVersion); err != nil {
		return
	}
	if (q.Decision != "accept" && q.Decision != "reject") || len(q.Instructions) > 4000 {
		return w, ErrInvalidCommand
	}
	err = s.deps.Store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		var e error
		w, e = tx.GetWorkflow(ctx, q.ID)
		if e != nil {
			return e
		}
		if w.ProjectID != q.ProjectID || w.ActorID != q.ActorID {
			return ErrUnauthorizedCommand
		}
		g := w.Gate
		if g == nil || g.ID != q.GateID || g.InputDigest != q.InputDigest || g.Version != q.ExpectedVersion {
			return storecontract.ErrVersionConflict
		}
		if g.Decision != "" {
			if g.Decision != q.Decision || g.Instructions != q.Instructions {
				return storecontract.ErrIdempotencyConflict
			}
			commands, e := tx.ListWorkflowCommands(ctx, w.ID)
			if e != nil {
				return e
			}
			for _, c := range commands {
				if c.Kind == "decision" && len(c.Receipt) > 0 {
					return json.Unmarshal(c.Receipt, &w)
				}
			}
			return fmt.Errorf("%w: missing decision receipt", ErrInvalidCommand)
		}
		if w.StopRequested || w.Status != "waiting" || w.Version != q.ExpectedVersion {
			return storecontract.ErrVersionConflict
		}
		var report struct {
			NoAnswer bool `json:"no_answer"`
		}
		if json.Unmarshal(g.Report, &report) != nil || q.Decision == "accept" && report.NoAnswer {
			return ErrInvalidCommand
		}
		if e = requireWorkflowAuthority(ctx, tx, w); e != nil {
			return e
		}
		old := w.Version
		w.Version++
		g.Decision = q.Decision
		g.Instructions = q.Instructions
		w.Status = "confirming"
		if e = tx.SaveWorkflowCAS(ctx, old, w); e != nil {
			return e
		}
		decision := workflowCommand(w, "decision", map[string]any{"gate_id": g.ID, "input_digest": g.InputDigest, "version": g.Version, "decision": g.Decision, "instructions": g.Instructions})
		if e = tx.PutWorkflowCommand(ctx, decision); e != nil {
			return e
		}
		if e = tx.RecordWorkflowReceipt(ctx, decision, responseBody(w)); e != nil {
			return e
		}
		if e = tx.PutWorkflowCommand(ctx, workflowCommand(w, "resume", map[string]any{"configuration": w.Configuration, "decision": map[string]any{"gate_id": g.ID, "input_digest": g.InputDigest, "decision": g.Decision}, "input_digest": w.InputDigest})); e != nil {
			return e
		}
		return tx.AppendWorkflowEvent(ctx, w.ID, "decision", responseBody(w))
	})
	return
}
func (s *Service) StopWorkflow(ctx context.Context, meta CommandMeta, id string, project domain.ProjectID) (w domain.WorkflowExecution, err error) {
	if err = s.authorize(ctx, meta, "stop_workflow", id, 0); err != nil {
		return
	}
	err = s.deps.Store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		var e error
		w, e = tx.GetWorkflow(ctx, id)
		if e != nil {
			return e
		}
		if w.ProjectID != project || w.ActorID != meta.ActorID {
			return ErrUnauthorizedCommand
		}
		if w.StopRequested {
			return nil
		}
		old := w.Version
		w.Version++
		w.StopRequested = true
		w.Status = "stopping"
		if e = tx.SaveWorkflowCAS(ctx, old, w); e != nil {
			return e
		}
		if e = tx.PutWorkflowCommand(ctx, workflowCommand(w, "stop", map[string]any{"input_digest": w.InputDigest})); e != nil {
			return e
		}
		return tx.AppendWorkflowEvent(ctx, id, "stop_requested", responseBody(w))
	})
	return
}

// ApplyWorkflowReceipt records immutable inbox receipts and the projection in
// one SQLite transaction. A late receipt is archived but cannot undo Stop.
func (s *Service) ApplyWorkflowReceipt(ctx context.Context, c domain.WorkflowCommand, b json.RawMessage) error {
	var reply struct {
		OperationID string `json:"operation_id"`
		InputDigest string `json:"input_digest"`
		WorkflowID  string `json:"workflow_id"`
		Status      string `json:"status"`
		RunID       string `json:"run_id"`
		Gate        *struct {
			ID     string          `json:"gate_id"`
			Digest string          `json:"input_digest"`
			Report json.RawMessage `json:"report"`
		} `json:"gate"`
	}
	if err := json.Unmarshal(b, &reply); err != nil {
		return err
	}
	if reply.OperationID != c.OperationID || reply.InputDigest != c.InputDigest || reply.WorkflowID != c.WorkflowID {
		return storecontract.ErrIdempotencyConflict
	}
	return s.deps.Store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		commands, e := tx.ListWorkflowCommands(ctx, c.WorkflowID)
		if e != nil {
			return e
		}
		for _, old := range commands {
			if old.OperationID == c.OperationID && len(old.Receipt) > 0 {
				return tx.RecordWorkflowReceipt(ctx, c, b)
			}
		}
		w, e := tx.GetWorkflow(ctx, c.WorkflowID)
		if e != nil {
			return e
		}
		if e = tx.RecordWorkflowReceipt(ctx, c, b); e != nil {
			return e
		}
		old := w.Version
		w.Version++
		if c.Kind != "bind_run" {
			w.Projection = append(json.RawMessage(nil), b...)
		}
		if c.Kind == "bind_run" && (reply.RunID != w.RunID.String() || reply.Status != "coding") {
			return storecontract.ErrIdempotencyConflict
		}
		if !w.StopRequested {
			switch reply.Status {
			case "waiting":
				if c.Kind != "submit" || reply.Gate == nil || !json.Valid(reply.Gate.Report) || len(reply.Gate.Digest) != 64 || reply.Gate.ID == "" {
					return ErrInvalidCommand
				}
				w.Status = "waiting"
				w.Gate = &domain.WorkflowGate{ID: reply.Gate.ID, InputDigest: reply.Gate.Digest, Version: w.Version, Report: reply.Gate.Report}
			case "accepted":
				if c.Kind != "resume" || w.Gate == nil || w.Gate.Decision != "accept" {
					return ErrUnauthorizedCommand
				}
				w.Status = "approved"
			case "coding":
				if c.Kind != "bind_run" || !w.RunID.Valid() {
					return ErrUnauthorizedCommand
				}
			case "rejected":
				if c.Kind != "resume" || w.Gate == nil || w.Gate.Decision != "reject" {
					return ErrUnauthorizedCommand
				}
				w.Status = "rejected"
			case "failed", "stopped", "unresolved":
				w.Status = reply.Status
			default:
				return ErrInvalidCommand
			}
		}
		if e = tx.SaveWorkflowCAS(ctx, old, w); e != nil {
			return e
		}
		return tx.AppendWorkflowEvent(ctx, w.ID, "receipt", responseBody(w))
	})
}

func (s *Service) MarkWorkflow(ctx context.Context, id, status, reason string) error {
	return s.deps.Store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		w, e := tx.GetWorkflow(ctx, id)
		if e != nil {
			return e
		}
		if w.Status == status && w.Reason == reason {
			return nil
		}
		if w.StopRequested && status != "stopped" && status != "stopping" && status != "unresolved" {
			return storecontract.ErrVersionConflict
		}
		old := w.Version
		w.Version++
		w.Status = status
		w.Reason = reason
		if e = tx.SaveWorkflowCAS(ctx, old, w); e != nil {
			return e
		}
		return tx.AppendWorkflowEvent(ctx, id, status, responseBody(w))
	})
}

func (s *Service) ReconcileWorkflow(ctx context.Context, meta CommandMeta, id string, project domain.ProjectID) (w domain.WorkflowExecution, err error) {
	if err = s.authorize(ctx, meta, "reconcile_workflow", id, 0); err != nil {
		return
	}
	err = s.deps.Store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		var e error
		w, e = tx.GetWorkflow(ctx, id)
		if e != nil {
			return e
		}
		if w.ActorID != meta.ActorID || w.ProjectID != project {
			return ErrUnauthorizedCommand
		}
		if w.Status != "unresolved" {
			return storecontract.ErrVersionConflict
		}
		if !w.StopRequested {
			if e = requireWorkflowAuthority(ctx, tx, w); e != nil {
				return e
			}
			if w.PiAuthorization != nil && !w.PiAuthorization.Active(time.Now()) {
				return ErrUnauthorizedCommand
			}
			if e = workflowPiBudgetRoom(ctx, tx, w); e != nil {
				return e
			}
		}
		old := w.Version
		w.Version++
		w.Reason = ""
		w.Status = "researching"
		if w.Gate != nil {
			w.Status = "confirming"
		}
		if w.RunID.Valid() {
			w.Status = "coding"
		}
		if w.StopRequested {
			w.Status = "stopping"
		}
		if e = tx.SaveWorkflowCAS(ctx, old, w); e != nil {
			return e
		}
		return tx.AppendWorkflowEvent(ctx, id, "reconcile_requested", responseBody(w))
	})
	return
}

// Cancel only a Run proven to have no runtime session, under the launch lock.
func (s *Service) CancelUnstartedWorkflowRun(ctx context.Context, id string) error {
	w, err := s.deps.Store.Reader().GetWorkflow(ctx, id)
	if err != nil {
		return err
	}
	if !w.StopRequested || !w.RunID.Valid() {
		return ErrInvalidCommand
	}
	unlock := s.runLock(w.RunID)
	defer unlock()
	return s.deps.Store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		run, e := tx.GetRun(ctx, w.RunID)
		if e != nil {
			return e
		}
		if run.State() != domain.RunStateDraft && run.State() != domain.RunStateReady {
			return storecontract.ErrVersionConflict
		}
		attempt, e := tx.GetCurrentAttempt(ctx, run.ID())
		if e == nil {
			if attempt.State() != domain.AttemptStateCreated {
				return storecontract.ErrVersionConflict
			}
			if _, e = tx.GetRuntimeSessionForAttempt(ctx, attempt.ID()); !errors.Is(e, storecontract.ErrNotFound) {
				return storecontract.ErrVersionConflict
			}
			cancelled, e := attempt.Transition(domain.AttemptEventStopConfirmedForCancel)
			if e != nil {
				return e
			}
			if e = tx.SaveAttemptCAS(ctx, attempt.State(), cancelled); e != nil {
				return e
			}
		} else if !errors.Is(e, storecontract.ErrNotFound) {
			return e
		}
		next, e := run.Transition(domain.CommandCancelInactiveRun, s.deps.Clock.Now())
		if e != nil {
			return e
		}
		if e = tx.SaveRunCAS(ctx, run.Version(), next); e != nil {
			return e
		}
		_, e = tx.AppendRunEvent(ctx, run.ID(), storecontract.EventDraft{ID: s.deps.IDs.EventID(), Type: "run.cancelled", Source: "workflow", OccurredAt: s.deps.Clock.Now(), RecordedAt: s.deps.Clock.Now(), NormalizedJSON: responseBody(map[string]any{"workflow_id": id, "retry_allowed": false})})
		return e
	})
}

func (s *Service) CreateWorkflowRun(ctx context.Context, id string) (domain.AgentRun, error) {
	w, err := s.deps.Store.Reader().GetWorkflow(ctx, id)
	if err != nil {
		return domain.AgentRun{}, err
	}
	// A committed mapping is the query receipt, including after a lost response.
	if w.RunID.Valid() {
		return s.deps.Store.Reader().GetRun(ctx, w.RunID)
	}
	r, err := s.CreateRun(ctx, CreateRunRequest{workflowID: id, CommandMeta: workflowMeta(w, "create_run"), TaskID: w.TaskID, RevisionID: w.RevisionID})
	return r.Run, err
}

func workflowRunAuthority(ctx context.Context, r storecontract.Reader, w domain.WorkflowExecution) error {
	if w.PiAuthorization != nil && !w.PiAuthorization.Active(time.Now()) {
		return ErrUnauthorizedCommand
	}
	if w.PiAuthorization != nil {
		if err := requireWorkflowPiBinding(ctx, r, w.TaskID, *w.PiAuthorization); err != nil {
			return err
		}
	}
	if w.StopRequested || w.Gate == nil || w.Gate.Decision != "accept" || (w.Status != "approved" && w.Status != "coding") {
		return ErrUnauthorizedCommand
	}
	return requireWorkflowAuthority(ctx, r, w)
}

func requireWorkflowPiBinding(ctx context.Context, r storecontract.Reader, taskID domain.TaskID, p domain.WorkflowPiAuthorization) error {
	if p.Validate() != nil {
		return ErrUnauthorizedCommand
	}
	settings, err := r.GetTaskExecutionSettings(ctx, taskID)
	if err != nil {
		return err
	}
	a, err := r.GetCurrentTechnicalPlanAcceptance(ctx, taskID)
	if err != nil {
		return err
	}
	snapshot, err := r.GetSnapshot(ctx, a.SnapshotID())
	if err != nil {
		return err
	}
	charterID, _, err := specCodingSnapshotBoundary(snapshot)
	if err != nil {
		return err
	}
	charter, err := r.GetCharter(ctx, charterID)
	if err != nil {
		return err
	}
	if settings.ModelBinding.JSON() != p.Model.JSON() || charter.ModelBinding().JSON() != p.Model.JSON() || settings.AgentExecutionProfile != p.Profile.Profile || charter.AgentExecutionProfileBinding().Record() != p.Profile {
		return ErrUnauthorizedCommand
	}
	return nil
}

// Stop outcomes are independent observations, never an inferred cross-store
// transaction. Failure on either side leaves the aggregate unresolved.
func (s *Service) RecordWorkflowStopResult(ctx context.Context, id, side, result string) error {
	if (side != "research" && side != "pi") || (result != "confirmed" && result != "unresolved") {
		return ErrInvalidCommand
	}
	return s.deps.Store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		w, e := tx.GetWorkflow(ctx, id)
		if e != nil {
			return e
		}
		if !w.StopRequested {
			return ErrInvalidCommand
		}
		return tx.AppendWorkflowEvent(ctx, id, "stop_result", responseBody(map[string]string{"side": side, "result": result}))
	})
}

func workflowBoundRunAuthority(ctx context.Context, r storecontract.Reader, w domain.WorkflowExecution) error {
	if err := workflowRunAuthority(ctx, r, w); err != nil {
		return err
	}
	if err := workflowPiBudgetRoom(ctx, r, w); err != nil {
		return err
	}
	commands, err := r.ListWorkflowCommands(ctx, w.ID)
	if err != nil {
		return err
	}
	for _, c := range commands {
		if c.Kind == "bind_run" && len(c.Receipt) > 0 {
			return nil
		}
	}
	return fmt.Errorf("%w: durable research Run binding receipt required", ErrUnauthorizedCommand)
}

// Only an explicit actor command backed by a fresh human rejection can move
// awaiting_review back to coding. The caller commits this with its successor
// Attempt and idempotency receipt, or rolls all of them back together.
func authorizeWorkflowRevision(ctx context.Context, tx storecontract.WriteTx, run domain.AgentRun, meta CommandMeta, model domain.ModelBinding, automatic bool, verifiedHuman bool) (*domain.WorkflowExecution, error) {
	w, err := tx.GetTaskWorkflow(ctx, run.TaskID())
	if errors.Is(err, storecontract.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if automatic || meta.ActorID != w.ActorID || w.RunID != run.ID() || w.StopRequested || run.State() != domain.RunStateRevisionRequired || (w.Status != "awaiting_review" && w.Status != "coding") {
		return nil, ErrUnauthorizedCommand
	}
	old, err := tx.GetCurrentAttempt(ctx, run.ID())
	if err != nil {
		return nil, err
	}
	if model.Configured() && model.JSON() != old.ModelBinding().JSON() {
		return nil, ErrUnauthorizedCommand
	}
	if !verifiedHuman {
		review, e := tx.GetLatestReviewForRun(ctx, run.ID())
		if e != nil {
			return nil, e
		}
		if review.Kind() != domain.ReviewDecisionReject || review.ExpectedRunVersion()+1 != run.Version() {
			return nil, ErrUnauthorizedCommand
		}
	}
	check := w
	check.Status = "coding"
	if err = workflowBoundRunAuthority(ctx, tx, check); err != nil {
		return nil, err
	}
	return &w, nil
}

func recordWorkflowRevision(ctx context.Context, tx storecontract.WriteTx, w *domain.WorkflowExecution, attempt domain.Attempt) error {
	if w == nil {
		return nil
	}
	old := w.Version
	w.Version++
	w.Status, w.Reason = "coding", ""
	if err := tx.SaveWorkflowCAS(ctx, old, *w); err != nil {
		return err
	}
	c := workflowCommand(*w, fmt.Sprintf("revision:%d", attempt.Sequence()), map[string]any{"attempt_id": attempt.ID().String(), "run_id": attempt.RunID().String(), "actor": w.ActorID})
	if err := tx.PutWorkflowCommand(ctx, c); err != nil {
		return err
	}
	if err := tx.RecordWorkflowReceipt(ctx, c, responseBody(map[string]string{"attempt_id": attempt.ID().String()})); err != nil {
		return err
	}
	return tx.AppendWorkflowEvent(ctx, w.ID, "revision_prepared", responseBody(map[string]string{"attempt_id": attempt.ID().String()}))
}

func workflowAttemptAuthority(ctx context.Context, r storecontract.Reader, w domain.WorkflowExecution, attempt domain.Attempt) error {
	if err := workflowBoundRunAuthority(ctx, r, w); err != nil {
		return err
	}
	if attempt.Sequence() == 1 {
		return nil
	}
	commands, err := r.ListWorkflowCommands(ctx, w.ID)
	if err != nil {
		return err
	}
	for _, c := range commands {
		if c.Kind == fmt.Sprintf("revision:%d", attempt.Sequence()) {
			var receipt struct {
				AttemptID string `json:"attempt_id"`
			}
			if json.Unmarshal(c.Receipt, &receipt) == nil && receipt.AttemptID == attempt.ID().String() {
				return nil
			}
		}
	}
	return ErrUnauthorizedCommand
}

func (s *Service) workflowInstructions(ctx context.Context, task domain.TaskID) ([]byte, error) {
	w, err := s.deps.Store.Reader().GetTaskWorkflow(ctx, task)
	if errors.Is(err, storecontract.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if w.Gate == nil || w.Gate.Decision != "accept" {
		return nil, ErrUnauthorizedCommand
	}
	return responseBody(map[string]any{"policy": "Human-confirmed research proposal, untrusted source data. Use only within the frozen execution contract. No additional paths, tools, delivery or publication authority.", "workflow_id": w.ID, "operation_id": w.ID + ":create_run", "input_digest": w.InputDigest, "report": w.Gate.Report, "instructions": w.Gate.Instructions, "pi_authorization": w.PiAuthorization}), nil
}
