package speccoding

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/Yangyang96/chora/internal/agent"
	"github.com/Yangyang96/chora/internal/domain"
)

const (
	CoreContractSchemaVersion          = "chora.spec-coding-core.v1"
	CoreContractSchemaVersionV2        = "chora.spec-coding-core.v2"
	CoreContractSchemaVersionV3        = "chora.spec-coding-core.v3"
	CoreContractSchemaVersionV4        = "chora.spec-coding-core.v4"
	CoreContractSchemaVersionV5        = "chora.spec-coding-core.v5"
	CoreContractSchemaVersionV6        = "chora.spec-coding-core.v6"
	CoreContractSchemaVersionV7        = "chora.spec-coding-core.v7"
	CoreContractSchemaVersionV8        = "chora.spec-coding-core.v8"
	CoreContractSchemaVersionV9        = "chora.spec-coding-core.v9"
	CoreContractSchemaVersionV10       = "chora.spec-coding-core.v10"
	CoreContractSchemaVersionV11       = "chora.spec-coding-core.v11"
	CoreContractSchemaVersionV12       = "chora.spec-coding-core.v12"
	SupersededPlaceholderContextDigest = "80b62373d85c6d6676a935b2ba1487f8466530aa7fc5d506decf4839a27b336d"

	localConnectedSandboxProvider = "no-sandbox"
	localConnectedSandboxEngine   = "host"
	localConnectedModelStatus     = "USER_CONFIGURED"
	localConnectedModelAuth       = "user_configured"
	// LocalConnectedNoSandboxCapability is the capability a Local Connected
	// (v11) charter carries to disclose No Sandbox; it is the precise marker the
	// application uses to skip the absent independent verifier.
	LocalConnectedNoSandboxCapability = "disclosure.local_connected_no_sandbox"
	v2RuntimeConfigSHA256             = "bd9aa96442eecb57b7b1fa32c4153ebe5e30f9a9541bab0964320b7b82e93aee"
	v2SandboxPolicySHA256             = "dbec78436e8700dd457cd1c5f0cfd4041123ed229562ea495cc1c0028a270ccb"
	v3RuntimeConfigSHA256             = "485d3cbbbf8eec76caa0734727f65afb8eee81d8685d67043e5fd5c57e3cb0e5"
	v3SandboxPolicySHA256             = "c52edc1634dd521f2d77a4223f25bdf9d9fe57d84737464e35c79106eb3ac59f"
	v4RuntimeConfigSHA256             = "e89e375f8dc002d37e7b46e4242ae8cddea118844f0335be63f2f8bb910eb3a9"
	v5RuntimeConfigSHA256             = "0f5b80eb138df1cd9fdb21b7760a3544e1bbbb9a038d468c49349d3e5a25dc9e"
	v6RuntimeConfigSHA256             = "b4bb9e6e599732e31a5f20ff95f48587c8b702a63c754940b0a4631450f1a4e9"
	v4SandboxPolicySHA256             = "efe8918d0c9c4232c8292941f573fe386d93a3faa051329c883c9b8349b66f04"
	v2ColimaSHA256                    = "55278419bd4288e4ab11eb30be13f5e655f3886c0979b68e435df910dbc9bbdf"
	v2DockerClientSHA256              = "e8a1e5351c4d12337a4ee2b54523bc0107b4d13f795c9d6e791b9e4cf835f385"
	v2NodeImageDigest                 = "sha256:4a4884e8a44826194dff92ba316264f392056cbe243dcc9fd3551e71cea02b90"
	v2GoImageDigest                   = "sha256:1ecb7edf62a0408027bd5729dfd6b1b8766e578e8df93995b225dfd0944eb651"
	piOpenAICodexCatalogSHA256        = "4a73818291987693fcb53e9e61b4be3429ffd78b3f64ea877a34c5f9928c89e1"
	piRuntimeNPMIntegrity             = "sha512-ncAqFrG+iybuPGOhMiZoEHkEzTpJgz3guYD32pD+M7ucc0WeHmauP6wa7qwP8V/KWvsZDVNa5XGsdZ7fkC7w7A=="
	piRuntimeNPMIntegrityV5           = "sha512-l4E+B7hgXKWddRo8bC/eSue2aWZjEgJ9xIpf5p0Og+lq8a2TArCwJ0HCoCPCgaBP/tN4zbYH/wOwvx9pJpeLCA=="
	starpointRootCAPath               = "contracts/g2-m1a/starpoint-root-ca-2048-g2.pem"
	starpointRootCAContainerPath      = "/run/chora/trust/starpoint-root-ca-2048-g2.pem"
	starpointRootCASHA256             = "3444d3f6d9ef36946ff29ef382e402017a96ff6c97f7b8ae08643acceb2e0dac"
)

var ErrInvalidCoreContract = errors.New("invalid spec coding core contract")

type CoreContractDocument struct {
	SchemaVersion string               `json:"schema_version"`
	ContractID    string               `json:"contract_id"`
	Revision      int                  `json:"revision"`
	Task          TaskContract         `json:"task"`
	Requirement   RequirementContract  `json:"requirement"`
	Spec          SpecContract         `json:"spec"`
	Acceptance    AcceptanceContract   `json:"acceptance"`
	TechnicalPlan TechnicalPlan        `json:"technical_plan"`
	Decisions     []Decision           `json:"decisions"`
	Risks         []Risk               `json:"risks"`
	Unknowns      []Unknown            `json:"unknowns"`
	Execution     ExecutionContract    `json:"execution"`
	Candidate     CandidateCombination `json:"candidate"`
	EntryProbe    EntryProbeContract   `json:"entry_probe"`
}

type TaskContract struct {
	ID                     string                        `json:"id"`
	RoomID                 string                        `json:"room_id"`
	Title                  string                        `json:"title"`
	Goal                   string                        `json:"goal"`
	Repository             RepositoryTarget              `json:"repository,omitzero"`
	Resources              []ExecutionRepositoryResource `json:"resources,omitempty"`
	ResourceSnapshotDigest string                        `json:"resource_snapshot_digest,omitempty"`
}

type RepositoryTarget struct {
	Name         string `json:"name"`
	Locator      string `json:"locator"`
	BaseRevision string `json:"base_revision"`
}

type RequirementContract struct {
	Statement   string   `json:"statement"`
	Constraints []string `json:"constraints"`
}

type SpecContract struct {
	DesiredBehavior []string `json:"desired_behavior"`
	OutOfScope      []string `json:"out_of_scope"`
}

type AcceptanceContract struct {
	Criteria             []AcceptanceCriterionContract `json:"criteria"`
	VerificationCommands []BoundedCommand              `json:"verification_commands,omitempty"`
	NoChecks             bool                          `json:"no_checks,omitempty"`
}

