package dockersupervisor

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	engineIdentitySchema        = "chora.docker-engine-identity/v1"
	capabilityContractSchema    = "chora.docker-capability-probe-contract/v1"
	engineQualificationSchema   = "chora.docker-engine-qualification/v1"
	CapabilityContractVersion   = 1
	MinimumDockerAPIVersion     = "1.44"
	CapabilityProbeOperatingOS  = "linux"
	CapabilityProbeArchitecture = "arm64"

	probeOwnerLabelValue = "dockersupervisor"
	probeKindLabelValue  = "capability-probe-v1"

	engineVersionObservationFormat  = `{"api_version":{{json .Server.APIVersion}},"server_version":{{json .Server.Version}},"operating_system":{{json .Server.Os}},"architecture":{{json .Server.Arch}}}`
	engineInfoObservationFormat     = `{"daemon_id":{{json .ID}},"provider_name":{{json .OperatingSystem}}}`
	engineEndpointObservationFormat = `{{json .Endpoints.docker.Host}}`
	probeTerminalStateFormat        = `{"running":{{json .State.Running}},"exit_code":{{json .State.ExitCode}},"oom_killed":{{json .State.OOMKilled}},"dead":{{json .State.Dead}},"error":{{json .State.Error}}}`
	capabilityProbeReadinessTimeout = 2 * time.Second
	capabilityProbeReadinessPoll    = 10 * time.Millisecond
)

var (
	ErrInvalidEngineIdentity      = errors.New("invalid Docker Engine identity")
	ErrInvalidCapabilityContract  = errors.New("invalid Docker capability Probe contract")
	ErrInvalidEngineQualification = errors.New("invalid Docker Engine qualification")
	ErrInvalidCapabilityProbe     = errors.New("invalid Docker capability Probe")
	ErrProbeAuthorizationRequired = errors.New("Docker capability Probe requires explicit authorization")
)

var requiredProbeCapabilities = []string{
	"exact-preexisting-image",
	"internal-network",
	"precise-labels",
	"read-only-rootfs",
	"read-only-bind-mount",
	"resource-limits",
	"forced-termination",
	"exact-cleanup",
	"zero-residue",
}

// EngineIdentityInput is one observed Docker daemon identity. ProviderName,
// EngineVersion, and ContextName are evidence for people and diagnostics; they
// deliberately do not participate in Digest or compatibility decisions.
type EngineIdentityInput struct {
	DaemonID              string
	APIVersion            string
	OperatingSystem       string
	Architecture          string
	ContextEndpointDigest string
	ProviderName          string
	EngineVersion         string
	ContextName           string
}

// EngineIdentityRecord is the strict persisted representation of an
// EngineIdentity. IdentityDigest binds only the compatibility-bearing fields.
type EngineIdentityRecord struct {
	SchemaVersion         string `json:"schema_version"`
	DaemonID              string `json:"daemon_id"`
	APIVersion            string `json:"api_version"`
	OperatingSystem       string `json:"operating_system"`
	Architecture          string `json:"architecture"`
	ContextEndpointDigest string `json:"context_endpoint_digest"`
	ProviderName          string `json:"provider_name"`
	EngineVersion         string `json:"engine_version"`
	ContextName           string `json:"context_name"`
	IdentityDigest        string `json:"identity_digest"`
}

type engineIdentityCore struct {
	SchemaVersion         string `json:"schema_version"`
	DaemonID              string `json:"daemon_id"`
	APIVersion            string `json:"api_version"`
	OperatingSystem       string `json:"operating_system"`
	Architecture          string `json:"architecture"`
	ContextEndpointDigest string `json:"context_endpoint_digest"`
}

// EngineIdentity is immutable: construction and restoration validate and copy
// its complete canonical record, and no field is exposed by reference.
type EngineIdentity struct {
	record    EngineIdentityRecord
	canonical []byte
}

func NewEngineIdentity(input EngineIdentityInput) (EngineIdentity, error) {
	record := EngineIdentityRecord{
		SchemaVersion: engineIdentitySchema, DaemonID: input.DaemonID,
		APIVersion: input.APIVersion, OperatingSystem: input.OperatingSystem,
		Architecture: input.Architecture, ContextEndpointDigest: input.ContextEndpointDigest,
		ProviderName: input.ProviderName, EngineVersion: input.EngineVersion, ContextName: input.ContextName,
	}
	digest, err := engineIdentityDigest(record)
	if err != nil {
		return EngineIdentity{}, err
	}
	record.IdentityDigest = digest
	return restoreEngineIdentityRecord(record)
}

func RestoreEngineIdentity(record EngineIdentityRecord) (EngineIdentity, error) {
	return restoreEngineIdentityRecord(record)
}

func restoreEngineIdentityRecord(record EngineIdentityRecord) (EngineIdentity, error) {
	want, err := engineIdentityDigest(record)
	if err != nil || record.IdentityDigest != want {
		return EngineIdentity{}, fmt.Errorf("%w: identity digest drift", ErrInvalidEngineIdentity)
	}
	canonical, err := json.Marshal(record)
	if err != nil {
		return EngineIdentity{}, fmt.Errorf("%w: encode record: %v", ErrInvalidEngineIdentity, err)
	}
	return EngineIdentity{record: record, canonical: canonical}, nil
}

func engineIdentityDigest(record EngineIdentityRecord) (string, error) {
	if record.SchemaVersion != engineIdentitySchema || !validText(record.DaemonID, 256) ||
		!compatibleAPIVersion(record.APIVersion, MinimumDockerAPIVersion) ||
		record.OperatingSystem != CapabilityProbeOperatingOS || record.Architecture != CapabilityProbeArchitecture ||
		!validCanonicalDigest(record.ContextEndpointDigest) || !validText(record.ProviderName, 128) ||
		!validText(record.EngineVersion, 128) || !validText(record.ContextName, 256) {
		return "", ErrInvalidEngineIdentity
	}
	core := engineIdentityCore{
		SchemaVersion: record.SchemaVersion, DaemonID: record.DaemonID, APIVersion: record.APIVersion,
		OperatingSystem: record.OperatingSystem, Architecture: record.Architecture,
		ContextEndpointDigest: record.ContextEndpointDigest,
	}
	return digestJSON(core)
}

