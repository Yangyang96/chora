package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/Yangyang96/chora/internal/contextcore"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/speccoding"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

type specCodingResponse struct {
	TaskID  string `json:"task_id"`
	DraftID string `json:"draft_id"`
}

func (s *Service) MaterializeSpecCodingContract(ctx context.Context, request MaterializeSpecCodingContractRequest) (MaterializeSpecCodingContractResult, error) {
	if request.AgentAdapter == "" {
		request.AgentAdapter = "pi"
	}
	agentProfile, err := agentExecutionProfileForAdapter(request.AgentAdapter, request.AgentExecutionProfile)
	if err != nil {
		return MaterializeSpecCodingContractResult{}, err
	}
	document, taskID, roomID, snapshotID, contractDigest, err := validateMaterializationRequest(request)
	if err != nil {
		return MaterializeSpecCodingContractResult{}, err
	}
	if s == nil || s.deps.Store == nil {
		return MaterializeSpecCodingContractResult{}, fmt.Errorf("%w: missing store", ErrInvalidCommand)
	}
	if s.deps.RoomCreationMode == RoomCreationProjectOnly {
		return MaterializeSpecCodingContractResult{}, fmt.Errorf("%w: legacy standalone materialization is unavailable in Workbench", ErrInvalidCommand)
	}
	if err := s.authorize(ctx, request.CommandMeta, "materialize_spec_coding_contract", taskID.String(), 0); err != nil {
		return MaterializeSpecCodingContractResult{}, err
	}
	now := s.deps.Clock.Now()
	key, err := s.commandKey(request.CommandMeta, "materialize_spec_coding_contract", taskID.String(), 0, struct {
		ContractDigest, WorkspaceRoot, ContextEntryID, ContextRevisionID, CharterID, AgentAdapter string
		AgentExecutionProfile                                                                     domain.AgentExecutionProfile
		FrozenAt                                                                                  time.Time
	}{request.Contract.DigestHex(), request.WorkspaceRoot, request.ContextEntryID.String(), request.ContextRevisionID.String(), request.CharterID.String(), request.AgentAdapter, agentProfile, request.FrozenAt}, now)
	if err != nil {
		return MaterializeSpecCodingContractResult{}, err
	}
	var result MaterializeSpecCodingContractResult
	err = s.deps.Store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		response, replayed, err := tx.LookupCommand(ctx, key)
		if err != nil {
			return err
		}
		if replayed {
			var wire specCodingResponse
			if err := json.Unmarshal(response.Body, &wire); err != nil || wire.TaskID != taskID.String() {
				return invalidSpecCoding("invalid materialization replay")
			}
			draftID, parseErr := domain.ParseTechnicalPlanDraftID(wire.DraftID)
			if parseErr != nil {
				return invalidSpecCoding("invalid materialization replay Draft")
			}
			result, err = loadMaterializedSpecCoding(ctx, tx, taskID, draftID, contractDigest)
			if err == nil && request.AgentAdapter == "pi" {
				preference, _, profileErr := taskAgentExecutionProfileBinding(ctx, tx, taskID)
				if profileErr != nil || preference.Profile() != agentProfile {
					return invalidSpecCoding("materialized Agent execution profile drift")
				}
			}
			result.Replayed = err == nil
			return err
		}
		if err := requireProfileAcknowledgement(ctx, tx, agentProfile, request.ActorID); err != nil {
			return err
		}

		criteria, err := contractCriteria(document.Acceptance.Criteria)
		if err != nil {
			return err
		}
		room, err := domain.NewRoom(domain.RoomParams{ID: roomID, OwnershipKind: domain.RoomOwnershipLegacyStandalone, Name: document.Task.Repository.Name, Description: document.Requirement.Statement, WorkspaceRoot: request.WorkspaceRoot, CreatedAt: request.FrozenAt, UpdatedAt: request.FrozenAt})
		if err != nil {
			return err
		}
		task, err := domain.NewTask(taskID, roomID, document.Task.Title, document.Task.Goal, criteria)
		if err != nil {
			return err
		}
		revision, err := domain.NewRoomContextRevision(domain.RoomContextRevisionParams{
			EntryID: request.ContextEntryID, RevisionID: request.ContextRevisionID, RoomID: roomID, Kind: domain.ContextKindBrief, RevisionNumber: 1,
			Title: "Spec Coding Contract", Body: string(request.Contract.CanonicalJSON()), Locator: "spec-coding://" + document.ContractID + "/v4", CreatedAt: request.FrozenAt, UpdatedAt: request.FrozenAt,
		})
		if err != nil {
			return err
		}
		roomRevision, err := domain.NewRoomRevision(revision, domain.HumanRoomProvenance(request.ActorID), request.FrozenAt)
		if err != nil {
			return err
		}
		selection, err := domain.NewTaskRevisionSelection(taskID, roomID, []domain.RevisionSelectionItem{{RevisionID: revision.RevisionID(), Digest: roomRevision.Digest(), Provenance: roomRevision.Provenance()}}, nil, request.FrozenAt)
		if err != nil {
			return err
		}
		draft, err := contractTaskPlan(s.deps.IDs.TechnicalPlanDraftID(), task, selection.Digest(), document, request.FrozenAt)
		if err != nil {
			return err
		}
		charter, err := contractCharter(request, task, document, agentProfile)
		if err != nil {
			return err
		}
		if err := tx.InsertRoom(ctx, room); err != nil {
			return err
		}
		if err := tx.InsertTask(ctx, task); err != nil {
			return err
		}
		if request.AgentAdapter == "pi" {
			preference, err := newTaskAgentExecutionProfilePreference(task.ID(), 1, agentProfile, request.CommandMeta, request.FrozenAt)
			if err != nil {
				return err
			}
			if err := tx.InsertTaskAgentExecutionProfilePreference(ctx, preference); err != nil {
				return err
			}
		}
		if err := tx.InsertTechnicalPlanDraft(ctx, draft); err != nil {
			return err
		}
		if err := tx.InsertRevision(ctx, revision); err != nil {
			return err
		}
		if err := tx.InsertRoomRevision(ctx, roomRevision); err != nil {
			return err
		}
		if err := tx.InsertTaskRevisionSelection(ctx, selection); err != nil {
			return err
		}
		if err := tx.InsertCharter(ctx, charter); err != nil {
			return err
		}
		snapshot, err := s.deps.Context.Assemble(ctx, tx, contextcore.AssembleRequest{SnapshotID: snapshotID, Task: task, Charter: charter, Selection: &selection})
		if err != nil {
			return err
		}
		binding := storecontract.SpecCodingBinding{TaskID: taskID, SnapshotID: snapshotID, SnapshotDigest: snapshot.Digest(), MaterializedContractDigest: contractDigest, MaterializedContractJSON: request.Contract.CanonicalJSON(), Status: storecontract.SpecCodingMaterialized, MaterializedAt: request.FrozenAt}
		if err := tx.InsertSpecCodingBinding(ctx, binding); err != nil {
			return err
		}
		response, err = jsonResponse(specCodingResponse{TaskID: taskID.String(), DraftID: draft.ID().String()})
		if err != nil {
			return err
		}
		if err := tx.SaveCommand(ctx, key, response); err != nil {
			return err
		}
		result = MaterializeSpecCodingContractResult{Task: task, Draft: draft, Snapshot: snapshot, Binding: binding}
		return nil
	})
	return result, err
}

