package speccoding

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Yangyang96/chora/internal/agent"
	"github.com/Yangyang96/chora/internal/domain"
)

const (
	// InstalledWorkspaceRoot is the stable product-facing repository identity.
	// The installed server maps it to the digest-verified host Baseline only at
	// the runtime boundary, so frozen Snapshots never contain a private host path.
	InstalledWorkspaceRoot        = "/workspace/repository"
	installedRepositoryName       = "chora"
	installedBaselineSchema       = "chora.source-baseline.v6"
	installedSourceRevision       = "67b83d9ed8c4bdc17fa4d16da53c9943b255c222"
	installedBaselineDigest       = "b28cb1624124736e745f2b217e841957330f763b6b41fab87b2a88b809b8a0cd"
	installedManifestSHA256       = "42a5768b46f9e06aaa628bf354db130d46985491f93416565d095cd4792705d1"
	installedV8TemplateSHA256     = "c03c66397d60263d45667eb82edb7ba7d3e3c1f703b214c5b85bd8971d003598"
	installedManifestRelativePath = "contracts/g2-m4/source-baseline-v6/manifest.json"
	installedTemplateRelativePath = "contracts/g2-m1a/chora-m1-real-task.v8.json"
)

var (
	ErrInvalidInstalledEnvelope = errors.New("invalid installed spec coding envelope")
	ErrUnsupportedUserTask      = errors.New("unsupported user spec coding task")
)

// RepositoryIdentity is the only repository authority exposed by the installed
// M1 Real Spec Coding envelope. Its locator deliberately remains internal.
type RepositoryIdentity struct {
	Name           string `json:"name"`
	SourceRevision string `json:"source_revision"`
	BaselineDigest string `json:"baseline_digest"`
}

type UserAcceptanceCriterion struct {
	ID                         domain.CriterionID
	Title                      string
	Description                string
	VerificationCommandIndexes []int
}

type UserVerificationCommand struct {
	Argv             []string
	WorkingDirectory string
}

// UserTaskDeclaration contains server identities that exist at Task creation
// and the complete user-governed execution authority. Snapshot and Charter
// identities do not exist until Plan acceptance and therefore are absent here.
type UserTaskDeclaration struct {
	Resources            *domain.TaskResourceSnapshot
	ContractID           string
	TaskID               domain.TaskID
	RoomID               domain.RoomID
	WorkspaceRoot        string
	Title                string
	Requirement          string
	Constraints          []string
	OutOfScope           []string
	Criteria             []UserAcceptanceCriterion
	WritableFiles        []string
	WritableDirectories  []string
	VerificationCommands []UserVerificationCommand
	NoChecks             bool
}

// UserTaskPlan is the editable Plan surface. It intentionally excludes Task,
// repository, Acceptance, Snapshot, Runtime, Sandbox, and capability authority.
type UserTaskPlan struct {
	TechnicalPlan TechnicalPlan `json:"technical_plan"`
	Decisions     []Decision    `json:"decisions"`
	Risks         []Risk        `json:"risks"`
	Unknowns      []Unknown     `json:"unknowns"`
}

type InstalledEnvelope struct {
	repositoryRoot string
	repository     RepositoryIdentity
	template       CoreContractDocument
	templateDigest [32]byte
	defaultWrites  []string
}

// DefaultUserTaskInput is the fixed installed-product authority Chora derives
// from a one-sentence requirement. It deliberately contains no host locator or
// caller-editable capability surface.
type DefaultUserTaskInput struct {
	Constraints          []string
	OutOfScope           []string
	WritableFiles        []string
	WritableDirectories  []string
	VerificationCommands []UserVerificationCommand
	NoChecks             bool
}

type DeclaredUserTask struct {
	frozen         frozenUserTask
	canonical      []byte
	digest         [32]byte
	initialPlan    UserTaskPlan
	templateDigest [32]byte
}

type sourceBaselineManifest struct {
	SchemaVersion   string `json:"schema_version"`
	SourceRevision  string `json:"source_revision"`
	AggregateSHA256 string `json:"aggregate_sha256"`
	Entries         []struct {
		Path string `json:"path"`
	} `json:"entries"`
}

type frozenUserTask struct {
	Resources              *domain.TaskResourceSnapshot `json:"resources,omitempty"`
	EnvelopeTemplateDigest string                       `json:"envelope_template_digest"`
	Repository             RepositoryIdentity           `json:"repository"`
	ContractID             string                       `json:"contract_id"`
	TaskID                 string                       `json:"task_id"`
	RoomID                 string                       `json:"room_id"`
	Title                  string                       `json:"title"`
	Requirement            string                       `json:"requirement"`
	Constraints            []string                     `json:"constraints"`
	OutOfScope             []string                     `json:"out_of_scope"`
	Criteria               []frozenAcceptanceCriterion  `json:"criteria"`
	WritableFiles          []string                     `json:"writable_files"`
	WritableDirectories    []string                     `json:"writable_directories,omitempty"`
	VerificationCommands   []frozenVerificationCommand  `json:"verification_commands"`
	NoChecks               bool                         `json:"no_checks,omitempty"`
}

type frozenAcceptanceCriterion struct {
	ID                         string `json:"id"`
	Title                      string `json:"title"`
	Description                string `json:"description"`
	VerificationCommandIndexes []int  `json:"verification_command_indexes"`
}

type frozenVerificationCommand struct {
	Argv             []string `json:"argv"`
	WorkingDirectory string   `json:"working_directory,omitempty"`
}

