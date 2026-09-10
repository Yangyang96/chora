package speccoding

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Yangyang96/chora/internal/domain"
)

func TestInstalledEnvelopeWritableAuthorityRejectsSymlinksAndRequiresOneSafeExistingParent(t *testing.T) {
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := LoadInstalledEnvelope(repositoryRoot)
	if err != nil {
		t.Fatal(err)
	}
	testRoot := t.TempDir()
	for _, directory := range []string{"safe", "outside", "directory.go"} {
		if err := os.Mkdir(filepath.Join(testRoot, directory), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(testRoot, "outside"), filepath.Join(testRoot, "link")); err != nil {
		t.Fatal(err)
	}
	envelope.repositoryRoot = testRoot

	valid := validUserTaskDeclaration(testRoot)
	valid.WritableFiles = []string{"safe/new_file.go"}
	if _, err := envelope.Declare(valid); err != nil {
		t.Fatalf("one explicit new file under an existing safe parent was rejected: %v", err)
	}
	for name, writable := range map[string]string{
		"symlink parent":   "link/escape.go",
		"missing parent":   "missing/new_file.go",
		"directory target": "directory.go",
	} {
		t.Run(name, func(t *testing.T) {
			input := validUserTaskDeclaration(testRoot)
			input.WritableFiles = []string{writable}
			if _, err := envelope.Declare(input); err == nil {
				t.Fatal("unsafe writable authority was accepted")
			}
		})
	}
}

func TestInstalledEnvelopeDeclaresDistinctUserTasksAndFreezesAcceptedContract(t *testing.T) {
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := LoadInstalledEnvelope(repositoryRoot)
	if err != nil {
		t.Fatal(err)
	}
	identity := envelope.Repository()
	if identity.Name != "chora" || identity.SourceRevision != "67b83d9ed8c4bdc17fa4d16da53c9943b255c222" || identity.BaselineDigest != "b28cb1624124736e745f2b217e841957330f763b6b41fab87b2a88b809b8a0cd" {
		t.Fatalf("repository identity = %#v", identity)
	}

	first := validUserTaskDeclaration(repositoryRoot)
	second := validUserTaskDeclaration(repositoryRoot)
	second.TaskID = domain.NewTaskID()
	second.ContractID = "user-task-2"
	firstIntent, err := envelope.Declare(first)
	if err != nil {
		t.Fatal(err)
	}
	secondIntent, err := envelope.Declare(second)
	if err != nil {
		t.Fatal(err)
	}
	if firstIntent.TaskID() == secondIntent.TaskID() || firstIntent.Digest() == secondIntent.Digest() {
		t.Fatal("two user-created Tasks reused a fixed identity")
	}
	if len(firstIntent.CanonicalJSON()) == 0 || firstIntent.Digest() == ([32]byte{}) {
		t.Fatal("declared intent is not immutable and digest bound")
	}
	canonical := firstIntent.CanonicalJSON()
	if bytes.Contains(canonical, []byte(repositoryRoot)) {
		t.Fatal("declared intent leaked the internal repository path")
	}
	canonical[0] = '!'
	first.Title = "mutated after declaration"
	plan := firstIntent.InitialPlan()
	plan.TechnicalPlan.Steps[0].Files[0] = "mutated.go"

	snapshotID := domain.NewContextSnapshotID()
	accepted, err := envelope.Accept(firstIntent, firstIntent.InitialPlan(), snapshotID, "cc3f993e53e357c8b1a03ccfea74c3853adaf60c947777f3f0ab8b580a7279ad")
	if err != nil {
		t.Fatal(err)
	}
	document := accepted.Document()
	if document.SchemaVersion != CoreContractSchemaVersionV10 || document.Revision != 10 || document.Candidate.Runtime.Version != "0.84.2" || document.Candidate.Runtime.Config.Path != "contracts/g2-m1a/pi-runtime-config.v6.json" || document.Task.ID != first.TaskID.String() || document.Task.RoomID != first.RoomID.String() || document.Execution.Input.ContextSnapshotID != snapshotID.String() || document.Execution.Input.ContextSnapshotDigest == SupersededPlaceholderContextDigest {
		t.Fatalf("accepted contract identity = %#v", document)
	}
	if !reflect.DeepEqual(document.Execution.Boundary.WritableFiles, first.WritableFiles) || !reflect.DeepEqual(document.Acceptance.VerificationCommands[0].Argv, first.VerificationCommands[0].Argv) {
		t.Fatalf("accepted contract changed declared authority: %#v", document.Execution.Boundary)
	}
	if document.Task.Title != "Require reviewable descriptions" || !reflect.DeepEqual(document.Candidate, envelope.template.Candidate) || !reflect.DeepEqual(document.EntryProbe, envelope.template.EntryProbe) || !reflect.DeepEqual(document.Execution.RequiredCapabilities, envelope.template.Execution.RequiredCapabilities) || document.Execution.Output != envelope.template.Execution.Output {
		t.Fatal("accepted contract substituted frozen input or the low-level installed envelope")
	}
}

func TestInstalledEnvelopeDerivesDefensiveIntentFirstBoundary(t *testing.T) {
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := LoadInstalledEnvelope(repositoryRoot)
	if err != nil {
		t.Fatal(err)
	}
	defaults, err := envelope.DefaultUserTask()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(defaults.WritableFiles, []string{"internal/domain/task.go", "internal/domain/task_test.go"}) || !reflect.DeepEqual(defaults.VerificationCommands[0].Argv, []string{"go", "test", "./internal/domain"}) {
		t.Fatalf("default boundary = %#v", defaults)
	}
	for _, file := range defaults.WritableFiles {
		if !safeExactGoPath(file) || !(strings.HasPrefix(file, "cmd/") || strings.HasPrefix(file, "internal/")) {
			t.Fatalf("unsafe default writable file %q", file)
		}
	}
	defaults.WritableFiles[0] = "mutated.go"
	again, err := envelope.DefaultUserTask()
	if err != nil || again.WritableFiles[0] == "mutated.go" {
		t.Fatal("default boundary was not defensively copied")
	}
}

func TestInstalledEnvelopeRejectsUnsupportedUserTaskWithoutAnIntent(t *testing.T) {
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := LoadInstalledEnvelope(repositoryRoot)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*UserTaskDeclaration)
	}{
		{"absolute write", func(input *UserTaskDeclaration) { input.WritableFiles[0] = "/tmp/escape.go" }},
		{"traversal write", func(input *UserTaskDeclaration) { input.WritableFiles[0] = "../escape.go" }},
		{"git write", func(input *UserTaskDeclaration) { input.WritableFiles[0] = ".git/config" }},
		{"glob write", func(input *UserTaskDeclaration) { input.WritableFiles[0] = "internal/**/*.go" }},
		{"shell command", func(input *UserTaskDeclaration) {
			input.VerificationCommands[0].Argv = []string{"sh", "-c", "go test ./internal/domain"}
		}},
		{"pipe token", func(input *UserTaskDeclaration) {
			input.VerificationCommands[0].Argv = []string{"go", "test", "./internal/domain", "|", "tee", "out"}
		}},
		{"network install", func(input *UserTaskDeclaration) {
			input.VerificationCommands[0].Argv = []string{"go", "install", "example.com/tool@latest"}
		}},
		{"missing criterion binding", func(input *UserTaskDeclaration) { input.Criteria[0].VerificationCommandIndexes = nil }},
		{"unknown criterion binding", func(input *UserTaskDeclaration) { input.Criteria[0].VerificationCommandIndexes = []int{1} }},
		{"wrong Room repository", func(input *UserTaskDeclaration) { input.WorkspaceRoot = filepath.Join(repositoryRoot, "internal") }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := validUserTaskDeclaration(repositoryRoot)
			test.mutate(&input)
			if _, err := envelope.Declare(input); err == nil {
				t.Fatal("unsupported declaration was accepted")
			}
		})
	}
}