func (s *Service) RegisterSpecCodingContract(ctx context.Context, request RegisterSpecCodingContractRequest) (RegisterSpecCodingContractResult, error) {
	if s == nil || s.deps.Store == nil || request.Contract.DigestHex() == "" {
		return RegisterSpecCodingContractResult{}, invalidSpecCoding("missing contract or store")
	}
	document := request.Contract.Document()
	if !((document.SchemaVersion == speccoding.CoreContractSchemaVersionV5 && document.Revision == 5) ||
		(document.SchemaVersion == speccoding.CoreContractSchemaVersionV6 && document.Revision == 6) ||
		(document.SchemaVersion == speccoding.CoreContractSchemaVersionV7 && document.Revision == 7) ||
		(document.SchemaVersion == speccoding.CoreContractSchemaVersionV8 && document.Revision == 8) ||
		(document.SchemaVersion == speccoding.CoreContractSchemaVersionV9 && document.Revision == 9) ||
		(document.SchemaVersion == speccoding.CoreContractSchemaVersionV10 && document.Revision == 10)) {
		return RegisterSpecCodingContractResult{}, invalidSpecCoding("registration requires v5 through v10")
	}
	taskID, err := domain.ParseTaskID(document.Task.ID)
	if err != nil {
		return RegisterSpecCodingContractResult{}, invalidSpecCoding("invalid Task identity")
	}
	if err := s.authorize(ctx, request.CommandMeta, "register_spec_coding_contract", taskID.String(), 0); err != nil {
		return RegisterSpecCodingContractResult{}, err
	}
	now := s.deps.Clock.Now()
	key, err := s.commandKey(request.CommandMeta, "register_spec_coding_contract", taskID.String(), 0, request.Contract.DigestHex(), now)
	if err != nil {
		return RegisterSpecCodingContractResult{}, err
	}
	var result RegisterSpecCodingContractResult
	err = s.deps.Store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		response, replayed, err := tx.LookupCommand(ctx, key)
		if err != nil {
			return err
		}
		if replayed {
			binding, err := tx.GetSpecCodingBinding(ctx, taskID)
			if err != nil || binding.Status != storecontract.SpecCodingRegistered || !bytes.Equal(binding.ActiveContractJSON, request.Contract.CanonicalJSON()) {
				return invalidSpecCoding("invalid registration replay")
			}
			result = RegisterSpecCodingContractResult{Binding: binding, Replayed: true}
			return nil
		}
		binding, err := tx.GetSpecCodingBinding(ctx, taskID)
		if err != nil {
			return invalidSpecCoding("materialized binding not found")
		}
		if binding.Status == storecontract.SpecCodingRegistered {
			if !bytes.Equal(binding.ActiveContractJSON, request.Contract.CanonicalJSON()) {
				return invalidSpecCoding("registered contract drift")
			}
			result = RegisterSpecCodingContractResult{Binding: binding, Replayed: true}
			return nil
		}
		if err := verifySpecCodingRegistration(ctx, tx, binding, request.Contract); err != nil {
			return err
		}
		binding.Status = storecontract.SpecCodingRegistered
		binding.ActiveContractJSON = request.Contract.CanonicalJSON()
		binding.ActiveContractDigest = sha256.Sum256(binding.ActiveContractJSON)
		binding.RegisteredAt = now
		if err := tx.RegisterSpecCodingBinding(ctx, binding); err != nil {
			return err
		}
		response, err = jsonResponse(specCodingResponse{TaskID: taskID.String()})
		if err != nil {
			return err
		}
		if err := tx.SaveCommand(ctx, key, response); err != nil {
			return err
		}
		result = RegisterSpecCodingContractResult{Binding: binding}
		return nil
	})
	return result, err
}