func (identity EngineIdentity) Digest() string          { return identity.record.IdentityDigest }
func (identity EngineIdentity) DaemonID() string        { return identity.record.DaemonID }
func (identity EngineIdentity) APIVersion() string      { return identity.record.APIVersion }
func (identity EngineIdentity) OperatingSystem() string { return identity.record.OperatingSystem }
func (identity EngineIdentity) Architecture() string    { return identity.record.Architecture }
func (identity EngineIdentity) ContextEndpointDigest() string {
	return identity.record.ContextEndpointDigest
}
func (identity EngineIdentity) ProviderName() string         { return identity.record.ProviderName }
func (identity EngineIdentity) EngineVersion() string        { return identity.record.EngineVersion }
func (identity EngineIdentity) ContextName() string          { return identity.record.ContextName }
func (identity EngineIdentity) Record() EngineIdentityRecord { return identity.record }
func (identity EngineIdentity) CanonicalJSON() []byte        { return bytes.Clone(identity.canonical) }

func (identity EngineIdentity) MarshalJSON() ([]byte, error) {
	if len(identity.canonical) == 0 {
		return nil, ErrInvalidEngineIdentity
	}
	return bytes.Clone(identity.canonical), nil
}

func (identity *EngineIdentity) UnmarshalJSON(data []byte) error {
	if identity == nil {
		return ErrInvalidEngineIdentity
	}
	var record EngineIdentityRecord
	if err := strictJSON(data, &record); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidEngineIdentity, err)
	}
	restored, err := RestoreEngineIdentity(record)
	if err != nil {
		return err
	}
	*identity = restored
	return nil
}

// DigestContextEndpoint produces the non-secret context endpoint identity.
// Raw endpoint paths and hostnames are intentionally not persisted.
func DigestContextEndpoint(endpoint string) (string, error) {
	if endpoint == "" || !utf8.ValidString(endpoint) || endpoint != strings.TrimSpace(endpoint) || strings.ContainsAny(endpoint, "\x00\r\n") {
		return "", fmt.Errorf("%w: invalid context endpoint", ErrInvalidEngineIdentity)
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("%w: invalid context endpoint", ErrInvalidEngineIdentity)
	}
	switch parsed.Scheme {
	case "unix":
		if parsed.Host != "" || !filepath.IsAbs(parsed.Path) {
			return "", fmt.Errorf("%w: invalid unix context endpoint", ErrInvalidEngineIdentity)
		}
	case "tcp", "ssh":
		if parsed.Host == "" || parsed.Path != "" {
			return "", fmt.Errorf("%w: invalid network context endpoint", ErrInvalidEngineIdentity)
		}
	default:
		return "", fmt.Errorf("%w: unsupported context endpoint", ErrInvalidEngineIdentity)
	}
	sum := sha256.Sum256([]byte(endpoint))
	return hex.EncodeToString(sum[:]), nil
}

type CapabilityProbeContractRecord struct {
	SchemaVersion       string   `json:"schema_version"`
	ContractVersion     int      `json:"contract_version"`
	MinimumAPIVersion   string   `json:"minimum_api_version"`
	OperatingSystem     string   `json:"operating_system"`
	Architecture        string   `json:"architecture"`
	ProbeImageID        string   `json:"probe_image_id"`
	SandboxPolicyDigest string   `json:"sandbox_policy_digest"`
	Capabilities        []string `json:"capabilities"`
	ContractDigest      string   `json:"contract_digest"`
}

type CapabilityProbeContract struct {
	record    CapabilityProbeContractRecord
	canonical []byte
}

func NewCapabilityProbeContract(probeImageID, sandboxPolicyDigest string) (CapabilityProbeContract, error) {
	record := CapabilityProbeContractRecord{
		SchemaVersion: capabilityContractSchema, ContractVersion: CapabilityContractVersion,
		MinimumAPIVersion: MinimumDockerAPIVersion, OperatingSystem: CapabilityProbeOperatingOS,
		Architecture: CapabilityProbeArchitecture, ProbeImageID: probeImageID,
		SandboxPolicyDigest: sandboxPolicyDigest, Capabilities: slices.Clone(requiredProbeCapabilities),
	}
	digest, err := capabilityContractDigest(record)
	if err != nil {
		return CapabilityProbeContract{}, err
	}
	record.ContractDigest = digest
	return restoreCapabilityContractRecord(record)
}

func RestoreCapabilityProbeContract(record CapabilityProbeContractRecord) (CapabilityProbeContract, error) {
	return restoreCapabilityContractRecord(record)
}

func restoreCapabilityContractRecord(record CapabilityProbeContractRecord) (CapabilityProbeContract, error) {
	want, err := capabilityContractDigest(record)
	if err != nil || record.ContractDigest != want {
		return CapabilityProbeContract{}, fmt.Errorf("%w: contract digest drift", ErrInvalidCapabilityContract)
	}
	record.Capabilities = slices.Clone(record.Capabilities)
	canonical, err := json.Marshal(record)
	if err != nil {
		return CapabilityProbeContract{}, fmt.Errorf("%w: encode record: %v", ErrInvalidCapabilityContract, err)
	}
	return CapabilityProbeContract{record: record, canonical: canonical}, nil
}

