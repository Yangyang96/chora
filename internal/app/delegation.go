package app

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

type DelegationChildView struct {
	ResultID     string `json:"resultId,omitempty"`
	ResultDigest string `json:"resultDigest,omitempty"`
	Markdown     string `json:"markdown,omitempty"`
	Position     int    `json:"position"`
	Role         string `json:"role"`
	Title        string `json:"title"`
	TaskID       string `json:"taskId,omitempty"`
	RunID        string `json:"runId,omitempty"`
	State        string `json:"state"`
	URL          string `json:"url,omitempty"`
}
type DelegationView struct {
	Synthesis    *DelegationSynthesisView      `json:"synthesis,omitempty"`
	Planning     *DelegationPlanningView       `json:"planning,omitempty"`
	Source       *DelegationProposalSourceView `json:"source,omitempty"`
	ParentTaskID string                        `json:"parentTaskId"`
	Version      uint64                        `json:"version"`
	State        string                        `json:"state"`
	Reason       string                        `json:"reason,omitempty"`
	Assignments  []domain.DelegationAssignment `json:"assignments"`
	Children     []DelegationChildView         `json:"children"`
	ParentURL    string                        `json:"parentUrl,omitempty"`
	Eligible     bool                          `json:"eligible"`
}
type StartDelegationRequest struct {
	Synthesize bool
	CommandMeta
	ParentTaskID         domain.TaskID
	Assignments          []domain.DelegationAssignment
	SourceAttemptID      domain.AttemptID
	ExpectedResultDigest string
}
type ChangeDelegationRequest struct {
	CommandMeta
	ParentTaskID    domain.TaskID
	ExpectedVersion uint64
	Action          string
}