func LoadInstalledEnvelope(repositoryRoot string) (InstalledEnvelope, error) {
	root, err := filepath.Abs(repositoryRoot)
	if err != nil {
		return InstalledEnvelope{}, invalidInstalled("repository root", err)
	}
	root = filepath.Clean(root)
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return InstalledEnvelope{}, invalidInstalled("repository root", err)
	}

	manifestData, err := readPinnedFile(root, installedManifestRelativePath, installedManifestSHA256)
	if err != nil {
		return InstalledEnvelope{}, err
	}
	var manifest sourceBaselineManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		return InstalledEnvelope{}, invalidInstalled("source baseline manifest", err)
	}
	if manifest.SchemaVersion != installedBaselineSchema || manifest.SourceRevision != installedSourceRevision || manifest.AggregateSHA256 != installedBaselineDigest {
		return InstalledEnvelope{}, invalidInstalled("source baseline identity", nil)
	}
	defaultWrites := make([]string, 0, len(manifest.Entries))
	for _, entry := range manifest.Entries {
		if (strings.HasPrefix(entry.Path, "cmd/") || strings.HasPrefix(entry.Path, "internal/")) && safeExactGoPath(entry.Path) {
			defaultWrites = append(defaultWrites, entry.Path)
		}
	}
	if len(defaultWrites) == 0 {
		return InstalledEnvelope{}, invalidInstalled("default Go write boundary", nil)
	}

	templateData, err := readPinnedFile(root, installedTemplateRelativePath, installedV8TemplateSHA256)
	if err != nil {
		return InstalledEnvelope{}, err
	}
	template, err := DecodeCoreContract(templateData)
	if err != nil {
		return InstalledEnvelope{}, invalidInstalled("v8 template", err)
	}
	document := template.Document()
	if document.SchemaVersion != CoreContractSchemaVersionV8 || document.Revision != 8 ||
		document.Task.Repository.Name != installedRepositoryName || document.Task.Repository.Locator != "." ||
		document.Task.Repository.BaseRevision != installedSourceRevision {
		return InstalledEnvelope{}, invalidInstalled("v8 template identity", nil)
	}
	document.SchemaVersion = CoreContractSchemaVersionV10
	document.Revision = 10
	document.Candidate.Runtime.Version = "0.84.2"
	document.Candidate.Runtime.NPMIntegrity = piRuntimeNPMIntegrityV5
	document.Candidate.Runtime.Config = ConfigReference{Path: "contracts/g2-m1a/pi-runtime-config.v6.json", SHA256: v6RuntimeConfigSHA256}
	document.EntryProbe.PassCriteria = append([]string(nil), requiredProbePassCriteriaV5...)

	activeTemplate, err := json.Marshal(document)
	if err != nil {
		return InstalledEnvelope{}, invalidInstalled("v10 active template", err)
	}
	templateDigest := sha256.Sum256(activeTemplate)
	return InstalledEnvelope{
		repositoryRoot: root,
		repository:     RepositoryIdentity{Name: installedRepositoryName, SourceRevision: installedSourceRevision, BaselineDigest: installedBaselineDigest},
		template:       document,
		templateDigest: templateDigest,
		defaultWrites:  defaultWrites,
	}, nil
}

func (envelope InstalledEnvelope) Repository() RepositoryIdentity { return envelope.repository }

// ValidateApplyTarget proves that an explicit local target is the supported
// Chora repository shape without exposing or persisting its host path.
func (envelope InstalledEnvelope) ValidateApplyTarget(root string) error {
	absolute, err := filepath.Abs(root)
	if err != nil || absolute != filepath.Clean(root) {
		return invalidInstalled("apply target", err)
	}
	if _, err := readPinnedFile(absolute, installedManifestRelativePath, installedManifestSHA256); err != nil {
		return invalidInstalled("apply target manifest", err)
	}
	if _, err := readPinnedFile(absolute, installedTemplateRelativePath, installedV8TemplateSHA256); err != nil {
		return invalidInstalled("apply target contract", err)
	}
	return nil
}

// DefaultUserTask returns the installed M1 Alpha's pre-declared reversible
// coding boundary. The caller supplies intent only; Chora owns these fixed
// constraints and returns defensive copies so UI input cannot expand them.
func (envelope InstalledEnvelope) DefaultUserTask() (DefaultUserTaskInput, error) {
	if envelope.repositoryRoot == "" || len(envelope.defaultWrites) == 0 {
		return DefaultUserTaskInput{}, unsupported("installed default task boundary")
	}
	if len(envelope.template.Execution.Boundary.WritableFiles) == 0 || len(envelope.template.Acceptance.VerificationCommands) == 0 {
		return DefaultUserTaskInput{}, unsupported("installed template task boundary")
	}
	commands := make([]UserVerificationCommand, len(envelope.template.Acceptance.VerificationCommands))
	for index, command := range envelope.template.Acceptance.VerificationCommands {
		commands[index] = UserVerificationCommand{Argv: cloneStrings(command.Argv), WorkingDirectory: command.WorkingDirectory}
	}
	return DefaultUserTaskInput{
		Constraints: []string{
			"Keep changes inside the installed Chora Go source boundary.",
			"Preserve existing behavior outside the stated requirement.",
			"Return a reviewable Patch and complete independent verification.",
		},
		OutOfScope: []string{
			"Changing web, dependency, migration, contract, or generated files.",
			"Accessing unrelated host files, publishing, merging, or deploying.",
		},
		WritableFiles:        cloneStrings(envelope.template.Execution.Boundary.WritableFiles),
		VerificationCommands: commands,
	}, nil
}

func (envelope InstalledEnvelope) RequiredCapabilities() []string {
	return cloneStrings(envelope.template.Execution.RequiredCapabilities)
}

func (envelope InstalledEnvelope) CapabilityEnvelope() domain.CapabilityEnvelope {
	capabilities := make(domain.CapabilityEnvelope, len(envelope.template.Execution.RequiredCapabilities))
	for _, capability := range envelope.template.Execution.RequiredCapabilities {
		capabilities[capability] = true
	}
	return capabilities
}

// MatchesRepositoryRoot validates the stable installed Room workspace
// authority without exposing or serializing the installed host path.
func (envelope InstalledEnvelope) MatchesRepositoryRoot(repositoryRoot string) bool {
	return envelope.repositoryRoot != "" && repositoryRoot == InstalledWorkspaceRoot
}

func (envelope InstalledEnvelope) Declare(input UserTaskDeclaration) (DeclaredUserTask, error) {
	if input.Resources != nil {
		return DeclaredUserTask{}, unsupported("legacy resource contract")
	}
	if envelope.repositoryRoot == "" || envelope.templateDigest == ([32]byte{}) {
		return DeclaredUserTask{}, unsupported("installed envelope is not loaded")
	}
	if !validText(input.ContractID) || !input.TaskID.Valid() || !input.RoomID.Valid() ||
		!validText(input.Title) || !validText(input.Requirement) || !validTextList(input.Constraints) || !validTextList(input.OutOfScope) {
		return DeclaredUserTask{}, unsupported("Task identity or requirement")
	}
	if !envelope.MatchesRepositoryRoot(input.WorkspaceRoot) {
		return DeclaredUserTask{}, unsupported("Room repository authority")
	}
	if len(input.WritableDirectories) != 0 || input.NoChecks {
		return DeclaredUserTask{}, unsupported("installed task boundary")
	}

	writableFiles, err := envelope.validateWritableFiles(input.WritableFiles)
	if err != nil {
		return DeclaredUserTask{}, err
	}
	commands, err := freezeVerificationCommands(input.VerificationCommands)
	if err != nil {
		return DeclaredUserTask{}, err
	}
	criteria, err := freezeCriteria(input.Criteria, len(commands), false)
	if err != nil {
		return DeclaredUserTask{}, err
	}

	frozen := frozenUserTask{
		EnvelopeTemplateDigest: hex.EncodeToString(envelope.templateDigest[:]),
		Repository:             envelope.repository,
		ContractID:             input.ContractID,
		TaskID:                 input.TaskID.String(),
		RoomID:                 input.RoomID.String(),
		Title:                  input.Title,
		Requirement:            input.Requirement,
		Constraints:            cloneStrings(input.Constraints),
		OutOfScope:             cloneStrings(input.OutOfScope),
		Criteria:               criteria,
		WritableFiles:          writableFiles,
		VerificationCommands:   commands,
	}
	canonical, err := json.Marshal(frozen)
	if err != nil {
		return DeclaredUserTask{}, unsupported("canonical declaration")
	}
	digest := sha256.Sum256(canonical)
	return DeclaredUserTask{
		frozen:         frozen,
		canonical:      canonical,
		digest:         digest,
		initialPlan:    deterministicInitialPlan(frozen),
		templateDigest: envelope.templateDigest,
	}, nil
}

