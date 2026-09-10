package speccoding

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/Yangyang96/chora/internal/domain"
)

func boundRepositoryIdentity() RepositoryIdentity {
	return RepositoryIdentity{Name: "acme-project", SourceRevision: "0123456789abcdef0123456789abcdef01234567"}
}

func TestBoundEnvelopeDeclaresAndAcceptsV11LocalConnectedContract(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	envelope, err := NewBoundEnvelope(root, boundRepositoryIdentity(), "0.84.2")
	if err != nil {
		t.Fatal(err)
	}
	if !envelope.MatchesRepositoryRoot(root) {
		t.Fatal("bound envelope must match its own repository root")
	}
	if envelope.MatchesRepositoryRoot(root + "/subdir") {
		t.Fatal("bound envelope must not match a different root")
	}

	taskID := domain.NewTaskID()
	roomID := domain.NewRoomID()
	criteria := []UserAcceptanceCriterion{{ID: domain.NewCriterionID(), Title: "Change is safe", Description: "The declared file changes without breaking tests.", VerificationCommandIndexes: []int{0}}}
	intent, err := envelope.Declare(UserTaskDeclaration{
		ContractID: "user-" + taskID.String(), TaskID: taskID, RoomID: roomID, WorkspaceRoot: root,
		Title: "Add a README note", Requirement: "Document the project entry point.",
		Constraints: []string{"Keep the change inside src/."}, OutOfScope: []string{"No dependency changes."},
		Criteria: criteria, WritableFiles: []string{"src/note.txt"},
		VerificationCommands: []UserVerificationCommand{{Argv: []string{"true"}}},
	})
	if err != nil {
		t.Fatal(err)
	}

	restored, err := envelope.RestoreDeclaredUserTask(intent.CanonicalJSON())
	if err != nil || restored.Digest() != intent.Digest() {
		t.Fatalf("restore drift: %v", err)
	}

	plan := intent.InitialPlan()
	snapshotID := domain.NewContextSnapshotID()
	digest := sha256hex("snapshot")
	contract, err := envelope.Accept(intent, plan, snapshotID, digest)
	if err != nil {
		t.Fatal(err)
	}
	document := contract.Document()
	if document.SchemaVersion != CoreContractSchemaVersionV11 || document.Revision != 11 {
		t.Fatalf("v11 identity = %s r%d", document.SchemaVersion, document.Revision)
	}
	if document.Task.Repository.Name != "acme-project" || document.Task.Repository.BaseRevision != boundRepositoryIdentity().SourceRevision {
		t.Fatalf("bound repository target = %#v", document.Task.Repository)
	}
	if len(document.Candidate.BaseImages) != 0 || document.Candidate.Sandbox.Provider != localConnectedSandboxProvider {
		t.Fatalf("local connected candidate = %#v", document.Candidate)
	}
	if document.Candidate.Model.IdentityStatus != localConnectedModelStatus {
		t.Fatalf("user-configured model identity = %#v", document.Candidate.Model)
	}
	hasDisclosure := false
	for _, capability := range document.Execution.RequiredCapabilities {
		if capability == "sandbox.fail_closed" {
			t.Fatal("Local Connected contract must not claim a sandbox capability")
		}
		if capability == LocalConnectedNoSandboxCapability {
			hasDisclosure = true
		}
	}
	if !hasDisclosure {
		t.Fatal("Local Connected contract must disclose No Sandbox")
	}
}

func TestBoundEnvelopeRejectsForeignRootAndUnsafePaths(t *testing.T) {
	root := t.TempDir()
	envelope, err := NewBoundEnvelope(root, boundRepositoryIdentity(), "0.84.2")
	if err != nil {
		t.Fatal(err)
	}
	taskID := domain.NewTaskID()
	roomID := domain.NewRoomID()
	criteria := []UserAcceptanceCriterion{{ID: domain.NewCriterionID(), Title: "T", Description: "D", VerificationCommandIndexes: []int{0}}}
	if _, err := envelope.Declare(UserTaskDeclaration{
		ContractID: "c", TaskID: taskID, RoomID: roomID, WorkspaceRoot: root + "-other",
		Title: "T", Requirement: "R", Constraints: []string{"C"}, OutOfScope: []string{"O"},
		Criteria: criteria, WritableFiles: []string{"ok.txt"}, VerificationCommands: []UserVerificationCommand{{Argv: []string{"true"}}},
	}); err == nil {
		t.Fatal("foreign root must be rejected")
	}
	if _, err := envelope.Declare(UserTaskDeclaration{
		ContractID: "c", TaskID: taskID, RoomID: roomID, WorkspaceRoot: root,
		Title: "T", Requirement: "R", Constraints: []string{"C"}, OutOfScope: []string{"O"},
		Criteria: criteria, WritableFiles: []string{"../escape.txt"}, VerificationCommands: []UserVerificationCommand{{Argv: []string{"true"}}},
	}); err == nil {
		t.Fatal("path escape must be rejected")
	}
	if _, err := envelope.Declare(UserTaskDeclaration{
		ContractID: "c", TaskID: taskID, RoomID: roomID, WorkspaceRoot: root,
		Title: "T", Requirement: "R", Constraints: []string{"C"}, OutOfScope: []string{"O"},
		Criteria: criteria, WritableFiles: []string{"ok.txt"}, VerificationCommands: []UserVerificationCommand{{Argv: []string{"echo", "$(evil)"}}},
	}); err == nil {
		t.Fatal("command substitution must be rejected")
	}
}

func sha256hex(seed string) string {
	value := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(value[:])
}
