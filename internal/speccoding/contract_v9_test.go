package speccoding

import (
	"encoding/json"
	"testing"
)

func TestV9ChangesOnlyPinnedPiRuntimeIdentity(t *testing.T) {
	v8, err := DecodeCoreContract(readV4ContractFixture(t, "chora-m1-real-task.v8.json"))
	if err != nil {
		t.Fatal(err)
	}
	document := v8.Document()
	document.SchemaVersion = CoreContractSchemaVersionV9
	document.Revision = 9
	document.Candidate.Runtime.Version = "0.84.2"
	document.Candidate.Runtime.NPMIntegrity = piRuntimeNPMIntegrityV5
	document.Candidate.Runtime.Config = ConfigReference{Path: "contracts/g2-m1a/pi-runtime-config.v5.json", SHA256: v5RuntimeConfigSHA256}
	document.EntryProbe.PassCriteria = append([]string(nil), requiredProbePassCriteriaV5...)
	encoded, _ := json.Marshal(document)
	contract, err := DecodeCoreContract(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if got := contract.Document(); got.SchemaVersion != CoreContractSchemaVersionV9 || got.Revision != 9 || got.Candidate.Runtime.Version != "0.84.2" {
		t.Fatalf("v9 identity = %#v", got.Candidate.Runtime)
	}
}

func TestV9RejectsOldPiRuntimeIdentity(t *testing.T) {
	v8, _ := DecodeCoreContract(readV4ContractFixture(t, "chora-m1-real-task.v8.json"))
	document := v8.Document()
	document.SchemaVersion = CoreContractSchemaVersionV9
	document.Revision = 9
	document.EntryProbe.PassCriteria = append([]string(nil), requiredProbePassCriteriaV5...)
	encoded, _ := json.Marshal(document)
	if _, err := DecodeCoreContract(encoded); err == nil {
		t.Fatal("v9 accepted Pi 0.84.1")
	}
}
