package speccoding

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Yangyang96/chora/internal/domain"
)

func TestWritableScopeAllowsExactFilesAndDirectoryDescendants(t *testing.T) {
	files := []string{"README.md", "docs/exact note.txt"}
	directories := []string{"src", "docs/指南"}
	for _, path := range []string{"README.md", "docs/exact note.txt", "src", "src/main.go", "src/nested/main_test.go", "docs/指南/说明 文档.md"} {
		if !WritableScopeAllows(path, files, directories) {
			t.Errorf("declared path %q was rejected", path)
		}
	}
	for _, path := range []string{"README.md.bak", "source/main.go", "src-other/main.go", ".", ".git/config", "src/.git/config", "../src/main.go", "/tmp/escape", `src\escape.go`, "src/*.go"} {
		if WritableScopeAllows(path, files, directories) {
			t.Errorf("out-of-scope or unsafe path %q was accepted", path)
		}
	}
	if WritableScopeAllows("src/main.go", nil, []string{"src/../src"}) {
		t.Fatal("an invalid declared scope granted authority")
	}
}

func TestCanonicalCommandTextIncludesWorkingDirectoryAndUnambiguousQuoting(t *testing.T) {
	tests := []struct {
		name    string
		command BoundedCommand
		want    string
	}{
		{"repository root default", BoundedCommand{Argv: []string{"go", "test", "./..."}}, "go test ./..."},
		{"repository root explicit", BoundedCommand{Argv: []string{"make", "test"}, WorkingDirectory: "."}, "make test"},
		{"subdirectory", BoundedCommand{Argv: []string{"npm", "run", "unit tests"}, WorkingDirectory: "web/client tests"}, "cd 'web/client tests' && npm run 'unit tests'"},
		{"literal quote and dollar", BoundedCommand{Argv: []string{"tool", "it's", "$VALUE"}}, "tool 'it'\"'\"'s' '$VALUE'"},
		{"unicode", BoundedCommand{Argv: []string{"tool", "测试 参数"}, WorkingDirectory: "检查"}, "cd '检查' && tool '测试 参数'"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := CanonicalCommandText(test.command)
			if err != nil || got != test.want {
				t.Fatalf("CanonicalCommandText() = %q, %v; want %q", got, err, test.want)
			}
		})
	}
	for _, command := range []BoundedCommand{
		{},
		{Argv: []string{"echo", "x; y"}},
		{Argv: []string{"true"}, WorkingDirectory: "../escape"},
	} {
		if _, err := CanonicalCommandText(command); err == nil {
			t.Fatalf("unsafe command rendered: %#v", command)
		}
	}
}