type AcceptanceCriterionContract struct {
	ID                     string   `json:"id"`
	Title                  string   `json:"title"`
	Description            string   `json:"description"`
	Verification           string   `json:"verification"`
	VerificationCommandIDs []string `json:"verification_command_ids,omitempty"`
}

type TechnicalPlan struct {
	Summary string     `json:"summary"`
	Steps   []PlanStep `json:"steps"`
}

type PlanStep struct {
	ID          string   `json:"id"`
	Description string   `json:"description"`
	Files       []string `json:"files"`
}

type Decision struct {
	ID         string `json:"id"`
	Question   string `json:"question"`
	Resolution string `json:"resolution"`
	Rationale  string `json:"rationale"`
}

type Risk struct {
	ID          string `json:"id"`
	Severity    string `json:"severity"`
	Description string `json:"description"`
	Mitigation  string `json:"mitigation"`
}

type Unknown struct {
	ID             string `json:"id"`
	Question       string `json:"question"`
	ResolutionGate string `json:"resolution_gate"`
}

type ExecutionContract struct {
	Input                FrozenInput       `json:"input"`
	Boundary             ExecutionBoundary `json:"boundary"`
	Output               OutputContract    `json:"output"`
	RequiredCapabilities []string          `json:"required_capabilities"`
}

type FrozenInput struct {
	ContextSnapshotID     string `json:"context_snapshot_id"`
	ContextSnapshotDigest string `json:"context_snapshot_digest"`
}

type ExecutionBoundary struct {
	WritableFiles       []string         `json:"writable_files"`
	WritableDirectories []string         `json:"writable_directories,omitempty"`
	Commands            []BoundedCommand `json:"commands"`
	TestCommandIDs      []string         `json:"test_command_ids"`
}

type BoundedCommand struct {
	ID               string   `json:"id"`
	Argv             []string `json:"argv"`
	WorkingDirectory string   `json:"working_directory,omitempty"`
}

type OutputContract struct {
	ResultSchemaVersion  string `json:"result_schema_version"`
	ArtifactLocatorScope string `json:"artifact_locator_scope"`
	CheckBinding         string `json:"check_binding"`
	UnknownPolicy        string `json:"unknown_policy"`
}

type CandidateCombination struct {
	Runtime    RuntimeCandidate `json:"runtime"`
	Sandbox    SandboxCandidate `json:"sandbox"`
	Model      ModelCandidate   `json:"model"`
	BaseImages []BaseImage      `json:"base_images"`
	Policy     ConfigReference  `json:"policy"`
}

type RuntimeCandidate struct {
	Package      string          `json:"package"`
	Version      string          `json:"version"`
	NPMIntegrity string          `json:"npm_integrity"`
	Transport    []string        `json:"transport"`
	Config       ConfigReference `json:"config"`
}

type ConfigReference struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type SandboxCandidate struct {
	Provider      string `json:"provider"`
	Version       string `json:"version"`
	SHA256        string `json:"sha256"`
	Engine        string `json:"engine"`
	EngineVersion string `json:"engine_version"`
	EngineSHA256  string `json:"engine_sha256"`
}

type ModelCandidate struct {
	Provider                 string `json:"provider"`
	ModelID                  string `json:"model_id"`
	ModelDigest              string `json:"model_digest"`
	IdentityStatus           string `json:"identity_status"`
	Endpoint                 string `json:"endpoint"`
	AuthenticationType       string `json:"authentication_type"`
	DependencyScope          string `json:"dependency_scope,omitempty"`
	ProductInterfaceExposure string `json:"product_interface_exposure,omitempty"`
}

type BaseImage struct {
	Reference string `json:"reference"`
	Digest    string `json:"digest"`
	Platform  string `json:"platform"`
	Purpose   string `json:"purpose"`
}

type EntryProbeContract struct {
	PassCriteria                          []string `json:"pass_criteria"`
	FailureClasses                        []string `json:"failure_classes"`
	StopCriteria                          []string `json:"stop_criteria"`
	CapsuleChangeBehavior                 string   `json:"capsule_change_behavior"`
	FallbackCandidateRequiresUserApproval bool     `json:"fallback_candidate_requires_user_approval"`
}

type CoreContract struct {
	document  CoreContractDocument
	canonical []byte
	digest    string
}

func DecodeCoreContract(data []byte) (CoreContract, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var document CoreContractDocument
	if err := decoder.Decode(&document); err != nil {
		return CoreContract{}, fmt.Errorf("%w: decode: %v", ErrInvalidCoreContract, err)
	}
	if err := ensureDecoderEOF(decoder); err != nil {
		return CoreContract{}, err
	}
	return NewCoreContract(document)
}

func NewCoreContract(document CoreContractDocument) (CoreContract, error) {
	if err := validateCoreContract(document); err != nil {
		return CoreContract{}, err
	}
	canonical, err := json.Marshal(document)
	if err != nil {
		return CoreContract{}, fmt.Errorf("%w: canonicalize: %v", ErrInvalidCoreContract, err)
	}
	var frozen CoreContractDocument
	if err := json.Unmarshal(canonical, &frozen); err != nil {
		return CoreContract{}, fmt.Errorf("%w: freeze: %v", ErrInvalidCoreContract, err)
	}
	digest := sha256.Sum256(canonical)
	return CoreContract{document: frozen, canonical: canonical, digest: hex.EncodeToString(digest[:])}, nil
}

func (contract CoreContract) Document() CoreContractDocument {
	var document CoreContractDocument
	if err := json.Unmarshal(contract.canonical, &document); err != nil {
		panic(fmt.Sprintf("decode validated immutable core contract: %v", err))
	}
	return document
}

func (contract CoreContract) CanonicalJSON() []byte {
	return bytes.Clone(contract.canonical)
}

func (contract CoreContract) DigestHex() string {
	return contract.digest
}

func ensureDecoderEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("%w: trailing JSON document", ErrInvalidCoreContract)
		}
		return fmt.Errorf("%w: trailing data: %v", ErrInvalidCoreContract, err)
	}
	return nil
}