func validateMaterializationRequest(request MaterializeSpecCodingContractRequest) (speccoding.CoreContractDocument, domain.TaskID, domain.RoomID, domain.ContextSnapshotID, [32]byte, error) {
	if request.Contract.DigestHex() == "" || request.FrozenAt.IsZero() || !request.ContextEntryID.Valid() || !request.ContextRevisionID.Valid() || !request.CharterID.Valid() ||
		(request.AgentAdapter != "pi" && request.AgentAdapter != "fake") || !filepath.IsAbs(request.WorkspaceRoot) || filepath.Clean(request.WorkspaceRoot) != request.WorkspaceRoot {
		return speccoding.CoreContractDocument{}, domain.TaskID{}, domain.RoomID{}, domain.ContextSnapshotID{}, [32]byte{}, invalidSpecCoding("invalid materialization request")
	}
	document := request.Contract.Document()
	if document.SchemaVersion != speccoding.CoreContractSchemaVersionV4 || document.Revision != 4 || document.Execution.Input.ContextSnapshotDigest != speccoding.SupersededPlaceholderContextDigest {
		return speccoding.CoreContractDocument{}, domain.TaskID{}, domain.RoomID{}, domain.ContextSnapshotID{}, [32]byte{}, invalidSpecCoding("materialization requires placeholder v4")
	}
	taskID, taskErr := domain.ParseTaskID(document.Task.ID)
	roomID, roomErr := domain.ParseRoomID(document.Task.RoomID)
	snapshotID, snapshotErr := domain.ParseContextSnapshotID(document.Execution.Input.ContextSnapshotID)
	digestBytes, digestErr := hex.DecodeString(request.Contract.DigestHex())
	if taskErr != nil || roomErr != nil || snapshotErr != nil || digestErr != nil || len(digestBytes) != 32 {
		return speccoding.CoreContractDocument{}, domain.TaskID{}, domain.RoomID{}, domain.ContextSnapshotID{}, [32]byte{}, invalidSpecCoding("invalid fixed identities")
	}
	var digest [32]byte
	copy(digest[:], digestBytes)
	return document, taskID, roomID, snapshotID, digest, nil
}

