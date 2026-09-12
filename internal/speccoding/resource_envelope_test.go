package speccoding

import (
	"bytes"
	"github.com/Yangyang96/chora/internal/domain"
	"sort"
	"strings"
	"testing"
)

func TestResourceEnvelopeRoundTripMultiRepositoryWithoutGlobalBase(t *testing.T) {
	taskID, roomID := domain.NewTaskID(), domain.NewRoomID()
	snapshot := domain.TaskResourceSnapshot{SchemaVersion: domain.TaskResourceSchemaV2, TaskID: taskID.String(), RoomID: roomID.String(), ProjectID: domain.NewProjectID().String(), SelectionSource: "user_selection"}
	for _, name := range []string{"frontend", "backend"} {
		snapshot.Resources = append(snapshot.Resources, domain.TaskRepositoryResource{RepoID: domain.NewRepositoryID().String(), Name: name, Checkout: "/original/" + name, CommonGitDir: "/original/" + name + "/.git", PhysicalIdentity: strings.Repeat("a", 64), Role: "write", BaseCommit: strings.Repeat("b", 40), BaseTree: strings.Repeat("c", 40), AssociationVersion: 1, Scope: domain.TaskRepositoryScope{Mode: "repository", MigrationChoice: "not_needed"}, Checks: domain.TaskCheckPolicy{Mode: "auto", SelectionSource: "user"}})
	}
	sort.Slice(snapshot.Resources, func(i, j int) bool { return snapshot.Resources[i].RepoID < snapshot.Resources[j].RepoID })
	envelope, err := NewResourceEnvelope("/logical/project", snapshot, "0.84.2")
	if err != nil {
		t.Fatal(err)
	}
	intent, err := envelope.Declare(UserTaskDeclaration{ContractID: "user-" + taskID.String(), TaskID: taskID, RoomID: roomID, WorkspaceRoot: "/logical/project", Title: "Join change", Requirement: "Change both repositories", Constraints: []string{"Respect selected scopes"}, OutOfScope: []string{"Original checkout changes"}, Criteria: []UserAcceptanceCriterion{{ID: domain.NewCriterionID(), Title: "Works", Description: "Both changes agree"}}, Resources: &snapshot})
	if err != nil {
		t.Fatal(err)
	}
	restored, err := envelope.RestoreDeclaredUserTask(intent.CanonicalJSON())
	if err != nil || restored.Digest() != intent.Digest() {
		t.Fatalf("restore: %v", err)
	}
	contract, err := envelope.Accept(restored, restored.InitialPlan(), domain.NewContextSnapshotID(), strings.Repeat("d", 64))
	if err != nil {
		t.Fatal(err)
	}
	raw := contract.CanonicalJSON()
	if bytes.Contains(raw, []byte("/original/")) || bytes.Contains(raw, []byte(`"repository":`)) {
		t.Fatalf("global repository or original locator leaked: %s", raw)
	}
	decoded, err := DecodeCoreContract(raw)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Document().SchemaVersion != CoreContractSchemaVersionV12 || len(decoded.Document().Task.Resources) != 2 {
		t.Fatal("resource vector lost")
	}
	snapshot.Resources[0].BaseCommit = strings.Repeat("e", 40)
	if intent.Resources().Resources[0].BaseCommit != strings.Repeat("b", 40) {
		t.Fatal("intent aliases caller memory")
	}
	doc := decoded.Document()
	doc.Task.Resources[1].RepoID = doc.Task.Resources[0].RepoID
	if _, err := NewCoreContract(doc); err == nil {
		t.Fatal("duplicate resource accepted")
	}
}

func TestIsolatedResourceEnvelopeFreezesImageAndNeverClaimsNoSandbox(t *testing.T) {
	taskID, roomID := domain.NewTaskID(), domain.NewRoomID()
	snapshot := domain.TaskResourceSnapshot{SchemaVersion: domain.TaskResourceSchemaV2, TaskID: taskID.String(), RoomID: roomID.String(), ProjectID: domain.NewProjectID().String(), SelectionSource: "user_selection"}
	for _, name := range []string{"frontend", "backend"} {
		snapshot.Resources = append(snapshot.Resources, domain.TaskRepositoryResource{RepoID: domain.NewRepositoryID().String(), Name: name, Checkout: "/original/" + name, CommonGitDir: "/original/" + name + "/.git", PhysicalIdentity: strings.Repeat("a", 64), Role: "write", BaseCommit: strings.Repeat("b", 40), BaseTree: strings.Repeat("c", 40), AssociationVersion: 1, Scope: domain.TaskRepositoryScope{Mode: "repository", MigrationChoice: "not_needed"}, Checks: domain.TaskCheckPolicy{Mode: "auto", SelectionSource: "user"}})
	}
	sort.Slice(snapshot.Resources, func(i, j int) bool { return snapshot.Resources[i].RepoID < snapshot.Resources[j].RepoID })
	envelope, err := NewResourceEnvelope("/logical/project", snapshot, "0.84.2")
	if err != nil {
		t.Fatal(err)
	}
	envelope, err = envelope.WithIsolatedExecution(IsolatedExecutionIdentity{ImageSHA256: strings.Repeat("1", 64), SourceSHA256: strings.Repeat("2", 64), PolicySHA256: strings.Repeat("3", 64)})
	if err != nil {
		t.Fatal(err)
	}
	if envelope.CapabilityEnvelope()[LocalConnectedNoSandboxCapability] || !envelope.CapabilityEnvelope()[IsolatedLocalCapability] {
		t.Fatal("isolated envelope has false No Sandbox authority")
	}
	intent, err := envelope.Declare(UserTaskDeclaration{ContractID: "user-" + taskID.String(), TaskID: taskID, RoomID: roomID, WorkspaceRoot: "/logical/project", Title: "Join change", Requirement: "Change both repositories", Constraints: []string{"Respect selected scopes"}, OutOfScope: []string{"Original checkout changes"}, Criteria: []UserAcceptanceCriterion{{ID: domain.NewCriterionID(), Title: "Works", Description: "Both changes agree"}}, Resources: &snapshot})
	if err != nil {
		t.Fatal(err)
	}
	restored, err := envelope.RestoreDeclaredUserTask(intent.CanonicalJSON())
	if err != nil || restored.Digest() != intent.Digest() {
		t.Fatalf("restore: %v", err)
	}
	contract, err := envelope.Accept(restored, restored.InitialPlan(), domain.NewContextSnapshotID(), strings.Repeat("d", 64))
	if err != nil {
		t.Fatal(err)
	}
	if contract.Document().Candidate.Sandbox.SHA256 != strings.Repeat("1", 64) {
		t.Fatal("frozen image missing")
	}
	raw := contract.CanonicalJSON()
	if bytes.Contains(raw, []byte("/original/")) || bytes.Contains(raw, []byte(`"repository":`)) {
		t.Fatalf("global repository or original locator leaked: %s", raw)
	}
	decoded, err := DecodeCoreContract(raw)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Document().SchemaVersion != CoreContractSchemaVersionV12 || len(decoded.Document().Task.Resources) != 2 {
		t.Fatal("resource vector lost")
	}
	snapshot.Resources[0].BaseCommit = strings.Repeat("e", 40)
	if intent.Resources().Resources[0].BaseCommit != strings.Repeat("b", 40) {
		t.Fatal("intent aliases caller memory")
	}
	doc := decoded.Document()
	doc.Task.Resources[1].RepoID = doc.Task.Resources[0].RepoID
	if _, err := NewCoreContract(doc); err == nil {
		t.Fatal("duplicate resource accepted")
	}
}
