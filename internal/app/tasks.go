package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Yangyang96/chora/internal/contextcore"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/speccoding"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

type criterionWire struct{ ID, Title, Description string }

type taskWire struct {
	ID, RoomID, PredecessorTaskID, Title, Goal string
	Criteria                                   []criterionWire
	State                                      domain.TaskState
	Archived                                   bool
	ArchivedAt                                 time.Time
}

type creationWire struct {
	TaskID, DraftID       string
	ExecutionProfile      TaskExecutionProfile
	AgentExecutionProfile domain.AgentExecutionProfile
	Task                  *taskWire
	Draft                 *technicalPlanDraftWire
}

type draftCommandWire struct {
	DraftID, RevisionID string
	Draft               *technicalPlanDraftWire
}

type technicalPlanDraftWire struct {
	ID, TaskID, PredecessorRevisionID, SubmittedRevisionID string
	EditVersion, NextRevisionNumber                        uint64
	BaseContentDigest, SelectionDigest                     [32]byte
	Content                                                domain.TechnicalPlanContent
	CreatedAt, UpdatedAt, ClosedAt                         time.Time
}

type planningReviewWire struct {
	ReviewID, TaskID, RevisionID, DraftID, SnapshotID, CharterID string
	SnapshotDigest                                               [32]byte
	BoundAt                                                      time.Time
	Draft                                                        *technicalPlanDraftWire
}

func wireTechnicalPlanDraft(draft domain.TechnicalPlanDraft) technicalPlanDraftWire {
	return technicalPlanDraftWire{ID: draft.ID().String(), TaskID: draft.TaskID().String(), PredecessorRevisionID: optionalPlanRevisionID(draft.PredecessorRevisionID()), SubmittedRevisionID: optionalPlanRevisionID(draft.SubmittedRevisionID()), EditVersion: draft.EditVersion(), NextRevisionNumber: draft.NextRevisionNumber(), BaseContentDigest: draft.BaseContentDigest(), SelectionDigest: draft.SelectionDigest(), Content: draft.Content(), CreatedAt: draft.CreatedAt(), UpdatedAt: draft.UpdatedAt(), ClosedAt: draft.ClosedAt()}
}

func wireTask(task domain.Task) taskWire {
	criteria := make([]criterionWire, 0, len(task.Criteria()))
	for _, criterion := range task.Criteria() {
		criteria = append(criteria, criterionWire{ID: criterion.ID().String(), Title: criterion.Title(), Description: criterion.Description()})
	}
	predecessor := ""
	if task.PredecessorTaskID().Valid() {
		predecessor = task.PredecessorTaskID().String()
	}
	return taskWire{ID: task.ID().String(), RoomID: task.RoomID().String(), PredecessorTaskID: predecessor, Title: task.Title(), Goal: task.Goal(), Criteria: criteria, State: task.State(), Archived: task.Archived(), ArchivedAt: task.ArchivedAt()}
}

func restoreTaskWire(wire taskWire) (domain.Task, error) {
	id, err := domain.ParseTaskID(wire.ID)
	if err != nil {
		return domain.Task{}, err
	}
	roomID, err := domain.ParseRoomID(wire.RoomID)
	if err != nil {
		return domain.Task{}, err
	}
	var predecessor domain.TaskID
	if wire.PredecessorTaskID != "" {
		predecessor, err = domain.ParseTaskID(wire.PredecessorTaskID)
		if err != nil {
			return domain.Task{}, err
		}
	}
	criteria := make([]domain.AcceptanceCriterion, 0, len(wire.Criteria))
	for _, value := range wire.Criteria {
		criterionID, err := domain.ParseCriterionID(value.ID)
		if err != nil {
			return domain.Task{}, err
		}
		criterion, err := domain.NewAcceptanceCriterion(criterionID, value.Title, value.Description)
		if err != nil {
			return domain.Task{}, err
		}
		criteria = append(criteria, criterion)
	}
	return domain.RestoreTask(domain.TaskRecord{ID: id, RoomID: roomID, PredecessorTaskID: predecessor, Title: wire.Title, Goal: wire.Goal, Criteria: criteria, State: wire.State, Archived: wire.Archived, ArchivedAt: wire.ArchivedAt})
}

func optionalPlanRevisionID(id domain.TechnicalPlanRevisionID) string {
	if !id.Valid() {
		return ""
	}
	return id.String()
}