func capabilityContractDigest(record CapabilityProbeContractRecord) (string, error) {
	if record.SchemaVersion != capabilityContractSchema || record.ContractVersion != CapabilityContractVersion ||
		record.MinimumAPIVersion != MinimumDockerAPIVersion || record.OperatingSystem != CapabilityProbeOperatingOS ||
		record.Architecture != CapabilityProbeArchitecture || !validImageID(record.ProbeImageID) ||
		!validCanonicalDigest(record.SandboxPolicyDigest) || !slices.Equal(record.Capabilities, requiredProbeCapabilities) {
		return "", ErrInvalidCapabilityContract
	}
	copy := record
	copy.ContractDigest = ""
	return digestJSON(struct {
		SchemaVersion       string   `json:"schema_version"`
		ContractVersion     int      `json:"contract_version"`
		MinimumAPIVersion   string   `json:"minimum_api_version"`
		OperatingSystem     string   `json:"operating_system"`
		Architecture        string   `json:"architecture"`
		ProbeImageID        string   `json:"probe_image_id"`
		SandboxPolicyDigest string   `json:"sandbox_policy_digest"`
		Capabilities        []string `json:"capabilities"`
	}{copy.SchemaVersion, copy.ContractVersion, copy.MinimumAPIVersion, copy.OperatingSystem,
		copy.Architecture, copy.ProbeImageID, copy.SandboxPolicyDigest, copy.Capabilities})
}

func (contract CapabilityProbeContract) Digest() string       { return contract.record.ContractDigest }
func (contract CapabilityProbeContract) Version() int         { return contract.record.ContractVersion }
func (contract CapabilityProbeContract) MinimumAPI() string   { return contract.record.MinimumAPIVersion }
func (contract CapabilityProbeContract) ProbeImageID() string { return contract.record.ProbeImageID }
func (contract CapabilityProbeContract) SandboxPolicyDigest() string {
	return contract.record.SandboxPolicyDigest
}
func (contract CapabilityProbeContract) Capabilities() []string {
	return slices.Clone(contract.record.Capabilities)
}
func (contract CapabilityProbeContract) Record() CapabilityProbeContractRecord {
	record := contract.record
	record.Capabilities = slices.Clone(record.Capabilities)
	return record
}
func (contract CapabilityProbeContract) CanonicalJSON() []byte {
	return bytes.Clone(contract.canonical)
}

func (contract CapabilityProbeContract) MarshalJSON() ([]byte, error) {
	if len(contract.canonical) == 0 {
		return nil, ErrInvalidCapabilityContract
	}
	return bytes.Clone(contract.canonical), nil
}

func (contract *CapabilityProbeContract) UnmarshalJSON(data []byte) error {
	if contract == nil {
		return ErrInvalidCapabilityContract
	}
	var record CapabilityProbeContractRecord
	if err := strictJSON(data, &record); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidCapabilityContract, err)
	}
	restored, err := RestoreCapabilityProbeContract(record)
	if err != nil {
		return err
	}
	*contract = restored
	return nil
}

type EngineQualificationRecord struct {
	SchemaVersion        string `json:"schema_version"`
	EngineIdentityDigest string `json:"engine_identity_digest"`
	ProbeContractDigest  string `json:"probe_contract_digest"`
	ProbeImageID         string `json:"probe_image_id"`
	SandboxPolicyDigest  string `json:"sandbox_policy_digest"`
	CompletedAt          string `json:"completed_at"`
	QualificationDigest  string `json:"qualification_digest"`
}

type EngineQualification struct {
	record    EngineQualificationRecord
	canonical []byte
}

func RestoreEngineQualification(record EngineQualificationRecord) (EngineQualification, error) {
	return restoreEngineQualificationRecord(record)
}

func newEngineQualification(identity EngineIdentity, contract CapabilityProbeContract, completedAt time.Time) (EngineQualification, error) {
	if len(identity.canonical) == 0 || len(contract.canonical) == 0 || completedAt.IsZero() {
		return EngineQualification{}, ErrInvalidEngineQualification
	}
	record := EngineQualificationRecord{
		SchemaVersion: engineQualificationSchema, EngineIdentityDigest: identity.Digest(),
		ProbeContractDigest: contract.Digest(), ProbeImageID: contract.ProbeImageID(),
		SandboxPolicyDigest: contract.SandboxPolicyDigest(), CompletedAt: completedAt.UTC().Format(time.RFC3339Nano),
	}
	digest, err := engineQualificationDigest(record)
	if err != nil {
		return EngineQualification{}, err
	}
	record.QualificationDigest = digest
	return restoreEngineQualificationRecord(record)
}

func restoreEngineQualificationRecord(record EngineQualificationRecord) (EngineQualification, error) {
	want, err := engineQualificationDigest(record)
	if err != nil || record.QualificationDigest != want {
		return EngineQualification{}, fmt.Errorf("%w: qualification digest drift", ErrInvalidEngineQualification)
	}
	canonical, err := json.Marshal(record)
	if err != nil {
		return EngineQualification{}, fmt.Errorf("%w: encode record: %v", ErrInvalidEngineQualification, err)
	}
	return EngineQualification{record: record, canonical: canonical}, nil
}

func engineQualificationDigest(record EngineQualificationRecord) (string, error) {
	completed, err := time.Parse(time.RFC3339Nano, record.CompletedAt)
	if err != nil || completed.IsZero() || completed.Location() != time.UTC || completed.Format(time.RFC3339Nano) != record.CompletedAt ||
		record.SchemaVersion != engineQualificationSchema || !validCanonicalDigest(record.EngineIdentityDigest) ||
		!validCanonicalDigest(record.ProbeContractDigest) || !validImageID(record.ProbeImageID) ||
		!validCanonicalDigest(record.SandboxPolicyDigest) {
		return "", ErrInvalidEngineQualification
	}
	copy := record
	copy.QualificationDigest = ""
	return digestJSON(struct {
		SchemaVersion        string `json:"schema_version"`
		EngineIdentityDigest string `json:"engine_identity_digest"`
		ProbeContractDigest  string `json:"probe_contract_digest"`
		ProbeImageID         string `json:"probe_image_id"`
		SandboxPolicyDigest  string `json:"sandbox_policy_digest"`
		CompletedAt          string `json:"completed_at"`
	}{copy.SchemaVersion, copy.EngineIdentityDigest, copy.ProbeContractDigest, copy.ProbeImageID,
		copy.SandboxPolicyDigest, copy.CompletedAt})
}