func TestInstalledEnvelopeRestoresOnlyExactCanonicalIntent(t *testing.T) {
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := LoadInstalledEnvelope(repositoryRoot)
	if err != nil {
		t.Fatal(err)
	}
	intent, err := envelope.Declare(validUserTaskDeclaration(repositoryRoot))
	if err != nil {
		t.Fatal(err)
	}
	restored, err := envelope.RestoreDeclaredUserTask(intent.CanonicalJSON())
	if err != nil {
		t.Fatal(err)
	}
	if restored.Digest() != intent.Digest() || restored.TaskID() != intent.TaskID() || !bytes.Equal(restored.CanonicalJSON(), intent.CanonicalJSON()) || !reflect.DeepEqual(restored.InitialPlanContent(), intent.InitialPlanContent()) {
		t.Fatal("restart changed persisted intent authority")
	}

	canonical := intent.CanonicalJSON()
	tamperedDigest := bytes.Replace(canonical, []byte(installedBaselineDigest), bytes.Repeat([]byte{'0'}, len(installedBaselineDigest)), 1)
	unknownField := append(append([]byte(nil), canonical[:len(canonical)-1]...), []byte(`,"unexpected":true}`)...)
	for _, invalid := range [][]byte{append(append([]byte(nil), canonical...), ' '), tamperedDigest, unknownField, canonical[:len(canonical)-1]} {
		if _, err := envelope.RestoreDeclaredUserTask(invalid); err == nil {
			t.Fatal("tampered or non-canonical persisted intent was restored")
		}
	}
}