// RestoreDeclaredUserTask rehydrates a persisted canonical declaration after
// restart. It accepts only the exact canonical encoding and sends all restored
// authority back through Declare's current installed-envelope validation.
func (envelope InstalledEnvelope) RestoreDeclaredUserTask(canonical []byte) (DeclaredUserTask, error) {
	decoder := json.NewDecoder(bytes.NewReader(canonical))
	decoder.DisallowUnknownFields()
	var frozen frozenUserTask
	if err := decoder.Decode(&frozen); err != nil {
		return DeclaredUserTask{}, unsupported("stored declared intent")
	}
	if err := ensureDecoderEOF(decoder); err != nil {
		return DeclaredUserTask{}, unsupported("stored declared intent")
	}
	if frozen.Resources != nil {
		return DeclaredUserTask{}, unsupported("legacy intent contains resources")
	}
	recanonical, err := json.Marshal(frozen)
	if err != nil || !bytes.Equal(recanonical, canonical) {
		return DeclaredUserTask{}, unsupported("non-canonical stored declared intent")
	}
	taskID, err := domain.ParseTaskID(frozen.TaskID)
	if err != nil {
		return DeclaredUserTask{}, unsupported("stored Task identity")
	}
	roomID, err := domain.ParseRoomID(frozen.RoomID)
	if err != nil {
		return DeclaredUserTask{}, unsupported("stored Room identity")
	}
	criteria := make([]UserAcceptanceCriterion, len(frozen.Criteria))
	for index, criterion := range frozen.Criteria {
		criterionID, err := domain.ParseCriterionID(criterion.ID)
		if err != nil {
			return DeclaredUserTask{}, unsupported("stored criterion identity")
		}
		criteria[index] = UserAcceptanceCriterion{
			ID: criterionID, Title: criterion.Title, Description: criterion.Description,
			VerificationCommandIndexes: append([]int(nil), criterion.VerificationCommandIndexes...),
		}
	}
	commands := make([]UserVerificationCommand, len(frozen.VerificationCommands))
	for index, command := range frozen.VerificationCommands {
		commands[index] = UserVerificationCommand{Argv: cloneStrings(command.Argv), WorkingDirectory: command.WorkingDirectory}
	}
	restored, err := envelope.Declare(UserTaskDeclaration{
		ContractID: frozen.ContractID, TaskID: taskID, RoomID: roomID, WorkspaceRoot: InstalledWorkspaceRoot,
		Title: frozen.Title, Requirement: frozen.Requirement, Constraints: cloneStrings(frozen.Constraints),
		OutOfScope: cloneStrings(frozen.OutOfScope), Criteria: criteria, WritableFiles: cloneStrings(frozen.WritableFiles),
		WritableDirectories: cloneStrings(frozen.WritableDirectories), VerificationCommands: commands, NoChecks: frozen.NoChecks,
	})
	if err != nil || !bytes.Equal(restored.canonical, canonical) || restored.frozen.Repository != frozen.Repository || restored.frozen.EnvelopeTemplateDigest != frozen.EnvelopeTemplateDigest {
		return DeclaredUserTask{}, unsupported("stored declared intent authority")
	}
	return restored, nil
}

func (intent DeclaredUserTask) TaskID() domain.TaskID {
	id, _ := domain.ParseTaskID(intent.frozen.TaskID)
	return id
}

func (intent DeclaredUserTask) RoomID() domain.RoomID {
	id, _ := domain.ParseRoomID(intent.frozen.RoomID)
	return id
}

func (intent DeclaredUserTask) Repository() RepositoryIdentity { return intent.frozen.Repository }
func (intent DeclaredUserTask) Resources() *domain.TaskResourceSnapshot {
	if intent.frozen.Resources == nil {
		return nil
	}
	b, _ := json.Marshal(intent.frozen.Resources)
	var s domain.TaskResourceSnapshot
	_ = json.Unmarshal(b, &s)
	return &s
}

func (intent DeclaredUserTask) ContractID() string    { return intent.frozen.ContractID }
func (intent DeclaredUserTask) Digest() [32]byte      { return intent.digest }
func (intent DeclaredUserTask) DigestHex() string     { return hex.EncodeToString(intent.digest[:]) }
func (intent DeclaredUserTask) CanonicalJSON() []byte { return bytes.Clone(intent.canonical) }
func (intent DeclaredUserTask) WritableFiles() []string {
	return cloneStrings(intent.frozen.WritableFiles)
}
func (intent DeclaredUserTask) WritableDirectories() []string {
	return cloneStrings(intent.frozen.WritableDirectories)
}
func (intent DeclaredUserTask) VerificationCommands() []UserVerificationCommand {
	commands := make([]UserVerificationCommand, len(intent.frozen.VerificationCommands))
	for index, command := range intent.frozen.VerificationCommands {
		commands[index] = UserVerificationCommand{Argv: cloneStrings(command.Argv), WorkingDirectory: command.WorkingDirectory}
	}
	return commands
}
func (intent DeclaredUserTask) NoChecks() bool { return intent.frozen.NoChecks }
func (intent DeclaredUserTask) InitialPlan() UserTaskPlan {
	return cloneUserTaskPlan(intent.initialPlan)
}

// InitialPlanContent is the deterministic editable domain representation used
// by planning persistence. Returned slices do not alias the frozen intent.
func (intent DeclaredUserTask) InitialPlanContent() domain.TechnicalPlanContent {
	return cloneTechnicalPlanContent(deterministicInitialPlanContent(intent.frozen))
}

// PlanFromContent converts editable planning text back to a contract Plan.
// PlanStep file authority is always regenerated from the frozen declaration;
// editable content has no file or identifier channel.
func (intent DeclaredUserTask) PlanFromContent(content domain.TechnicalPlanContent) (UserTaskPlan, error) {
	if !validDeclaredIntent(intent) {
		return UserTaskPlan{}, unsupported("declared intent")
	}
	return planFromContent(intent.frozen, content)
}