func validateCoreContract(document CoreContractDocument) error {
	if document.SchemaVersion == CoreContractSchemaVersionV12 {
		return validateResourceCoreContract(document)
	}
	if len(document.Task.Resources) > 0 || document.Task.ResourceSnapshotDigest != "" {
		return invalid("legacy contract contains v2 resources")
	}
	if !validText(document.ContractID) || !validCoreIdentity(document.SchemaVersion, document.Revision) {
		return invalid("contract identity")
	}
	if _, err := domain.ParseTaskID(document.Task.ID); err != nil {
		return invalid("task id")
	}
	if _, err := domain.ParseRoomID(document.Task.RoomID); err != nil {
		return invalid("room id")
	}
	if !validText(document.Task.Title) || !validText(document.Task.Goal) || !validText(document.Task.Repository.Name) || document.Task.Repository.Locator != "." || !validLowerHex(document.Task.Repository.BaseRevision, 40) {
		return invalid("task repository target")
	}
	if !validText(document.Requirement.Statement) || !validTextList(document.Requirement.Constraints) || !validTextList(document.Spec.DesiredBehavior) || !validTextList(document.Spec.OutOfScope) {
		return invalid("requirement or spec")
	}
	if err := validateAcceptance(document.Acceptance, document.SchemaVersion); err != nil {
		return err
	}
	writeSet, err := validateWriteBoundary(document.Execution.Boundary, document.SchemaVersion, document.Acceptance.NoChecks)
	if err != nil {
		return err
	}
	if document.SchemaVersion == CoreContractSchemaVersionV11 && !validV11VerificationBoundary(document.Acceptance.VerificationCommands, document.Execution.Boundary) {
		return invalid("verification command boundary")
	}
	if err := validateTechnicalPlan(document.TechnicalPlan, writeSet); err != nil {
		return err
	}
	if err := validatePlanningRecords(document.Decisions, document.Risks, document.Unknowns); err != nil {
		return err
	}
	if _, err := domain.ParseContextSnapshotID(document.Execution.Input.ContextSnapshotID); err != nil || !validLowerHex(document.Execution.Input.ContextSnapshotDigest, 64) {
		return invalid("frozen context snapshot")
	}
	if (document.SchemaVersion == CoreContractSchemaVersionV5 || document.SchemaVersion == CoreContractSchemaVersionV6 || document.SchemaVersion == CoreContractSchemaVersionV7 || document.SchemaVersion == CoreContractSchemaVersionV8 || document.SchemaVersion == CoreContractSchemaVersionV9 || document.SchemaVersion == CoreContractSchemaVersionV10 || document.SchemaVersion == CoreContractSchemaVersionV11) && document.Execution.Input.ContextSnapshotDigest == SupersededPlaceholderContextDigest {
		return invalid("unsupported placeholder context snapshot digest")
	}
	if document.Execution.Output.ResultSchemaVersion != agent.ResultSchemaVersion || document.Execution.Output.ArtifactLocatorScope != "execution_workspace_relative" || document.Execution.Output.CheckBinding != "acceptance_criterion_id" || document.Execution.Output.UnknownPolicy != "explicit" {
		return invalid("output contract")
	}
	if !sameStringSet(document.Execution.RequiredCapabilities, requiredCapabilitiesFor(document.SchemaVersion)) {
		return invalid("required runtime and sandbox capabilities")
	}
	if err := validateCandidate(document.Candidate, document.Unknowns, document.SchemaVersion); err != nil {
		return err
	}
	if err := validateEntryProbe(document.EntryProbe, document.SchemaVersion); err != nil {
		return err
	}
	return nil
}

func validateAcceptance(acceptance AcceptanceContract, schemaVersion string) error {
	if len(acceptance.Criteria) == 0 {
		return invalid("acceptance criteria")
	}
	structured := schemaVersion == CoreContractSchemaVersionV8 || schemaVersion == CoreContractSchemaVersionV9 || schemaVersion == CoreContractSchemaVersionV10 || schemaVersion == CoreContractSchemaVersionV11
	if acceptance.NoChecks && schemaVersion != CoreContractSchemaVersionV11 {
		return invalid("legacy no-check authority")
	}
	commands := make(map[string]struct{}, len(acceptance.VerificationCommands))
	if structured {
		if acceptance.NoChecks && len(acceptance.VerificationCommands) != 0 {
			return invalid("no-check verification commands")
		}
		if !acceptance.NoChecks && len(acceptance.VerificationCommands) == 0 {
			return invalid("acceptance verification commands")
		}
		for _, command := range acceptance.VerificationCommands {
			if !validText(command.ID) || len(command.Argv) == 0 || !validTextList(command.Argv) || !addUnique(commands, command.ID) ||
				(schemaVersion != CoreContractSchemaVersionV11 && command.WorkingDirectory != "") ||
				(schemaVersion == CoreContractSchemaVersionV11 && (!safeVerificationArgv(command.Argv) || !safeVerificationWorkingDirectory(command.WorkingDirectory))) {
				return invalid("acceptance verification command")
			}
		}
	} else if len(acceptance.VerificationCommands) != 0 {
		return invalid("legacy acceptance structured verification authority")
	}
	seen := map[string]struct{}{}
	for _, criterion := range acceptance.Criteria {
		if _, err := domain.ParseCriterionID(criterion.ID); err != nil || !validText(criterion.Title) || !validText(criterion.Description) || !validText(criterion.Verification) {
			return invalid("acceptance criterion")
		}
		if !addUnique(seen, criterion.ID) {
			return invalid("duplicate acceptance criterion")
		}
		if structured {
			if acceptance.NoChecks {
				if len(criterion.VerificationCommandIDs) != 0 || criterion.Verification != explicitNoChecksVerification {
					return invalid("no-check acceptance criterion")
				}
				continue
			}
			if len(criterion.VerificationCommandIDs) == 0 {
				return invalid("acceptance criterion verification binding")
			}
			bound := make(map[string]struct{}, len(criterion.VerificationCommandIDs))
			for _, commandID := range criterion.VerificationCommandIDs {
				if _, ok := commands[commandID]; !ok || !addUnique(bound, commandID) {
					return invalid("acceptance criterion verification binding")
				}
			}
		} else if len(criterion.VerificationCommandIDs) != 0 {
			return invalid("legacy criterion structured verification authority")
		}
	}
	return nil
}

func validateWriteBoundary(boundary ExecutionBoundary, schemaVersion string, noChecks bool) (map[string]struct{}, error) {
	v11 := schemaVersion == CoreContractSchemaVersionV11
	if (!v11 && (len(boundary.WritableFiles) == 0 || len(boundary.WritableDirectories) != 0 || noChecks)) ||
		(v11 && len(boundary.WritableFiles)+len(boundary.WritableDirectories) == 0) ||
		(noChecks && (len(boundary.Commands) != 0 || len(boundary.TestCommandIDs) != 0)) ||
		(!noChecks && (len(boundary.Commands) == 0 || len(boundary.TestCommandIDs) == 0)) {
		return nil, invalid("execution boundary")
	}
	writes := map[string]struct{}{}
	for _, file := range boundary.WritableFiles {
		if (v11 && !safeWritablePath(file)) || (!v11 && !validRelativePath(file)) || !addUnique(writes, file) {
			return nil, invalid("writable file")
		}
	}
	for _, directory := range boundary.WritableDirectories {
		if !safeWritablePath(directory) || !addUnique(writes, directory) {
			return nil, invalid("writable directory")
		}
	}
	commands := map[string]struct{}{}
	for _, command := range boundary.Commands {
		if !validText(command.ID) || len(command.Argv) == 0 || !validTextList(command.Argv) || !addUnique(commands, command.ID) ||
			(v11 && (!safeVerificationArgv(command.Argv) || !safeVerificationWorkingDirectory(command.WorkingDirectory))) ||
			(!v11 && command.WorkingDirectory != "") {
			return nil, invalid("bounded command")
		}
	}
	tests := map[string]struct{}{}
	for _, id := range boundary.TestCommandIDs {
		if _, ok := commands[id]; !ok || !addUnique(tests, id) {
			return nil, invalid("test command reference")
		}
	}
	return writes, nil
}

