package speccoding

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestV8AddsExplicitCriterionVerificationBindingsWithoutBoundaryDrift(t *testing.T) {
	v7, err := DecodeCoreContract(readV4ContractFixture(t, "chora-m1-real-task.v7.json"))
	if err != nil {
		t.Fatal(err)
	}
	v8, err := DecodeCoreContract(readV4ContractFixture(t, "chora-m1-real-task.v8.json"))
	if err != nil {
		t.Fatal(err)
	}
	got := v8.Document()
	if got.SchemaVersion != CoreContractSchemaVersionV8 || got.Revision != 8 {
		t.Fatalf("v8 identity = %q revision %d", got.SchemaVersion, got.Revision)
	}
	if len(got.Acceptance.VerificationCommands) != 1 || got.Acceptance.VerificationCommands[0].ID != "domain-tests" {
		t.Fatalf("verification commands = %#v", got.Acceptance.VerificationCommands)
	}
	for _, criterion := range got.Acceptance.Criteria {
		if !reflect.DeepEqual(criterion.VerificationCommandIDs, []string{"domain-tests"}) {
			t.Fatalf("criterion binding = %#v", criterion.VerificationCommandIDs)
		}
	}

	want := v7.Document()
	want.SchemaVersion, want.Revision = got.SchemaVersion, got.Revision
	want.Acceptance.VerificationCommands = got.Acceptance.VerificationCommands
	for index := range want.Acceptance.Criteria {
		want.Acceptance.Criteria[index].VerificationCommandIDs = got.Acceptance.Criteria[index].VerificationCommandIDs
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatal("v8 drifted Task, Acceptance meaning, Context, Runtime/model, files, or Agent commands")
	}
}

func TestV8RejectsMissingUnknownAmbiguousAndAgentOnlyBindings(t *testing.T) {
	base, _ := DecodeCoreContract(readV4ContractFixture(t, "chora-m1-real-task.v7.json"))
	tests := []struct {
		name   string
		mutate func(*CoreContractDocument)
	}{
		{"missing binding", func(document *CoreContractDocument) { document.Acceptance.Criteria[0].VerificationCommandIDs = nil }},
		{"unknown binding", func(document *CoreContractDocument) {
			document.Acceptance.Criteria[0].VerificationCommandIDs = []string{"missing"}
		}},
		{"duplicate binding", func(document *CoreContractDocument) {
			document.Acceptance.Criteria[0].VerificationCommandIDs = []string{"domain-tests", "domain-tests"}
		}},
		{"agent format command", func(document *CoreContractDocument) {
			document.Acceptance.Criteria[0].VerificationCommandIDs = []string{"format"}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document := v8Document(base.Document())
			test.mutate(&document)
			encoded, _ := json.Marshal(document)
			if _, err := DecodeCoreContract(encoded); err == nil {
				t.Fatal("invalid structured verification binding accepted")
			}
		})
	}
}

func TestV7FreeTextCannotCarryV8StructuredAuthority(t *testing.T) {
	v7, _ := DecodeCoreContract(readV4ContractFixture(t, "chora-m1-real-task.v7.json"))
	document := v7.Document()
	document.Acceptance.VerificationCommands = []BoundedCommand{{ID: "domain-tests", Argv: []string{"go", "test", "./internal/domain"}}}
	document.Acceptance.Criteria[0].VerificationCommandIDs = []string{"domain-tests"}
	encoded, _ := json.Marshal(document)
	if _, err := DecodeCoreContract(encoded); err == nil {
		t.Fatal("v7 silently accepted structured verification authority")
	}
}

func v8Document(document CoreContractDocument) CoreContractDocument {
	document.SchemaVersion = CoreContractSchemaVersionV8
	document.Revision = 8
	document.Acceptance.VerificationCommands = []BoundedCommand{{ID: "domain-tests", Argv: []string{"go", "test", "./internal/domain"}}}
	for index := range document.Acceptance.Criteria {
		document.Acceptance.Criteria[index].VerificationCommandIDs = []string{"domain-tests"}
	}
	return document
}