func (qualification EngineQualification) Digest() string {
	return qualification.record.QualificationDigest
}
func (qualification EngineQualification) EngineDigest() string {
	return qualification.record.EngineIdentityDigest
}
func (qualification EngineQualification) ContractDigest() string {
	return qualification.record.ProbeContractDigest
}
func (qualification EngineQualification) ProbeImageID() string {
	return qualification.record.ProbeImageID
}
func (qualification EngineQualification) SandboxPolicyDigest() string {
	return qualification.record.SandboxPolicyDigest
}
func (qualification EngineQualification) CompletedAt() time.Time {
	value, _ := time.Parse(time.RFC3339Nano, qualification.record.CompletedAt)
	return value
}
func (qualification EngineQualification) Record() EngineQualificationRecord {
	return qualification.record
}
func (qualification EngineQualification) CanonicalJSON() []byte {
	return bytes.Clone(qualification.canonical)
}
func (qualification EngineQualification) ValidFor(identity EngineIdentity, contract CapabilityProbeContract) bool {
	return len(qualification.canonical) != 0 && len(identity.canonical) != 0 && len(contract.canonical) != 0 &&
		qualification.EngineDigest() == identity.Digest() && qualification.ContractDigest() == contract.Digest() &&
		qualification.ProbeImageID() == contract.ProbeImageID() &&
		qualification.SandboxPolicyDigest() == contract.SandboxPolicyDigest()
}

func (qualification EngineQualification) MarshalJSON() ([]byte, error) {
	if len(qualification.canonical) == 0 {
		return nil, ErrInvalidEngineQualification
	}
	return bytes.Clone(qualification.canonical), nil
}

func (qualification *EngineQualification) UnmarshalJSON(data []byte) error {
	if qualification == nil {
		return ErrInvalidEngineQualification
	}
	var record EngineQualificationRecord
	if err := strictJSON(data, &record); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidEngineQualification, err)
	}
	restored, err := RestoreEngineQualification(record)
	if err != nil {
		return err
	}
	*qualification = restored
	return nil
}

// ProbeAuthorization is deliberately unforgeable outside this package except
// through AuthorizeCapabilityProbe, which is the setup boundary's explicit act.
type ProbeAuthorization struct{ authorized bool }

func AuthorizeCapabilityProbe() ProbeAuthorization        { return ProbeAuthorization{authorized: true} }
func (authorization ProbeAuthorization) Authorized() bool { return authorization.authorized }

type CapabilityProbeConfig struct {
	Runner      CommandRunner
	RuntimeRoot string
	Identity    EngineIdentity
	Contract    CapabilityProbeContract
	Clock       func() time.Time
	IDSource    func() (string, error)
}

type CapabilityProbe struct {
	runner      CommandRunner
	runtimeRoot string
	identity    EngineIdentity
	contract    CapabilityProbeContract
	clock       func() time.Time
	idSource    func() (string, error)
}

func NewCapabilityProbe(config CapabilityProbeConfig) (*CapabilityProbe, error) {
	root := config.RuntimeRoot
	cleanRoot := filepath.Clean(root)
	info, err := os.Lstat(root)
	if config.Runner == nil || root != cleanRoot || !filepath.IsAbs(root) || root == string(filepath.Separator) ||
		err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 ||
		len(config.Identity.canonical) == 0 || len(config.Contract.canonical) == 0 ||
		!compatibleAPIVersion(config.Identity.APIVersion(), config.Contract.MinimumAPI()) ||
		config.Identity.OperatingSystem() != config.Contract.record.OperatingSystem ||
		config.Identity.Architecture() != config.Contract.record.Architecture {
		return nil, ErrInvalidCapabilityProbe
	}
	if config.Clock == nil {
		config.Clock = time.Now
	}
	if config.IDSource == nil {
		config.IDSource = randomProbeID
	}
	return &CapabilityProbe{runner: config.Runner, runtimeRoot: root, identity: config.Identity,
		contract: config.Contract, clock: config.Clock, idSource: config.IDSource}, nil
}

// Probe performs the single setup-authorized disposable Probe. Every error,
// including cleanup uncertainty, returns the zero qualification.
func (probe *CapabilityProbe) Probe(ctx context.Context, authorization ProbeAuthorization) (EngineQualification, error) {
	if probe == nil {
		return EngineQualification{}, ErrInvalidCapabilityProbe
	}
	if !authorization.authorized {
		return EngineQualification{}, ErrProbeAuthorizationRequired
	}
	observedBefore, err := probe.observeEngine(ctx)
	if err != nil {
		return EngineQualification{}, fmt.Errorf("observe Docker Engine before Probe: %w", err)
	}
	if observedBefore.Digest() != probe.identity.Digest() {
		return EngineQualification{}, errors.New("observe Docker Engine before Probe: expected identity mismatch")
	}
	probeID, err := probe.idSource()
	if err != nil || !validProbeID(probeID) {
		return EngineQualification{}, fmt.Errorf("%w: invalid Probe ID", ErrInvalidCapabilityProbe)
	}
	root, err := os.MkdirTemp(probe.runtimeRoot, "capability-probe-"+probeID+"-")
	if err != nil {
		return EngineQualification{}, fmt.Errorf("create Probe root: %w", err)
	}
	if err := os.Chmod(root, 0o755); err != nil {
		_ = os.RemoveAll(root)
		return EngineQualification{}, fmt.Errorf("make Probe root traversable: %w", err)
	}
	containerName := "chora-capability-probe-" + probeID
	networkName := containerName + "-internal"
	labels := probe.resourceLabels(probeID)
	process, operationErr := probe.runOperations(ctx, root, containerName, networkName, labels)
	cleanupErr := probe.cleanup(root, containerName, networkName, labels, process)
	postContext, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	observedAfter, observationErr := probe.observeEngine(postContext)
	if observationErr == nil && (observedAfter.Digest() != probe.identity.Digest() || observedAfter.Digest() != observedBefore.Digest() ||
		observedAfter.ContextName() != observedBefore.ContextName()) {
		observationErr = errors.New("Docker Engine identity drifted during Probe")
	}
	if operationErr != nil || cleanupErr != nil || observationErr != nil {
		return EngineQualification{}, errors.Join(operationErr, cleanupErr, observationErr)
	}
	qualification, err := newEngineQualification(observedAfter, probe.contract, probe.clock())
	if err != nil {
		return EngineQualification{}, err
	}
	return qualification, nil
}