func restoreTechnicalPlanDraftWire(wire technicalPlanDraftWire) (domain.TechnicalPlanDraft, error) {
	id, err := domain.ParseTechnicalPlanDraftID(wire.ID)
	if err != nil {
		return domain.TechnicalPlanDraft{}, err
	}
	taskID, err := domain.ParseTaskID(wire.TaskID)
	if err != nil {
		return domain.TechnicalPlanDraft{}, err
	}
	var predecessor, submitted domain.TechnicalPlanRevisionID
	if wire.PredecessorRevisionID != "" {
		predecessor, err = domain.ParseTechnicalPlanRevisionID(wire.PredecessorRevisionID)
		if err != nil {
			return domain.TechnicalPlanDraft{}, err
		}
	}
	if wire.SubmittedRevisionID != "" {
		submitted, err = domain.ParseTechnicalPlanRevisionID(wire.SubmittedRevisionID)
		if err != nil {
			return domain.TechnicalPlanDraft{}, err
		}
	}
	return domain.RestoreTechnicalPlanDraft(domain.TechnicalPlanDraftRecord{ID: id, TaskID: taskID, EditVersion: wire.EditVersion, PredecessorRevisionID: predecessor, NextRevisionNumber: wire.NextRevisionNumber, BaseContentDigest: wire.BaseContentDigest, SelectionDigest: wire.SelectionDigest, Content: wire.Content, CreatedAt: wire.CreatedAt, UpdatedAt: wire.UpdatedAt, ClosedAt: wire.ClosedAt, SubmittedRevisionID: submitted})
}

type runCreationWire struct{ RunID string }