func (s *Service) GetDelegation(ctx context.Context, id domain.TaskID) (DelegationView, error) {
	r := s.deps.Store.Reader()
	task, err := r.GetTask(ctx, id)
	if err != nil {
		return DelegationView{}, err
	}
	v := DelegationView{ParentTaskID: id.String(), State: "not_started", Assignments: []domain.DelegationAssignment{}, Children: []DelegationChildView{}}
	if child, e := r.GetDelegationChild(ctx, id); e == nil {
		v.State = "child"
		v.ParentTaskID = child.ParentTaskID.String()
		v.ParentURL = fmt.Sprintf("/rooms/%s/tasks/%s", task.RoomID(), child.ParentTaskID)
		return v, nil
	} else if !errors.Is(e, storecontract.ErrNotFound) {
		return v, e
	}
	if item, e := r.GetSynthesisForTask(ctx, id); e == nil {
		v.State = "child"
		v.ParentTaskID = item.ParentTaskID.String()
		v.ParentURL = fmt.Sprintf("/rooms/%s/tasks/%s", task.RoomID(), item.ParentTaskID)
		return v, nil
	} else if !errors.Is(e, storecontract.ErrNotFound) {
		return v, e
	}
	_, _, eligibilityErr := delegationParent(ctx, r, id)
	v.Eligible = eligibilityErr == nil
	if p, e := r.GetDelegationPlanning(ctx, id); e == nil {
		v.Planning = &DelegationPlanningView{State: p.State, Version: p.Version, RunID: p.RunID.String(), Reason: p.Reason}
	} else if !errors.Is(e, storecontract.ErrNotFound) {
		return v, e
	}
	v.Synthesis, err = s.synthesisView(ctx, r, id, task.RoomID())
	if err != nil {
		return v, err
	}
	d, err := r.GetTaskDelegation(ctx, id)
	if errors.Is(err, storecontract.ErrNotFound) {
		return v, nil
	}
	if err != nil {
		return v, err
	}
	v.Version = d.Version
	v.State = string(d.State)
	v.Reason = d.Reason
	if d.State == domain.DelegationAwaitingReview && v.Synthesis != nil && v.Synthesis.State != string(domain.RunStateAwaitingReview) && v.Synthesis.State != string(domain.RunStateAccepted) && v.Synthesis.State != string(domain.RunStateCompleted) {
		v.State = "needs_attention"
		v.Reason = "The synthesis result changed after the delegation finished. Review its current Run."
	}
	v.Assignments = d.Assignments
	if source, e := r.GetDelegationProposalSource(ctx, id); e == nil {
		_, current, currentErr := loadDelegationProposalEvidence(ctx, r, id, source.AttemptID, fmt.Sprintf("%x", source.ResultDigest), false)
		if currentErr != nil && !delegationProposalUnavailable(currentErr) {
			return v, currentErr
		}
		sv := delegationSourceView(source, currentErr == nil && current.ProjectDocumentSource == source.ProjectDocumentSource && current.PlanDigest == source.PlanDigest)
		v.Source = &sv
	} else if !errors.Is(e, storecontract.ErrNotFound) {
		return v, e
	}
	children, err := r.ListDelegationChildren(ctx, id)
	if err != nil {
		return v, err
	}
	byPosition := map[int]domain.DelegationChild{}
	for _, c := range children {
		byPosition[c.Position] = c
	}
	for i, a := range d.Assignments {
		item := DelegationChildView{Position: i, Role: a.Role, Title: a.Title, State: "pending"}
		if c, ok := byPosition[i]; ok {
			item.TaskID = c.TaskID.String()
			item.URL = fmt.Sprintf("/rooms/%s/tasks/%s", task.RoomID(), c.TaskID)
			item.State = "preparing"
			if c.RunID.Valid() {
				run, e := r.GetRun(ctx, c.RunID)
				if e != nil {
					return v, e
				}
				item.RunID = run.ID().String()
				item.State = string(run.State())
				item.URL += "/runs/" + run.ID().String()
				if run.State() == domain.RunStateAwaitingReview || run.State() == domain.RunStateAccepted || run.State() == domain.RunStateCompleted {
					attempt, e := r.GetCurrentAttempt(ctx, run.ID())
					if e != nil {
						return v, e
					}
					group, e := r.GetResourceResultGroup(ctx, attempt.ID())
					if e != nil {
						return v, e
					}
					if group.TaskID != c.TaskID.String() || group.RunID != run.ID().String() || group.OutcomeKind != "document" || group.Outcome != "review_ready" {
						return v, fmt.Errorf("%w: delegated result provenance is unavailable", ErrInvalidCommand)
					}
					_, digest, e := group.CanonicalJSON()
					if e != nil {
						return v, e
					}
					item.ResultID = group.ID
					item.ResultDigest = fmt.Sprintf("%x", digest)
					item.Markdown = group.Markdown
				}
				if d.State == domain.DelegationAwaitingReview && run.State() != domain.RunStateAwaitingReview && run.State() != domain.RunStateAccepted && run.State() != domain.RunStateCompleted {
					v.State = "needs_attention"
					v.Reason = "A child result changed after the delegation finished. Review its current Run."
				}

			}
		}
		v.Children = append(v.Children, item)
	}
	return v, nil
}