func TestBoundEnvelopeFreezesDirectoriesAndVerificationWorkingDirectory(t *testing.T) {
	root := t.TempDir()
	for _, directory := range []string{"src", "checks/unit"} {
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(directory)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	envelope := mustBoundEnvelope(t, root)
	declaration := boundP3P4Declaration(root)
	declaration.WritableFiles = nil
	declaration.WritableDirectories = []string{"src"}
	declaration.VerificationCommands[0].WorkingDirectory = "checks/unit"

	intent, err := envelope.Declare(declaration)
	if err != nil {
		t.Fatal(err)
	}
	declaration.WritableDirectories[0] = "mutated"
	declaration.VerificationCommands[0].WorkingDirectory = "mutated"
	if !reflect.DeepEqual(intent.WritableDirectories(), []string{"src"}) || intent.VerificationCommands()[0].WorkingDirectory != "checks/unit" || intent.NoChecks() {
		t.Fatalf("declaration was not frozen: directories=%v commands=%#v noChecks=%v", intent.WritableDirectories(), intent.VerificationCommands(), intent.NoChecks())
	}
	for _, step := range intent.InitialPlan().TechnicalPlan.Steps {
		if !reflect.DeepEqual(step.Files, []string{"src"}) {
			t.Fatalf("directory-only Plan lost its bounded scope: %#v", step)
		}
	}

	canonical := intent.CanonicalJSON()
	if err := os.RemoveAll(filepath.Join(root, "checks")); err != nil {
		t.Fatal(err)
	}
	restored, err := envelope.RestoreDeclaredUserTask(canonical)
	if err != nil {
		t.Fatalf("immutable restart restore depended on mutable directory state: %v", err)
	}
	if !bytes.Equal(restored.CanonicalJSON(), canonical) || restored.Digest() != intent.Digest() {
		t.Fatal("restart changed frozen directory or command authority")
	}

	contract, err := envelope.Accept(restored, restored.InitialPlan(), domain.NewContextSnapshotID(), sha256hex("p3-p4-working-directory"))
	if err != nil {
		t.Fatal(err)
	}
	document := contract.Document()
	if !reflect.DeepEqual(document.Execution.Boundary.WritableDirectories, []string{"src"}) || len(document.Execution.Boundary.WritableFiles) != 0 {
		t.Fatalf("materialized writable scope = %#v", document.Execution.Boundary)
	}
	if len(document.Execution.Boundary.Commands) != 1 || len(document.Execution.Boundary.TestCommandIDs) != 1 || document.Execution.Boundary.Commands[0].WorkingDirectory != "checks/unit" {
		t.Fatalf("materialized execution commands = %#v", document.Execution.Boundary.Commands)
	}
	if !reflect.DeepEqual(document.Acceptance.VerificationCommands, document.Execution.Boundary.Commands) {
		t.Fatal("acceptance and execution verification commands diverged")
	}
	if document.Execution.Boundary.Commands[0].ID == "format" {
		t.Fatal("Local Connected task gained an undeclared formatter command")
	}
}

func TestBoundEnvelopeExplicitNoChecksMaterializesTruthfully(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	envelope := mustBoundEnvelope(t, root)
	declaration := boundP3P4Declaration(root)
	declaration.WritableFiles = nil
	declaration.WritableDirectories = []string{"src"}
	declaration.VerificationCommands = nil
	declaration.Criteria[0].VerificationCommandIndexes = nil
	declaration.NoChecks = true

	intent, err := envelope.Declare(declaration)
	if err != nil {
		t.Fatal(err)
	}
	if !intent.NoChecks() || len(intent.VerificationCommands()) != 0 {
		t.Fatal("explicit no-check choice was not frozen")
	}
	restored, err := envelope.RestoreDeclaredUserTask(intent.CanonicalJSON())
	if err != nil || !restored.NoChecks() {
		t.Fatalf("explicit no-check restore: %v", err)
	}
	contract, err := envelope.Accept(restored, restored.InitialPlan(), domain.NewContextSnapshotID(), sha256hex("p4-no-checks"))
	if err != nil {
		t.Fatal(err)
	}
	document := contract.Document()
	if !document.Acceptance.NoChecks || len(document.Acceptance.VerificationCommands) != 0 || len(document.Execution.Boundary.Commands) != 0 || len(document.Execution.Boundary.TestCommandIDs) != 0 {
		t.Fatalf("no-check task synthesized command evidence: acceptance=%#v boundary=%#v", document.Acceptance, document.Execution.Boundary)
	}
	criterion := document.Acceptance.Criteria[0]
	if len(criterion.VerificationCommandIDs) != 0 || !strings.Contains(criterion.Verification, "Not run") || strings.Contains(strings.ToUpper(criterion.Verification), "PASS") {
		t.Fatalf("no-check provenance is not truthful: %#v", criterion)
	}
	if _, err := DecodeCoreContract(contract.CanonicalJSON()); err != nil {
		t.Fatalf("truthful no-check contract did not round trip: %v", err)
	}
}

func TestBoundEnvelopeRejectsUnsafeDirectoryAndWorkingDirectoryAuthority(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "safe"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "regular"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "safe"), filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	envelope := mustBoundEnvelope(t, root)

	valid := boundP3P4Declaration(root)
	valid.WritableFiles = []string{"safe/new file.txt"}
	valid.WritableDirectories = []string{"safe/new-directory"}
	valid.VerificationCommands[0].WorkingDirectory = "safe"
	if _, err := envelope.Declare(valid); err != nil {
		t.Fatalf("safe exact new file or scoped directory was rejected: %v", err)
	}
	duplicate := boundP3P4Declaration(root)
	duplicate.WritableFiles = []string{"safe/new-scope"}
	duplicate.WritableDirectories = []string{"safe/new-scope"}
	if _, err := envelope.Declare(duplicate); err == nil {
		t.Fatal("one path was accepted as both an exact file and a directory scope")
	}

	for _, directory := range []string{".", "../escape", "/tmp/escape", ".git", "safe/.git", "safe/*", "missing/nested", "regular", "link"} {
		t.Run("directory_"+strings.ReplaceAll(directory, "/", "_"), func(t *testing.T) {
			input := boundP3P4Declaration(root)
			input.WritableDirectories = []string{directory}
			if _, err := envelope.Declare(input); err == nil {
				t.Fatalf("unsafe writable directory %q was accepted", directory)
			}
		})
	}
	for _, directory := range []string{"../escape", "/tmp/escape", ".git", "missing", "regular", "link"} {
		t.Run("working_"+strings.ReplaceAll(directory, "/", "_"), func(t *testing.T) {
			input := boundP3P4Declaration(root)
			input.VerificationCommands[0].WorkingDirectory = directory
			if _, err := envelope.Declare(input); err == nil {
				t.Fatalf("unsafe verification working directory %q was accepted", directory)
			}
		})
	}
}

