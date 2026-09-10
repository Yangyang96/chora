package dockersupervisor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

const (
	residueObservationSchema           = "chora.m1-o4-residue-observation.v1"
	volumeOwnershipObservationFormat   = `{"name":{{json .Name}},"labels":{{json .Labels}}}`
	configOwnershipObservationFormat   = `{"name":{{json .Spec.Name}},"labels":{{json .Spec.Labels}}}`
	verifierContainerObservationFormat = `{"name":{{json .Name}},"image":{{json .Image}},"labels":{{json .Config.Labels}}}`
	swarmStateObservationFormat        = `{{json .Swarm.LocalNodeState}}|{{json .Swarm.ControlAvailable}}|{{json .Swarm.NodeID}}`
)

// ResidueCount is a safe aggregate. Resource identifiers and raw diagnostics
// never cross the observer boundary.
type ResidueCount struct {
	Count           int    `json:"count"`
	AggregateSHA256 string `json:"aggregateSha256"`
}

type ResidueObservation struct {
	SchemaVersion             string       `json:"schemaVersion"`
	Status                    string       `json:"status"`
	EngineQualificationSHA256 string       `json:"engineQualificationSha256"`
	RuntimeScopeSHA256        string       `json:"runtimeScopeSha256"`
	Containers                ResidueCount `json:"containers"`
	VerifierContainers        ResidueCount `json:"verifierContainers"`
	Networks                  ResidueCount `json:"networks"`
	Volumes                   ResidueCount `json:"volumes"`
	Configs                   ResidueCount `json:"configs"`
	ManagedWorkspaces         ResidueCount `json:"managedWorkspaces"`
	AttemptProcessGroups      ResidueCount `json:"attemptProcessGroups"`
	SwarmInactive             bool         `json:"swarmInactive"`
	ServingProcessExcluded    bool         `json:"servingProcessExcluded"`
	OperationLedgerSettled    bool         `json:"operationLedgerSettled"`
	OperationLedgerCount      int          `json:"operationLedgerCount"`
	OperationLedgerSHA256     string       `json:"operationLedgerSha256"`
}

type observedResource struct {
	Kind   string            `json:"kind"`
	ID     string            `json:"id"`
	Name   string            `json:"name"`
	Image  string            `json:"image,omitempty"`
	Labels map[string]string `json:"labels"`
}

// ObserveResidue reuses the exact installed Runner and qualification held by
// this Supervisor. It is read-only. Every non-empty resource is independently
// inspected and authenticated before only count/digest aggregates are returned.
func (supervisor *Supervisor) ObserveResidue(ctx context.Context) (ResidueObservation, error) {
	if supervisor == nil || supervisor.operationLedger == nil {
		return ResidueObservation{}, errors.New("installed Docker operation ledger observation is unavailable")
	}
	ctx = WithOperationPhase(ctx, OperationPhaseAttempt)
	if _, err := supervisor.qualifyExecution(ctx); err != nil {
		return ResidueObservation{}, fmt.Errorf("residue Engine qualification failed: %w", err)
	}
	filters := supervisor.ownedResourceFilters("")
	containers, err := supervisor.observeResourceKind(ctx, "container", appendDockerFilters([]string{"ps", "-aq"}, filters), containerOwnershipObservationFormat)
	if err != nil {
		return ResidueObservation{}, err
	}
	verifierContainers, err := supervisor.observeVerifierContainers(ctx)
	if err != nil {
		return ResidueObservation{}, err
	}
	networks, err := supervisor.observeResourceKind(ctx, "network", appendDockerFilters([]string{"network", "ls", "-q"}, filters), networkOwnershipObservationFormat)
	if err != nil {
		return ResidueObservation{}, err
	}
	volumes, err := supervisor.observeResourceKind(ctx, "volume", appendDockerFilters([]string{"volume", "ls", "-q"}, filters), volumeOwnershipObservationFormat)
	if err != nil {
		return ResidueObservation{}, err
	}
	swarmText, err := supervisor.identityCommand(ctx, []string{"info", "--format", swarmStateObservationFormat})
	if err != nil {
		return ResidueObservation{}, errors.New("authenticated Swarm state observation failed")
	}
	var configs []observedResource
	swarmInactive := swarmText == `"inactive"|false|""`
	if !swarmInactive {
		if swarmText != `"active"|true|`+jsonQuotedNonemptyToken(swarmNodeIDFromObservation(swarmText)) {
			return ResidueObservation{}, errors.New("Swarm state is neither authenticated inactive nor active manager")
		}
		configs, err = supervisor.observeResourceKind(ctx, "config", appendDockerFilters([]string{"config", "ls", "-q"}, filters), configOwnershipObservationFormat)
		if err != nil {
			return ResidueObservation{}, err
		}
	}
	workspaces, err := supervisor.observeManagedWorkspaces()
	if err != nil {
		return ResidueObservation{}, err
	}
	ledger, err := supervisor.operationLedger.Snapshot()
	if err != nil {
		return ResidueObservation{}, errors.New("Docker operation ledger is unsettled or invalid")
	}
	containerCount := aggregateObservedResources(containers)
	verifierContainerCount := aggregateObservedResources(verifierContainers)
	return ResidueObservation{
		SchemaVersion: residueObservationSchema, Status: "proven",
		EngineQualificationSHA256: supervisor.config.EngineQualification.Digest(), RuntimeScopeSHA256: strings.TrimPrefix(supervisor.recoveryScope, "sha256:"),
		Containers: containerCount, VerifierContainers: verifierContainerCount, Networks: aggregateObservedResources(networks), Volumes: aggregateObservedResources(volumes),
		Configs: aggregateObservedResources(configs), ManagedWorkspaces: aggregateBytes(workspaces), AttemptProcessGroups: combineResidueCounts(containerCount, verifierContainerCount),
		SwarmInactive: swarmInactive, ServingProcessExcluded: true, OperationLedgerSettled: true,
		OperationLedgerCount: ledger.AuditRecordCount, OperationLedgerSHA256: ledger.AuditFinalDigest,
	}, nil
}