func TestDeclaredIntentBridgesEditablePlanWithoutEditableAuthority(t *testing.T) {
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

	initial := intent.InitialPlanContent()
	initial.TechnicalSteps[0] = "caller mutation"
	if reflect.DeepEqual(initial, intent.InitialPlanContent()) {
		t.Fatal("initial Plan content was not defensively copied")
	}
	edited := domain.TechnicalPlanContent{
		TechnicalSteps: []string{"Add focused failing coverage.", "Implement the bounded validation."},
		Decisions:      []string{"Keep validation in the existing constructor."},
		Risks:          []string{"Existing callers may depend on whitespace-only descriptions."},
		Unknowns:       []string{"Whether any persisted fixtures contain blank descriptions."},
	}
	plan, err := intent.PlanFromContent(edited)
	if err != nil {
		t.Fatal(err)
	}
	again, err := intent.PlanFromContent(edited)
	if err != nil || !reflect.DeepEqual(plan, again) {
		t.Fatal("edited Plan conversion is not deterministic")
	}
	for index, step := range plan.TechnicalPlan.Steps {
		if step.ID != []string{"plan-1", "plan-2"}[index] || !reflect.DeepEqual(step.Files, declaration.WritableFiles) {
			t.Fatalf("Plan step gained editable identity or file authority: %#v", step)
		}
	}
	plan.TechnicalPlan.Steps[0].Files = []string{"internal/app/service.go"}
	if _, err := envelope.Accept(intent, plan, domain.NewContextSnapshotID(), "cc3f993e53e357c8b1a03ccfea74c3853adaf60c947777f3f0ab8b580a7279ad"); err == nil {
		t.Fatal("edited Plan expanded frozen file authority")
	}

	invalid := []domain.TechnicalPlanContent{
		{},
		{TechnicalSteps: []string{" "}, Decisions: edited.Decisions, Risks: edited.Risks, Unknowns: edited.Unknowns},
		{TechnicalSteps: edited.TechnicalSteps, Decisions: nil, Risks: edited.Risks, Unknowns: edited.Unknowns},
		{TechnicalSteps: edited.TechnicalSteps, Decisions: edited.Decisions, Risks: []string{""}, Unknowns: edited.Unknowns},
		{TechnicalSteps: edited.TechnicalSteps, Decisions: edited.Decisions, Risks: edited.Risks, Unknowns: nil},
	}
	for _, content := range invalid {
		if _, err := intent.PlanFromContent(content); err == nil {
			t.Fatal("invalid Plan content was converted")
		}
	}

	capabilities := envelope.RequiredCapabilities()
	capabilityEnvelope := envelope.CapabilityEnvelope()
	capabilities[0] = "mutated"
	capabilityEnvelope[envelope.RequiredCapabilities()[0]] = false
	if envelope.RequiredCapabilities()[0] == "mutated" || !envelope.CapabilityEnvelope()[envelope.RequiredCapabilities()[0]] {
		t.Fatal("installed capability authority was not defensively copied")
	}
	if !envelope.MatchesRepositoryRoot(InstalledWorkspaceRoot) || envelope.MatchesRepositoryRoot(repositoryRoot) || envelope.MatchesRepositoryRoot(filepath.Join(InstalledWorkspaceRoot, "internal")) {
		t.Fatal("installed repository exact-match helper widened authority")
	}
}

func validUserTaskDeclaration(_ string) UserTaskDeclaration {
	return UserTaskDeclaration{
		ContractID: "user-task-1",
		TaskID:     domain.NewTaskID(), RoomID: domain.NewRoomID(),
		WorkspaceRoot: InstalledWorkspaceRoot,
		Title:         "Require reviewable descriptions", Requirement: "Reject blank acceptance descriptions.",
		Constraints: []string{"Preserve valid criteria."}, OutOfScope: []string{"Persistence changes."},
		Criteria:             []UserAcceptanceCriterion{{ID: domain.NewCriterionID(), Title: "Blank descriptions fail", Description: "Empty descriptions are rejected.", VerificationCommandIndexes: []int{0}}},
		WritableFiles:        []string{"internal/domain/task.go", "internal/domain/task_test.go"},
		VerificationCommands: []UserVerificationCommand{{Argv: []string{"go", "test", "./internal/domain"}}},
	}
}