type capabilityProbeProcess struct {
	process Process
	done    chan struct{}
	result  capabilityProbeProcessResult

	closeOnce sync.Once
	closeErr  error
	killOnce  sync.Once
	killErr   error
}

type capabilityProbeProcessResult struct {
	exitCode int
	err      error
}

func newCapabilityProbeProcess(process Process) *capabilityProbeProcess {
	state := &capabilityProbeProcess{process: process, done: make(chan struct{})}
	go func() {
		state.result.exitCode, state.result.err = process.Wait()
		close(state.done)
	}()
	return state
}

func (state *capabilityProbeProcess) close() error {
	state.closeOnce.Do(func() {
		state.closeErr = state.process.Close()
		if errors.Is(state.closeErr, os.ErrClosed) {
			state.closeErr = nil
		}
	})
	return state.closeErr
}

func (state *capabilityProbeProcess) kill() error {
	state.killOnce.Do(func() { state.killErr = state.process.Kill() })
	return state.killErr
}

func (state *capabilityProbeProcess) terminal() bool {
	select {
	case <-state.done:
		return true
	default:
		return false
	}
}

func (state *capabilityProbeProcess) observe(ctx context.Context) (capabilityProbeProcessResult, error) {
	select {
	case <-state.done:
		return state.result, nil
	case <-ctx.Done():
		return capabilityProbeProcessResult{}, ctx.Err()
	}
}

func (probe *CapabilityProbe) runOperations(ctx context.Context, root, containerName, networkName string, labels []string) (*capabilityProbeProcess, error) {
	marker := filepath.Join(root, "marker")
	if err := os.WriteFile(marker, []byte("chora-capability-probe-v1\n"), 0o444); err != nil {
		return nil, fmt.Errorf("write Probe marker: %w", err)
	}
	actualImage, err := probe.runText(ctx, []string{"image", "inspect", "--format", "{{.Id}}", probe.contract.ProbeImageID()})
	if err != nil {
		return nil, fmt.Errorf("verify pre-existing Probe image: %w", err)
	}
	if actualImage != probe.contract.ProbeImageID() {
		return nil, errors.New("verify pre-existing Probe image: identity mismatch")
	}
	networkArgs := []string{"network", "create", "--internal"}
	networkArgs = appendLabelArgs(networkArgs, labels)
	networkArgs = append(networkArgs, networkName)
	if err := probe.runOK(ctx, networkArgs); err != nil {
		return nil, fmt.Errorf("create Probe internal network: %w", err)
	}
	networkInspect, err := probe.runText(ctx, []string{"network", "inspect", "--format", "{{json .}}", networkName})
	if err != nil {
		return nil, fmt.Errorf("inspect effective Probe network: %w", err)
	}
	if err := verifyEffectiveProbeNetwork([]byte(networkInspect), networkName, labels); err != nil {
		return nil, fmt.Errorf("inspect effective Probe network: %w", err)
	}
	runArgs := []string{"run", "--pull=never", "--name", containerName, "--network", networkName,
		"--user", "1000:1000", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true",
		"--pids-limit", "16", "--cpus", "0.10", "--memory", "32m", "--memory-swap", "32m",
		"--ulimit", "nofile=64:64", "--log-driver", "none", "--tmpfs", "/tmp:rw,nosuid,nodev,noexec,size=8m,uid=1000,gid=1000",
		"--mount", "type=bind,src=" + root + ",dst=/chora-probe,readonly",
	}
	runArgs = appendLabelArgs(runArgs, labels)
	runArgs = append(runArgs, "--entrypoint", "/bin/sh", probe.contract.ProbeImageID(), "-c", "while :; do sleep 1; done")
	process, err := probe.runner.Start(ctx, Command{Args: runArgs})
	if err != nil {
		return nil, fmt.Errorf("start Probe container: %w", err)
	}
	if process == nil {
		return nil, errors.New("start Probe container: nil process")
	}
	state := newCapabilityProbeProcess(process)
	inspect, err := probe.waitForProbeContainer(ctx, state, containerName)
	if err != nil {
		return state, fmt.Errorf("inspect effective Probe container: %w", err)
	}
	if err := verifyEffectiveProbe([]byte(inspect), root, containerName, networkName, probe.contract.ProbeImageID(), labels); err != nil {
		return state, fmt.Errorf("inspect effective Probe container: %w", err)
	}
	if err := probe.runOK(ctx, []string{"exec", "--user", "1000:1000", containerName, "/usr/bin/test", "-r", "/chora-probe/marker"}); err != nil {
		return state, fmt.Errorf("verify Probe read-only mount: %w", err)
	}
	if err := probe.runOK(ctx, []string{"kill", "--signal", "KILL", containerName}); err != nil {
		return state, fmt.Errorf("terminate Probe container: %w", err)
	}
	if err := state.close(); err != nil {
		return state, fmt.Errorf("close Probe process: %w", err)
	}
	result, err := state.observe(ctx)
	if err != nil {
		return state, fmt.Errorf("wait for Probe termination: %w", err)
	}
	if result.exitCode != 137 {
		return state, fmt.Errorf("prove forced Probe termination: Docker run CLI exit=%d, want 137", result.exitCode)
	}
	terminalState, err := probe.runText(ctx, []string{"container", "inspect", "--format", probeTerminalStateFormat, containerName})
	if err != nil {
		return state, fmt.Errorf("inspect terminal Probe state: %w", err)
	}
	if err := verifyProbeTerminalState([]byte(terminalState)); err != nil {
		return state, fmt.Errorf("prove forced Probe termination: %w", err)
	}
	return state, nil
}

