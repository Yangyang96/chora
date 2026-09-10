package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Yangyang96/chora/internal/contextcore"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/speccoding"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

func normalizedTaskExecutionProfile(profile TaskExecutionProfile) (TaskExecutionProfile, error) {
	if profile == "" {
		return TaskExecutionProfileDiagnosticFake, nil
	}
	switch profile {
	case TaskExecutionProfileDiagnosticFake, TaskExecutionProfileRealSpecCoding:
		return profile, nil
	default:
		return "", fmt.Errorf("%w: unsupported Task execution profile %q", ErrInvalidCommand, profile)
	}
}

func validateProfileInput(request CreateTaskRequest, profile TaskExecutionProfile) error {
	switch profile {
	case TaskExecutionProfileDiagnosticFake:
		if request.RealSpecCoding != nil {
			return fmt.Errorf("%w: diagnostic Fake Task cannot carry Real Spec Coding input", ErrInvalidCommand)
		}
	case TaskExecutionProfileRealSpecCoding:
		if request.RealSpecCoding == nil {
			return fmt.Errorf("%w: Real Spec Coding input is required", speccoding.ErrUnsupportedUserTask)
		}
		if request.Goal != "" || len(request.Criteria) != 0 || !emptyTechnicalPlanContent(request.PlanContent) {
			return fmt.Errorf("%w: Real Task authority must use the Real Spec Coding declaration", speccoding.ErrUnsupportedUserTask)
		}
	}
	return nil
}

func emptyTechnicalPlanContent(content domain.TechnicalPlanContent) bool {
	return len(content.TechnicalSteps) == 0 && len(content.Decisions) == 0 && len(content.Risks) == 0 && len(content.Unknowns) == 0
}

func (s *Service) declareRealSpecCodingTask(ctx context.Context, room domain.Room, request CreateTaskRequest) (domain.Task, speccoding.DeclaredUserTask, error) {
	input := request.RealSpecCoding
	criteria := make([]domain.AcceptanceCriterion, 0, len(input.Criteria))
	declaredCriteria := make([]speccoding.UserAcceptanceCriterion, 0, len(input.Criteria))
	for _, value := range input.Criteria {
		id := domain.NewCriterionID()
		criterion, err := domain.NewAcceptanceCriterion(id, value.Title, value.Description)
		if err != nil {
			return domain.Task{}, speccoding.DeclaredUserTask{}, fmt.Errorf("%w: invalid acceptance criterion", speccoding.ErrUnsupportedUserTask)
		}
		criteria = append(criteria, criterion)
		declaredCriteria = append(declaredCriteria, speccoding.UserAcceptanceCriterion{
			ID: id, Title: value.Title, Description: value.Description,
			VerificationCommandIndexes: append([]int(nil), value.VerificationCommandIndexes...),
		})
	}
	task, err := domain.NewTask(s.deps.IDs.TaskID(), request.RoomID, request.Title, input.Requirement, criteria)
	if err != nil {
		return domain.Task{}, speccoding.DeclaredUserTask{}, fmt.Errorf("%w: invalid Task declaration", speccoding.ErrUnsupportedUserTask)
	}
	var envelope SpecCodingEnvelope
	var resources *domain.TaskResourceSnapshot
	if input.Resources != nil {
		b, _ := json.Marshal(input.Resources)
		var snapshot domain.TaskResourceSnapshot
		if err = json.Unmarshal(b, &snapshot); err != nil {
			return domain.Task{}, speccoding.DeclaredUserTask{}, err
		}
		snapshot.RoomID = room.ID().String()
		snapshot.ProjectID = room.ProjectID().String()
		snapshot, err = BindTaskResourceIdentity(snapshot, task.ID(), task.Title())
		if err != nil {
			return domain.Task{}, speccoding.DeclaredUserTask{}, err
		}
		resources = &snapshot
		envelope, err = s.resolveResourceSpecCodingEnvelope(ctx, room, snapshot)
	} else {
		envelope, err = s.resolveSpecCodingEnvelope(ctx, room)
	}
	if err != nil {
		return domain.Task{}, speccoding.DeclaredUserTask{}, err
	}
	commands := make([]speccoding.UserVerificationCommand, len(input.VerificationCommands))
	for index, command := range input.VerificationCommands {
		commands[index] = speccoding.UserVerificationCommand{Argv: append([]string(nil), command.Argv...), WorkingDirectory: command.WorkingDirectory}
	}
	intent, err := envelope.Declare(speccoding.UserTaskDeclaration{
		Resources:  resources,
		ContractID: "user-" + task.ID().String(), TaskID: task.ID(), RoomID: room.ID(), WorkspaceRoot: room.WorkspaceRoot(),
		Title: task.Title(), Requirement: input.Requirement, Constraints: append([]string(nil), input.Constraints...),
		OutOfScope: append([]string(nil), input.OutOfScope...), Criteria: declaredCriteria,
		WritableFiles: append([]string(nil), input.WritableFiles...), WritableDirectories: append([]string(nil), input.WritableDirectories...), NoChecks: input.NoChecks, VerificationCommands: commands,
	})
	if err != nil {
		return domain.Task{}, speccoding.DeclaredUserTask{}, err
	}
	return task, intent, nil
}