func validV11VerificationBoundary(acceptance []BoundedCommand, boundary ExecutionBoundary) bool {
	commands := boundary.Commands
	if len(commands) == len(acceptance)+1 && len(boundary.WritableDirectories) == 0 && legacyV11FormatCommand(commands[0], boundary.WritableFiles) {
		// Pre-P3 Bound v11 contracts included one Chora-generated gofmt command.
		// Keep those immutable canonical contracts decodable while all newly
		// materialized v11 contracts use only the declared verification commands.
		commands = commands[1:]
	}
	if len(acceptance) != len(commands) || len(boundary.TestCommandIDs) != len(acceptance) {
		return false
	}
	for index := range acceptance {
		if acceptance[index].ID != commands[index].ID || acceptance[index].WorkingDirectory != commands[index].WorkingDirectory || !slicesEqual(acceptance[index].Argv, commands[index].Argv) || boundary.TestCommandIDs[index] != acceptance[index].ID {
			return false
		}
	}
	return true
}

func legacyV11FormatCommand(command BoundedCommand, files []string) bool {
	return command.ID == "format" && command.WorkingDirectory == "" && slicesEqual(command.Argv, append([]string{"gofmt", "-w"}, files...))
}

// CanonicalCommandText renders the exact shell command text used in the Pi
// prompt and matched against Pi's command observations. Empty and "." working
// directories mean the repository root; a subdirectory is represented by an
// explicit cd. Every word that is not safe unquoted POSIX shell text is quoted.
func CanonicalCommandText(command BoundedCommand) (string, error) {
	if !safeVerificationArgv(command.Argv) || !safeVerificationWorkingDirectory(command.WorkingDirectory) {
		return "", invalid("canonical command text")
	}
	argv := make([]string, len(command.Argv))
	for index, argument := range command.Argv {
		argv[index] = quoteShellWord(argument)
	}
	text := strings.Join(argv, " ")
	if command.WorkingDirectory != "" && command.WorkingDirectory != "." {
		text = "cd " + quoteShellWord(command.WorkingDirectory) + " && " + text
	}
	return text, nil
}

func quoteShellWord(value string) string {
	for index := 0; index < len(value); index++ {
		character := value[index]
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || strings.ContainsRune("_@%+=:,./-", rune(character)) {
			continue
		}
		return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
	}
	return value
}

func validateTechnicalPlan(plan TechnicalPlan, writes map[string]struct{}) error {
	if !validText(plan.Summary) || len(plan.Steps) == 0 {
		return invalid("technical plan")
	}
	seen := map[string]struct{}{}
	for _, step := range plan.Steps {
		if !validText(step.ID) || !validText(step.Description) || len(step.Files) == 0 || !addUnique(seen, step.ID) {
			return invalid("technical plan step")
		}
		for _, file := range step.Files {
			if _, ok := writes[file]; !ok {
				return invalid("technical plan file outside write boundary")
			}
		}
	}
	return nil
}

func validatePlanningRecords(decisions []Decision, risks []Risk, unknowns []Unknown) error {
	if len(decisions) == 0 || len(risks) == 0 || len(unknowns) == 0 {
		return invalid("decision, risk, or unknown records")
	}
	seen := map[string]struct{}{}
	for _, decision := range decisions {
		if !validText(decision.ID) || !validText(decision.Question) || !validText(decision.Resolution) || !validText(decision.Rationale) || !addUnique(seen, decision.ID) {
			return invalid("decision")
		}
	}
	for _, risk := range risks {
		if !validText(risk.ID) || !oneOf(risk.Severity, "low", "medium", "high", "critical") || !validText(risk.Description) || !validText(risk.Mitigation) || !addUnique(seen, risk.ID) {
			return invalid("risk")
		}
	}
	for _, unknown := range unknowns {
		if !validText(unknown.ID) || !validText(unknown.Question) || !validText(unknown.ResolutionGate) || !addUnique(seen, unknown.ID) {
			return invalid("unknown")
		}
	}
	return nil
}