func delegationParent(ctx context.Context, r storecontract.Reader, id domain.TaskID) (domain.Task, domain.TaskResourceSnapshot, error) {
	task, err := r.GetTask(ctx, id)
	if err != nil {
		return task, domain.TaskResourceSnapshot{}, err
	}
	if task.Archived() || task.State() != domain.TaskStateOpen {
		return task, domain.TaskResourceSnapshot{}, fmt.Errorf("%w: delegation requires an open Task", ErrInvalidCommand)
	}
	room, err := r.GetRoom(ctx, task.RoomID())
	if err != nil {
		return task, domain.TaskResourceSnapshot{}, err
	}
	if room.State() != domain.RoomStateActive || !room.ProjectID().Valid() {
		return task, domain.TaskResourceSnapshot{}, storecontract.ErrRoomStateForbidden
	}
	project, err := r.GetProject(ctx, room.ProjectID())
	if err != nil {
		return task, domain.TaskResourceSnapshot{}, err
	}
	if project.State() != domain.ProjectStateActive {
		return task, domain.TaskResourceSnapshot{}, storecontract.ErrRoomStateForbidden
	}
	if _, err := r.GetDelegationChild(ctx, id); !errors.Is(err, storecontract.ErrNotFound) {
		if err != nil {
			return task, domain.TaskResourceSnapshot{}, err
		}
		return task, domain.TaskResourceSnapshot{}, fmt.Errorf("%w: recursive delegation is unavailable", ErrInvalidCommand)
	}
	if _, e := r.GetSynthesisForTask(ctx, id); !errors.Is(e, storecontract.ErrNotFound) {
		if e != nil {
			return task, domain.TaskResourceSnapshot{}, e
		}
		return task, domain.TaskResourceSnapshot{}, fmt.Errorf("%w: synthesis cannot delegate", ErrInvalidCommand)
	}
	record, err := r.GetTaskResourceSnapshot(ctx, id)
	if err != nil {
		return task, domain.TaskResourceSnapshot{}, err
	}
	var resources domain.TaskResourceSnapshot
	if err = json.Unmarshal(record.CanonicalJSON, &resources); err != nil {
		return task, resources, err
	}
	_, digest, err := resources.CanonicalJSON()
	if err != nil || digest != record.Digest {
		return task, resources, fmt.Errorf("%w: parent resources changed", ErrInvalidCommand)
	}
	// The first slice reuses S9 research results. Repository delegation needs a
	// separate integration/merge contract before it becomes an available choice.
	if resources.OutcomeKind != "document" {
		return task, resources, fmt.Errorf("%w: delegation currently requires a research or document Task", ErrInvalidCommand)
	}
	settings, err := r.GetTaskExecutionSettings(ctx, id)
	if err != nil {
		return task, resources, err
	}
	if err = settings.Validate(); err != nil {
		return task, resources, err
	}
	return task, resources, nil
}