func (s *Service) restoreRealSpecCodingIntent(ctx context.Context, reader storecontract.Reader, taskID domain.TaskID) (speccoding.DeclaredUserTask, error) {
	persisted, err := reader.GetSpecCodingIntent(ctx, taskID)
	if err != nil {
		return speccoding.DeclaredUserTask{}, err
	}
	task, err := reader.GetTask(ctx, taskID)
	if err != nil {
		return speccoding.DeclaredUserTask{}, err
	}
	room, err := reader.GetRoom(ctx, task.RoomID())
	if err != nil {
		return speccoding.DeclaredUserTask{}, err
	}
	envelope, err := s.resolveTaskSpecCodingEnvelope(ctx, reader, room, taskID)
	if err != nil {
		return speccoding.DeclaredUserTask{}, err
	}
	return s.restorePersistedRealSpecCodingIntent(ctx, room, envelope, persisted, taskID)
}

func (s *Service) restorePersistedRealSpecCodingIntent(ctx context.Context, room domain.Room, envelope SpecCodingEnvelope, persisted storecontract.SpecCodingIntent, taskID domain.TaskID) (speccoding.DeclaredUserTask, error) {
	restored, err := envelope.RestoreDeclaredUserTask(persisted.IntentJSON)
	if err != nil || persisted.TaskID != taskID || restored.TaskID() != taskID || restored.Digest() != persisted.IntentDigest {
		if err == nil {
			err = errors.New("intent identity or digest drift")
		}
		return speccoding.DeclaredUserTask{}, fmt.Errorf("%w: persisted Real Spec Coding intent: %v", storecontract.ErrSpecCodingConflict, err)
	}
	if !envelope.MatchesRepositoryRoot(room.WorkspaceRoot()) || restored.RoomID() != room.ID() {
		return speccoding.DeclaredUserTask{}, fmt.Errorf("%w: persisted Real Spec Coding Room authority drift", storecontract.ErrSpecCodingConflict)
	}
	return restored, nil
}