func (s *Service) CreateTask(ctx context.Context, request CreateTaskRequest) (CreateTaskResult, error) {
	if err := s.authorize(ctx, request.CommandMeta, "create_task", request.RoomID.String(), 0); err != nil {
		return CreateTaskResult{}, err
	}
	profile, err := normalizedTaskExecutionProfile(request.ExecutionProfile)
	if err != nil {
		return CreateTaskResult{}, err
	}
	if err := validateProfileInput(request, profile); err != nil {
		return CreateTaskResult{}, err
	}
	var agentProfile domain.AgentExecutionProfile
	if profile == TaskExecutionProfileRealSpecCoding {
		agentProfile, err = normalizedAgentExecutionProfile(request.AgentExecutionProfile)
		if err != nil {
			return CreateTaskResult{}, err
		}
	} else if request.AgentExecutionProfile != "" {
		return CreateTaskResult{}, fmt.Errorf("%w: diagnostic Fake Tasks do not accept an Agent execution profile", ErrInvalidCommand)
	}
	now := s.deps.Clock.Now()
	revisionIDs := make([]string, 0, len(request.RevisionIDs))
	for _, id := range request.RevisionIDs {
		revisionIDs = append(revisionIDs, id.String())
	}
	criteria := make([]criterionWire, 0, len(request.Criteria))
	for _, criterion := range request.Criteria {
		criteria = append(criteria, criterionWire{ID: criterion.ID().String(), Title: criterion.Title(), Description: criterion.Description()})
	}
	key, err := s.commandKey(request.CommandMeta, "create_task", request.RoomID.String(), 0, struct {
		Title, Goal           string
		ExecutionProfile      TaskExecutionProfile
		AgentExecutionProfile domain.AgentExecutionProfile
		Criteria              []criterionWire
		RevisionIDs           []string
		PlanContent           domain.TechnicalPlanContent
		RealSpecCoding        *RealSpecCodingInput
	}{request.Title, request.Goal, profile, agentProfile, criteria, revisionIDs, request.PlanContent, request.RealSpecCoding}, now)
	if err != nil {
		return CreateTaskResult{}, err
	}
	var result CreateTaskResult
	var plannedWorktree *domain.TaskWorktreeBinding
	err = s.deps.Store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		response, replayed, err := tx.LookupCommand(ctx, key)
		if err != nil {
			return err
		}
		if replayed {
			var wire creationWire
			if err := json.Unmarshal(response.Body, &wire); err != nil {
				return err
			}
			taskID, err := domain.ParseTaskID(wire.TaskID)
			if err != nil {
				return err
			}
			draftID, err := domain.ParseTechnicalPlanDraftID(wire.DraftID)
			if err != nil {
				return err
			}
			if wire.Task != nil {
				result.Task, err = restoreTaskWire(*wire.Task)
			} else {
				result.Task, err = tx.GetTask(ctx, taskID)
			}
			if err != nil {
				return err
			}
			result.Selection, err = tx.GetTaskRevisionSelection(ctx, taskID)
			if err != nil {
				return err
			}
			if wire.Draft != nil {
				result.Draft, err = restoreTechnicalPlanDraftWire(*wire.Draft)
			} else {
				result.Draft, err = tx.GetTechnicalPlanDraft(ctx, draftID)
			}
			wireProfile, err := normalizedTaskExecutionProfile(wire.ExecutionProfile)
			if err != nil {
				return err
			}
			if wireProfile == TaskExecutionProfileRealSpecCoding {
				if _, err := s.restoreRealSpecCodingIntent(ctx, tx, taskID); err != nil {
					return err
				}
				// Commands persisted before Task-level preferences had implicit
				// Standard semantics; migration 0026 preserves that exact meaning.
				if wire.AgentExecutionProfile == "" {
					wire.AgentExecutionProfile = domain.AgentExecutionProfileStandard
				}
				preference, err := tx.GetTaskAgentExecutionProfilePreference(ctx, taskID)
				if err != nil || preference.Profile() != wire.AgentExecutionProfile {
					return fmt.Errorf("%w: replayed Task Agent execution profile drift", ErrInvalidCommand)
				}
				result.AgentExecutionProfile = preference.Profile()
			} else if wire.AgentExecutionProfile != "" {
				return fmt.Errorf("%w: diagnostic Task replay contains an Agent execution profile", ErrInvalidCommand)
			}
			result.ExecutionProfile = wireProfile
			if binding, bindingErr := tx.GetTaskWorktreeBinding(ctx, taskID); bindingErr == nil {
				result.Worktree = &binding
			} else if !errors.Is(bindingErr, storecontract.ErrNotFound) {
				return bindingErr
			}
			result.Replayed = err == nil
			return err
		}
		room, err := tx.GetRoom(ctx, request.RoomID)
		if err != nil {
			return err
		}
		if err := s.requireWorkbenchRoom(ctx, tx, room.ID()); err != nil {
			return err
		}
		if room.State() != domain.RoomStateActive {
			return storecontract.ErrRoomStateForbidden
		}
		var task domain.Task
		var intent speccoding.DeclaredUserTask
		planContent := request.PlanContent
		if profile == TaskExecutionProfileRealSpecCoding {
			if err := requireProfileAcknowledgement(ctx, tx, agentProfile, request.ActorID); err != nil {
				return err
			}
			task, intent, err = s.declareRealSpecCodingTask(ctx, room, request)
			if err != nil {
				return err
			}
			planContent = intent.InitialPlanContent()
		} else {
			task, err = domain.NewTask(s.deps.IDs.TaskID(), request.RoomID, request.Title, request.Goal, request.Criteria)
			if err != nil {
				return err
			}
		}
		available, err := tx.ListRoomRevisions(ctx, request.RoomID)
		if err != nil {
			return err
		}
		selection, err := buildTaskSelection(task, request.RevisionIDs, available, now)
		if err != nil {
			return err
		}
		draft, err := domain.NewTechnicalPlanDraft(domain.NewTechnicalPlanDraftParams{
			ID: s.deps.IDs.TechnicalPlanDraftID(), TaskID: task.ID(), SelectionDigest: selection.Digest(), Content: planContent, CreatedAt: now,
		})
		if err != nil {
			return err
		}
		if err := tx.InsertTask(ctx, task); err != nil {
			return err
		}
		if profile == TaskExecutionProfileRealSpecCoding {
			preference, err := newTaskAgentExecutionProfilePreference(task.ID(), 1, agentProfile, request.CommandMeta, now)
			if err != nil {
				return err
			}
			if err := tx.InsertTaskAgentExecutionProfilePreference(ctx, preference); err != nil {
				return err
			}
		}
		if err := tx.InsertTaskRevisionSelection(ctx, selection); err != nil {
			return err
		}
		if err := tx.InsertTechnicalPlanDraft(ctx, draft); err != nil {
			return err
		}
		if profile == TaskExecutionProfileRealSpecCoding {
			if err := tx.InsertSpecCodingIntent(ctx, storecontract.SpecCodingIntent{TaskID: task.ID(), IntentDigest: intent.Digest(), IntentJSON: intent.CanonicalJSON(), CreatedAt: now}); err != nil {
				return err
			}
			if resources := intent.Resources(); resources != nil {
				b, digest, err := resources.CanonicalJSON()
				if err != nil {
					return err
				}
				if err = tx.InsertTaskResourceSnapshot(ctx, storecontract.TaskResourceRecord{TaskID: task.ID(), CanonicalJSON: b, Digest: digest, CreatedAt: now}); err != nil {
					return err
				}
			} else if s.deps.TaskWorktrees != nil {
				binding, err := s.planDeclaredTaskWorktree(ctx, task, intent, now)
				if err != nil {
					return err
				}
				if err := tx.InsertTaskWorktreeBinding(ctx, binding); err != nil {
					return err
				}
				plannedWorktree = &binding
			}
		}
		draftWire := wireTechnicalPlanDraft(draft)
		taskWire := wireTask(task)
		response, err = jsonResponse(creationWire{TaskID: task.ID().String(), DraftID: draft.ID().String(), ExecutionProfile: profile, AgentExecutionProfile: agentProfile, Task: &taskWire, Draft: &draftWire})
		if err != nil {
			return err
		}
		if err := tx.SaveCommand(ctx, key, response); err != nil {
			return err
		}
		result = CreateTaskResult{Task: task, Draft: draft, Selection: selection, ExecutionProfile: profile, AgentExecutionProfile: agentProfile, Worktree: plannedWorktree}
		return nil
	})
	if err != nil {
		return result, err
	}
	if result.Worktree != nil {
		binding, ensureErr := s.ensureTaskWorktree(ctx, result.Task.ID())
		result.Worktree = &binding
		if ensureErr != nil && binding.State() != domain.TaskWorktreeRecoveryRequired {
			return result, ensureErr
		}
	}
	return result, nil
}

