// Package acceptanceauthority implements the installed-product-only O4 fault
// authority. The authority is deliberately startup-only: callers load and bind
// it before opening the product Store or activating Docker, then pass the
// resulting Controller to the managed supervisor.
package acceptanceauthority

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/Yangyang96/chora/internal/domain"
)

const (
	SchemaVersion         = "chora.m1-o4-fault-authority.v1"
	StatusAuthorized      = "authorized"
	ScopeO4AcceptanceOnly = "o4_acceptance_only"

	ScenarioFailure                  = "failure"
	ScenarioTimeout                  = "timeout"
	ActionForceManagedNonzero        = "force_exact_managed_attempt_nonzero_after_started_v1"
	ActionAcceleratedManagedDeadline = "accelerated_managed_attempt_deadline_v1"

	AgentExecutionProfileStandard = "standard"
	AcceleratedDeadlineSeconds    = 3

	maxAuthorityBytes = 64 << 10
)

var (
	ErrDisabled = errors.New("O4 acceptance authority is disabled")
	ErrInvalid  = errors.New("invalid O4 acceptance authority")
	ErrConsumed = errors.New("O4 acceptance authority was already consumed")

	digestPattern    = regexp.MustCompile(`^[0-9a-f]{64}$`)
	productIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	opaqueIDPattern  = regexp.MustCompile(`^(?:env|ins|gen)_[A-Za-z0-9_-]{24,80}$`)

	ClaimBoundary = []string{
		"typed_failure_projection",
		"typed_timeout_projection",
		"process_tree_death",
		"no_patch",
		"no_automatic_retry",
		"resource_cleanup",
	}
)

// Document is the complete safe authority body. It intentionally contains no
// command, executable, signal, duration, container identity, URL, or free-form
// trigger field.
type Document struct {
	SchemaVersion         string     `json:"schemaVersion"`
	Status                string     `json:"status"`
	Scope                 string     `json:"scope"`
	TupleIdentity         string     `json:"tupleIdentity"`
	GenerationID          string     `json:"generationId"`
	EnvironmentID         string     `json:"environmentId"`
	InstallID             string     `json:"installId"`
	BinarySHA256          string     `json:"binarySha256"`
	SourceAggregateSHA256 string     `json:"sourceAggregateSha256"`
	DataRootSHA256        string     `json:"dataRootSha256"`
	PolicySHA256          string     `json:"policySha256"`
	Scenarios             []Scenario `json:"scenarios"`
	ClaimBoundary         []string   `json:"claimBoundary"`
}

type Scenario struct {
	Scenario              string `json:"scenario"`
	Action                string `json:"action"`
	TaskID                string `json:"taskId"`
	SnapshotID            string `json:"snapshotId"`
	SnapshotDigest        string `json:"snapshotDigest"`
	AttemptSequence       int    `json:"attemptSequence"`
	AgentExecutionProfile string `json:"agentExecutionProfile"`
}

type StartupInput struct {
	AuthorityPath    string
	AuthoritySHA256  string
	CandidateTuple   string
	GenerationID     string
	DataRoot         string
	BinaryPath       string
	SourceAggregate  string
	PolicySHA256     string
	InstalledProduct bool
}

// Loaded is immutable startup authority which has passed filesystem, byte,
// digest, schema, and installed candidate binding checks. It has no execution
// effect until BindDatabase succeeds.
type Loaded struct {
	document Document
	digest   string
	dataRoot string
}

func (loaded Loaded) Enabled() bool      { return loaded.digest != "" }
func (loaded Loaded) Digest() string     { return loaded.digest }
func (loaded Loaded) Document() Document { return cloneDocument(loaded.document) }

// RejectLegacyEnvironment makes obsolete environment seams fail closed rather
// than silently becoming an alternative authority source.
func RejectLegacyEnvironment() error {
	for _, name := range []string{"CHORA_TEST_BOUNDARY_FAULT_SEAM", "CHORA_G2_M3_ACCEPTANCE_TIMEOUT_POLICY"} {
		if _, present := os.LookupEnv(name); present {
			return fmt.Errorf("%w: obsolete environment authority %s is forbidden", ErrInvalid, name)
		}
	}
	return nil
}