func (envelope InstalledEnvelope) Accept(intent DeclaredUserTask, plan UserTaskPlan, snapshotID domain.ContextSnapshotID, snapshotDigest string) (CoreContract, error) {
	if !validAcceptIdentity(envelope.templateDigest, envelope.repository, intent, snapshotID, snapshotDigest) {
		return CoreContract{}, unsupported("acceptance identity or Snapshot")
	}
	return materializeAcceptedContract(envelope.template, envelope.repository, intent, plan, snapshotID, snapshotDigest)
}

func validAcceptIdentity(templateDigest [32]byte, repository RepositoryIdentity, intent DeclaredUserTask, snapshotID domain.ContextSnapshotID, snapshotDigest string) bool {
	return templateDigest != ([32]byte{}) && intent.templateDigest == templateDigest &&
		intent.frozen.EnvelopeTemplateDigest == hex.EncodeToString(templateDigest[:]) &&
		intent.frozen.Repository == repository && len(intent.canonical) != 0 &&
		sha256.Sum256(intent.canonical) == intent.digest && snapshotID.Valid() &&
		validLowerHex(snapshotDigest, 64) && snapshotDigest != strings.Repeat("0", 64) && snapshotDigest != SupersededPlaceholderContextDigest
}

func materializeAcceptedContract(template CoreContractDocument, repository RepositoryIdentity, intent DeclaredUserTask, plan UserTaskPlan, snapshotID domain.ContextSnapshotID, snapshotDigest string) (CoreContract, error) {
	recanonical, err := json.Marshal(intent.frozen)
	if err != nil || !bytes.Equal(recanonical, intent.canonical) {
		return CoreContract{}, unsupported("declared intent was changed")
	}

	document := template
	document.ContractID = intent.frozen.ContractID
	document.Task = TaskContract{
		ID:         intent.frozen.TaskID,
		RoomID:     intent.frozen.RoomID,
		Title:      intent.frozen.Title,
		Goal:       intent.frozen.Requirement,
		Repository: RepositoryTarget{Name: repository.Name, Locator: ".", BaseRevision: repository.SourceRevision},
	}
	document.Requirement = RequirementContract{Statement: intent.frozen.Requirement, Constraints: cloneStrings(intent.frozen.Constraints)}
	document.Spec = SpecContract{DesiredBehavior: desiredBehavior(intent.frozen.Criteria), OutOfScope: cloneStrings(intent.frozen.OutOfScope)}
	document.Acceptance = acceptanceContract(intent.frozen.Criteria, intent.frozen.VerificationCommands, intent.frozen.NoChecks)
	document.TechnicalPlan = cloneTechnicalPlan(plan.TechnicalPlan)
	document.Decisions = cloneDecisions(plan.Decisions)
	document.Risks = cloneRisks(plan.Risks)
	document.Unknowns = cloneUnknowns(plan.Unknowns)
	document.Execution.Input = FrozenInput{ContextSnapshotID: snapshotID.String(), ContextSnapshotDigest: snapshotDigest}
	document.Execution.Boundary = executionBoundary(document.SchemaVersion, intent.frozen.WritableFiles, intent.frozen.WritableDirectories, intent.frozen.VerificationCommands)

	contract, err := NewCoreContract(document)
	if err != nil {
		return CoreContract{}, fmt.Errorf("%w: accepted Plan: %v", ErrUnsupportedUserTask, err)
	}
	return contract, nil
}

func readPinnedFile(root, relativePath, expectedSHA256 string) ([]byte, error) {
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relativePath)))
	if err != nil {
		return nil, invalidInstalled(relativePath, err)
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != expectedSHA256 {
		return nil, invalidInstalled(relativePath+" digest", nil)
	}
	return data, nil
}

func (envelope InstalledEnvelope) validateWritableFiles(values []string) ([]string, error) {
	if len(values) == 0 {
		return nil, unsupported("writable files")
	}
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if !safeExactGoPath(value) || !addUnique(seen, value) {
			return nil, unsupported("writable file")
		}
		if err := validateRepositoryFile(envelope.repositoryRoot, value); err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, nil
}

func validateRepositoryFile(root, relative string) error {
	parts := strings.Split(relative, "/")
	current := root
	for index, part := range parts {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) && index == len(parts)-1 {
				return nil // A new file has one explicit, existing safe parent.
			}
			return unsupported("writable file parent")
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return unsupported("symlink writable file or parent")
		}
		if index < len(parts)-1 && !info.IsDir() {
			return unsupported("writable file parent")
		}
		if index == len(parts)-1 && !info.Mode().IsRegular() {
			return unsupported("ordinary writable file")
		}
	}
	return nil
}

func safeExactGoPath(value string) bool {
	if !validRelativePath(value) || path.Ext(value) != ".go" || strings.ContainsAny(value, "*?[") {
		return false
	}
	for _, component := range strings.Split(value, "/") {
		if component == ".git" || component == "" {
			return false
		}
	}
	return true
}

func freezeVerificationCommands(values []UserVerificationCommand) ([]frozenVerificationCommand, error) {
	if len(values) == 0 {
		return nil, unsupported("verification commands")
	}
	result := make([]frozenVerificationCommand, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		if !safeGoTestArgv(value.Argv) || value.WorkingDirectory != "" {
			return nil, unsupported("verification command")
		}
		argv := cloneStrings(value.Argv)
		key, _ := json.Marshal(argv)
		if !addUnique(seen, string(key)) {
			return nil, unsupported("duplicate verification command")
		}
		result = append(result, frozenVerificationCommand{Argv: argv})
	}
	return result, nil
}

func safeGoTestArgv(argv []string) bool {
	if len(argv) < 3 || argv[0] != "go" || argv[1] != "test" {
		return false
	}
	packageCount := 0
	for index := 2; index < len(argv); index++ {
		argument := argv[index]
		if !validText(argument) || unsafeCommandToken(argument) {
			return false
		}
		if strings.HasPrefix(argument, "-") {
			name, hasValue := argument, strings.Contains(argument, "=")
			if hasValue {
				name = argument[:strings.IndexByte(argument, '=')]
			}
			switch name {
			case "-v", "-short", "-failfast":
				if hasValue {
					return false
				}
			case "-run", "-count", "-timeout":
				if !hasValue {
					index++
					if index >= len(argv) || !validText(argv[index]) || unsafeCommandToken(argv[index]) {
						return false
					}
				}
			default:
				return false
			}
			continue
		}
		if !safeGoPackagePattern(argument) {
			return false
		}
		packageCount++
	}
	return packageCount > 0
}