func (s *Service) SaveTechnicalPlanDraft(ctx context.Context, request SaveTechnicalPlanDraftRequest) (SaveTechnicalPlanDraftResult, error) {
	if err := s.authorize(ctx, request.CommandMeta, "save_technical_plan_draft", request.DraftID.String(), request.ExpectedEditVersion); err != nil {
		return SaveTechnicalPlanDraftResult{}, err
	}
	now := s.deps.Clock.Now()
	key, err := s.commandKey(request.CommandMeta, "save_technical_plan_draft", request.DraftID.String(), request.ExpectedEditVersion, request.Content, now)
	if err != nil {
		return SaveTechnicalPlanDraftResult{}, err
	}
	var result SaveTechnicalPlanDraftResult
	err = s.deps.Store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		response, replayed, err := tx.LookupCommand(ctx, key)
		if err != nil {
			return err
		}
		if replayed {
			var wire draftCommandWire
			if err := json.Unmarshal(response.Body, &wire); err != nil {
				return err
			}
			if wire.Draft == nil {
				return fmt.Errorf("%w: saved Draft replay has no exact response", ErrInvalidCommand)
			}
			result.Draft, err = restoreTechnicalPlanDraftWire(*wire.Draft)
			result.Replayed = err == nil
			return err
		}
		before, err := tx.GetTechnicalPlanDraft(ctx, request.DraftID)
		if err != nil {
			return err
		}
		if before.EditVersion() != request.ExpectedEditVersion {
			return storecontract.ErrVersionConflict
		}
		after, err := before.Edit(request.ExpectedEditVersion, request.Content, now)
		if err != nil {
			return err
		}
		if err := tx.SaveTechnicalPlanDraftCAS(ctx, request.ExpectedEditVersion, after); err != nil {
			return err
		}
		wire := wireTechnicalPlanDraft(after)
		response, err = jsonResponse(draftCommandWire{DraftID: after.ID().String(), Draft: &wire})
		if err != nil {
			return err
		}
		if err := tx.SaveCommand(ctx, key, response); err != nil {
			return err
		}
		result.Draft = after
		return nil
	})
	return result, err
}

func (s *Service) SubmitTechnicalPlanDraft(ctx context.Context, request SubmitTechnicalPlanDraftRequest) (SubmitTechnicalPlanDraftResult, error) {
	if err := s.authorize(ctx, request.CommandMeta, "submit_technical_plan_draft", request.DraftID.String(), request.ExpectedEditVersion); err != nil {
		return SubmitTechnicalPlanDraftResult{}, err
	}
	now := s.deps.Clock.Now()
	key, err := s.commandKey(request.CommandMeta, "submit_technical_plan_draft", request.DraftID.String(), request.ExpectedEditVersion, request.ConfirmUnchanged, now)
	if err != nil {
		return SubmitTechnicalPlanDraftResult{}, err
	}
	var result SubmitTechnicalPlanDraftResult
	err = s.deps.Store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		response, replayed, err := tx.LookupCommand(ctx, key)
		if err != nil {
			return err
		}
		if replayed {
			var wire draftCommandWire
			if err := json.Unmarshal(response.Body, &wire); err != nil {
				return err
			}
			revisionID, err := domain.ParseTechnicalPlanRevisionID(wire.RevisionID)
			if err != nil {
				return err
			}
			if wire.Draft != nil {
				result.Draft, err = restoreTechnicalPlanDraftWire(*wire.Draft)
			} else {
				result.Draft, err = tx.GetTechnicalPlanDraft(ctx, request.DraftID)
			}
			if err != nil {
				return err
			}
			result.Revision, err = tx.GetTechnicalPlanRevision(ctx, revisionID)
			result.Replayed = err == nil
			return err
		}
		before, err := tx.GetTechnicalPlanDraft(ctx, request.DraftID)
		if err != nil {
			return err
		}
		if before.EditVersion() != request.ExpectedEditVersion {
			return storecontract.ErrVersionConflict
		}
		closed, revision, err := before.Submit(request.ExpectedEditVersion, s.deps.IDs.TechnicalPlanRevisionID(), request.ConfirmUnchanged, now)
		if err != nil {
			return err
		}
		if err := tx.SubmitTechnicalPlanDraftCAS(ctx, request.ExpectedEditVersion, closed, revision); err != nil {
			return err
		}
		wire := wireTechnicalPlanDraft(closed)
		response, err = jsonResponse(draftCommandWire{DraftID: closed.ID().String(), RevisionID: revision.ID().String(), Draft: &wire})
		if err != nil {
			return err
		}
		if err := tx.SaveCommand(ctx, key, response); err != nil {
			return err
		}
		result = SubmitTechnicalPlanDraftResult{Draft: closed, Revision: revision}
		return nil
	})
	return result, err
}