func validateCandidate(candidate CandidateCombination, unknowns []Unknown, schemaVersion string) error {
	if schemaVersion == CoreContractSchemaVersionV11 {
		return validateLocalConnectedCandidate(candidate)
	}
	runtime := candidate.Runtime
	if runtime.Package != "@earendil-works/pi-coding-agent" || !validText(runtime.Version) || !strings.HasPrefix(runtime.NPMIntegrity, "sha512-") || len(runtime.NPMIntegrity) <= len("sha512-") || !validConfigReference(runtime.Config) {
		return invalid("runtime identity")
	}
	if !containsOrdered(runtime.Transport, []string{"pi", "--mode", "rpc", "--no-session"}) || !containsAll(runtime.Transport, requiredPiArgumentsFor(schemaVersion)) {
		return invalid("runtime transport")
	}
	sandbox := candidate.Sandbox
	if !validText(sandbox.Provider) || !validText(sandbox.Version) || !validLowerHex(sandbox.SHA256, 64) || !validText(sandbox.Engine) || !validText(sandbox.EngineVersion) || !validLowerHex(sandbox.EngineSHA256, 64) {
		return invalid("sandbox identity")
	}
	model := candidate.Model
	if !validModelProviderFor(model.Provider, schemaVersion) || !validText(model.ModelID) || !validText(model.Endpoint) || !validText(model.AuthenticationType) {
		return invalid("model identity")
	}
	switch model.IdentityStatus {
	case "RESOLVED":
		if !validDigest(model.ModelDigest) {
			return invalid("resolved model digest")
		}
	case "UNRESOLVED_BEFORE_ENTRY_PROBE":
		if model.ModelDigest != "" || !hasUnknown(unknowns, "unknown-model-identity", "G2-M2E Entry Probe") {
			return invalid("unresolved model identity")
		}
	default:
		return invalid("model identity status")
	}
	if len(candidate.BaseImages) == 0 {
		return invalid("base images")
	}
	images := map[string]struct{}{}
	for _, image := range candidate.BaseImages {
		if !validText(image.Reference) || !validDigest(image.Digest) || !validText(image.Platform) || !validText(image.Purpose) || !addUnique(images, image.Reference+"@"+image.Digest+"/"+image.Platform) {
			return invalid("base image identity")
		}
	}
	if !validConfigReference(candidate.Policy) {
		return invalid("sandbox policy reference")
	}
	if schemaVersion == CoreContractSchemaVersionV2 {
		if runtime.Version != "0.84.1" || runtime.NPMIntegrity != "sha512-ncAqFrG+iybuPGOhMiZoEHkEzTpJgz3guYD32pD+M7ucc0WeHmauP6wa7qwP8V/KWvsZDVNa5XGsdZ7fkC7w7A==" || runtime.Config.Path != "contracts/g2-m1a/pi-runtime-config.v2.json" || runtime.Config.SHA256 != v2RuntimeConfigSHA256 {
			return invalid("v2 runtime identity")
		}
		if sandbox.Provider != "colima-docker" || sandbox.Version != "0.10.3" || sandbox.SHA256 != v2ColimaSHA256 || sandbox.Engine != "docker" || sandbox.EngineVersion != "29.6.1" || sandbox.EngineSHA256 != v2DockerClientSHA256 {
			return invalid("v2 sandbox identity")
		}
		if candidate.Policy.Path != "contracts/g2-m1a/sandbox-policy.v2.json" || candidate.Policy.SHA256 != v2SandboxPolicySHA256 {
			return invalid("v2 sandbox policy reference")
		}
		if len(candidate.BaseImages) != 2 || candidate.BaseImages[0].Reference != "node:22.19.0-bookworm-slim" || candidate.BaseImages[0].Digest != v2NodeImageDigest || candidate.BaseImages[0].Platform != "linux/arm64" || candidate.BaseImages[0].Purpose != "Pi install stage" || candidate.BaseImages[1].Reference != "golang:1.26.5-bookworm" || candidate.BaseImages[1].Digest != v2GoImageDigest || candidate.BaseImages[1].Platform != "linux/arm64" || candidate.BaseImages[1].Purpose != "Runtime, boundary, and Chora test stage" {
			return invalid("v2 base image identity")
		}
		if model.Provider != "ollama" || model.ModelID != "minimax-m3:cloud" || model.ModelDigest != "sha256:8cd948b96f47afd232cef7d49faf65791d22bd9dbbef74add3b9355d9d75f765" || model.IdentityStatus != "RESOLVED" || model.Endpoint != "http://ollama-boundary:11434/v1" || model.AuthenticationType != "host_ollama_cloud_session" || model.DependencyScope != "replaceable_task_dependency" || model.ProductInterfaceExposure != "none" {
			return invalid("v2 task model boundary")
		}
	}
	if schemaVersion == CoreContractSchemaVersionV3 {
		if runtime.Version != "0.84.1" || runtime.NPMIntegrity != "sha512-ncAqFrG+iybuPGOhMiZoEHkEzTpJgz3guYD32pD+M7ucc0WeHmauP6wa7qwP8V/KWvsZDVNa5XGsdZ7fkC7w7A==" || runtime.Config.Path != "contracts/g2-m1a/pi-runtime-config.v3.json" || runtime.Config.SHA256 != v3RuntimeConfigSHA256 || !slicesEqual(runtime.Transport, append([]string{"pi"}, requiredPiArgumentsV3...)) {
			return invalid("v3 runtime identity")
		}
		if sandbox.Provider != "colima-docker" || sandbox.Version != "0.10.3" || sandbox.SHA256 != v2ColimaSHA256 || sandbox.Engine != "docker" || sandbox.EngineVersion != "29.6.1" || sandbox.EngineSHA256 != v2DockerClientSHA256 {
			return invalid("v3 sandbox identity")
		}
		if candidate.Policy.Path != "contracts/g2-m1a/sandbox-policy.v3.json" || candidate.Policy.SHA256 != v3SandboxPolicySHA256 {
			return invalid("v3 sandbox policy reference")
		}
		if len(candidate.BaseImages) != 2 || candidate.BaseImages[0].Reference != "node:22.19.0-bookworm-slim" || candidate.BaseImages[0].Digest != v2NodeImageDigest || candidate.BaseImages[0].Platform != "linux/arm64" || candidate.BaseImages[0].Purpose != "Pi install stage" || candidate.BaseImages[1].Reference != "golang:1.26.5-bookworm" || candidate.BaseImages[1].Digest != v2GoImageDigest || candidate.BaseImages[1].Platform != "linux/arm64" || candidate.BaseImages[1].Purpose != "Runtime, boundary, and Chora test stage" {
			return invalid("v3 base image identity")
		}
		if model.Provider != "openai-codex" || model.ModelID != "gpt-5.6-sol" || model.ModelDigest != "sha256:"+piOpenAICodexCatalogSHA256 || model.IdentityStatus != "RESOLVED" || model.Endpoint != "https://chatgpt.com/backend-api/codex/responses" || model.AuthenticationType != "openai_codex_oauth" || model.DependencyScope != "replaceable_task_dependency" || model.ProductInterfaceExposure != "none" {
			return invalid("v3 task model boundary")
		}
	}
	if schemaVersion == CoreContractSchemaVersionV4 || schemaVersion == CoreContractSchemaVersionV5 || schemaVersion == CoreContractSchemaVersionV6 || schemaVersion == CoreContractSchemaVersionV7 || schemaVersion == CoreContractSchemaVersionV8 {
		if runtime.Version != "0.84.1" || runtime.NPMIntegrity != piRuntimeNPMIntegrity || runtime.Config.Path != "contracts/g2-m1a/pi-runtime-config.v4.json" || runtime.Config.SHA256 != v4RuntimeConfigSHA256 || !slicesEqual(runtime.Transport, append([]string{"pi"}, requiredPiArgumentsV3...)) {
			return invalid("v4 runtime identity")
		}
		if sandbox.Provider != "colima-docker" || sandbox.Version != "0.10.3" || sandbox.SHA256 != v2ColimaSHA256 || sandbox.Engine != "docker" || sandbox.EngineVersion != "29.6.1" || sandbox.EngineSHA256 != v2DockerClientSHA256 {
			return invalid("v4 sandbox identity")
		}
		if candidate.Policy.Path != "contracts/g2-m1a/sandbox-policy.v4.json" || candidate.Policy.SHA256 != v4SandboxPolicySHA256 {
			return invalid("v4 sandbox policy reference")
		}
		if len(candidate.BaseImages) != 2 || candidate.BaseImages[0].Reference != "node:22.19.0-bookworm-slim" || candidate.BaseImages[0].Digest != v2NodeImageDigest || candidate.BaseImages[0].Platform != "linux/arm64" || candidate.BaseImages[0].Purpose != "Pi install stage" || candidate.BaseImages[1].Reference != "golang:1.26.5-bookworm" || candidate.BaseImages[1].Digest != v2GoImageDigest || candidate.BaseImages[1].Platform != "linux/arm64" || candidate.BaseImages[1].Purpose != "Runtime, boundary, and Chora test stage" {
			return invalid("v4 base image identity")
		}
		if model.Provider != "openai-codex" || model.ModelID != "gpt-5.6-sol" || model.ModelDigest != "sha256:"+piOpenAICodexCatalogSHA256 || model.IdentityStatus != "RESOLVED" || model.Endpoint != "https://chatgpt.com/backend-api/codex/responses" || model.AuthenticationType != "openai_codex_oauth" || model.DependencyScope != "replaceable_task_dependency" || model.ProductInterfaceExposure != "none" {
			return invalid("v4 task model boundary")
		}
	}
	if schemaVersion == CoreContractSchemaVersionV9 || schemaVersion == CoreContractSchemaVersionV10 {
		configPath, configSHA256 := "contracts/g2-m1a/pi-runtime-config.v5.json", v5RuntimeConfigSHA256
		if schemaVersion == CoreContractSchemaVersionV10 {
			configPath, configSHA256 = "contracts/g2-m1a/pi-runtime-config.v6.json", v6RuntimeConfigSHA256
		}
		if runtime.Version != "0.84.2" || runtime.NPMIntegrity != piRuntimeNPMIntegrityV5 || runtime.Config.Path != configPath || runtime.Config.SHA256 != configSHA256 || !slicesEqual(runtime.Transport, append([]string{"pi"}, requiredPiArgumentsV3...)) {
			return invalid("v5 runtime identity")
		}
		if sandbox.Provider != "colima-docker" || sandbox.Version != "0.10.3" || sandbox.SHA256 != v2ColimaSHA256 || sandbox.Engine != "docker" || sandbox.EngineVersion != "29.6.1" || sandbox.EngineSHA256 != v2DockerClientSHA256 {
			return invalid("v5 sandbox identity")
		}
		if candidate.Policy.Path != "contracts/g2-m1a/sandbox-policy.v4.json" || candidate.Policy.SHA256 != v4SandboxPolicySHA256 {
			return invalid("v5 sandbox policy reference")
		}
		if len(candidate.BaseImages) != 2 || candidate.BaseImages[0].Reference != "node:22.19.0-bookworm-slim" || candidate.BaseImages[0].Digest != v2NodeImageDigest || candidate.BaseImages[0].Platform != "linux/arm64" || candidate.BaseImages[0].Purpose != "Pi install stage" || candidate.BaseImages[1].Reference != "golang:1.26.5-bookworm" || candidate.BaseImages[1].Digest != v2GoImageDigest || candidate.BaseImages[1].Platform != "linux/arm64" || candidate.BaseImages[1].Purpose != "Runtime, boundary, and Chora test stage" {
			return invalid("v5 base image identity")
		}
		if model.Provider != "openai-codex" || model.ModelID != "gpt-5.6-sol" || model.ModelDigest != "sha256:"+piOpenAICodexCatalogSHA256 || model.IdentityStatus != "RESOLVED" || model.Endpoint != "https://chatgpt.com/backend-api/codex/responses" || model.AuthenticationType != "openai_codex_oauth" || model.DependencyScope != "replaceable_task_dependency" || model.ProductInterfaceExposure != "none" {
			return invalid("v5 task model boundary")
		}
	}
	return nil
}