func safeGoPackagePattern(value string) bool {
	if !strings.HasPrefix(value, "./") || strings.Contains(value, "\\") || strings.ContainsAny(value, "*?[") || strings.Contains(value, "@") {
		return false
	}
	base := strings.TrimSuffix(value, "/...")
	if base == "." {
		return value == "./..."
	}
	cleaned := path.Clean(base)
	return cleaned != "." && !strings.HasPrefix(cleaned, "../") && base == "./"+cleaned && !containsGitComponent(cleaned)
}

func unsafeCommandToken(value string) bool {
	return strings.ContainsAny(value, "|;&<>`\n\r") || strings.Contains(value, "$(") || strings.Contains(value, "${")
}

func containsGitComponent(value string) bool {
	for _, component := range strings.Split(value, "/") {
		if component == ".git" {
			return true
		}
	}
	return false
}

func freezeCriteria(values []UserAcceptanceCriterion, commandCount int, noChecks bool) ([]frozenAcceptanceCriterion, error) {
	if len(values) == 0 {
		return nil, unsupported("acceptance criteria")
	}
	result := make([]frozenAcceptanceCriterion, 0, len(values))
	criterionIDs := map[string]struct{}{}
	boundCommands := make([]bool, commandCount)
	for _, value := range values {
		if !value.ID.Valid() || !validText(value.Title) || !validText(value.Description) || !addUnique(criterionIDs, value.ID.String()) {
			return nil, unsupported("acceptance criterion")
		}
		if noChecks != (len(value.VerificationCommandIndexes) == 0) {
			return nil, unsupported("acceptance criterion verification binding")
		}
		bindings := append([]int(nil), value.VerificationCommandIndexes...)
		if noChecks {
			bindings = make([]int, 0)
		}
		seenBindings := map[int]struct{}{}
		for _, index := range bindings {
			if index < 0 || index >= commandCount {
				return nil, unsupported("acceptance criterion verification binding")
			}
			if _, exists := seenBindings[index]; exists {
				return nil, unsupported("duplicate acceptance criterion verification binding")
			}
			seenBindings[index] = struct{}{}
			boundCommands[index] = true
		}
		result = append(result, frozenAcceptanceCriterion{ID: value.ID.String(), Title: value.Title, Description: value.Description, VerificationCommandIndexes: bindings})
	}
	for _, bound := range boundCommands {
		if !bound {
			return nil, unsupported("unbound verification command")
		}
	}
	return result, nil
}

func deterministicInitialPlan(intent frozenUserTask) UserTaskPlan {
	plan, err := planFromContent(intent, deterministicInitialPlanContent(intent))
	if err != nil {
		panic(fmt.Sprintf("construct validated deterministic initial Plan: %v", err))
	}
	return plan
}

func deterministicInitialPlanContent(intent frozenUserTask) domain.TechnicalPlanContent {
	if intent.Resources != nil {
		return domain.TechnicalPlanContent{TechnicalSteps: []string{"Inspect the selected repositories and relevant modules.", "Implement the requirement within the frozen repository roles and optional limits.", "Select and report checks under each frozen policy, then produce grouped reviewable changes."}, Decisions: []string{"Use one Local Connected Pi attempt; original checkouts stay unchanged until human Apply."}, Risks: []string{"Checks may be unavailable or fail; report evidence and unknowns explicitly."}, Unknowns: []string{"Relevant files and applicable checks will be identified during the task."}}
	}
	return domain.TechnicalPlanContent{
		TechnicalSteps: []string{
			"Inspect the relevant Chora code and tests for the stated requirement.",
			"Implement the smallest coherent change inside the fixed Go source boundary.",
			"Format changed Go files and run the bound verification suite.",
		},
		Decisions: []string{"Proceed automatically only inside the Room's pre-declared reversible boundary."},
		Risks:     []string{"The implementation could regress behavior outside the declared acceptance criteria."},
		Unknowns:  []string{"The exact files and implementation details remain to be confirmed by execution and final Review."},
	}
}

func planFromContent(intent frozenUserTask, content domain.TechnicalPlanContent) (UserTaskPlan, error) {
	scopes := append(cloneStrings(intent.WritableFiles), intent.WritableDirectories...)
	if intent.Resources != nil {
		for _, r := range intent.Resources.Resources {
			scopes = append(scopes, r.RepoID)
		}
	}
	if len(scopes) == 0 || !validPlanTexts(content.TechnicalSteps) || !validPlanTexts(content.Decisions) || !validPlanTexts(content.Risks) || !validPlanTexts(content.Unknowns) {
		return UserTaskPlan{}, unsupported("Technical Plan content")
	}
	plan := UserTaskPlan{
		TechnicalPlan: TechnicalPlan{Summary: "Implement " + intent.Title + " within the frozen repository boundary.", Steps: make([]PlanStep, len(content.TechnicalSteps))},
		Decisions:     make([]Decision, len(content.Decisions)),
		Risks:         make([]Risk, len(content.Risks)),
		Unknowns:      make([]Unknown, len(content.Unknowns)),
	}
	for index, description := range content.TechnicalSteps {
		plan.TechnicalPlan.Steps[index] = PlanStep{ID: "plan-" + strconv.Itoa(index+1), Description: description, Files: cloneStrings(scopes)}
	}
	for index, resolution := range content.Decisions {
		plan.Decisions[index] = Decision{ID: "decision-" + strconv.Itoa(index+1), Question: "Planning decision " + strconv.Itoa(index+1), Resolution: resolution, Rationale: "Recorded in the accepted Technical Plan revision."}
	}
	for index, description := range content.Risks {
		plan.Risks[index] = Risk{ID: "risk-" + strconv.Itoa(index+1), Severity: "medium", Description: description, Mitigation: "Keep execution within frozen authority and require declared verification evidence."}
	}
	for index, question := range content.Unknowns {
		plan.Unknowns[index] = Unknown{ID: "unknown-" + strconv.Itoa(index+1), Question: question, ResolutionGate: "Accepted Technical Plan and independent verification"}
	}
	return plan, nil
}

func validPlanTexts(values []string) bool {
	if len(values) == 0 {
		return false
	}
	for _, value := range values {
		if !domain.ValidTrustedContextText(value, domain.MaxTaskPlanTextRunes, true) {
			return false
		}
	}
	return true
}

func validDeclaredIntent(intent DeclaredUserTask) bool {
	if intent.digest == ([32]byte{}) || len(intent.canonical) == 0 || sha256.Sum256(intent.canonical) != intent.digest || (len(intent.frozen.WritableFiles)+len(intent.frozen.WritableDirectories) == 0 && intent.frozen.Resources == nil) {
		return false
	}
	recanonical, err := json.Marshal(intent.frozen)
	return err == nil && bytes.Equal(recanonical, intent.canonical)
}

