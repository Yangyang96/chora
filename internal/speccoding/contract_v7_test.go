package speccoding

import (
	"encoding/json"
	"testing"
)

func TestV7CoreContractReplacesOnlyTheBaselineBoundSnapshotDigest(t *testing.T) {
	v6, err := DecodeCoreContract(readV4ContractFixture(t, "chora-m1-real-task.v6.json"))
	if err != nil {
		t.Fatal(err)
	}
	v7, err := DecodeCoreContract(readV4ContractFixture(t, "chora-m1-real-task.v7.json"))
	if err != nil {
		t.Fatal(err)
	}
	got := v7.Document()
	if got.SchemaVersion != CoreContractSchemaVersionV7 || got.Revision != 7 {
		t.Fatalf("v7 identity = %q revision %d", got.SchemaVersion, got.Revision)
	}
	if got.Execution.Input.ContextSnapshotDigest != "cc3f993e53e357c8b1a03ccfea74c3853adaf60c947777f3f0ab8b580a7279ad" {
		t.Fatalf("v7 frozen Snapshot = %#v", got.Execution.Input)
	}
	assertV5PreservesV4Boundary(t, v6.Document(), got)
}

func TestV7CoreContractRejectsPlaceholderDigest(t *testing.T) {
	contract, err := DecodeCoreContract(readV4ContractFixture(t, "chora-m1-real-task.v4.json"))
	if err != nil {
		t.Fatal(err)
	}
	document := contract.Document()
	document.SchemaVersion = CoreContractSchemaVersionV7
	document.Revision = 7
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeCoreContract(encoded); err == nil {
		t.Fatal("v7 accepted the unsupported placeholder Context Snapshot digest")
	}
}