func validateLocalConnectedCandidate(candidate CandidateCombination) error {
	runtime := candidate.Runtime
	if runtime.Package != "@earendil-works/pi-coding-agent" || !validText(runtime.Version) || runtime.NPMIntegrity != "" ||
		!slicesEqual(runtime.Transport, append([]string{"pi"}, requiredPiArgumentsLocalConnected...)) ||
		runtime.Config != (ConfigReference{}) {
		return invalid("local connected runtime identity")
	}
	sandbox := candidate.Sandbox
	if sandbox.Provider != localConnectedSandboxProvider || sandbox.Version != "" || sandbox.SHA256 != "" ||
		sandbox.Engine != localConnectedSandboxEngine || sandbox.EngineVersion != "" || sandbox.EngineSHA256 != "" {
		return invalid("local connected sandbox identity")
	}
	model := candidate.Model
	if model.Provider != "" || model.ModelID != "" || model.ModelDigest != "" || model.IdentityStatus != localConnectedModelStatus ||
		model.Endpoint != "" || model.AuthenticationType != localConnectedModelAuth {
		return invalid("local connected model identity")
	}
	if len(candidate.BaseImages) != 0 || candidate.Policy != (ConfigReference{}) {
		return invalid("local connected candidate authority")
	}
	return nil
}

func validateEntryProbe(probe EntryProbeContract, schemaVersion string) error {
	pass, failures, stops := requiredProbeContractsFor(schemaVersion)
	if !sameStringSet(probe.PassCriteria, pass) || !sameStringSet(probe.FailureClasses, failures) || !sameStringSet(probe.StopCriteria, stops) {
		return invalid("entry probe criteria")
	}
	if probe.CapsuleChangeBehavior != "MARK_STALE_AND_RESTART" || !probe.FallbackCandidateRequiresUserApproval {
		return invalid("entry probe invalidation or fallback authority")
	}
	return nil
}

func validCoreIdentity(schemaVersion string, revision int) bool {
	return (schemaVersion == CoreContractSchemaVersion && revision == 1) || (schemaVersion == CoreContractSchemaVersionV2 && revision == 2) || (schemaVersion == CoreContractSchemaVersionV3 && revision == 3) || (schemaVersion == CoreContractSchemaVersionV4 && revision == 4) || (schemaVersion == CoreContractSchemaVersionV5 && revision == 5) || (schemaVersion == CoreContractSchemaVersionV6 && revision == 6) || (schemaVersion == CoreContractSchemaVersionV7 && revision == 7) || (schemaVersion == CoreContractSchemaVersionV8 && revision == 8) || (schemaVersion == CoreContractSchemaVersionV9 && revision == 9) || (schemaVersion == CoreContractSchemaVersionV10 && revision == 10) || (schemaVersion == CoreContractSchemaVersionV11 && revision == 11)
}

func requiredCapabilitiesFor(schemaVersion string) []string {
	if schemaVersion == CoreContractSchemaVersionV11 {
		return requiredCapabilitiesLocalConnected
	}
	if schemaVersion == CoreContractSchemaVersionV4 || schemaVersion == CoreContractSchemaVersionV5 || schemaVersion == CoreContractSchemaVersionV6 || schemaVersion == CoreContractSchemaVersionV7 || schemaVersion == CoreContractSchemaVersionV8 || schemaVersion == CoreContractSchemaVersionV9 || schemaVersion == CoreContractSchemaVersionV10 {
		return requiredCapabilitiesV4
	}
	if schemaVersion == CoreContractSchemaVersionV3 {
		return requiredCapabilitiesV3
	}
	if schemaVersion == CoreContractSchemaVersionV2 {
		return requiredCapabilitiesV2
	}
	return requiredCapabilities
}