func (s *Service) ReviewTechnicalPlanRevision(ctx context.Context, request ReviewTechnicalPlanRevisionRequest) (ReviewTechnicalPlanRevisionResult, error) {
	action := "accept_technical_plan_revision"
	if request.Kind == domain.TechnicalPlanReviewRequestRevision {
		action = "request_technical_plan_revision"
	} else if request.Kind != domain.TechnicalPlanReviewAccept {
		return ReviewTechnicalPlanRevisionResult{}, fmt.Errorf("%w: invalid technical plan review kind", ErrInvalidCommand)
	}
	if err := s.authorize(ctx, request.CommandMeta, action, request.RevisionID.String(), 0); err != nil {
		return ReviewTechnicalPlanRevisionResult{}, err
	}
	now := s.deps.Clock.Now()
	key, err := s.commandKey(request.CommandMeta, action, request.RevisionID.String(), 0, request.Note, now)
	if err != nil {
		return ReviewTechnicalPlanRevisionResult{}, err
	}
	var result ReviewTechnicalPlanRevisionResult
	err = s.deps.Store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		response, replayed, err := tx.LookupCommand(ctx, key)
		if err != nil {
			return err
		}
		if replayed {
			return s.replayPlanningReview(ctx, tx, response, &result)
		}
		revision, err := tx.GetTechnicalPlanRevision(ctx, request.RevisionID)
		if err != nil {
			return err
		}
		review, err := domain.NewTechnicalPlanReview(domain.NewTechnicalPlanReviewParams{
			ID: s.deps.IDs.TechnicalPlanReviewID(), RevisionID: revision.ID(), TaskID: revision.TaskID(), Kind: request.Kind,
			Reviewer: request.ActorID, Note: request.Note, DecidedAt: now,
		})
		if err != nil {
			return err
		}
		wire := planningReviewWire{ReviewID: review.ID().String(), TaskID: revision.TaskID().String(), RevisionID: revision.ID().String()}
		result.Review = review
		if request.Kind == domain.TechnicalPlanReviewRequestRevision {
			draft, err := domain.NewTechnicalPlanSuccessorDraft(s.deps.IDs.TechnicalPlanDraftID(), revision, now)
			if err != nil {
				return err
			}
			if err := tx.InsertTechnicalPlanReviewCAS(ctx, review); err != nil {
				return err
			}
			if err := tx.InsertTechnicalPlanDraft(ctx, draft); err != nil {
				return err
			}
			wire.DraftID = draft.ID().String()
			draftWire := wireTechnicalPlanDraft(draft)
			wire.Draft = &draftWire
			result.Draft = &draft
		} else {
			binding, charter, snapshot, err := s.acceptTechnicalPlanRevision(ctx, tx, revision, review, now)
			if err != nil {
				return err
			}
			wire.SnapshotID, wire.CharterID, wire.SnapshotDigest, wire.BoundAt = snapshot.ID().String(), charter.ID().String(), binding.SnapshotDigest(), binding.BoundAt()
			result.Binding, result.Charter, result.Snapshot = &binding, &charter, &snapshot
		}
		response, err = jsonResponse(wire)
		if err != nil {
			return err
		}
		return tx.SaveCommand(ctx, key, response)
	})
	return result, err
}

func (s *Service) acceptTechnicalPlanRevision(ctx context.Context, tx storecontract.WriteTx, revision domain.TechnicalPlanRevision, review domain.TechnicalPlanReview, now time.Time) (domain.TechnicalPlanAcceptanceBinding, domain.RunCharter, contextcore.Snapshot, error) {
	task, err := tx.GetTask(ctx, revision.TaskID())
	if err != nil {
		return domain.TechnicalPlanAcceptanceBinding{}, domain.RunCharter{}, contextcore.Snapshot{}, err
	}
	selection, err := tx.GetTaskRevisionSelection(ctx, task.ID())
	if err != nil {
		return domain.TechnicalPlanAcceptanceBinding{}, domain.RunCharter{}, contextcore.Snapshot{}, err
	}
	if revision.SelectionDigest() != selection.Digest() {
		return domain.TechnicalPlanAcceptanceBinding{}, domain.RunCharter{}, contextcore.Snapshot{}, fmt.Errorf("%w: technical plan selection drift", ErrInvalidCommand)
	}
	var charter domain.RunCharter
	var snapshot contextcore.Snapshot
	persistedIntent, intentErr := tx.GetSpecCodingIntent(ctx, task.ID())
	if intentErr == nil {
		charter, snapshot, err = s.acceptRealSpecCodingResources(ctx, tx, task, selection, revision, review, persistedIntent, now)
	} else if errors.Is(intentErr, storecontract.ErrNotFound) {
		charter, snapshot, err = s.acceptanceResources(ctx, tx, task, selection, now)
	} else {
		err = intentErr
	}
	if err != nil {
		return domain.TechnicalPlanAcceptanceBinding{}, domain.RunCharter{}, contextcore.Snapshot{}, err
	}
	binding, err := domain.NewTechnicalPlanAcceptanceBinding(domain.TechnicalPlanAcceptanceBindingRecord{
		TaskID: task.ID(), RevisionID: revision.ID(), ReviewID: review.ID(), SnapshotID: snapshot.ID(), SnapshotDigest: snapshot.Digest(), BoundAt: now,
	})
	if err != nil {
		return domain.TechnicalPlanAcceptanceBinding{}, domain.RunCharter{}, contextcore.Snapshot{}, err
	}
	if err := tx.InsertTechnicalPlanReviewCAS(ctx, review); err != nil {
		return domain.TechnicalPlanAcceptanceBinding{}, domain.RunCharter{}, contextcore.Snapshot{}, err
	}
	if err := tx.InsertTechnicalPlanAcceptance(ctx, binding); err != nil {
		return domain.TechnicalPlanAcceptanceBinding{}, domain.RunCharter{}, contextcore.Snapshot{}, err
	}
	return binding, charter, snapshot, nil
}