func (s *Service) StartDelegation(ctx context.Context, req StartDelegationRequest) (DelegationView, error) {
	if err := s.authorize(ctx, req.CommandMeta, "start_delegation", req.ParentTaskID.String(), 0); err != nil {
		return DelegationView{}, err
	}
	now := s.deps.Clock.Now()
	// Preserve the original assignments-mode command digest across upgrades.
	var semantic any = req.Assignments
	sourceMode := req.SourceAttemptID.Valid() || req.ExpectedResultDigest != ""
	if sourceMode {
		if req.Assignments != nil || !req.SourceAttemptID.Valid() || req.ExpectedResultDigest == "" {
			return DelegationView{}, fmt.Errorf("%w: choose assignments or one exact source Attempt and digest", ErrInvalidCommand)
		}
		decoded, e := hex.DecodeString(req.ExpectedResultDigest)
		if e != nil || len(decoded) != 32 || hex.EncodeToString(decoded) != req.ExpectedResultDigest {
			return DelegationView{}, fmt.Errorf("%w: source result digest must be a lowercase SHA-256", ErrInvalidCommand)
		}
		semantic = struct{ SourceAttemptID, ExpectedResultDigest string }{req.SourceAttemptID.String(), req.ExpectedResultDigest}
	}
	if req.Synthesize {
		semantic = struct {
			Request    any
			Synthesize bool
		}{semantic, true}
	}
	key, err := s.commandKey(req.CommandMeta, "start_delegation", req.ParentTaskID.String(), 0, semantic, now)
	if err != nil {
		return DelegationView{}, err
	}
	err = s.deps.Store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		if _, replay, e := tx.LookupCommand(ctx, key); e != nil {
			return e
		} else if replay {
			return nil
		}
		if _, e := tx.GetDelegationPlanning(ctx, req.ParentTaskID); !errors.Is(e, storecontract.ErrNotFound) {
			if e != nil {
				return e
			}
			return storecontract.ErrVersionConflict
		}
		if _, _, e := delegationParent(ctx, tx, req.ParentTaskID); e != nil {
			return e
		}
		settings, e := tx.GetTaskExecutionSettings(ctx, req.ParentTaskID)
		if e != nil {
			return e
		}
		if e = requireProfileAcknowledgement(ctx, tx, settings.AgentExecutionProfile, req.ActorID); e != nil {
			return e
		}
		if _, e = tx.GetTaskDelegation(ctx, req.ParentTaskID); !errors.Is(e, storecontract.ErrNotFound) {
			if e != nil {
				return e
			}
			return storecontract.ErrVersionConflict
		}
		assignments := req.Assignments
		var source domain.DelegationProposalSource
		if sourceMode {
			assignments, source, e = loadDelegationProposal(ctx, tx, req.ParentTaskID, req.SourceAttemptID, req.ExpectedResultDigest)
			if e != nil {
				return e
			}
			source.CreatedAt = now
		}
		d := domain.TaskDelegation{ParentTaskID: req.ParentTaskID, Version: 1, State: domain.DelegationRunning, Assignments: assignments, ActorID: req.ActorID, SessionID: req.SessionID, CreatedAt: now, UpdatedAt: now}
		if e = tx.InsertTaskDelegation(ctx, d); e != nil {
			return e
		}
		if req.Synthesize {
			if e = tx.InsertDelegationSynthesis(ctx, domain.DelegationSynthesis{ParentTaskID: req.ParentTaskID, ActorID: req.ActorID, SessionID: req.SessionID, CreatedAt: now}); e != nil {
				return e
			}
		}
		if sourceMode {
			if e = tx.InsertDelegationProposalSource(ctx, source); e != nil {
				return e
			}
		}
		return tx.SaveCommand(ctx, key, storecontract.Response{Body: []byte(`{}`)})
	})
	if err != nil {
		return DelegationView{}, err
	}
	return s.GetDelegation(ctx, req.ParentTaskID)
}
func (s *Service) ChangeDelegation(ctx context.Context, req ChangeDelegationRequest) (DelegationView, error) {
	if req.Action != "stop" && req.Action != "resume" {
		return DelegationView{}, ErrInvalidCommand
	}
	if err := s.authorize(ctx, req.CommandMeta, "change_delegation", req.ParentTaskID.String(), req.ExpectedVersion); err != nil {
		return DelegationView{}, err
	}
	now := s.deps.Clock.Now()
	key, err := s.commandKey(req.CommandMeta, "change_delegation", req.ParentTaskID.String(), req.ExpectedVersion, req.Action, now)
	if err != nil {
		return DelegationView{}, err
	}
	err = s.deps.Store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		if _, replay, e := tx.LookupCommand(ctx, key); e != nil {
			return e
		} else if replay {
			return nil
		}
		d, e := tx.GetTaskDelegation(ctx, req.ParentTaskID)
		if e != nil {
			return e
		}
		if d.Version != req.ExpectedVersion {
			return storecontract.ErrVersionConflict
		}
		next := domain.DelegationStopping
		if req.Action == "resume" {
			if _, _, e = delegationParent(ctx, tx, req.ParentTaskID); e != nil {
				return e
			}
			next = domain.DelegationRunning
		}
		d, e = d.Transition(next, "", now)
		if e != nil {
			return e
		}
		if e = tx.SaveTaskDelegationCAS(ctx, req.ExpectedVersion, d); e != nil {
			return e
		}
		return tx.SaveCommand(ctx, key, storecontract.Response{Body: []byte(`{}`)})
	})
	if err != nil {
		return DelegationView{}, err
	}
	return s.GetDelegation(ctx, req.ParentTaskID)
}

// Only this service-owned path can attach child lineage and frozen settings.
// HTTP Task creation never accepts these fields.
type delegatedTaskCreation struct {
	SynthesisInputs []domain.DelegationSynthesisInput
	ParentTaskID    domain.TaskID
	Position        int
	Settings        domain.TaskExecutionSettings
}