func (probe *CapabilityProbe) waitForProbeContainer(ctx context.Context, state *capabilityProbeProcess, containerName string) (string, error) {
	readinessCtx, cancel := context.WithTimeout(ctx, capabilityProbeReadinessTimeout)
	defer cancel()
	args := []string{"container", "inspect", "--format", "{{json .}}", containerName}
	var lastErr error
	for {
		if err := readinessCtx.Err(); err != nil {
			return "", fmt.Errorf("wait for Probe container readiness: %w", err)
		}
		select {
		case <-state.done:
			return "", fmt.Errorf("Probe container process exited before it became inspectable: Docker run CLI exit=%d: %v",
				state.result.exitCode, state.result.err)
		default:
		}
		result, err := probe.runner.Run(readinessCtx, Command{Args: slices.Clone(args)})
		if err != nil || result.ExitCode != 0 {
			lastErr = fmt.Errorf("docker exit %d: %v: %s", result.ExitCode, err, boundedDiagnostic(result.Stderr))
		}
		if readinessCtx.Err() != nil {
			return "", fmt.Errorf("wait for Probe container readiness: %w (last inspect: %v)", readinessCtx.Err(), lastErr)
		}
		select {
		case <-state.done:
			return "", fmt.Errorf("Probe container process exited before it became inspectable: Docker run CLI exit=%d: %v",
				state.result.exitCode, state.result.err)
		default:
		}
		if err == nil && result.ExitCode == 0 {
			running, stateErr := probeContainerRunning(result.Stdout)
			if stateErr != nil {
				return "", stateErr
			}
			if running {
				return strings.TrimSpace(string(result.Stdout)), nil
			}
			lastErr = errors.New("Probe container is visible but not running")
		} else {
			if !containerNotYetVisible(result.Stderr) {
				return "", lastErr
			}
		}

		timer := time.NewTimer(capabilityProbeReadinessPoll)
		select {
		case <-state.done:
			timer.Stop()
			return "", fmt.Errorf("Probe container process exited before it became inspectable: Docker run CLI exit=%d: %v",
				state.result.exitCode, state.result.err)
		case <-readinessCtx.Done():
			timer.Stop()
			return "", fmt.Errorf("wait for Probe container readiness: %w (last inspect: %v)", readinessCtx.Err(), lastErr)
		case <-timer.C:
		}
	}
}

func probeContainerRunning(data []byte) (bool, error) {
	var document struct {
		State *struct {
			Running *bool `json:"Running"`
		} `json:"State"`
	}
	if err := json.Unmarshal(data, &document); err != nil || document.State == nil || document.State.Running == nil {
		return false, errors.New("malformed effective container readiness state")
	}
	return *document.State.Running, nil
}

func containerNotYetVisible(stderr []byte) bool {
	diagnostic := strings.ToLower(string(stderr))
	return strings.Contains(diagnostic, "no such container") || strings.Contains(diagnostic, "no such object")
}

func (probe *CapabilityProbe) cleanup(root, containerName, networkName string, labels []string, process *capabilityProbeProcess) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var failures []error
	if process != nil {
		if err := process.close(); err != nil {
			failures = append(failures, fmt.Errorf("close Probe CLI: %w", err))
		}
	}
	if err := probe.cleanupCommand(ctx, []string{"rm", "-f", containerName}, "No such container"); err != nil {
		failures = append(failures, fmt.Errorf("remove Probe container: %w", err))
	}
	if process != nil {
		if !process.terminal() {
			if err := process.kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
				failures = append(failures, fmt.Errorf("kill Probe CLI: %w", err))
			}
		}
		if _, err := process.observe(ctx); err != nil {
			if killErr := process.kill(); killErr != nil && !errors.Is(killErr, os.ErrProcessDone) {
				failures = append(failures, fmt.Errorf("kill timed-out Probe CLI: %w", killErr))
			}
			failures = append(failures, fmt.Errorf("reap Probe CLI: %w", err))
		} else if err := process.killErr; err != nil && !errors.Is(err, os.ErrProcessDone) {
			failures = append(failures, fmt.Errorf("kill Probe CLI: %w", err))
		}
	}
	if err := probe.cleanupCommand(ctx, []string{"network", "rm", networkName}, "No such network"); err != nil {
		failures = append(failures, fmt.Errorf("remove Probe network: %w", err))
	}
	if err := os.RemoveAll(root); err != nil {
		failures = append(failures, fmt.Errorf("remove Probe root: %w", err))
	}
	containerInventory := []string{"ps", "-aq"}
	networkInventory := []string{"network", "ls", "-q"}
	for _, label := range labels {
		containerInventory = append(containerInventory, "--filter", "label="+label)
		networkInventory = append(networkInventory, "--filter", "label="+label)
	}
	if residue, err := probe.runText(ctx, containerInventory); err != nil {
		failures = append(failures, fmt.Errorf("inventory Probe containers: %w", err))
	} else if residue != "" {
		failures = append(failures, fmt.Errorf("inventory Probe containers: residue %s", boundedDiagnostic([]byte(residue))))
	}
	if residue, err := probe.runText(ctx, networkInventory); err != nil {
		failures = append(failures, fmt.Errorf("inventory Probe networks: %w", err))
	} else if residue != "" {
		failures = append(failures, fmt.Errorf("inventory Probe networks: residue %s", boundedDiagnostic([]byte(residue))))
	}
	if residue, err := probe.runText(ctx, []string{"ps", "-aq", "--filter", "name=^/" + containerName + "$"}); err != nil {
		failures = append(failures, fmt.Errorf("prove exact Probe container absent: %w", err))
	} else if residue != "" {
		failures = append(failures, errors.New("prove exact Probe container absent: residue remains"))
	}
	if residue, err := probe.runText(ctx, []string{"network", "ls", "-q", "--filter", "name=^" + networkName + "$"}); err != nil {
		failures = append(failures, fmt.Errorf("prove exact Probe network absent: %w", err))
	} else if residue != "" {
		failures = append(failures, errors.New("prove exact Probe network absent: residue remains"))
	}
	if _, err := os.Stat(root); err == nil {
		failures = append(failures, errors.New("prove Probe root absent: residue remains"))
	} else if !errors.Is(err, os.ErrNotExist) {
		failures = append(failures, fmt.Errorf("prove Probe root absent: %w", err))
	}
	return errors.Join(failures...)
}