func (s *Service) acceptanceResources(ctx context.Context, tx storecontract.WriteTx, task domain.Task, selection domain.TaskRevisionSelection, now time.Time) (domain.RunCharter, contextcore.Snapshot, error) {
	specBinding, err := tx.GetSpecCodingBinding(ctx, task.ID())
	if err == nil {
		if specBinding.Status != storecontract.SpecCodingRegistered {
			return domain.RunCharter{}, contextcore.Snapshot{}, fmt.Errorf("%w: materialized SpecCoding Task is not registered", storecontract.ErrPlanningConflict)
		}
		snapshot, err := tx.GetSnapshot(ctx, specBinding.SnapshotID)
		if err != nil || snapshot.Digest() != specBinding.SnapshotDigest || sha256.Sum256(snapshot.CanonicalJSON()) != specBinding.SnapshotDigest {
			return domain.RunCharter{}, contextcore.Snapshot{}, fmt.Errorf("%w: registered SpecCoding Snapshot drift", ErrInvalidCommand)
		}
		charterID, _, err := specCodingSnapshotBoundary(snapshot)
		if err != nil {
			return domain.RunCharter{}, contextcore.Snapshot{}, err
		}
		charter, err := tx.GetCharter(ctx, charterID)
		if err != nil || charter.AdapterID() != "pi" || charter.TaskID() != task.ID() || !sameRevisionIDs(charter.ContextRevisionIDs(), selection.SelectedRevisionIDs()) || !snapshotMatchesSelection(snapshot, selection) {
			return domain.RunCharter{}, contextcore.Snapshot{}, fmt.Errorf("%w: registered SpecCoding acceptance boundary drift", ErrInvalidCommand)
		}
		return charter, snapshot, nil
	}
	if !errors.Is(err, storecontract.ErrNotFound) {
		return domain.RunCharter{}, contextcore.Snapshot{}, err
	}
	room, err := tx.GetRoom(ctx, task.RoomID())
	if err != nil {
		return domain.RunCharter{}, contextcore.Snapshot{}, err
	}
	charter, err := domain.NewRunCharter(domain.RunCharterParams{
		ID: s.deps.IDs.CharterID(), TaskID: task.ID(), TaskGoal: task.Goal(), Criteria: task.Criteria(), ContextRevisionIDs: selection.SelectedRevisionIDs(),
		WorkspaceRoot: room.WorkspaceRoot(), AdapterID: "fake", SandboxMode: "workspace", ExpectedOutput: "reviewable implementation patch",
		ResponsibleHuman: "local-human", CapabilityEnvelope: domain.CapabilityEnvelope{"workspace_write": true}, Initiator: "local-human", CreatedAt: now,
	})
	if err != nil {
		return domain.RunCharter{}, contextcore.Snapshot{}, err
	}
	if err := tx.InsertCharter(ctx, charter); err != nil {
		return domain.RunCharter{}, contextcore.Snapshot{}, err
	}
	snapshot, err := s.deps.Context.Assemble(ctx, tx, contextcore.AssembleRequest{SnapshotID: s.deps.IDs.SnapshotID(), Task: task, Charter: charter, Selection: &selection})
	return charter, snapshot, err
}

func snapshotMatchesSelection(snapshot contextcore.Snapshot, selection domain.TaskRevisionSelection) bool {
	frozen, ok := snapshot.Selection()
	return ok && frozen.Digest() == selection.Digest() && sameRevisionIDs(snapshot.IncludedRevisionIDs(), selection.SelectedRevisionIDs())
}