func (s *Service) CreateDelegationChild(ctx context.Context, parentID domain.TaskID, position int) (CreateTaskResult, error) {
	r := s.deps.Store.Reader()
	d, err := r.GetTaskDelegation(ctx, parentID)
	if err != nil {
		return CreateTaskResult{}, err
	}
	if d.State != domain.DelegationRunning || position < 0 || position >= len(d.Assignments) {
		return CreateTaskResult{}, storecontract.ErrVersionConflict
	}
	parent, resources, err := delegationParent(ctx, r, parentID)
	if err != nil {
		return CreateTaskResult{}, err
	}
	settings, err := r.GetTaskExecutionSettings(ctx, parentID)
	if err != nil {
		return CreateTaskResult{}, err
	}
	selection, err := r.GetTaskRevisionSelection(ctx, parentID)
	if err != nil {
		return CreateTaskResult{}, err
	}
	ids := []domain.ContextRevisionID{}
	for _, item := range selection.Selected() {
		ids = append(ids, item.RevisionID)
	}
	resources.TaskID = ""
	resources.BranchType = ""
	resources.SelectionSource = "parent_delegation"
	a := d.Assignments[position]
	req := CreateTaskRequest{CommandMeta: CommandMeta{ActorID: d.ActorID, SessionID: "system.delegation", IdempotencyKey: fmt.Sprintf("delegation:%s:child:%d", parentID, position)}, RoomID: parent.RoomID(), Title: a.Title, ExecutionProfile: TaskExecutionProfileRealSpecCoding, AgentExecutionProfile: settings.AgentExecutionProfile, ModelBinding: settings.ModelBinding, RevisionIDs: ids,
		RealSpecCoding: &RealSpecCodingInput{Resources: &resources, Requirement: a.Requirement, Constraints: []string{"Delegated role: " + a.Role, "Use only the parent's frozen software-project material. Return a complete Markdown result with source locators, uncertainties and missing evidence."}, OutOfScope: []string{"Repository writes, publication, delivery, further delegation, or access to unselected material. Source locators are references, not permission to fetch."}, Criteria: []RealSpecCodingCriterion{{Title: "Reviewable delegated finding", Description: a.Requirement}}}, delegation: &delegatedTaskCreation{ParentTaskID: parentID, Position: position, Settings: settings}}
	return s.CreateTask(ctx, req)
}

// ValidateDelegatedPlan proves default-pass activation still refers to the fixed
// assignment, including recovery after draft submission but before activation.
func (s *Service) ValidateDelegatedPlan(ctx context.Context, taskID domain.TaskID, revisionID domain.TechnicalPlanRevisionID) error {
	r := s.deps.Store.Reader()
	if _, err := r.GetDelegationChild(ctx, taskID); err != nil {
		if !errors.Is(err, storecontract.ErrNotFound) {
			return err
		}
		if _, err = r.GetSynthesisForTask(ctx, taskID); err != nil {
			return err
		}
	}
	intent, err := s.restoreRealSpecCodingIntent(ctx, r, taskID)
	if err != nil {
		return err
	}
	revision, err := r.GetTechnicalPlanRevision(ctx, revisionID)
	if err != nil {
		return err
	}
	if revision.TaskID() != taskID || revision.RevisionNumber() != 1 || revision.ContentDigest() != domain.CanonicalTechnicalPlanContentDigest(intent.InitialPlanContent()) {
		return fmt.Errorf("%w: delegated plan differs from the fixed assignment", ErrInvalidCommand)
	}
	return nil
}