// Load implements the all-or-none startup boundary. An all-empty input is the
// sole disabled form. Any partial input is fatal.
func Load(input StartupInput) (Loaded, error) {
	primary := []string{input.AuthorityPath, input.AuthoritySHA256, input.CandidateTuple}
	absent := 0
	for _, value := range primary {
		if strings.TrimSpace(value) == "" {
			absent++
		}
	}
	if absent == len(primary) {
		return Loaded{}, nil
	}
	if absent != 0 || !input.InstalledProduct {
		return Loaded{}, fmt.Errorf("%w: three complete installed-product CLI bindings are required", ErrInvalid)
	}
	if !canonicalAbsolute(input.AuthorityPath) || !canonicalAbsolute(input.DataRoot) || !canonicalAbsolute(input.BinaryPath) ||
		!validDigest(input.AuthoritySHA256) || !validDigest(input.CandidateTuple) || !validDigest(input.SourceAggregate) || !validDigest(input.PolicySHA256) ||
		!productIDPattern.MatchString(input.GenerationID) {
		return Loaded{}, fmt.Errorf("%w: startup binding is malformed", ErrInvalid)
	}
	data, err := readOwnerOnlyCanonicalFile(input.AuthorityPath, maxAuthorityBytes)
	if err != nil {
		return Loaded{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	digest := sha256.Sum256(data)
	digestText := hex.EncodeToString(digest[:])
	if digestText != input.AuthoritySHA256 {
		return Loaded{}, fmt.Errorf("%w: authority SHA-256 mismatch", ErrInvalid)
	}
	document, err := decodeDocument(data)
	if err != nil {
		return Loaded{}, err
	}
	if document.TupleIdentity != input.CandidateTuple || document.GenerationID != input.GenerationID ||
		document.SourceAggregateSHA256 != input.SourceAggregate || document.PolicySHA256 != input.PolicySHA256 ||
		document.DataRootSHA256 != pathDigest(input.DataRoot) {
		return Loaded{}, fmt.Errorf("%w: installed tuple, generation, source, data root, or policy binding drifted", ErrInvalid)
	}
	binary, err := readBoundedRegular(input.BinaryPath, 256<<20, false)
	if err != nil || hexDigest(binary) != document.BinarySHA256 {
		return Loaded{}, fmt.Errorf("%w: installed binary identity drifted", ErrInvalid)
	}
	if err := validateOwnerDirectory(input.DataRoot); err != nil {
		return Loaded{}, fmt.Errorf("%w: data root: %v", ErrInvalid, err)
	}
	loaded := Loaded{document: document, digest: digestText, dataRoot: input.DataRoot}
	if err := loaded.proveUnconsumed(); err != nil {
		return Loaded{}, err
	}
	return loaded, nil
}

func decodeDocument(data []byte) (Document, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var document Document
	if err := decoder.Decode(&document); err != nil {
		return Document{}, fmt.Errorf("%w: decode authority", ErrInvalid)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Document{}, fmt.Errorf("%w: trailing authority content", ErrInvalid)
	}
	canonical, err := json.Marshal(document)
	if err != nil || !bytes.Equal(data, append(canonical, '\n')) {
		return Document{}, fmt.Errorf("%w: authority bytes are not canonical", ErrInvalid)
	}
	if document.SchemaVersion != SchemaVersion || document.Status != StatusAuthorized || document.Scope != ScopeO4AcceptanceOnly ||
		!validDigest(document.TupleIdentity) || !opaqueIDPattern.MatchString(document.GenerationID) ||
		!opaqueIDPattern.MatchString(document.EnvironmentID) || !opaqueIDPattern.MatchString(document.InstallID) ||
		!validDigest(document.BinarySHA256) || !validDigest(document.SourceAggregateSHA256) ||
		!validDigest(document.DataRootSHA256) || !validDigest(document.PolicySHA256) ||
		!slices.Equal(document.ClaimBoundary, ClaimBoundary) || len(document.Scenarios) != 2 {
		return Document{}, fmt.Errorf("%w: fixed body contract mismatch", ErrInvalid)
	}
	want := []struct{ scenario, action string }{
		{ScenarioFailure, ActionForceManagedNonzero},
		{ScenarioTimeout, ActionAcceleratedManagedDeadline},
	}
	seenTasks := map[string]struct{}{}
	seenSnapshots := map[string]struct{}{}
	for index, scenario := range document.Scenarios {
		if scenario.Scenario != want[index].scenario || scenario.Action != want[index].action ||
			!validTaskID(scenario.TaskID) || !validSnapshotID(scenario.SnapshotID) ||
			!validDigest(scenario.SnapshotDigest) || scenario.AttemptSequence != 1 ||
			scenario.AgentExecutionProfile != AgentExecutionProfileStandard {
			return Document{}, fmt.Errorf("%w: scenario %d contract mismatch", ErrInvalid, index)
		}
		if _, duplicate := seenTasks[scenario.TaskID]; duplicate {
			return Document{}, fmt.Errorf("%w: scenario Tasks must be distinct", ErrInvalid)
		}
		if _, duplicate := seenSnapshots[scenario.SnapshotID]; duplicate {
			return Document{}, fmt.Errorf("%w: scenario Snapshots must be distinct", ErrInvalid)
		}
		seenTasks[scenario.TaskID] = struct{}{}
		seenSnapshots[scenario.SnapshotID] = struct{}{}
	}
	return document, nil
}

func CanonicalBytes(document Document) ([]byte, error) {
	data, err := json.Marshal(document)
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func pathDigest(path string) string { return hexDigest([]byte(filepath.Clean(path))) }
func hexDigest(value []byte) string { sum := sha256.Sum256(value); return hex.EncodeToString(sum[:]) }
func validDigest(value string) bool { return digestPattern.MatchString(value) }

func validTaskID(value string) bool {
	_, err := domain.ParseTaskID(value)
	return err == nil
}

func validSnapshotID(value string) bool {
	_, err := domain.ParseContextSnapshotID(value)
	return err == nil
}

func cloneDocument(document Document) Document {
	document.Scenarios = append([]Scenario(nil), document.Scenarios...)
	document.ClaimBoundary = append([]string(nil), document.ClaimBoundary...)
	return document
}

func executablePath() (string, error) {
	path, err := os.Executable()
	if err != nil {
		return "", err
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	return filepath.Clean(path), nil
}