func desiredBehavior(criteria []frozenAcceptanceCriterion) []string {
	result := make([]string, len(criteria))
	for index, criterion := range criteria {
		result[index] = criterion.Description
	}
	return result
}

func acceptanceContract(criteria []frozenAcceptanceCriterion, commands []frozenVerificationCommand, noChecks bool) AcceptanceContract {
	result := AcceptanceContract{Criteria: make([]AcceptanceCriterionContract, len(criteria)), VerificationCommands: make([]BoundedCommand, len(commands)), NoChecks: noChecks}
	for index, command := range commands {
		result.VerificationCommands[index] = BoundedCommand{ID: verificationCommandID(index), Argv: cloneStrings(command.Argv), WorkingDirectory: command.WorkingDirectory}
	}
	for index, criterion := range criteria {
		if noChecks {
			result.Criteria[index] = AcceptanceCriterionContract{ID: criterion.ID, Title: criterion.Title, Description: criterion.Description, Verification: explicitNoChecksVerification}
			continue
		}
		ids := make([]string, len(criterion.VerificationCommandIndexes))
		for bindingIndex, commandIndex := range criterion.VerificationCommandIndexes {
			ids[bindingIndex] = verificationCommandID(commandIndex)
		}
		result.Criteria[index] = AcceptanceCriterionContract{ID: criterion.ID, Title: criterion.Title, Description: criterion.Description, Verification: strings.Join(commands[criterion.VerificationCommandIndexes[0]].Argv, " "), VerificationCommandIDs: ids}
	}
	return result
}

const explicitNoChecksVerification = "Not run: project explicitly configured no checks."

func executionBoundary(schemaVersion string, files, directories []string, commands []frozenVerificationCommand) ExecutionBoundary {
	boundary := ExecutionBoundary{
		WritableFiles:       cloneStrings(files),
		WritableDirectories: cloneStrings(directories),
		Commands:            make([]BoundedCommand, 0, len(commands)+1),
		TestCommandIDs:      make([]string, len(commands)),
	}
	if schemaVersion != CoreContractSchemaVersionV11 {
		boundary.Commands = []BoundedCommand{{ID: "format", Argv: append([]string{"gofmt", "-w"}, files...)}}
	}
	for index, command := range commands {
		id := verificationCommandID(index)
		boundary.Commands = append(boundary.Commands, BoundedCommand{ID: id, Argv: cloneStrings(command.Argv), WorkingDirectory: command.WorkingDirectory})
		boundary.TestCommandIDs[index] = id
	}
	return boundary
}

func verificationCommandID(index int) string { return "verify-" + strconv.Itoa(index+1) }

func cloneUserTaskPlan(plan UserTaskPlan) UserTaskPlan {
	return UserTaskPlan{TechnicalPlan: cloneTechnicalPlan(plan.TechnicalPlan), Decisions: cloneDecisions(plan.Decisions), Risks: cloneRisks(plan.Risks), Unknowns: cloneUnknowns(plan.Unknowns)}
}

func cloneTechnicalPlanContent(content domain.TechnicalPlanContent) domain.TechnicalPlanContent {
	return domain.TechnicalPlanContent{
		TechnicalSteps: cloneStrings(content.TechnicalSteps),
		Decisions:      cloneStrings(content.Decisions),
		Risks:          cloneStrings(content.Risks),
		Unknowns:       cloneStrings(content.Unknowns),
	}
}

func cloneTechnicalPlan(plan TechnicalPlan) TechnicalPlan {
	result := TechnicalPlan{Summary: plan.Summary, Steps: make([]PlanStep, len(plan.Steps))}
	for index, step := range plan.Steps {
		result.Steps[index] = PlanStep{ID: step.ID, Description: step.Description, Files: cloneStrings(step.Files)}
	}
	return result
}

func cloneDecisions(values []Decision) []Decision { return append([]Decision(nil), values...) }
func cloneRisks(values []Risk) []Risk             { return append([]Risk(nil), values...) }
func cloneUnknowns(values []Unknown) []Unknown    { return append([]Unknown(nil), values...) }
func cloneStrings(values []string) []string       { return append([]string(nil), values...) }

func invalidInstalled(field string, cause error) error {
	if cause != nil {
		return fmt.Errorf("%w: %s: %v", ErrInvalidInstalledEnvelope, field, cause)
	}
	return fmt.Errorf("%w: %s", ErrInvalidInstalledEnvelope, field)
}

func unsupported(field string) error { return fmt.Errorf("%w: %s", ErrUnsupportedUserTask, field) }

// BoundEnvelope is the per-Room Spec Coding start envelope for the M2-S1 Local
// Connected path. Unlike InstalledEnvelope, it is bound to one RepositoryBinding:
// an arbitrary Git repository opened or cloned into the Workbench. It reuses the
// same frozen-declaration, canonicalization, Plan, and Accept machinery, but
// produces a v11 Local Connected contract (No Sandbox, PATH Pi, user-configured
// model) and matches a concrete host repository root instead of the fixed
// installed `/workspace/repository` logical root.
type BoundEnvelope struct {
	repositoryRoot string
	repository     RepositoryIdentity
	template       CoreContractDocument
	templateDigest [32]byte
}

// NewBoundEnvelope proves an absolute, clean repository root and constructs the
// frozen v11 Local Connected template bound to the admitted repository identity
// and the discovered PATH Pi version.
func NewBoundEnvelope(repositoryRoot string, repository RepositoryIdentity, piVersion string) (BoundEnvelope, error) {
	root, err := filepath.Abs(repositoryRoot)
	if err != nil {
		return BoundEnvelope{}, invalidInstalled("repository root", err)
	}
	root = filepath.Clean(root)
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return BoundEnvelope{}, invalidInstalled("repository root", err)
	}
	if !validText(repository.Name) || !validLowerHex(repository.SourceRevision, 40) {
		return BoundEnvelope{}, invalidInstalled("repository identity", nil)
	}
	if !validText(piVersion) {
		return BoundEnvelope{}, invalidInstalled("Pi version", nil)
	}
	document := CoreContractDocument{
		SchemaVersion: CoreContractSchemaVersionV11,
		Revision:      11,
		Execution: ExecutionContract{
			Output: OutputContract{
				ResultSchemaVersion:  agent.ResultSchemaVersion,
				ArtifactLocatorScope: "execution_workspace_relative",
				CheckBinding:         "acceptance_criterion_id",
				UnknownPolicy:        "explicit",
			},
			RequiredCapabilities: cloneStrings(requiredCapabilitiesLocalConnected),
		},
		Candidate: CandidateCombination{
			Runtime: RuntimeCandidate{
				Package:   "@earendil-works/pi-coding-agent",
				Version:   piVersion,
				Transport: append([]string{"pi"}, requiredPiArgumentsLocalConnected...),
			},
			Sandbox: SandboxCandidate{Provider: localConnectedSandboxProvider, Engine: localConnectedSandboxEngine},
			Model:   ModelCandidate{IdentityStatus: localConnectedModelStatus, AuthenticationType: localConnectedModelAuth},
		},
		EntryProbe: EntryProbeContract{
			PassCriteria:                          cloneStrings(requiredProbePassCriteriaLocalConnected),
			FailureClasses:                        cloneStrings(requiredProbeFailureClassesLocalConnected),
			StopCriteria:                          cloneStrings(requiredProbeStopCriteriaLocalConnected),
			CapsuleChangeBehavior:                 "MARK_STALE_AND_RESTART",
			FallbackCandidateRequiresUserApproval: true,
		},
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		return BoundEnvelope{}, invalidInstalled("template", err)
	}
	return BoundEnvelope{repositoryRoot: root, repository: repository, template: document, templateDigest: sha256.Sum256(encoded)}, nil
}