func (s *Service) replayPlanningReview(ctx context.Context, reader storecontract.Reader, response storecontract.Response, result *ReviewTechnicalPlanRevisionResult) error {
	var wire planningReviewWire
	if err := json.Unmarshal(response.Body, &wire); err != nil {
		return err
	}
	revisionID, err := domain.ParseTechnicalPlanRevisionID(wire.RevisionID)
	if err != nil {
		return err
	}
	result.Review, err = reader.GetTechnicalPlanReview(ctx, revisionID)
	if err != nil || result.Review.ID().String() != wire.ReviewID {
		return err
	}
	if wire.DraftID != "" {
		var draft domain.TechnicalPlanDraft
		if wire.Draft != nil {
			draft, err = restoreTechnicalPlanDraftWire(*wire.Draft)
		} else {
			draftID, parseErr := domain.ParseTechnicalPlanDraftID(wire.DraftID)
			if parseErr != nil {
				return parseErr
			}
			draft, err = reader.GetTechnicalPlanDraft(ctx, draftID)
		}
		if err != nil {
			return err
		}
		result.Draft = &draft
	} else {
		taskID, err := domain.ParseTaskID(wire.TaskID)
		if err != nil {
			return err
		}
		reviewID, err := domain.ParseTechnicalPlanReviewID(wire.ReviewID)
		if err != nil {
			return err
		}
		snapshotID, err := domain.ParseContextSnapshotID(wire.SnapshotID)
		if err != nil {
			return err
		}
		binding, err := domain.NewTechnicalPlanAcceptanceBinding(domain.TechnicalPlanAcceptanceBindingRecord{TaskID: taskID, RevisionID: revisionID, ReviewID: reviewID, SnapshotID: snapshotID, SnapshotDigest: wire.SnapshotDigest, BoundAt: wire.BoundAt})
		if err != nil {
			return err
		}
		charterID, err := domain.ParseCharterID(wire.CharterID)
		if err != nil {
			return err
		}
		charter, err := reader.GetCharter(ctx, charterID)
		if err != nil {
			return err
		}
		snapshot, err := reader.GetSnapshot(ctx, snapshotID)
		if err != nil || snapshot.Digest() != wire.SnapshotDigest {
			return fmt.Errorf("%w: accepted Snapshot drift", ErrInvalidCommand)
		}
		result.Binding, result.Charter, result.Snapshot = &binding, &charter, &snapshot
		if err := s.validateAcceptedRealSpecCoding(ctx, reader, taskID, revisionID, charter, snapshot); err != nil {
			return err
		}
	}
	result.Replayed = true
	return nil
}

func (s *Service) CreateRun(ctx context.Context, request CreateRunRequest) (CreateRunResult, error) {
	if err := s.authorize(ctx, request.CommandMeta, "create_run", request.TaskID.String(), 0); err != nil {
		return CreateRunResult{}, err
	}
	if err := s.ensureExistingTaskWorktree(ctx, request.TaskID); err != nil {
		return CreateRunResult{}, err
	}
	now := s.deps.Clock.Now()
	key, err := s.commandKey(request.CommandMeta, "create_run", request.TaskID.String(), 0, request.RevisionID.String(), now)
	if err != nil {
		return CreateRunResult{}, err
	}
	var result CreateRunResult
	err = s.deps.Store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		response, replayed, err := tx.LookupCommand(ctx, key)
		if err != nil {
			return err
		}
		if replayed {
			var wire runCreationWire
			if err := json.Unmarshal(response.Body, &wire); err != nil {
				return err
			}
			runID, err := domain.ParseRunID(wire.RunID)
			if err != nil {
				return err
			}
			result.Run, err = tx.GetRun(ctx, runID)
			if err != nil {
				return err
			}
			result.Binding, err = tx.GetTechnicalPlanRunBinding(ctx, runID)
			result.Replayed = err == nil
			return err
		}
		task, err := tx.GetTask(ctx, request.TaskID)
		if err != nil {
			return err
		}
		if err := s.requireWorkbenchRoom(ctx, tx, task.RoomID()); err != nil {
			return err
		}
		if task.State() != domain.TaskStateOpen {
			return storecontract.ErrVersionConflict
		}
		history, err := tx.ListTaskRunHistory(ctx, task.RoomID(), task.ID())
		if err != nil {
			return err
		}
		for _, prior := range history {
			if prior.Run.State() == domain.RunStateAccepted {
				return storecontract.ErrVersionConflict
			}
		}
		acceptance, err := tx.GetCurrentTechnicalPlanAcceptance(ctx, request.TaskID)
		if err != nil {
			return err
		}
		if acceptance.RevisionID() != request.RevisionID {
			return storecontract.ErrVersionConflict
		}
		snapshot, err := tx.GetSnapshot(ctx, acceptance.SnapshotID())
		if err != nil || snapshot.Digest() != acceptance.SnapshotDigest() || sha256.Sum256(snapshot.CanonicalJSON()) != acceptance.SnapshotDigest() {
			return fmt.Errorf("%w: accepted Snapshot drift", ErrInvalidCommand)
		}
		charterID, _, err := specCodingSnapshotBoundary(snapshot)
		if err != nil {
			return err
		}
		charter, err := tx.GetCharter(ctx, charterID)
		if err != nil || charter.TaskID() != request.TaskID {
			return fmt.Errorf("%w: accepted Charter drift", ErrInvalidCommand)
		}
		if err := s.validateAcceptedRealSpecCoding(ctx, tx, request.TaskID, request.RevisionID, charter, snapshot); err != nil {
			return err
		}
		run, err := domain.NewAgentRun(s.deps.IDs.RunID(), request.TaskID, charter.ID(), now)
		if err != nil {
			return err
		}
		binding, err := domain.NewTechnicalPlanRunBinding(domain.TechnicalPlanRunBindingRecord{RunID: run.ID(), TaskID: request.TaskID, RevisionID: acceptance.RevisionID(), CharterID: charter.ID(), SnapshotID: snapshot.ID(), SnapshotDigest: snapshot.Digest(), BoundAt: now})
		if err != nil {
			return err
		}
		if err := tx.InsertRun(ctx, run); err != nil {
			return err
		}
		if err := tx.InsertTechnicalPlanRunBinding(ctx, binding); err != nil {
			return err
		}
		if charter.AdapterID() == "pi" {
			payload := automaticRetryEvent{Type: automaticRetryEnabledEvent, RunID: run.ID().String(), MaxRetries: AutomaticRetryMaxRetries}
			if _, err := tx.AppendRunEvent(ctx, run.ID(), storecontract.EventDraft{ID: s.deps.IDs.EventID(), Type: automaticRetryEnabledEvent, Source: "app", OccurredAt: now, RecordedAt: now, NormalizedJSON: responseBody(payload)}); err != nil {
				return err
			}
		}
		response, err = jsonResponse(runCreationWire{RunID: run.ID().String()})
		if err != nil {
			return err
		}
		if err := tx.SaveCommand(ctx, key, response); err != nil {
			return err
		}
		result = CreateRunResult{Run: run, Binding: binding}
		return nil
	})
	return result, err
}

