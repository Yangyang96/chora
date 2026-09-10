package speccoding

import (
	"encoding/json"
	"testing"
)

func TestV10BindsCompletedAssistantTextRuntimeProjection(t *testing.T) {
	v8, err := DecodeCoreContract(readV4ContractFixture(t, "chora-m1-real-task.v8.json"))
	if err != nil {
		t.Fatal(err)
	}
	document := v8.Document()
	document.SchemaVersion = CoreContractSchemaVersionV10
	document.Revision = 10
	document.Candidate.Runtime.Version = "0.84.2"
	document.Candidate.Runtime.NPMIntegrity = piRuntimeNPMIntegrityV5
	document.Candidate.Runtime.Config = ConfigReference{Path: "contracts/g2-m1a/pi-runtime-config.v6.json", SHA256: v6RuntimeConfigSHA256}
	document.EntryProbe.PassCriteria = append([]string(nil), requiredProbePassCriteriaV5...)
	encoded, _ := json.Marshal(document)
	contract, err := DecodeCoreContract(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if got := contract.Document(); got.SchemaVersion != CoreContractSchemaVersionV10 || got.Revision != 10 || got.Candidate.Runtime.Config.SHA256 != v6RuntimeConfigSHA256 {
		t.Fatalf("v10 identity = %#v", got.Candidate.Runtime)
	}

	document.Candidate.Runtime.Config = ConfigReference{Path: "contracts/g2-m1a/pi-runtime-config.v5.json", SHA256: v5RuntimeConfigSHA256}
	encoded, _ = json.Marshal(document)
	if _, err := DecodeCoreContract(encoded); err == nil {
		t.Fatal("v10 accepted the pre-conversation runtime projection")
	}
}