func TestBoundEnvelopeRejectsAmbiguousCheckConfiguration(t *testing.T) {
	root := t.TempDir()
	envelope := mustBoundEnvelope(t, root)
	tests := []struct {
		name   string
		mutate func(*UserTaskDeclaration)
	}{
		{"commands with noChecks", func(input *UserTaskDeclaration) {
			input.NoChecks = true
			input.Criteria[0].VerificationCommandIndexes = nil
		}},
		{"no commands without noChecks", func(input *UserTaskDeclaration) {
			input.VerificationCommands = nil
			input.Criteria[0].VerificationCommandIndexes = nil
		}},
		{"binding on noChecks", func(input *UserTaskDeclaration) { input.NoChecks = true; input.VerificationCommands = nil }},
		{"missing binding with checks", func(input *UserTaskDeclaration) { input.Criteria[0].VerificationCommandIndexes = nil }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := boundP3P4Declaration(root)
			test.mutate(&input)
			if _, err := envelope.Declare(input); err == nil {
				t.Fatal("ambiguous check configuration was accepted")
			}
		})
	}
}

func TestV11ContractValidationRejectsScopeAndCheckAuthorityDrift(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "checks"), 0o755); err != nil {
		t.Fatal(err)
	}
	envelope := mustBoundEnvelope(t, root)
	declaration := boundP3P4Declaration(root)
	declaration.WritableDirectories = []string{"new-scope"}
	declaration.VerificationCommands[0].WorkingDirectory = "checks"
	intent, err := envelope.Declare(declaration)
	if err != nil {
		t.Fatal(err)
	}
	contract, err := envelope.Accept(intent, intent.InitialPlan(), domain.NewContextSnapshotID(), sha256hex("v11-validation"))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*CoreContractDocument)
	}{
		{"repository root directory", func(document *CoreContractDocument) {
			document.Execution.Boundary.WritableDirectories = []string{"."}
			document.TechnicalPlan.Steps[0].Files = []string{"."}
		}},
		{"git directory", func(document *CoreContractDocument) {
			document.Execution.Boundary.WritableDirectories = []string{".git"}
			document.TechnicalPlan.Steps[0].Files = []string{".git"}
		}},
		{"working directory traversal", func(document *CoreContractDocument) {
			document.Acceptance.VerificationCommands[0].WorkingDirectory = "../escape"
			document.Execution.Boundary.Commands[0].WorkingDirectory = "../escape"
		}},
		{"acceptance boundary mismatch", func(document *CoreContractDocument) {
			document.Acceptance.VerificationCommands[0].Argv = []string{"false"}
		}},
		{"noChecks with commands", func(document *CoreContractDocument) { document.Acceptance.NoChecks = true }},
		{"unbound command", func(document *CoreContractDocument) { document.Execution.Boundary.TestCommandIDs = nil }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document := contract.Document()
			test.mutate(&document)
			if _, err := NewCoreContract(document); err == nil {
				t.Fatal("drifted v11 contract was accepted")
			}
		})
	}
}

