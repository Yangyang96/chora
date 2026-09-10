package speccoding

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/Yangyang96/chora/internal/agent"
	"github.com/Yangyang96/chora/internal/domain"
)

// ExecutionRepositoryResource is deliberately free of original checkout paths.
type ExecutionRepositoryResource struct {
	RepoID               string                     `json:"repo_id"`
	Name                 string                     `json:"name"`
	Locator              string                     `json:"locator"`
	TargetIdentityDigest string                     `json:"target_identity_digest"`
	Role                 string                     `json:"role"`
	BaseCommit           string                     `json:"base_commit"`
	BaseTree             string                     `json:"base_tree"`
	BaseRef              string                     `json:"base_ref"`
	Scope                domain.TaskRepositoryScope `json:"scope"`
	Checks               domain.TaskCheckPolicy     `json:"checks"`
}
type ResourceEnvelope struct {
	logicalRoot    string
	snapshot       domain.TaskResourceSnapshot
	template       CoreContractDocument
	templateDigest [32]byte
}

func NewResourceEnvelope(logicalRoot string, snapshot domain.TaskResourceSnapshot, piVersion string) (ResourceEnvelope, error) {
	if !filepath.IsAbs(logicalRoot) || filepath.Clean(logicalRoot) != logicalRoot || piVersion == "" {
		return ResourceEnvelope{}, unsupported("resource envelope")
	}
	raw, _, err := snapshot.CanonicalJSON()
	if err != nil {
		return ResourceEnvelope{}, err
	}
	var frozen domain.TaskResourceSnapshot
	_ = json.Unmarshal(raw, &frozen)
	template := CoreContractDocument{SchemaVersion: CoreContractSchemaVersionV12, Revision: 12, Execution: ExecutionContract{Output: OutputContract{ResultSchemaVersion: agent.ResultSchemaVersion, ArtifactLocatorScope: "execution_workspace_relative", CheckBinding: "acceptance_criterion_id", UnknownPolicy: "explicit"}, RequiredCapabilities: cloneStrings(requiredCapabilitiesLocalConnected)}, Candidate: CandidateCombination{Runtime: RuntimeCandidate{Package: "@earendil-works/pi-coding-agent", Version: piVersion, Transport: append([]string{"pi"}, requiredPiArgumentsLocalConnected...)}, Sandbox: SandboxCandidate{Provider: localConnectedSandboxProvider, Engine: localConnectedSandboxEngine}, Model: ModelCandidate{IdentityStatus: localConnectedModelStatus, AuthenticationType: localConnectedModelAuth}}, EntryProbe: EntryProbeContract{PassCriteria: cloneStrings(requiredProbePassCriteriaLocalConnected), FailureClasses: cloneStrings(requiredProbeFailureClassesLocalConnected), StopCriteria: cloneStrings(requiredProbeStopCriteriaLocalConnected), CapsuleChangeBehavior: "MARK_STALE_AND_RESTART", FallbackCandidateRequiresUserApproval: true}}
	b, _ := json.Marshal(template)
	return ResourceEnvelope{logicalRoot: logicalRoot, snapshot: frozen, template: template, templateDigest: sha256.Sum256(b)}, nil
}
func (e ResourceEnvelope) Repository() RepositoryIdentity { return RepositoryIdentity{} }
func (e ResourceEnvelope) CapabilityEnvelope() domain.CapabilityEnvelope {
	out := domain.CapabilityEnvelope{}
	for _, c := range requiredCapabilitiesLocalConnected {
		out[c] = true
	}
	return out
}
func (e ResourceEnvelope) MatchesRepositoryRoot(root string) bool { return root == e.logicalRoot }
func (e ResourceEnvelope) Declare(in UserTaskDeclaration) (DeclaredUserTask, error) {
	if in.Resources == nil || in.TaskID.String() != e.snapshot.TaskID || in.RoomID.String() != e.snapshot.RoomID || in.WorkspaceRoot != e.logicalRoot || !validText(in.Title) || !validText(in.Requirement) || !validText(in.ContractID) || len(in.WritableFiles)+len(in.WritableDirectories)+len(in.VerificationCommands) != 0 || in.NoChecks {
		return DeclaredUserTask{}, unsupported("resource declaration")
	}
	a, ad, err := in.Resources.CanonicalJSON()
	_, ed, eerr := e.snapshot.CanonicalJSON()
	if err != nil || eerr != nil || ad != ed {
		return DeclaredUserTask{}, unsupported("resource snapshot mismatch")
	}
	var snapshot domain.TaskResourceSnapshot
	_ = json.Unmarshal(a, &snapshot)
	criteria := []frozenAcceptanceCriterion{}
	for _, c := range in.Criteria {
		if !c.ID.Valid() || !validText(c.Title) || !validText(c.Description) || len(c.VerificationCommandIndexes) != 0 {
			return DeclaredUserTask{}, unsupported("resource criteria")
		}
		criteria = append(criteria, frozenAcceptanceCriterion{ID: c.ID.String(), Title: c.Title, Description: c.Description, VerificationCommandIndexes: []int{}})
	}
	if len(criteria) == 0 {
		return DeclaredUserTask{}, unsupported("resource criteria")
	}
	f := frozenUserTask{Resources: &snapshot, EnvelopeTemplateDigest: hex.EncodeToString(e.templateDigest[:]), ContractID: in.ContractID, TaskID: in.TaskID.String(), RoomID: in.RoomID.String(), Title: in.Title, Requirement: in.Requirement, Constraints: cloneStrings(in.Constraints), OutOfScope: cloneStrings(in.OutOfScope), Criteria: criteria, WritableFiles: []string{}, VerificationCommands: []frozenVerificationCommand{}}
	b, err := json.Marshal(f)
	if err != nil {
		return DeclaredUserTask{}, err
	}
	return DeclaredUserTask{frozen: f, canonical: b, digest: sha256.Sum256(b), templateDigest: e.templateDigest, initialPlan: deterministicInitialPlan(f)}, nil
}
func (e ResourceEnvelope) RestoreDeclaredUserTask(raw []byte) (DeclaredUserTask, error) {
	var f frozenUserTask
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&f); err != nil {
		return DeclaredUserTask{}, err
	}
	if err := ensureDecoderEOF(d); err != nil {
		return DeclaredUserTask{}, err
	}
	taskID, err := domain.ParseTaskID(f.TaskID)
	if err != nil {
		return DeclaredUserTask{}, err
	}
	roomID, err := domain.ParseRoomID(f.RoomID)
	if err != nil {
		return DeclaredUserTask{}, err
	}
	criteria := []UserAcceptanceCriterion{}
	for _, c := range f.Criteria {
		id, err := domain.ParseCriterionID(c.ID)
		if err != nil {
			return DeclaredUserTask{}, err
		}
		criteria = append(criteria, UserAcceptanceCriterion{ID: id, Title: c.Title, Description: c.Description, VerificationCommandIndexes: c.VerificationCommandIndexes})
	}
	intent, err := e.Declare(UserTaskDeclaration{Resources: f.Resources, ContractID: f.ContractID, TaskID: taskID, RoomID: roomID, WorkspaceRoot: e.logicalRoot, Title: f.Title, Requirement: f.Requirement, Constraints: f.Constraints, OutOfScope: f.OutOfScope, Criteria: criteria, WritableFiles: f.WritableFiles, WritableDirectories: f.WritableDirectories, NoChecks: f.NoChecks})
	if err != nil || !bytes.Equal(intent.canonical, raw) {
		return DeclaredUserTask{}, unsupported("stored resource declaration changed")
	}
	return intent, nil
}
func (e ResourceEnvelope) Accept(intent DeclaredUserTask, plan UserTaskPlan, snapshotID domain.ContextSnapshotID, snapshotDigest string) (CoreContract, error) {
	if !validAcceptIdentity(e.templateDigest, RepositoryIdentity{}, intent, snapshotID, snapshotDigest) || intent.Resources() == nil {
		return CoreContract{}, unsupported("resource acceptance")
	}
	if _, err := e.RestoreDeclaredUserTask(intent.canonical); err != nil {
		return CoreContract{}, err
	}
	f := intent.frozen
	doc := e.template
	doc.ContractID = f.ContractID
	_, digest, _ := e.snapshot.CanonicalJSON()
	doc.Task = TaskContract{ID: f.TaskID, RoomID: f.RoomID, Title: f.Title, Goal: f.Requirement, ResourceSnapshotDigest: hex.EncodeToString(digest[:])}
	for _, r := range e.snapshot.Resources {
		doc.Task.Resources = append(doc.Task.Resources, ExecutionRepositoryResource{RepoID: r.RepoID, Name: r.Name, Locator: r.WorkspaceDirectory(), TargetIdentityDigest: r.PhysicalIdentity, Role: r.Role, BaseCommit: r.BaseCommit, BaseTree: r.BaseTree, BaseRef: r.BaseRef, Scope: r.Scope, Checks: r.Checks})
	}
	doc.Requirement = RequirementContract{Statement: f.Requirement, Constraints: cloneStrings(f.Constraints)}
	doc.Spec = SpecContract{DesiredBehavior: desiredBehavior(f.Criteria), OutOfScope: cloneStrings(f.OutOfScope)}
	for _, c := range f.Criteria {
		doc.Acceptance.Criteria = append(doc.Acceptance.Criteria, AcceptanceCriterionContract{ID: c.ID, Title: c.Title, Description: c.Description, Verification: "Report actual checks under the frozen per-repository policy; missing evidence remains unverified."})
	}
	doc.TechnicalPlan = cloneTechnicalPlan(plan.TechnicalPlan)
	doc.Decisions = cloneDecisions(plan.Decisions)
	doc.Risks = cloneRisks(plan.Risks)
	doc.Unknowns = cloneUnknowns(plan.Unknowns)
	doc.Execution.Input = FrozenInput{ContextSnapshotID: snapshotID.String(), ContextSnapshotDigest: snapshotDigest}
	doc.Execution.Boundary = ExecutionBoundary{WritableFiles: []string{}, Commands: []BoundedCommand{}, TestCommandIDs: []string{}}
	return NewCoreContract(doc)
}
func validateResourceCoreContract(d CoreContractDocument) error {
	bad := func(s string) error { return fmt.Errorf("%w: v12 %s", ErrInvalidCoreContract, s) }
	if d.Revision != 12 || !validText(d.ContractID) || !validText(d.Task.Title) || !validText(d.Task.Goal) || d.Task.Repository != (RepositoryTarget{}) || !validLowerHex(d.Task.ResourceSnapshotDigest, 64) || len(d.Task.Resources) == 0 || len(d.Task.Resources) > domain.TaskRepositoryLimit {
		return bad("identity/resources")
	}
	if _, err := domain.ParseTaskID(d.Task.ID); err != nil {
		return bad("task")
	}
	if _, err := domain.ParseRoomID(d.Task.RoomID); err != nil {
		return bad("Room")
	}
	previous := ""
	writes := map[string]struct{}{}
	for _, r := range d.Task.Resources {
		if _, err := domain.ParseRepositoryID(r.RepoID); err != nil {
			return bad("repo ID")
		}
		if r.RepoID <= previous || !domain.ValidRepositoryWorkspaceLocator(r.RepoID, r.Locator) || r.Name == "" || !validLowerHex(r.TargetIdentityDigest, 64) || !validLowerHex(r.BaseCommit, 40) || !validLowerHex(r.BaseTree, 40) || (r.Role != "write" && r.Role != "reference") {
			return bad("resource identity")
		}
		previous = r.RepoID
		writes[r.RepoID] = struct{}{}
		// Reuse snapshot scope/check validation with fixed internal placeholders, never
		// interpreted as actual host locations or persisted target authority.
		fake := domain.TaskResourceSnapshot{SchemaVersion: domain.TaskResourceSchemaV2, TaskID: d.Task.ID, RoomID: d.Task.RoomID, ProjectID: "project_" + d.Task.RoomID[len("room_"):], SelectionSource: "core_validation", Resources: []domain.TaskRepositoryResource{{RepoID: r.RepoID, Name: r.Name, Checkout: "/validation", CommonGitDir: "/validation/.git", PhysicalIdentity: r.TargetIdentityDigest, Role: r.Role, BaseCommit: r.BaseCommit, BaseTree: r.BaseTree, BaseRef: r.BaseRef, AssociationVersion: 1, Scope: r.Scope, Checks: r.Checks}}}
		if err := fake.Validate(); err != nil {
			return bad("scope/check policy")
		}
	}
	if !validText(d.Requirement.Statement) || !validTextList(d.Requirement.Constraints) || !validTextList(d.Spec.DesiredBehavior) || !validTextList(d.Spec.OutOfScope) || len(d.Acceptance.Criteria) == 0 || d.Acceptance.NoChecks || len(d.Acceptance.VerificationCommands) > 0 {
		return bad("acceptance")
	}
	seen := map[string]bool{}
	for _, c := range d.Acceptance.Criteria {
		if _, err := domain.ParseCriterionID(c.ID); err != nil {
			return bad("criterion")
		}
		if seen[c.ID] || !validText(c.Title) || !validText(c.Description) || !validText(c.Verification) || len(c.VerificationCommandIDs) > 0 {
			return bad("criterion evidence")
		}
		seen[c.ID] = true
	}
	b := d.Execution.Boundary
	if len(b.WritableFiles)+len(b.WritableDirectories)+len(b.Commands)+len(b.TestCommandIDs) != 0 {
		return bad("legacy execution boundary")
	}
	if err := validateTechnicalPlan(d.TechnicalPlan, writes); err != nil {
		return err
	}
	if err := validatePlanningRecords(d.Decisions, d.Risks, d.Unknowns); err != nil {
		return err
	}
	if _, err := domain.ParseContextSnapshotID(d.Execution.Input.ContextSnapshotID); err != nil {
		return bad("context")
	}
	if !validLowerHex(d.Execution.Input.ContextSnapshotDigest, 64) || d.Execution.Input.ContextSnapshotDigest == SupersededPlaceholderContextDigest {
		return bad("context digest")
	}
	if d.Execution.Output.ResultSchemaVersion != agent.ResultSchemaVersion || d.Execution.Output.ArtifactLocatorScope != "execution_workspace_relative" || d.Execution.Output.CheckBinding != "acceptance_criterion_id" || d.Execution.Output.UnknownPolicy != "explicit" || !sameStringSet(d.Execution.RequiredCapabilities, requiredCapabilitiesLocalConnected) {
		return bad("execution")
	}
	if err := validateLocalConnectedCandidate(d.Candidate); err != nil {
		return err
	}
	return validateEntryProbe(d.EntryProbe, CoreContractSchemaVersionV11)
}