func contractCriteria(source []speccoding.AcceptanceCriterionContract) ([]domain.AcceptanceCriterion, error) {
	criteria := make([]domain.AcceptanceCriterion, 0, len(source))
	for _, value := range source {
		id, err := domain.ParseCriterionID(value.ID)
		if err != nil {
			return nil, invalidSpecCoding("invalid Acceptance identity")
		}
		criterion, err := domain.NewAcceptanceCriterion(id, value.Title, value.Description)
		if err != nil {
			return nil, err
		}
		criteria = append(criteria, criterion)
	}
	return criteria, nil
}

func contractTaskPlan(id domain.TechnicalPlanDraftID, task domain.Task, selectionDigest [32]byte, document speccoding.CoreContractDocument, at time.Time) (domain.TechnicalPlanDraft, error) {
	steps := []string{document.TechnicalPlan.Summary}
	for _, step := range document.TechnicalPlan.Steps {
		steps = append(steps, step.Description)
	}
	decisions := make([]string, 0, len(document.Decisions))
	for _, value := range document.Decisions {
		decisions = append(decisions, value.Resolution+": "+value.Rationale)
	}
	risks := make([]string, 0, len(document.Risks))
	for _, value := range document.Risks {
		risks = append(risks, value.Description+": "+value.Mitigation)
	}
	unknowns := make([]string, 0, len(document.Unknowns))
	for _, value := range document.Unknowns {
		unknowns = append(unknowns, value.Question+": "+value.ResolutionGate)
	}
	return domain.NewTechnicalPlanDraft(domain.NewTechnicalPlanDraftParams{
		ID: id, TaskID: task.ID(), SelectionDigest: selectionDigest,
		Content: domain.TechnicalPlanContent{TechnicalSteps: steps, Decisions: decisions, Risks: risks, Unknowns: unknowns}, CreatedAt: at,
	})
}

func contractCharter(request MaterializeSpecCodingContractRequest, task domain.Task, document speccoding.CoreContractDocument, profile domain.AgentExecutionProfile) (domain.RunCharter, error) {
	capabilities := make(domain.CapabilityEnvelope, len(document.Execution.RequiredCapabilities))
	for _, capability := range document.Execution.RequiredCapabilities {
		capabilities[capability] = true
	}
	var profileBinding domain.AgentExecutionProfileBinding
	var err error
	sandboxMode := "colima-docker"
	if request.AgentAdapter == "fake" {
		sandboxMode = "production-fake-verification-fixture"
	} else {
		profileBinding, err = domain.NewAgentExecutionProfileBinding(profile)
		if err != nil {
			return domain.RunCharter{}, err
		}
		sandboxMode = sandboxModeForAgentExecutionProfile(profileBinding)
	}
	return domain.NewRunCharter(domain.RunCharterParams{ID: request.CharterID, TaskID: task.ID(), TaskGoal: task.Goal(), Criteria: task.Criteria(), ContextRevisionIDs: []domain.ContextRevisionID{request.ContextRevisionID}, WorkspaceRoot: request.WorkspaceRoot, AdapterID: request.AgentAdapter, SandboxMode: sandboxMode, ExpectedOutput: "non-empty patch", ResponsibleHuman: request.ActorID, CapabilityEnvelope: capabilities, AgentExecutionProfileBinding: profileBinding, Initiator: request.ActorID, CreatedAt: request.FrozenAt})
}