func requiredProbeContractsFor(schemaVersion string) ([]string, []string, []string) {
	if schemaVersion == CoreContractSchemaVersionV11 {
		return requiredProbePassCriteriaLocalConnected, requiredProbeFailureClassesLocalConnected, requiredProbeStopCriteriaLocalConnected
	}
	if schemaVersion == CoreContractSchemaVersionV9 || schemaVersion == CoreContractSchemaVersionV10 {
		return requiredProbePassCriteriaV5, requiredProbeFailureClassesV4, requiredProbeStopCriteriaV4
	}
	if schemaVersion == CoreContractSchemaVersionV4 || schemaVersion == CoreContractSchemaVersionV5 || schemaVersion == CoreContractSchemaVersionV6 || schemaVersion == CoreContractSchemaVersionV7 || schemaVersion == CoreContractSchemaVersionV8 {
		return requiredProbePassCriteriaV4, requiredProbeFailureClassesV4, requiredProbeStopCriteriaV4
	}
	if schemaVersion == CoreContractSchemaVersionV3 {
		return requiredProbePassCriteriaV3, requiredProbeFailureClassesV3, requiredProbeStopCriteriaV3
	}
	if schemaVersion == CoreContractSchemaVersionV2 {
		return requiredProbePassCriteriaV2, requiredProbeFailureClassesV2, requiredProbeStopCriteriaV2
	}
	return requiredProbePassCriteria, requiredProbeFailureClasses, requiredProbeStopCriteria
}

func requiredPiArgumentsFor(schemaVersion string) []string {
	if schemaVersion == CoreContractSchemaVersionV11 {
		return requiredPiArgumentsLocalConnected
	}
	if schemaVersion == CoreContractSchemaVersionV3 || schemaVersion == CoreContractSchemaVersionV4 || schemaVersion == CoreContractSchemaVersionV5 || schemaVersion == CoreContractSchemaVersionV6 || schemaVersion == CoreContractSchemaVersionV7 || schemaVersion == CoreContractSchemaVersionV8 || schemaVersion == CoreContractSchemaVersionV9 || schemaVersion == CoreContractSchemaVersionV10 {
		return requiredPiArgumentsV3
	}
	return requiredPiArguments
}

func validModelProviderFor(provider, schemaVersion string) bool {
	if schemaVersion == CoreContractSchemaVersionV11 {
		return provider == ""
	}
	if schemaVersion == CoreContractSchemaVersionV3 || schemaVersion == CoreContractSchemaVersionV4 || schemaVersion == CoreContractSchemaVersionV5 || schemaVersion == CoreContractSchemaVersionV6 || schemaVersion == CoreContractSchemaVersionV7 || schemaVersion == CoreContractSchemaVersionV8 || schemaVersion == CoreContractSchemaVersionV9 || schemaVersion == CoreContractSchemaVersionV10 {
		return provider == "openai-codex"
	}
	return provider == "ollama"
}

func validConfigReference(reference ConfigReference) bool {
	return validRelativePath(reference.Path) && validLowerHex(reference.SHA256, 64)
}

func validRelativePath(value string) bool {
	return validText(value) && !strings.Contains(value, "\\") && !strings.HasPrefix(value, "/") && path.Clean(value) == value && value != "." && !strings.HasPrefix(value, "../")
}

func validText(value string) bool {
	return utf8.ValidString(value) && strings.TrimSpace(value) != "" && !strings.ContainsRune(value, '\x00') && utf8.RuneCountInString(value) <= 16_384
}

func validTextList(values []string) bool {
	if len(values) == 0 {
		return false
	}
	for _, value := range values {
		if !validText(value) {
			return false
		}
	}
	return true
}