type engineVersionObservation struct {
	APIVersion      string `json:"api_version"`
	ServerVersion   string `json:"server_version"`
	OperatingSystem string `json:"operating_system"`
	Architecture    string `json:"architecture"`
}

type engineInfoObservation struct {
	DaemonID     string `json:"daemon_id"`
	ProviderName string `json:"provider_name"`
}

func (probe *CapabilityProbe) observeEngine(ctx context.Context) (EngineIdentity, error) {
	return observeEngine(ctx, probe.runner)
}

// ObserveEngine reads the compatibility-bearing identity of the Docker daemon
// reached by runner. It performs no mutation and returns no qualification.
func ObserveEngine(ctx context.Context, runner CommandRunner) (EngineIdentity, error) {
	return observeEngine(ctx, runner)
}

// observeEngine re-observes the compatibility-bearing Docker Engine identity
// using only read-only CLI operations on the supplied Runner. Keeping the
// Runner explicit lets Probe, Start, and Recover bind their observations to the
// exact command authority that will perform the subsequent lifecycle work.
func observeEngine(ctx context.Context, runner CommandRunner) (EngineIdentity, error) {
	versionJSON, err := runnerText(ctx, runner, []string{"version", "--format", engineVersionObservationFormat})
	if err != nil {
		return EngineIdentity{}, fmt.Errorf("observe server version: %w", err)
	}
	var version engineVersionObservation
	if err := strictJSON([]byte(versionJSON), &version); err != nil {
		return EngineIdentity{}, fmt.Errorf("observe server version: malformed JSON: %w", err)
	}
	infoJSON, err := runnerText(ctx, runner, []string{"info", "--format", engineInfoObservationFormat})
	if err != nil {
		return EngineIdentity{}, fmt.Errorf("observe daemon identity: %w", err)
	}
	var info engineInfoObservation
	if err := strictJSON([]byte(infoJSON), &info); err != nil {
		return EngineIdentity{}, fmt.Errorf("observe daemon identity: malformed JSON: %w", err)
	}
	contextName, err := runnerText(ctx, runner, []string{"context", "show"})
	if err != nil {
		return EngineIdentity{}, fmt.Errorf("observe selected context: %w", err)
	}
	if !validText(contextName, 256) {
		return EngineIdentity{}, errors.New("observe selected context: invalid name")
	}
	endpointJSON, err := runnerText(ctx, runner, []string{"context", "inspect", "--format", engineEndpointObservationFormat, contextName})
	if err != nil {
		return EngineIdentity{}, fmt.Errorf("observe selected context endpoint: %w", err)
	}
	var endpoint string
	if err := strictJSON([]byte(endpointJSON), &endpoint); err != nil {
		return EngineIdentity{}, fmt.Errorf("observe selected context endpoint: malformed JSON: %w", err)
	}
	endpointDigest, err := DigestContextEndpoint(endpoint)
	if err != nil {
		return EngineIdentity{}, fmt.Errorf("observe selected context endpoint: %w", err)
	}
	return NewEngineIdentity(EngineIdentityInput{
		DaemonID: info.DaemonID, APIVersion: version.APIVersion, OperatingSystem: version.OperatingSystem,
		Architecture: version.Architecture, ContextEndpointDigest: endpointDigest, ProviderName: info.ProviderName,
		EngineVersion: version.ServerVersion, ContextName: contextName,
	})
}

func runnerText(ctx context.Context, runner CommandRunner, args []string) (string, error) {
	if runner == nil {
		return "", errors.New("Docker Runner is unavailable")
	}
	result, err := runner.Run(ctx, Command{Args: slices.Clone(args)})
	if err != nil || result.ExitCode != 0 {
		return "", fmt.Errorf("docker exit %d: %v: %s", result.ExitCode, err, boundedDiagnostic(result.Stderr))
	}
	return strings.TrimSpace(string(result.Stdout)), nil
}

func verifyProbeTerminalState(data []byte) error {
	var state struct {
		Running   bool   `json:"running"`
		ExitCode  int    `json:"exit_code"`
		OOMKilled bool   `json:"oom_killed"`
		Dead      bool   `json:"dead"`
		Error     string `json:"error"`
	}
	if err := strictJSON(data, &state); err != nil {
		return fmt.Errorf("malformed terminal state JSON: %w", err)
	}
	if state.Running || state.ExitCode != 137 || state.OOMKilled || state.Dead || state.Error != "" {
		return fmt.Errorf("terminal state drift: running=%t exit=%d oom_killed=%t dead=%t error=%q",
			state.Running, state.ExitCode, state.OOMKilled, state.Dead, state.Error)
	}
	return nil
}

func (probe *CapabilityProbe) cleanupCommand(ctx context.Context, args []string, absenceDiagnostic string) error {
	result, err := probe.runner.Run(ctx, Command{Args: slices.Clone(args)})
	if err == nil && result.ExitCode == 0 {
		return nil
	}
	if strings.Contains(string(result.Stderr), absenceDiagnostic) {
		return nil
	}
	return fmt.Errorf("docker exit %d: %v: %s", result.ExitCode, err, boundedDiagnostic(result.Stderr))
}

func (probe *CapabilityProbe) runOK(ctx context.Context, args []string) error {
	result, err := probe.runner.Run(ctx, Command{Args: slices.Clone(args)})
	if err != nil || result.ExitCode != 0 {
		return fmt.Errorf("docker exit %d: %v: %s", result.ExitCode, err, boundedDiagnostic(result.Stderr))
	}
	return nil
}

func (probe *CapabilityProbe) runText(ctx context.Context, args []string) (string, error) {
	result, err := probe.runner.Run(ctx, Command{Args: slices.Clone(args)})
	if err != nil || result.ExitCode != 0 {
		return "", fmt.Errorf("docker exit %d: %v: %s", result.ExitCode, err, boundedDiagnostic(result.Stderr))
	}
	return strings.TrimSpace(string(result.Stdout)), nil
}