func loadMaterializedSpecCoding(ctx context.Context, reader storecontract.Reader, taskID domain.TaskID, draftID domain.TechnicalPlanDraftID, contractDigest [32]byte) (MaterializeSpecCodingContractResult, error) {
	binding, err := reader.GetSpecCodingBinding(ctx, taskID)
	if err != nil || binding.Status != storecontract.SpecCodingMaterialized && binding.Status != storecontract.SpecCodingRegistered || binding.MaterializedContractDigest != contractDigest {
		return MaterializeSpecCodingContractResult{}, invalidSpecCoding("materialized binding drift")
	}
	task, err := reader.GetTask(ctx, taskID)
	if err != nil {
		return MaterializeSpecCodingContractResult{}, err
	}
	draft, err := reader.GetTechnicalPlanDraft(ctx, draftID)
	if err != nil || draft.TaskID() != taskID {
		return MaterializeSpecCodingContractResult{}, invalidSpecCoding("materialized Draft drift")
	}
	snapshot, err := reader.GetSnapshot(ctx, binding.SnapshotID)
	if err != nil || snapshot.Digest() != binding.SnapshotDigest {
		return MaterializeSpecCodingContractResult{}, invalidSpecCoding("materialized Snapshot drift")
	}
	return MaterializeSpecCodingContractResult{Task: task, Draft: draft, Snapshot: snapshot, Binding: binding}, nil
}

