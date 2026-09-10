package speccoding

import (
	"encoding/json"
	"testing"
)

func TestV6CoreContractReplacesOnlyTheBaselineBoundSnapshotDigest(t *testing.T) {
	v5, err := DecodeCoreContract(readV4ContractFixture(t, "chora-m1-real-task.v5.json"))
	if err != nil {
		t.Fatal(err)
	}
	v6, err := DecodeCoreContract(readV4ContractFixture(t, "chora-m1-real-task.v6.json"))
	if err != nil {
		t.Fatal(err)
	}
	got := v6.Document()
	if got.SchemaVersion != CoreContractSchemaVersionV6 || got.Revision != 6 {
		t.Fatalf("v6 identity = %q revision %d", got.SchemaVersion, got.Revision)
	}
	if got.Execution.Input.ContextSnapshotDigest != "0de5f2a3e65dcb81b9b7fde6949ca91022cf5259ca4c3a9a1881d987b2c66e5d" {
		t.Fatalf("v6 frozen Snapshot = %#v", got.Execution.Input)
	}
	assertV5PreservesV4Boundary(t, v5.Document(), got)
}

func TestV6CoreContractRejectsPlaceholderDigest(t *testing.T) {
	contract, err := DecodeCoreContract(readV4ContractFixture(t, "chora-m1-real-task.v4.json"))
	if err != nil {
		t.Fatal(err)
	}
	document := contract.Document()
	document.SchemaVersion = CoreContractSchemaVersionV6
	document.Revision = 6
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeCoreContract(encoded); err == nil {
		t.Fatal("v6 accepted the unsupported placeholder Context Snapshot digest")
	}
}