func buildTaskSelection(task domain.Task, requested []domain.ContextRevisionID, available []domain.RoomRevision, now time.Time) (domain.TaskRevisionSelection, error) {
	if len(requested) == 0 {
		return domain.TaskRevisionSelection{}, fmt.Errorf("%w: explicit revision selection required", ErrInvalidCommand)
	}
	byID := make(map[domain.ContextRevisionID]domain.RoomRevision, len(available))
	for _, revision := range available {
		if revision.RoomID() != task.RoomID() {
			return domain.TaskRevisionSelection{}, fmt.Errorf("%w: room revision mismatch", ErrInvalidCommand)
		}
		byID[revision.ID()] = revision
	}
	selected := make([]domain.RevisionSelectionItem, 0, len(requested))
	selectedSet := make(map[domain.ContextRevisionID]struct{}, len(requested))
	for _, id := range requested {
		revision, found := byID[id]
		if !found {
			return domain.TaskRevisionSelection{}, fmt.Errorf("%w: selected revision is unavailable", ErrInvalidCommand)
		}
		if _, duplicate := selectedSet[id]; duplicate {
			return domain.TaskRevisionSelection{}, fmt.Errorf("%w: duplicate selected revision", ErrInvalidCommand)
		}
		selectedSet[id] = struct{}{}
		selected = append(selected, domain.RevisionSelectionItem{RevisionID: id, Digest: revision.Digest(), Provenance: revision.Provenance()})
	}
	excluded := make([]domain.RevisionExclusion, 0, len(available)-len(selected))
	for _, revision := range available {
		if _, chosen := selectedSet[revision.ID()]; chosen {
			continue
		}
		excluded = append(excluded, domain.RevisionExclusion{RevisionSelectionItem: domain.RevisionSelectionItem{RevisionID: revision.ID(), Digest: revision.Digest(), Provenance: revision.Provenance()}, Reason: "explicitly_not_selected"})
	}
	return domain.NewTaskRevisionSelection(task.ID(), task.RoomID(), selected, excluded, now)
}

func (s *Service) planDeclaredTaskWorktree(ctx context.Context, task domain.Task, intent speccoding.DeclaredUserTask, now time.Time) (domain.TaskWorktreeBinding, error) {
	if resolver, ok := s.deps.TaskWorktrees.(interface {
		PlanForRevision(context.Context, domain.Task, string, time.Time) (domain.TaskWorktreeBinding, error)
	}); ok {
		return resolver.PlanForRevision(ctx, task, intent.Repository().SourceRevision, now)
	}
	return s.deps.TaskWorktrees.Plan(ctx, task, now)
}

// Workbench cannot reinterpret an unowned or legacy Room as project work.
func (s *Service) requireWorkbenchRoom(ctx context.Context, reader storecontract.Reader, roomID domain.RoomID) error {
	if s.deps.RoomCreationMode != RoomCreationProjectOnly {
		return nil
	}
	room, err := reader.GetRoom(ctx, roomID)
	if err != nil {
		return err
	}
	if room.OwnershipKind() != domain.RoomOwnershipProject || !room.ProjectID().Valid() {
		return fmt.Errorf("%w: Room has no qualified Project ownership", ErrInvalidCommand)
	}
	project, err := reader.GetProject(ctx, room.ProjectID())
	if err != nil {
		return err
	}
	if project.State() != domain.ProjectStateActive || room.State() != domain.RoomStateActive {
		return storecontract.ErrRoomStateForbidden
	}

	return nil
}
