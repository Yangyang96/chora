package speccoding

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestV5CoreContractReplacesOnlyTheUnsupportedSnapshotDigest(t *testing.T) {
	v4, err := DecodeCoreContract(readV4ContractFixture(t, "chora-m1-real-task.v4.json"))
	if err != nil {
		t.Fatal(err)
	}
	contract, err := DecodeCoreContract(readV4ContractFixture(t, "chora-m1-real-task.v5.json"))
	if err != nil {
		t.Fatal(err)
	}
	got := contract.Document()
	if got.SchemaVersion != CoreContractSchemaVersionV5 || got.Revision != 5 {
		t.Fatalf("v5 identity = %q revision %d", got.SchemaVersion, got.Revision)
	}
	if got.Execution.Input.ContextSnapshotID != "context_snapshot_018f0c4a-1a2f-7c3d-8e4f-1234567890ab" || got.Execution.Input.ContextSnapshotDigest != "28a1c018ea68e54241c275b5fa55f0ec49161eddc9a0e449f7bc655200799d81" {
		t.Fatalf("v5 frozen Snapshot = %#v", got.Execution.Input)
	}
	assertV5PreservesV4Boundary(t, v4.Document(), got)
}

func TestV5CoreReferencesExactV4CandidateFiles(t *testing.T) {
	contract, err := DecodeCoreContract(readV4ContractFixture(t, "chora-m1-real-task.v5.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, reference := range []ConfigReference{contract.Document().Candidate.Runtime.Config, contract.Document().Candidate.Policy} {
		data, err := os.ReadFile(filepath.Join("..", "..", reference.Path))
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(data)
		if hex.EncodeToString(digest[:]) != reference.SHA256 {
			t.Fatalf("v5 reference digest drift for %s", reference.Path)
		}
	}
}

func TestV5CoreContractRejectsTheUnsupportedSnapshotDigest(t *testing.T) {
	document, err := DecodeCoreContract(readV4ContractFixture(t, "chora-m1-real-task.v4.json"))
	if err != nil {
		t.Fatal(err)
	}
	mutated := document.Document()
	mutated.SchemaVersion = CoreContractSchemaVersionV5
	mutated.Revision = 5
	encoded, err := json.Marshal(mutated)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeCoreContract(encoded); err == nil {
		t.Fatal("v5 accepted the unsupported placeholder Context Snapshot digest")
	}
}

func assertV5PreservesV4Boundary(t *testing.T, v4, v5 CoreContractDocument) {
	t.Helper()
	v4.SchemaVersion, v5.SchemaVersion = "", ""
	v4.Revision, v5.Revision = 0, 0
	v4.Execution.Input.ContextSnapshotDigest = ""
	v5.Execution.Input.ContextSnapshotDigest = ""
	v4JSON, err := json.Marshal(v4)
	if err != nil {
		t.Fatal(err)
	}
	v5JSON, err := json.Marshal(v5)
	if err != nil {
		t.Fatal(err)
	}
	if string(v4JSON) != string(v5JSON) {
		t.Fatal("v5 changed Task, Acceptance, Context content, target files, or candidate boundary")
	}
}