func (probe *CapabilityProbe) resourceLabels(probeID string) []string {
	return []string{
		"chora.owner=" + probeOwnerLabelValue,
		"chora.resource_kind=" + probeKindLabelValue,
		"chora.probe_id=" + probeID,
		"chora.engine_digest=" + probe.identity.Digest(),
		"chora.contract_digest=" + probe.contract.Digest(),
		"chora.image_digest=" + probe.contract.ProbeImageID(),
		"chora.policy_digest=" + probe.contract.SandboxPolicyDigest(),
	}
}

func appendLabelArgs(args, labels []string) []string {
	for _, label := range labels {
		args = append(args, "--label", label)
	}
	return args
}

func verifyEffectiveProbeNetwork(data []byte, networkName string, labels []string) error {
	var network struct {
		Name     string            `json:"Name"`
		ID       string            `json:"Id"`
		Internal bool              `json:"Internal"`
		Labels   map[string]string `json:"Labels"`
	}
	if err := json.Unmarshal(data, &network); err != nil || network.Name != networkName || len(network.ID) != 64 ||
		!isLowerHex(network.ID) || !network.Internal {
		return errors.New("effective Probe network identity or internal boundary drift")
	}
	wantLabels := make(map[string]string, len(labels))
	for _, label := range labels {
		key, value, _ := strings.Cut(label, "=")
		wantLabels[key] = value
	}
	for key, value := range network.Labels {
		if strings.HasPrefix(key, "chora.") && wantLabels[key] != value {
			return fmt.Errorf("unexpected Probe network Chora label %s", key)
		}
	}
	for key, value := range wantLabels {
		if network.Labels[key] != value {
			return fmt.Errorf("Probe network Chora label %s drift", key)
		}
	}
	return nil
}

func verifyEffectiveProbe(data []byte, root, containerName, networkName, imageID string, labels []string) error {
	var container effectiveContainer
	// Docker's inspect document contains daemon-version-dependent fields. Only
	// the complete security-relevant projection below is accepted; unrelated
	// informational fields do not make compatible daemons provider-specific.
	if err := json.Unmarshal(data, &container); err != nil {
		return errors.New("malformed effective container JSON")
	}
	var state struct {
		State struct {
			Running bool `json:"Running"`
		} `json:"State"`
	}
	if err := json.Unmarshal(data, &state); err != nil || !state.State.Running {
		return errors.New("Probe container is not running")
	}
	wantLabels := make(map[string]string, len(labels))
	for _, label := range labels {
		key, value, _ := strings.Cut(label, "=")
		wantLabels[key] = value
	}
	for key, value := range container.Config.Labels {
		if strings.HasPrefix(key, "chora.") && wantLabels[key] != value {
			return fmt.Errorf("unexpected Chora label %s", key)
		}
	}
	for key, value := range wantLabels {
		if container.Config.Labels[key] != value {
			return fmt.Errorf("Chora label %s drift", key)
		}
	}
	host := container.HostConfig
	if len(container.ID) != 64 || !isLowerHex(container.ID) || container.Name != "/"+containerName || container.Image != imageID ||
		container.Config.User != "1000:1000" || host.NetworkMode != networkName || !host.ReadonlyRootfs || host.Privileged ||
		host.PidMode == "host" || host.IpcMode == "host" || host.NanoCPUs != 100_000_000 || host.Memory != 32<<20 ||
		host.MemorySwap != 32<<20 || host.PidsLimit != 16 || host.LogConfig.Type != "none" ||
		!slices.Equal(host.CapDrop, []string{"ALL"}) || len(host.CapAdd) != 0 ||
		!slices.Equal(host.SecurityOpt, []string{"no-new-privileges:true"}) || len(host.Devices) != 0 ||
		len(host.DeviceRequests) != 0 || len(host.PortBindings) != 0 || !hasNofileLimit(container, 64) ||
		!tmpfsMatches(host.Tmpfs["/tmp"], []string{"rw", "nosuid", "nodev", "noexec", "size=8m", "uid=1000", "gid=1000"}) ||
		!exactNetworkMembership(container, []string{networkName}) || !exactMounts(container, map[string]effectiveMount{
		"/chora-probe": {Source: filepath.Clean(root), RW: false},
	}) {
		return errors.New("effective Probe identity, isolation, limits, network, or mount drift")
	}
	return nil
}

func randomProbeID() (string, error) {
	value := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func validProbeID(value string) bool { return len(value) == 32 && isLowerHex(value) }

func strictJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func digestJSON(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func compatibleAPIVersion(actual, minimum string) bool {
	actualMajor, actualMinor, ok := parseAPIVersion(actual)
	if !ok {
		return false
	}
	minimumMajor, minimumMinor, ok := parseAPIVersion(minimum)
	return ok && (actualMajor > minimumMajor || actualMajor == minimumMajor && actualMinor >= minimumMinor)
}

func parseAPIVersion(value string) (int, int, bool) {
	parts := strings.Split(value, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" || (len(parts[0]) > 1 && parts[0][0] == '0') ||
		(len(parts[1]) > 1 && parts[1][0] == '0') {
		return 0, 0, false
	}
	major, errMajor := strconv.Atoi(parts[0])
	minor, errMinor := strconv.Atoi(parts[1])
	return major, minor, errMajor == nil && errMinor == nil && major >= 0 && minor >= 0
}

func validCanonicalDigest(value string) bool { return len(value) == 64 && isLowerHex(value) }

func isLowerHex(value string) bool {
	for _, character := range value {
		if !(character >= '0' && character <= '9') && !(character >= 'a' && character <= 'f') {
			return false
		}
	}
	return true
}

func validText(value string, maximum int) bool {
	return value != "" && utf8.ValidString(value) && value == strings.TrimSpace(value) && len(value) <= maximum && !strings.ContainsAny(value, "\x00\r\n")
}