func TestInstalledEnvelopeKeepsLegacyCanonicalBytesAndStrictBoundary(t *testing.T) {
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := LoadInstalledEnvelope(repositoryRoot)
	if err != nil {
		t.Fatal(err)
	}
	declaration := validUserTaskDeclaration(repositoryRoot)
	intent, err := envelope.Declare(declaration)
	if err != nil {
		t.Fatal(err)
	}
	legacy := struct {
		EnvelopeTemplateDigest string                      `json:"envelope_template_digest"`
		Repository             RepositoryIdentity          `json:"repository"`
		ContractID             string                      `json:"contract_id"`
		TaskID                 string                      `json:"task_id"`
		RoomID                 string                      `json:"room_id"`
		Title                  string                      `json:"title"`
		Requirement            string                      `json:"requirement"`
		Constraints            []string                    `json:"constraints"`
		OutOfScope             []string                    `json:"out_of_scope"`
		Criteria               []frozenAcceptanceCriterion `json:"criteria"`
		WritableFiles          []string                    `json:"writable_files"`
		VerificationCommands   []struct {
			Argv []string `json:"argv"`
		} `json:"verification_commands"`
	}{
		EnvelopeTemplateDigest: intent.frozen.EnvelopeTemplateDigest,
		Repository:             intent.frozen.Repository,
		ContractID:             intent.frozen.ContractID,
		TaskID:                 intent.frozen.TaskID,
		RoomID:                 intent.frozen.RoomID,
		Title:                  intent.frozen.Title,
		Requirement:            intent.frozen.Requirement,
		Constraints:            intent.frozen.Constraints,
		OutOfScope:             intent.frozen.OutOfScope,
		Criteria:               intent.frozen.Criteria,
		WritableFiles:          intent.frozen.WritableFiles,
		VerificationCommands: make([]struct {
			Argv []string `json:"argv"`
		}, len(intent.frozen.VerificationCommands)),
	}
	for index, command := range intent.frozen.VerificationCommands {
		legacy.VerificationCommands[index].Argv = command.Argv
	}
	wantCanonical, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(intent.CanonicalJSON(), wantCanonical) {
		t.Fatalf("empty optional P3/P4 fields changed legacy canonical bytes:\n got %s\nwant %s", intent.CanonicalJSON(), wantCanonical)
	}
	defaults, err := envelope.DefaultUserTask()
	if err != nil {
		t.Fatal(err)
	}
	if len(defaults.WritableDirectories) != 0 || defaults.NoChecks || defaults.VerificationCommands[0].WorkingDirectory != "" {
		t.Fatalf("installed defaults widened: %#v", defaults)
	}
	for _, mutate := range []func(*UserTaskDeclaration){
		func(input *UserTaskDeclaration) { input.WritableDirectories = []string{"internal"} },
		func(input *UserTaskDeclaration) { input.NoChecks = true },
		func(input *UserTaskDeclaration) { input.VerificationCommands[0].WorkingDirectory = "." },
	} {
		input := validUserTaskDeclaration(repositoryRoot)
		mutate(&input)
		if _, err := envelope.Declare(input); err == nil {
			t.Fatal("installed legacy envelope accepted new P3/P4 authority")
		}
	}
}

func TestBoundEnvelopeDecodesPreP3V11CanonicalContract(t *testing.T) {
	root := t.TempDir()
	envelope := mustBoundEnvelope(t, root)
	declaration := boundP3P4Declaration(root)
	declaration.VerificationCommands[0].WorkingDirectory = ""
	intent, err := envelope.Declare(declaration)
	if err != nil {
		t.Fatal(err)
	}
	contract, err := envelope.Accept(intent, intent.InitialPlan(), domain.NewContextSnapshotID(), sha256hex("legacy-v11"))
	if err != nil {
		t.Fatal(err)
	}
	document := contract.Document()
	legacyFormat := BoundedCommand{ID: "format", Argv: append([]string{"gofmt", "-w"}, document.Execution.Boundary.WritableFiles...)}
	document.Execution.Boundary.Commands = append([]BoundedCommand{legacyFormat}, document.Execution.Boundary.Commands...)
	legacyCanonical, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := DecodeCoreContract(legacyCanonical)
	if err != nil {
		t.Fatalf("pre-P3 immutable v11 contract was rejected: %v", err)
	}
	if !bytes.Equal(restored.CanonicalJSON(), legacyCanonical) {
		t.Fatal("optional P3/P4 fields changed pre-P3 v11 canonical bytes")
	}
}

func mustBoundEnvelope(t *testing.T, root string) BoundEnvelope {
	t.Helper()
	envelope, err := NewBoundEnvelope(root, boundRepositoryIdentity(), "0.84.2")
	if err != nil {
		t.Fatal(err)
	}
	return envelope
}

func boundP3P4Declaration(root string) UserTaskDeclaration {
	return UserTaskDeclaration{
		ContractID:    "p3-p4-contract",
		TaskID:        domain.NewTaskID(),
		RoomID:        domain.NewRoomID(),
		WorkspaceRoot: root,
		Title:         "Bound ordinary changes",
		Requirement:   "Create and review ordinary text changes.",
		Constraints:   []string{"Keep changes within frozen scope."},
		OutOfScope:    []string{"No repository metadata changes."},
		Criteria: []UserAcceptanceCriterion{{
			ID:                         domain.NewCriterionID(),
			Title:                      "Reviewable result",
			Description:                "The result remains reviewable and scoped.",
			VerificationCommandIndexes: []int{0},
		}},
		WritableFiles:        []string{"new file.txt"},
		VerificationCommands: []UserVerificationCommand{{Argv: []string{"custom-check", "--focused"}, WorkingDirectory: "."}},
	}
}