func (envelope BoundEnvelope) Repository() RepositoryIdentity { return envelope.repository }

// CapabilityEnvelope exposes the Local Connected capability surface: Pi's native
// runtime capabilities plus the explicit no-Sandbox disclosure; no sandbox.*
// capability is claimed.
func (envelope BoundEnvelope) CapabilityEnvelope() domain.CapabilityEnvelope {
	capabilities := make(domain.CapabilityEnvelope, len(envelope.template.Execution.RequiredCapabilities))
	for _, capability := range envelope.template.Execution.RequiredCapabilities {
		capabilities[capability] = true
	}
	return capabilities
}

// MatchesRepositoryRoot proves the declared Room workspace root equals the exact
// repository root this envelope is bound to.
func (envelope BoundEnvelope) MatchesRepositoryRoot(repositoryRoot string) bool {
	return envelope.repositoryRoot != "" && repositoryRoot == envelope.repositoryRoot
}

// Declare freezes the user-governed Task declaration against this Room's
// repository. Unlike InstalledEnvelope, writable files and verification commands
// are validated as safe relative paths and safe argv, not restricted to Go.
func (envelope BoundEnvelope) Declare(input UserTaskDeclaration) (DeclaredUserTask, error) {
	return envelope.declare(input, true)
}

func (envelope BoundEnvelope) declare(input UserTaskDeclaration, checkFilesystem bool) (DeclaredUserTask, error) {
	if input.Resources != nil {
		return DeclaredUserTask{}, unsupported("legacy resource contract")
	}
	if envelope.repositoryRoot == "" || envelope.templateDigest == ([32]byte{}) {
		return DeclaredUserTask{}, unsupported("bound envelope is not loaded")
	}
	if !validText(input.ContractID) || !input.TaskID.Valid() || !input.RoomID.Valid() ||
		!validText(input.Title) || !validText(input.Requirement) || !validTextList(input.Constraints) || !validTextList(input.OutOfScope) {
		return DeclaredUserTask{}, unsupported("Task identity or requirement")
	}
	if !envelope.MatchesRepositoryRoot(input.WorkspaceRoot) {
		return DeclaredUserTask{}, unsupported("Room repository authority")
	}
	writableFiles, err := envelope.validateWritableFiles(input.WritableFiles, checkFilesystem)
	if err != nil {
		return DeclaredUserTask{}, err
	}
	writableDirectories, err := envelope.validateWritableDirectories(input.WritableDirectories, checkFilesystem)
	if err != nil {
		return DeclaredUserTask{}, err
	}
	if len(writableFiles) == 0 && len(writableDirectories) == 0 {
		return DeclaredUserTask{}, unsupported("writable scope")
	}
	if !distinctWritableScopes(writableFiles, writableDirectories) {
		return DeclaredUserTask{}, unsupported("duplicate writable scope")
	}
	commands, err := envelope.freezeBoundVerificationCommands(input.VerificationCommands, input.NoChecks, checkFilesystem)
	if err != nil {
		return DeclaredUserTask{}, err
	}
	criteria, err := freezeCriteria(input.Criteria, len(commands), input.NoChecks)
	if err != nil {
		return DeclaredUserTask{}, err
	}
	frozen := frozenUserTask{
		EnvelopeTemplateDigest: hex.EncodeToString(envelope.templateDigest[:]),
		Repository:             envelope.repository,
		ContractID:             input.ContractID,
		TaskID:                 input.TaskID.String(),
		RoomID:                 input.RoomID.String(),
		Title:                  input.Title,
		Requirement:            input.Requirement,
		Constraints:            cloneStrings(input.Constraints),
		OutOfScope:             cloneStrings(input.OutOfScope),
		Criteria:               criteria,
		WritableFiles:          writableFiles,
		WritableDirectories:    writableDirectories,
		VerificationCommands:   commands,
		NoChecks:               input.NoChecks,
	}
	canonical, err := json.Marshal(frozen)
	if err != nil {
		return DeclaredUserTask{}, unsupported("canonical declaration")
	}
	digest := sha256.Sum256(canonical)
	return DeclaredUserTask{
		frozen:         frozen,
		canonical:      canonical,
		digest:         digest,
		initialPlan:    deterministicInitialPlan(frozen),
		templateDigest: envelope.templateDigest,
	}, nil
}