// CancelUnstartedDelegationChild proves absence of a runtime session while
// holding the same Run lock used by StartAttempt. It never infers process death
// from missing in-memory supervisor state.
func (s *Service) CancelUnstartedDelegationChild(ctx context.Context, parentID, taskID domain.TaskID) error {
	child, err := s.deps.Store.Reader().GetDelegationChild(ctx, taskID)
	if errors.Is(err, storecontract.ErrNotFound) {
		item, e := s.deps.Store.Reader().GetSynthesisForTask(ctx, taskID)
		if e != nil {
			return e
		}
		child = domain.DelegationChild{ParentTaskID: item.ParentTaskID, TaskID: item.TaskID, RunID: item.RunID}
	} else if err != nil {
		return err
	}
	if child.ParentTaskID != parentID || !child.RunID.Valid() {
		return ErrInvalidCommand
	}
	return s.cancelUnstartedDelegationRun(ctx, child.RunID, parentID, false)
}
func (s *Service) cancelUnstartedDelegationRun(ctx context.Context, runID domain.RunID, parentID domain.TaskID, planning bool) error {
	unlock := s.runLock(runID)
	defer unlock()
	return s.deps.Store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		if planning {
			p, e := tx.GetDelegationPlanning(ctx, parentID)
			if e != nil {
				return e
			}
			if p.State != domain.DelegationPlanningStopping || p.RunID != runID {
				return storecontract.ErrVersionConflict
			}
		} else {
			d, e := tx.GetTaskDelegation(ctx, parentID)
			if e != nil {
				return e
			}
			if d.State != domain.DelegationStopping {
				return storecontract.ErrVersionConflict
			}
		}
		run, e := tx.GetRun(ctx, runID)
		if e != nil {
			return e
		}
		if run.State() == domain.RunStateRecoveryRequired {
			attempt, e := tx.GetCurrentAttempt(ctx, run.ID())
			if e != nil {
				return e
			}
			session, e := tx.GetRuntimeSessionForAttempt(ctx, attempt.ID())
			if e != nil {
				return e
			}
			if !automaticRetryFinalized(attempt, session) {
				return storecontract.ErrVersionConflict
			}
			next, e := run.Transition(domain.CommandCancelInactiveRun, s.deps.Clock.Now())
			if e != nil {
				return e
			}
			if e = tx.SaveRunCAS(ctx, run.Version(), next); e != nil {
				return e
			}
			_, e = tx.AppendRunEvent(ctx, run.ID(), storecontract.EventDraft{ID: s.deps.IDs.EventID(), Type: "run.cancelled", Source: "app", OccurredAt: s.deps.Clock.Now(), RecordedAt: s.deps.Clock.Now(), NormalizedJSON: responseBody(map[string]any{"type": "run.cancelled", "run_id": run.ID().String(), "attempt_id": attempt.ID().String(), "reason": "Delegation stopped after runtime finalization", "retry_allowed": false})})
			return e
		}
		if run.State() != domain.RunStateDraft && run.State() != domain.RunStateReady {
			return storecontract.ErrVersionConflict
		}
		attempt, e := tx.GetCurrentAttempt(ctx, run.ID())
		if run.State() == domain.RunStateDraft {
			if !errors.Is(e, storecontract.ErrNotFound) {
				if e != nil {
					return e
				}
				return storecontract.ErrVersionConflict
			}
		} else {
			if e != nil {
				return e
			}
			if attempt.State() != domain.AttemptStateCreated {
				return storecontract.ErrVersionConflict
			}
			if _, e = tx.GetRuntimeSessionForAttempt(ctx, attempt.ID()); !errors.Is(e, storecontract.ErrNotFound) {
				if e != nil {
					return e
				}
				return storecontract.ErrVersionConflict
			}
			cancelled, e := attempt.Transition(domain.AttemptEventStopConfirmedForCancel)
			if e != nil {
				return e
			}
			if e = tx.SaveAttemptCAS(ctx, attempt.State(), cancelled); e != nil {
				return e
			}
		}
		next, e := run.Transition(domain.CommandCancelInactiveRun, s.deps.Clock.Now())
		if e != nil {
			return e
		}
		if e = tx.SaveRunCAS(ctx, run.Version(), next); e != nil {
			return e
		}
		_, e = tx.AppendRunEvent(ctx, run.ID(), storecontract.EventDraft{ID: s.deps.IDs.EventID(), Type: "run.cancelled", Source: "app", OccurredAt: s.deps.Clock.Now(), RecordedAt: s.deps.Clock.Now(), NormalizedJSON: responseBody(map[string]any{"type": "run.cancelled", "run_id": run.ID().String(), "reason": "Parent delegation stopped before runtime launch", "parent_task_id": parentID.String(), "retry_allowed": false})})
		return e
	})
}