func (s *Service) acceptRealSpecCodingResources(
	ctx context.Context,
	tx storecontract.WriteTx,
	task domain.Task,
	selection domain.TaskRevisionSelection,
	revision domain.TechnicalPlanRevision,
	review domain.TechnicalPlanReview,
	persisted storecontract.SpecCodingIntent,
	now time.Time,
) (domain.RunCharter, contextcore.Snapshot, error) {
	room, err := tx.GetRoom(ctx, task.RoomID())
	if err != nil {
		return domain.RunCharter{}, contextcore.Snapshot{}, err
	}
	envelope, err := s.resolveTaskSpecCodingEnvelope(ctx, tx, room, task.ID())
	if err != nil {
		return domain.RunCharter{}, contextcore.Snapshot{}, err
	}
	intent, err := s.restorePersistedRealSpecCodingIntent(ctx, room, envelope, persisted, task.ID())
	if err != nil {
		return domain.RunCharter{}, contextcore.Snapshot{}, err
	}
	if room.State() != domain.RoomStateActive || intent.RoomID() != room.ID() || !envelope.MatchesRepositoryRoot(room.WorkspaceRoot()) ||
		revision.TaskID() != task.ID() || revision.SelectionDigest() != selection.Digest() {
		return domain.RunCharter{}, contextcore.Snapshot{}, fmt.Errorf("%w: Real Task, Room, selection, or repository authority drift", storecontract.ErrPlanningConflict)
	}
	if !taskMatchesDeclaredIntent(task, intent.CanonicalJSON()) {
		return domain.RunCharter{}, contextcore.Snapshot{}, fmt.Errorf("%w: persisted Real Task authority drift", storecontract.ErrSpecCodingConflict)
	}
	plan, err := intent.PlanFromContent(revision.Content())
	if err != nil {
		return domain.RunCharter{}, contextcore.Snapshot{}, fmt.Errorf("%w: Real Plan authority: %v", storecontract.ErrPlanningConflict, err)
	}

	charterID := s.deps.IDs.CharterID()
	snapshotID := s.deps.IDs.SnapshotID()
	responsibleHuman := review.Reviewer()
	if responsibleHuman == "chora-system" {
		responsibleHuman = "local-human"
	}
	preference, profile, err := taskAgentExecutionProfileBinding(ctx, tx, task.ID())
	if err != nil {
		return domain.RunCharter{}, contextcore.Snapshot{}, err
	}
	if err := requireProfileAcknowledgement(ctx, tx, preference.Profile(), preference.ActorID()); err != nil {
		return domain.RunCharter{}, contextcore.Snapshot{}, err
	}
	expectedOutput := "non-empty patch"
	if envelope.CapabilityEnvelope()[speccoding.LocalConnectedNoSandboxCapability] {
		expectedOutput = "reviewable patch or successful no-change result; report checks separately"
	}
	charter, err := domain.NewRunCharter(domain.RunCharterParams{
		ID: charterID, TaskID: task.ID(), TaskGoal: task.Goal(), Criteria: task.Criteria(), ContextRevisionIDs: selection.SelectedRevisionIDs(),
		WorkspaceRoot: room.WorkspaceRoot(), AdapterID: "pi", SandboxMode: sandboxModeForAgentExecutionProfile(profile), ExpectedOutput: expectedOutput,
		ResponsibleHuman: responsibleHuman, CapabilityEnvelope: envelope.CapabilityEnvelope(), AgentExecutionProfileBinding: profile, Initiator: review.Reviewer(), CreatedAt: now,
	})
	if err != nil {
		return domain.RunCharter{}, contextcore.Snapshot{}, err
	}
	if err := tx.InsertCharter(ctx, charter); err != nil {
		return domain.RunCharter{}, contextcore.Snapshot{}, err
	}
	snapshot, err := s.deps.Context.Assemble(ctx, tx, contextcore.AssembleRequest{SnapshotID: snapshotID, Task: task, Charter: charter, Selection: &selection})
	if err != nil {
		return domain.RunCharter{}, contextcore.Snapshot{}, err
	}
	snapshotDigest := snapshot.Digest()
	contract, err := envelope.Accept(intent, plan, snapshot.ID(), hex.EncodeToString(snapshotDigest[:]))
	if err != nil {
		return domain.RunCharter{}, contextcore.Snapshot{}, err
	}
	document := contract.Document()
	if !taskMatchesContract(task, document.Task, document.Acceptance) || document.Task.RoomID != room.ID().String() ||
		document.Execution.Input.ContextSnapshotID != snapshot.ID().String() || document.Execution.Input.ContextSnapshotDigest != hex.EncodeToString(snapshotDigest[:]) ||
		!sameRevisionIDs(charter.ContextRevisionIDs(), selection.SelectedRevisionIDs()) || !snapshotMatchesSelection(snapshot, selection) {
		return domain.RunCharter{}, contextcore.Snapshot{}, fmt.Errorf("%w: materialized Real Spec Coding authority drift", storecontract.ErrSpecCodingConflict)
	}
	canonical := contract.CanonicalJSON()
	contractDigest := sha256.Sum256(canonical)
	binding := storecontract.SpecCodingBinding{
		TaskID: task.ID(), SnapshotID: snapshot.ID(), SnapshotDigest: snapshotDigest,
		MaterializedContractDigest: contractDigest, MaterializedContractJSON: canonical,
		Status: storecontract.SpecCodingMaterialized, MaterializedAt: now,
	}
	if err := tx.InsertSpecCodingBinding(ctx, binding); err != nil {
		return domain.RunCharter{}, contextcore.Snapshot{}, err
	}
	binding.Status = storecontract.SpecCodingRegistered
	binding.ActiveContractDigest = contractDigest
	binding.ActiveContractJSON = append([]byte(nil), canonical...)
	binding.RegisteredAt = now
	if err := tx.RegisterSpecCodingBinding(ctx, binding); err != nil {
		return domain.RunCharter{}, contextcore.Snapshot{}, err
	}
	return charter, snapshot, nil
}