// RestoreDeclaredUserTask rehydrates a persisted canonical declaration after
// restart, re-validating it against this envelope's bound repository.
func (envelope BoundEnvelope) RestoreDeclaredUserTask(canonical []byte) (DeclaredUserTask, error) {
	decoder := json.NewDecoder(bytes.NewReader(canonical))
	decoder.DisallowUnknownFields()
	var frozen frozenUserTask
	if err := decoder.Decode(&frozen); err != nil {
		return DeclaredUserTask{}, unsupported("stored declared intent")
	}
	if err := ensureDecoderEOF(decoder); err != nil {
		return DeclaredUserTask{}, unsupported("stored declared intent")
	}
	if frozen.Resources != nil {
		return DeclaredUserTask{}, unsupported("legacy intent contains resources")
	}
	recanonical, err := json.Marshal(frozen)
	if err != nil || !bytes.Equal(recanonical, canonical) {
		return DeclaredUserTask{}, unsupported("non-canonical stored declared intent")
	}
	taskID, err := domain.ParseTaskID(frozen.TaskID)
	if err != nil {
		return DeclaredUserTask{}, unsupported("stored Task identity")
	}
	roomID, err := domain.ParseRoomID(frozen.RoomID)
	if err != nil {
		return DeclaredUserTask{}, unsupported("stored Room identity")
	}
	criteria := make([]UserAcceptanceCriterion, len(frozen.Criteria))
	for index, criterion := range frozen.Criteria {
		criterionID, err := domain.ParseCriterionID(criterion.ID)
		if err != nil {
			return DeclaredUserTask{}, unsupported("stored criterion identity")
		}
		criteria[index] = UserAcceptanceCriterion{
			ID: criterionID, Title: criterion.Title, Description: criterion.Description,
			VerificationCommandIndexes: append([]int(nil), criterion.VerificationCommandIndexes...),
		}
	}
	commands := make([]UserVerificationCommand, len(frozen.VerificationCommands))
	for index, command := range frozen.VerificationCommands {
		commands[index] = UserVerificationCommand{Argv: cloneStrings(command.Argv), WorkingDirectory: command.WorkingDirectory}
	}
	restored, err := envelope.declare(UserTaskDeclaration{
		ContractID: frozen.ContractID, TaskID: taskID, RoomID: roomID, WorkspaceRoot: envelope.repositoryRoot,
		Title: frozen.Title, Requirement: frozen.Requirement, Constraints: cloneStrings(frozen.Constraints),
		OutOfScope: cloneStrings(frozen.OutOfScope), Criteria: criteria, WritableFiles: cloneStrings(frozen.WritableFiles),
		WritableDirectories: cloneStrings(frozen.WritableDirectories), VerificationCommands: commands, NoChecks: frozen.NoChecks,
	}, false)
	if err != nil || !bytes.Equal(restored.canonical, canonical) || restored.frozen.Repository != frozen.Repository || restored.frozen.EnvelopeTemplateDigest != frozen.EnvelopeTemplateDigest {
		return DeclaredUserTask{}, unsupported("stored declared intent authority")
	}
	return restored, nil
}

func (envelope BoundEnvelope) Accept(intent DeclaredUserTask, plan UserTaskPlan, snapshotID domain.ContextSnapshotID, snapshotDigest string) (CoreContract, error) {
	if !validAcceptIdentity(envelope.templateDigest, envelope.repository, intent, snapshotID, snapshotDigest) {
		return CoreContract{}, unsupported("acceptance identity or Snapshot")
	}
	return materializeAcceptedContract(envelope.template, envelope.repository, intent, plan, snapshotID, snapshotDigest)
}

func (envelope BoundEnvelope) validateWritableFiles(values []string, checkFilesystem bool) ([]string, error) {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if !safeWritablePath(value) || !addUnique(seen, value) {
			return nil, unsupported("writable file")
		}
		if checkFilesystem {
			if err := validateRepositoryFile(envelope.repositoryRoot, value); err != nil {
				return nil, err
			}
		}
		result = append(result, value)
	}
	return result, nil
}

func (envelope BoundEnvelope) validateWritableDirectories(values []string, checkFilesystem bool) ([]string, error) {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if !safeWritablePath(value) || !addUnique(seen, value) {
			return nil, unsupported("writable directory")
		}
		if checkFilesystem {
			if err := validateRepositoryDirectory(envelope.repositoryRoot, value, true); err != nil {
				return nil, err
			}
		}
		result = append(result, value)
	}
	return result, nil
}

func distinctWritableScopes(files, directories []string) bool {
	seen := make(map[string]struct{}, len(files)+len(directories))
	for _, value := range files {
		seen[value] = struct{}{}
	}
	for _, value := range directories {
		if _, exists := seen[value]; exists {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}

func validateRepositoryDirectory(root, relative string, allowMissingLeaf bool) error {
	if relative == "." {
		return nil
	}
	parts := strings.Split(relative, "/")
	current := root
	for index, part := range parts {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			if allowMissingLeaf && errors.Is(err, os.ErrNotExist) && index == len(parts)-1 {
				return nil
			}
			return unsupported("repository directory")
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return unsupported("symlink repository directory or parent")
		}
		if !info.IsDir() {
			return unsupported("repository directory")
		}
	}
	return nil
}

// safeWritablePath accepts any safe relative repository path (not Go-specific),
// excluding escapes, glob metacharacters, and .git components.
func safeWritablePath(value string) bool {
	if !validRelativePath(value) || strings.ContainsAny(value, "*?[") {
		return false
	}
	for _, component := range strings.Split(value, "/") {
		if component == ".git" || component == "" {
			return false
		}
	}
	return true
}

// WritableScopeAllows reports whether a normalized repository-relative path is
// exactly declared as a writable file or is the declared directory itself or a
// descendant. It is lexical scope validation; callers that touch the filesystem
// must separately reject symlink escapes at the repository boundary.
func WritableScopeAllows(relativePath string, writableFiles, writableDirectories []string) bool {
	if !safeWritablePath(relativePath) {
		return false
	}
	for _, file := range writableFiles {
		if safeWritablePath(file) && relativePath == file {
			return true
		}
	}
	for _, directory := range writableDirectories {
		if safeWritablePath(directory) && (relativePath == directory || strings.HasPrefix(relativePath, directory+"/")) {
			return true
		}
	}
	return false
}

func (envelope BoundEnvelope) freezeBoundVerificationCommands(values []UserVerificationCommand, noChecks, checkFilesystem bool) ([]frozenVerificationCommand, error) {
	if noChecks {
		if len(values) != 0 {
			return nil, unsupported("no-check task has verification commands")
		}
		return make([]frozenVerificationCommand, 0), nil
	}
	if len(values) == 0 {
		return nil, unsupported("verification commands")
	}
	result := make([]frozenVerificationCommand, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		if !safeVerificationArgv(value.Argv) || !safeVerificationWorkingDirectory(value.WorkingDirectory) {
			return nil, unsupported("verification command")
		}
		if checkFilesystem && value.WorkingDirectory != "" && value.WorkingDirectory != "." {
			if err := validateRepositoryDirectory(envelope.repositoryRoot, value.WorkingDirectory, false); err != nil {
				return nil, err
			}
		}
		argv := cloneStrings(value.Argv)
		key, _ := json.Marshal(frozenVerificationCommand{Argv: argv, WorkingDirectory: value.WorkingDirectory})
		if !addUnique(seen, string(key)) {
			return nil, unsupported("duplicate verification command")
		}
		result = append(result, frozenVerificationCommand{Argv: argv, WorkingDirectory: value.WorkingDirectory})
	}
	return result, nil
}

func safeVerificationWorkingDirectory(value string) bool {
	return value == "" || value == "." || safeWritablePath(value)
}

// safeVerificationArgv accepts a non-empty argv whose tokens contain no shell
// metacharacters, command substitution, or redirection. It deliberately does not
// restrict the command to `go test`: Local Connected Tasks may target any
// repository language, and Chora never executes these commands itself.
func safeVerificationArgv(argv []string) bool {
	if len(argv) == 0 {
		return false
	}
	for _, argument := range argv {
		if !validText(argument) || unsafeCommandToken(argument) {
			return false
		}
	}
	return true
}
