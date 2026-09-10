package speccoding

import (
	"encoding/json"
	"testing"
)

func TestPiRuntimeConfigV5PinsPi0842WithoutBoundaryDrift(t *testing.T) {
	v4Bytes := readV4ContractFixture(t, "pi-runtime-config.v4.json")
	v5Bytes := readV4ContractFixture(t, "pi-runtime-config.v5.json")
	v4, err := DecodePiRuntimeConfigV4(v4Bytes)
	if err != nil {
		t.Fatal(err)
	}
	v5, err := DecodePiRuntimeConfigV5(v5Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if v5.Runtime.Version != "0.84.2" || v5.Runtime.NPMIntegrity != piRuntimeNPMIntegrityV5 {
		t.Fatalf("v5 runtime identity = %#v", v5.Runtime)
	}
	normalized := PiRuntimeConfigV4(v5)
	normalized.SchemaVersion = PiRuntimeConfigSchemaVersionV4
	normalized.Runtime = v4.Runtime
	want, _ := json.Marshal(v4)
	got, _ := json.Marshal(normalized)
	if string(got) != string(want) {
		t.Fatal("v5 changed a boundary other than the pinned Pi package identity")
	}
}

func TestPiRuntimeConfigV5RejectsOldRuntime(t *testing.T) {
	config, err := DecodePiRuntimeConfigV5(readV4ContractFixture(t, "pi-runtime-config.v5.json"))
	if err != nil {
		t.Fatal(err)
	}
	config.Runtime.Version = "0.84.1"
	encoded, _ := json.Marshal(config)
	if _, err := DecodePiRuntimeConfigV5(encoded); err == nil {
		t.Fatal("v5 accepted old Pi runtime")
	}
}