func taskMatchesDeclaredIntent(task domain.Task, canonical []byte) bool {
	var declared struct {
		TaskID      string `json:"task_id"`
		RoomID      string `json:"room_id"`
		Title       string `json:"title"`
		Requirement string `json:"requirement"`
		Criteria    []struct {
			ID          string `json:"id"`
			Title       string `json:"title"`
			Description string `json:"description"`
		} `json:"criteria"`
	}
	if err := json.Unmarshal(canonical, &declared); err != nil || task.ID().String() != declared.TaskID || task.RoomID().String() != declared.RoomID ||
		task.Title() != declared.Title || task.Goal() != declared.Requirement || len(task.Criteria()) != len(declared.Criteria) {
		return false
	}
	for index, criterion := range task.Criteria() {
		frozen := declared.Criteria[index]
		if criterion.ID().String() != frozen.ID || criterion.Title() != frozen.Title || criterion.Description() != frozen.Description {
			return false
		}
	}
	return true
}

func (s *Service) validateAcceptedRealSpecCoding(ctx context.Context, reader storecontract.Reader, taskID domain.TaskID, revisionID domain.TechnicalPlanRevisionID, charter domain.RunCharter, snapshot contextcore.Snapshot) error {
	persisted, err := reader.GetSpecCodingIntent(ctx, taskID)
	if errors.Is(err, storecontract.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	task, err := reader.GetTask(ctx, taskID)
	if err != nil {
		return err
	}
	room, err := reader.GetRoom(ctx, task.RoomID())
	if err != nil {
		return err
	}
	envelope, err := s.resolveTaskSpecCodingEnvelope(ctx, reader, room, taskID)
	if err != nil {
		return err
	}
	intent, err := s.restorePersistedRealSpecCodingIntent(ctx, room, envelope, persisted, taskID)
	if err != nil {
		return err
	}
	if !taskMatchesDeclaredIntent(task, intent.CanonicalJSON()) {
		return fmt.Errorf("%w: accepted Real Task authority drift", storecontract.ErrSpecCodingConflict)
	}
	selection, err := reader.GetTaskRevisionSelection(ctx, taskID)
	if err != nil {
		return err
	}
	revision, err := reader.GetTechnicalPlanRevision(ctx, revisionID)
	if err != nil || revision.TaskID() != taskID || revision.SelectionDigest() != selection.Digest() {
		return fmt.Errorf("%w: accepted Real Plan or selection drift", storecontract.ErrPlanningConflict)
	}
	plan, err := intent.PlanFromContent(revision.Content())
	if err != nil {
		return fmt.Errorf("%w: accepted Real Plan authority: %v", storecontract.ErrPlanningConflict, err)
	}
	profile := charter.AgentExecutionProfileBinding()
	restoredProfile, err := domain.RestoreAgentExecutionProfileBinding(profile.Record())
	if err != nil || restoredProfile != profile || !profile.Bound() {
		return fmt.Errorf("%w: accepted Agent execution profile binding drift", storecontract.ErrSpecCodingConflict)
	}
	if room.State() != domain.RoomStateActive || intent.RoomID() != room.ID() || !envelope.MatchesRepositoryRoot(room.WorkspaceRoot()) ||
		charter.TaskID() != taskID || charter.AdapterID() != "pi" || charter.SandboxMode() != sandboxModeForAgentExecutionProfile(profile) || charter.WorkspaceRoot() != room.WorkspaceRoot() ||
		!sameCapabilities(charter.CapabilityEnvelope(), envelope.CapabilityEnvelope()) || !snapshotMatchesSelection(snapshot, selection) {
		return fmt.Errorf("%w: accepted Real execution authority drift", storecontract.ErrSpecCodingConflict)
	}
	snapshotDigest := snapshot.Digest()
	expected, err := envelope.Accept(intent, plan, snapshot.ID(), hex.EncodeToString(snapshotDigest[:]))
	if err != nil {
		return err
	}
	binding, err := reader.GetSpecCodingBinding(ctx, taskID)
	if err != nil || binding.Status != storecontract.SpecCodingRegistered || binding.SnapshotID != snapshot.ID() || binding.SnapshotDigest != snapshotDigest ||
		binding.MaterializedContractDigest != binding.ActiveContractDigest || sha256.Sum256(binding.ActiveContractJSON) != binding.ActiveContractDigest ||
		!bytes.Equal(binding.MaterializedContractJSON, binding.ActiveContractJSON) || !bytes.Equal(binding.ActiveContractJSON, expected.CanonicalJSON()) {
		return fmt.Errorf("%w: accepted Real contract binding drift", storecontract.ErrSpecCodingConflict)
	}
	return nil
}

func sameCapabilities(left, right domain.CapabilityEnvelope) bool {
	if len(left) != len(right) {
		return false
	}
	for name, allowed := range left {
		if right[name] != allowed {
			return false
		}
	}
	return true
}