func validLowerHex(value string, length int) bool {
	if len(value) != length || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validDigest(value string) bool {
	return strings.HasPrefix(value, "sha256:") && validLowerHex(strings.TrimPrefix(value, "sha256:"), 64)
}

func addUnique(seen map[string]struct{}, value string) bool {
	if _, exists := seen[value]; exists {
		return false
	}
	seen[value] = struct{}{}
	return true
}

func sameStringSet(values, expected []string) bool {
	if len(values) != len(expected) {
		return false
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if !addUnique(seen, value) {
			return false
		}
	}
	for _, value := range expected {
		if _, ok := seen[value]; !ok {
			return false
		}
	}
	return true
}

func containsOrdered(values, prefix []string) bool {
	return len(values) >= len(prefix) && slicesEqual(values[:len(prefix)], prefix)
}

func containsAll(values, expected []string) bool {
	seen := map[string]struct{}{}
	for _, value := range values {
		seen[value] = struct{}{}
	}
	for _, value := range expected {
		if _, ok := seen[value]; !ok {
			return false
		}
	}
	return true
}

func slicesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func hasUnknown(unknowns []Unknown, id, gate string) bool {
	for _, unknown := range unknowns {
		if unknown.ID == id && unknown.ResolutionGate == gate {
			return true
		}
	}
	return false
}

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func invalid(field string) error {
	return fmt.Errorf("%w: %s", ErrInvalidCoreContract, field)
}

var requiredCapabilities = []string{
	"runtime.agent_loop",
	"runtime.jsonl_rpc",
	"runtime.file_edit",
	"runtime.command_execution",
	"runtime.progress_events",
	"runtime.terminal_result",
	"runtime.cooperative_abort",
	"sandbox.workspace_only_writes",
	"sandbox.unrelated_host_unreadable",
	"sandbox.unrelated_host_unwritable",
	"sandbox.task_scoped_credentials",
	"sandbox.explicit_network_policy",
	"sandbox.cpu_limit",
	"sandbox.memory_limit",
	"sandbox.elapsed_time_limit",
	"sandbox.process_limit",
	"sandbox.container_kill_remove",
	"sandbox.process_tree_termination",
	"sandbox.cleanup",
	"sandbox.traceable_identity",
	"sandbox.fail_closed",
}

var requiredCapabilitiesV2 = append(append([]string{}, requiredCapabilities...),
	"runtime.allowlisted_event_projection",
	"runtime.chora_owned_result",
	"runtime.zero_implicit_context",
	"sandbox.ollama_only_network",
	"sandbox.command_timeout",
	"sandbox.attempt_timeout",
	"sandbox.exact_resource_limits",
	"sandbox.fresh_workspace_copy",
	"sandbox.abort_then_force_kill",
)

var requiredCapabilitiesV3 = append(append([]string{}, requiredCapabilities...),
	"runtime.allowlisted_event_projection",
	"runtime.chora_owned_result",
	"runtime.zero_implicit_context",
	"sandbox.openai_codex_only_network",
	"sandbox.explicit_oauth_projection",
	"sandbox.no_host_pi_state",
	"sandbox.command_timeout",
	"sandbox.attempt_timeout",
	"sandbox.exact_resource_limits",
	"sandbox.fresh_workspace_copy",
	"sandbox.abort_then_force_kill",
)

var requiredCapabilitiesV4 = append(append([]string{}, requiredCapabilitiesV3...),
	"sandbox.fixed_enterprise_proxy_upstream",
	"sandbox.explicit_enterprise_ca_projection",
	"sandbox.tls_verification_required",
)

var requiredPiArguments = []string{
	"--mode",
	"rpc",
	"--no-session",
	"--no-extensions",
	"--no-skills",
	"--no-prompt-templates",
	"--no-themes",
	"--no-context-files",
	"--no-approve",
	"--provider",
	"ollama",
	"--model",
	"minimax-m3:cloud",
}

var requiredPiArgumentsV3 = []string{
	"--mode",
	"rpc",
	"--no-session",
	"--no-extensions",
	"--no-skills",
	"--no-prompt-templates",
	"--no-themes",
	"--no-context-files",
	"--no-approve",
	"--provider",
	"openai-codex",
	"--model",
	"gpt-5.6-sol",
}

// requiredPiArgumentsLocalConnected freezes the M2-S1 Local Connected transport:
// the user's PATH Pi runs in `--mode rpc` with no provider/model/tool/approval
// flags. The Chora-owned `--session-dir` is appended per-Attempt at launch and
// is therefore not part of the frozen transport.
var requiredPiArgumentsLocalConnected = []string{
	"--mode",
	"rpc",
}

var requiredCapabilitiesLocalConnected = []string{
	"runtime.agent_loop",
	"runtime.jsonl_rpc",
	"runtime.file_edit",
	"runtime.command_execution",
	"runtime.progress_events",
	"runtime.terminal_result",
	"runtime.cooperative_abort",
	"runtime.allowlisted_event_projection",
	"runtime.chora_owned_result",
	"runtime.zero_implicit_context",
	LocalConnectedNoSandboxCapability,
}

var requiredProbePassCriteriaLocalConnected = []string{
	"real_pi_rpc_reached",
	"deterministic_file_edit_and_command_succeed",
	"runtime_identity_recorded",
}

var requiredProbeFailureClassesLocalConnected = []string{
	"ENVIRONMENT_FAILURE",
	"MODEL_INCOMPATIBLE",
	"CONTRACT_INVALIDATION",
}

var requiredProbeStopCriteriaLocalConnected = []string{
	"runtime_hard_gate_failure",
	"core_contract_input_changed",
	"termination_or_cleanup_uncertain",
}

var requiredProbePassCriteria = []string{
	"real_pi_rpc_reached",
	"workspace_write_succeeds",
	"deterministic_command_succeeds",
	"unrelated_host_read_denied",
	"unrelated_host_write_denied",
	"strict_container_termination_kills_process_tree",
	"owned_workspace_cleanup_succeeds",
	"runtime_model_sandbox_image_config_policy_identities_recorded",
}

var requiredProbeFailureClasses = []string{
	"CANDIDATE_OR_ENV_FAILURE",
	"CONTRACT_INVALIDATION",
}

var requiredProbeStopCriteria = []string{
	"hard_candidate_incompatibility",
	"core_contract_input_changed",
	"isolation_uncertain",
	"termination_or_cleanup_uncertain",
}

var requiredProbePassCriteriaV2 = []string{
	"real_pi_0_84_1_jsonl_rpc_reached",
	"deterministic_file_edit_and_command_succeed",
	"workspace_write_succeeds",
	"unrelated_host_read_denied",
	"unrelated_host_write_denied",
	"ollama_only_network_proven",
	"cooperative_abort_observed",
	"forced_termination_confirms_process_tree_dead_within_5_seconds",
	"container_network_workspace_cleanup_succeeds",
	"runtime_sandbox_image_model_network_policy_identities_recorded",
}

var requiredProbeFailureClassesV2 = []string{
	"ENVIRONMENT_FAILURE",
	"MODEL_INCOMPATIBLE",
	"RUNTIME_REJECTED",
	"SANDBOX_REJECTED",
	"CONTRACT_INVALIDATION",
}

var requiredProbeStopCriteriaV2 = []string{
	"second_model_semantic_failure",
	"runtime_hard_gate_failure",
	"sandbox_hard_gate_failure",
	"core_contract_input_changed",
	"isolation_uncertain",
	"termination_or_cleanup_uncertain",
}

var requiredProbePassCriteriaV3 = []string{
	"real_pi_0_84_1_jsonl_rpc_reached",
	"deterministic_file_edit_and_command_succeed",
	"workspace_write_succeeds",
	"unrelated_host_read_denied",
	"unrelated_host_write_denied",
	"openai_codex_only_network_proven",
	"task_scoped_oauth_projection_proven",
	"credential_redaction_and_cleanup_proven",
	"cooperative_abort_observed",
	"forced_termination_confirms_process_tree_dead_within_5_seconds",
	"container_network_workspace_cleanup_succeeds",
	"runtime_sandbox_image_model_network_policy_identities_recorded",
}

var requiredProbeFailureClassesV3 = []string{
	"ENVIRONMENT_FAILURE",
	"AUTH_EXPIRED",
	"MODEL_INCOMPATIBLE",
	"RUNTIME_REJECTED",
	"SANDBOX_REJECTED",
	"CONTRACT_INVALIDATION",
}

var requiredProbeStopCriteriaV3 = []string{
	"second_model_semantic_failure",
	"authentication_expired_or_refresh_failed",
	"credential_projection_or_redaction_uncertain",
	"runtime_hard_gate_failure",
	"sandbox_hard_gate_failure",
	"core_contract_input_changed",
	"isolation_uncertain",
	"termination_or_cleanup_uncertain",
}

var requiredProbePassCriteriaV4 = append(append([]string{}, requiredProbePassCriteriaV3...),
	"fixed_enterprise_proxy_upstream_proven",
	"explicit_enterprise_ca_projection_proven",
	"verified_tls_to_codex_endpoints_proven",
)

var requiredProbePassCriteriaV5 = func() []string {
	criteria := append([]string{}, requiredProbePassCriteriaV4...)
	criteria[0] = "real_pi_0_84_2_jsonl_rpc_reached"
	return criteria
}()

var requiredProbeFailureClassesV4 = append(append([]string{}, requiredProbeFailureClassesV3...),
	"TRUST_CONFIGURATION_INVALID",
)

var requiredProbeStopCriteriaV4 = append(append([]string{}, requiredProbeStopCriteriaV3...),
	"enterprise_trust_identity_changed",
	"tls_verification_disabled_or_uncertain",
)