func combineResidueCounts(values ...ResidueCount) ResidueCount {
	hash := sha256.New()
	count := 0
	for _, value := range values {
		count += value.Count
		_, _ = hash.Write([]byte(value.AggregateSHA256))
		_, _ = hash.Write([]byte{'\n'})
	}
	return ResidueCount{Count: count, AggregateSHA256: hex.EncodeToString(hash.Sum(nil))}
}

func (supervisor *Supervisor) observeResourceKind(ctx context.Context, kind string, listArgs []string, format string) ([]observedResource, error) {
	text, err := supervisor.identityCommand(ctx, listArgs)
	if err != nil {
		return nil, fmt.Errorf("enumerate authenticated %s resources: %w", kind, err)
	}
	identities := strings.Fields(text)
	if len(uniqueNonempty(identities)) != len(identities) {
		return nil, fmt.Errorf("%s resource identity collision", kind)
	}
	result := make([]observedResource, 0, len(identities))
	for _, identity := range identities {
		if !safeDockerIdentity(identity) {
			return nil, fmt.Errorf("%s resource identity is unsafe", kind)
		}
		observation, err := supervisor.observeResourceOwnership(ctx, kind, identity, format)
		if err != nil || !supervisor.validObservedResource(kind, observation) {
			return nil, fmt.Errorf("%s resource ownership is unknown", kind)
		}
		result = append(result, observedResource{Kind: kind, ID: identity, Name: observation.Name, Image: observation.Image, Labels: observation.Labels})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}

func (supervisor *Supervisor) observeVerifierContainers(ctx context.Context) ([]observedResource, error) {
	if !validImageID(supervisor.config.VerifierImageID) {
		return nil, errors.New("installed Verifier image identity is unavailable")
	}
	runtimeRoot := filepath.Dir(supervisor.config.RuntimeRoot)
	scopeDigest := sha256.Sum256([]byte(filepath.Clean(runtimeRoot)))
	scope := "sha256:" + hex.EncodeToString(scopeDigest[:])
	text, err := supervisor.identityCommand(ctx, []string{"container", "ls", "--all", "--quiet", "--no-trunc", "--filter", "label=chora.verifier_runtime_id=" + scope})
	if err != nil {
		return nil, errors.New("enumerate authenticated Verifier containers failed")
	}
	identities := strings.Fields(text)
	if len(uniqueNonempty(identities)) != len(identities) {
		return nil, errors.New("Verifier container identity collision")
	}
	resources := make([]observedResource, 0, len(identities))
	for _, identity := range identities {
		if len(identity) != 64 || !isLowerHex(identity) {
			return nil, errors.New("Verifier container identity is unsafe")
		}
		observation, err := supervisor.observeResourceOwnership(ctx, "container", identity, verifierContainerObservationFormat)
		if err != nil || !supervisor.validVerifierContainer(observation, scope) {
			return nil, errors.New("Verifier container ownership is unknown")
		}
		resources = append(resources, observedResource{Kind: "verifier_container", ID: identity, Name: observation.Name, Image: observation.Image, Labels: observation.Labels})
	}
	sort.Slice(resources, func(i, j int) bool { return resources[i].ID < resources[j].ID })
	return resources, nil
}

func (supervisor *Supervisor) validVerifierContainer(observation resourceOwnershipObservation, scope string) bool {
	keys := make([]string, 0, len(observation.Labels))
	for key := range observation.Labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	wantKeys := []string{
		"chora.acceptance_contract_digest", "chora.baseline_digest", "chora.image_digest", "chora.patch_digest", "chora.run_id",
		"chora.task_id", "chora.verification_attempt_id", "chora.verification_run_id", "chora.verifier_policy_digest", "chora.verifier_runtime_id", "chora.workspace_id",
	}
	labels := observation.Labels
	name := strings.TrimPrefix(observation.Name, "/")
	if !slices.Equal(keys, wantKeys) || observation.Image != supervisor.config.VerifierImageID || labels["chora.image_digest"] != supervisor.config.VerifierImageID ||
		labels["chora.verifier_runtime_id"] != scope || !strings.HasPrefix(name, "chora-verifier-") || len(name) != len("chora-verifier-")+20 || !isLowerHex(strings.TrimPrefix(name, "chora-verifier-")) ||
		!validText(labels["chora.run_id"], 256) || !validText(labels["chora.verification_run_id"], 256) || !validText(labels["chora.verification_attempt_id"], 256) || !validText(labels["chora.task_id"], 256) ||
		!validCanonicalDigest(labels["chora.verifier_policy_digest"]) || !validCanonicalDigest(labels["chora.baseline_digest"]) || !validCanonicalDigest(labels["chora.patch_digest"]) ||
		!validCanonicalDigest(labels["chora.acceptance_contract_digest"]) || !strings.HasPrefix(labels["chora.workspace_id"], "sha256:") || !validCanonicalDigest(strings.TrimPrefix(labels["chora.workspace_id"], "sha256:")) {
		return false
	}
	return true
}

func (supervisor *Supervisor) validObservedResource(kind string, observation resourceOwnershipObservation) bool {
	labels := observation.Labels
	wantKeys := []string{"chora.attempt_id", "chora.image_digest", "chora.owner", "chora.policy_digest", "chora.run_id", "chora.runtime_scope", "chora.task_id"}
	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if !slices.Equal(keys, wantKeys) || !supervisor.validRecoveryOwnership(labels) || labels["chora.policy_digest"] != supervisor.config.PolicyDigest ||
		!validText(labels["chora.run_id"], 256) || !validText(labels["chora.attempt_id"], 256) || !validText(labels["chora.task_id"], 256) {
		return false
	}
	image := labels["chora.image_digest"]
	if image != supervisor.config.AttemptImageID && image != supervisor.config.BoundaryImageID {
		return false
	}
	name := strings.TrimPrefix(observation.Name, "/")
	if len(name) <= len("chora-")+24 || !validAttemptRootName(name[:len("chora-")+24]) {
		return false
	}
	suffix := name[len("chora-")+24:]
	switch kind {
	case "container":
		if suffix == "-codex-boundary" {
			return image == supervisor.config.BoundaryImageID
		}
		return suffix == "-attempt" || strings.HasPrefix(suffix, "-attempt-check-")
	case "network":
		return image == supervisor.config.AttemptImageID && (suffix == "-internal" || suffix == "-upstream")
	case "volume", "config":
		return validOwnedResourceSuffix(suffix)
	default:
		return false
	}
}

func (supervisor *Supervisor) observeManagedWorkspaces() ([][]byte, error) {
	entries, err := os.ReadDir(supervisor.config.RuntimeRoot)
	if err != nil {
		return nil, errors.New("enumerate managed workspaces failed")
	}
	data := make([][]byte, 0, len(entries))
	for _, entry := range entries {
		path := filepath.Join(supervisor.config.RuntimeRoot, entry.Name())
		if err := supervisor.validateAttemptRoot(path, entry); err != nil {
			return nil, errors.New("unknown managed workspace entry")
		}
		marker, err := os.ReadFile(filepath.Join(path, attemptRootMarkerName))
		if err != nil {
			return nil, errors.New("read managed workspace marker failed")
		}
		data = append(data, marker)
	}
	sort.Slice(data, func(i, j int) bool { return bytes.Compare(data[i], data[j]) < 0 })
	return data, nil
}

func aggregateObservedResources(resources []observedResource) ResidueCount {
	encoded := make([][]byte, 0, len(resources))
	for _, resource := range resources {
		data, _ := json.Marshal(resource)
		encoded = append(encoded, data)
	}
	return aggregateBytes(encoded)
}

func aggregateBytes(values [][]byte) ResidueCount {
	hash := sha256.New()
	for _, value := range values {
		_, _ = hash.Write(value)
		_, _ = hash.Write([]byte{'\n'})
	}
	return ResidueCount{Count: len(values), AggregateSHA256: hex.EncodeToString(hash.Sum(nil))}
}

func safeDockerIdentity(value string) bool {
	return validText(value, 256) && !strings.ContainsAny(value, " \t\r\n")
}

func validOwnedResourceSuffix(value string) bool {
	if len(value) < 2 || len(value) > 96 || value[0] != '-' {
		return false
	}
	for _, character := range value[1:] {
		if character < 'a' || character > 'z' && character < '0' || character > '9' && character != '-' && character != '_' && character != '.' {
			return false
		}
	}
	return true
}

func swarmNodeIDFromObservation(value string) string {
	const prefix = `"active"|true|"`
	if !strings.HasPrefix(value, prefix) || !strings.HasSuffix(value, `"`) {
		return ""
	}
	return strings.TrimSuffix(strings.TrimPrefix(value, prefix), `"`)
}

func jsonQuotedNonemptyToken(value string) string {
	if !safeDockerIdentity(value) || value == "" {
		return ""
	}
	data, _ := json.Marshal(value)
	return string(data)
}