func verifySpecCodingRegistration(ctx context.Context, reader storecontract.Reader, binding storecontract.SpecCodingBinding, candidate speccoding.CoreContract) error {
	if binding.Status != storecontract.SpecCodingMaterialized || sha256.Sum256(binding.MaterializedContractJSON) != binding.MaterializedContractDigest {
		return invalidSpecCoding("invalid materialized bytes")
	}
	materialized, err := speccoding.DecodeCoreContract(binding.MaterializedContractJSON)
	if err != nil || materialized.DigestHex() != hex.EncodeToString(binding.MaterializedContractDigest[:]) {
		return invalidSpecCoding("invalid persisted v4 contract")
	}
	document := materialized.Document()
	if document.SchemaVersion != speccoding.CoreContractSchemaVersionV4 || document.Revision != 4 || document.Execution.Input.ContextSnapshotDigest != speccoding.SupersededPlaceholderContextDigest {
		return invalidSpecCoding("invalid persisted placeholder contract")
	}
	snapshot, err := reader.GetSnapshot(ctx, binding.SnapshotID)
	if err != nil || snapshot.ID().String() != document.Execution.Input.ContextSnapshotID || snapshot.Digest() != binding.SnapshotDigest || sha256.Sum256(snapshot.CanonicalJSON()) != binding.SnapshotDigest {
		return invalidSpecCoding("persisted Snapshot drift")
	}
	candidateDocument := candidate.Document()
	expectedDocument := document
	expectedDocument.SchemaVersion = candidateDocument.SchemaVersion
	expectedDocument.Revision = candidateDocument.Revision
	expectedDocument.Execution.Input.ContextSnapshotDigest = hex.EncodeToString(binding.SnapshotDigest[:])
	if candidateDocument.SchemaVersion == speccoding.CoreContractSchemaVersionV8 || candidateDocument.SchemaVersion == speccoding.CoreContractSchemaVersionV9 || candidateDocument.SchemaVersion == speccoding.CoreContractSchemaVersionV10 {
		expectedDocument.Acceptance.VerificationCommands = append([]speccoding.BoundedCommand(nil), candidateDocument.Acceptance.VerificationCommands...)
		for index := range expectedDocument.Acceptance.Criteria {
			expectedDocument.Acceptance.Criteria[index].VerificationCommandIDs = append([]string(nil), candidateDocument.Acceptance.Criteria[index].VerificationCommandIDs...)
		}
	}
	expected, err := speccoding.NewCoreContract(expectedDocument)
	if err != nil || !bytes.Equal(expected.CanonicalJSON(), candidate.CanonicalJSON()) {
		return invalidSpecCoding("registered contract drift")
	}
	task, err := reader.GetTask(ctx, binding.TaskID)
	if err != nil || !taskMatchesContract(task, document.Task, document.Acceptance) {
		return invalidSpecCoding("persisted Task or Acceptance drift")
	}
	room, err := reader.GetRoom(ctx, task.RoomID())
	if err != nil || room.ID().String() != document.Task.RoomID || room.Name() != document.Task.Repository.Name || room.Description() != document.Requirement.Statement {
		return invalidSpecCoding("persisted Task context drift")
	}
	charterID, workspaceRoot, err := specCodingSnapshotBoundary(snapshot)
	if err != nil || room.WorkspaceRoot() != workspaceRoot {
		return invalidSpecCoding("persisted Workspace drift")
	}
	charter, err := reader.GetCharter(ctx, charterID)
	if err != nil || charter.TaskID() != task.ID() || charter.TaskGoal() != task.Goal() || charter.WorkspaceRoot() != workspaceRoot || !sameCriteria(task.Criteria(), charter.Criteria()) {
		return invalidSpecCoding("persisted Charter drift")
	}
	included := snapshot.IncludedRevisionIDs()
	if len(included) != 1 {
		return invalidSpecCoding("persisted Context selection drift")
	}
	revisions, err := reader.LookupRevisions(ctx, task.RoomID(), included)
	if err != nil || len(revisions) != 1 || revisions[0].Body() != string(materialized.CanonicalJSON()) || revisions[0].Title() != "Spec Coding Contract" {
		return invalidSpecCoding("persisted Context drift")
	}
	selection, ok := snapshot.Selection()
	if !ok || selection.TaskID() != task.ID() || !sameRevisionIDs(selection.SelectedRevisionIDs(), included) {
		return invalidSpecCoding("persisted Context manifest drift")
	}
	return nil
}

func taskMatchesContract(task domain.Task, expected speccoding.TaskContract, acceptance speccoding.AcceptanceContract) bool {
	if task.ID().String() != expected.ID || task.RoomID().String() != expected.RoomID || task.Title() != expected.Title || task.Goal() != expected.Goal || len(task.Criteria()) != len(acceptance.Criteria) {
		return false
	}
	for index, criterion := range task.Criteria() {
		value := acceptance.Criteria[index]
		if criterion.ID().String() != value.ID || criterion.Title() != value.Title || criterion.Description() != value.Description {
			return false
		}
	}
	return true
}

func sameCriteria(left, right []domain.AcceptanceCriterion) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].ID() != right[index].ID() || left[index].Title() != right[index].Title() || left[index].Description() != right[index].Description() {
			return false
		}
	}
	return true
}

func invalidSpecCoding(reason string) error {
	return fmt.Errorf("%w: %s", ErrInvalidCommand, strings.TrimSpace(reason))
}

func specCodingSnapshotBoundary(snapshot contextcore.Snapshot) (domain.CharterID, string, error) {
	var document struct {
		Charter struct {
			ID            string `json:"id"`
			WorkspaceRoot string `json:"workspace_root"`
		} `json:"charter"`
	}
	if err := json.Unmarshal(snapshot.CanonicalJSON(), &document); err != nil || document.Charter.WorkspaceRoot == "" {
		return domain.CharterID{}, "", invalidSpecCoding("invalid Snapshot boundary")
	}
	id, err := domain.ParseCharterID(document.Charter.ID)
	if err != nil {
		return domain.CharterID{}, "", invalidSpecCoding("invalid Snapshot Charter")
	}
	return id, document.Charter.WorkspaceRoot, nil
}
