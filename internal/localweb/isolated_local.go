package localweb

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	agentpi "github.com/Yangyang96/chora/internal/agent/pi"
	"github.com/Yangyang96/chora/internal/dockersupervisor"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
	"github.com/Yangyang96/chora/internal/isolatedenv"
	"github.com/Yangyang96/chora/internal/isolatedworkspace"
	"github.com/Yangyang96/chora/internal/speccoding"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

type isolatedLocalView struct {
	Policy               isolatedPolicyView `json:"policy"`
	State                string             `json:"state"`
	Reason               string             `json:"reason"`
	ImageID              string             `json:"imageId,omitempty"`
	PiVersion            string             `json:"piVersion"`
	NodeVersion          string             `json:"nodeVersion"`
	PreparationAvailable bool               `json:"preparationAvailable"`
	RestartRequired      bool               `json:"restartRequired"`
}
type isolatedPolicyView struct {
	Network     string `json:"network"`
	Resources   string `json:"resources"`
	Files       string `json:"files"`
	Credentials string `json:"credentials"`
}

func isolatedPolicy() isolatedPolicyView {
	return isolatedPolicyView{
		Network:     "Outbound Docker bridge access; no domain allowlist, host networking or published ports. Host services may be reachable.",
		Resources:   "2 CPUs, 4 GiB container memory, 256 processes, plus a 256 MiB Docker VM memory workspace; 20 minutes per attempt.",
		Files:       "Private repository copies; Git metadata and reference repositories read-only. Validated output returns only to Task worktrees.",
		Credentials: "Only your DeepSeek API key enters temporary memory through stdin. Other provider credentials stay on the host; the projection disappears with the container.",
	}
}

type isolatedLocalEnvironment struct {
	mu                   sync.Mutex
	view                 isolatedLocalView
	sourceRoot, dataRoot string
	source               agentpi.IsolatedSource
}

func newIsolatedLocalEnvironment(sourceRoot, dataRoot string) *isolatedLocalEnvironment {
	return &isolatedLocalEnvironment{sourceRoot: sourceRoot, dataRoot: dataRoot, view: isolatedLocalView{Policy: isolatedPolicy(), State: "not_prepared", Reason: "Prepare the public Pi Docker environment before selecting Isolated Local.", PiVersion: agentpi.IsolatedPiVersion, NodeVersion: "22.19.0", PreparationAvailable: sourceRoot != ""}}
}
func (e *isolatedLocalEnvironment) status() isolatedLocalView {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.view
}
func (s *Server) getIsolatedLocal(w http.ResponseWriter, r *http.Request) {
	if s.isolatedLocal == nil {
		writeJSON(w, http.StatusOK, newIsolatedLocalEnvironment("", "").status())
		return
	}
	view := s.isolatedLocal.status()
	if view.State == "ready" {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		loaded, err := isolatedenv.Load(ctx, s.isolatedLocal.dataRoot)
		if err != nil || loaded.Source != s.isolatedLocal.source {
			view.State = "failed"
			view.Reason = "The prepared Docker environment is unavailable or changed. Restore it and restart Workbench; execution will not fall back to the host."
		}
	}
	writeJSON(w, http.StatusOK, view)
}
func (s *Server) prepareIsolatedLocal(w http.ResponseWriter, r *http.Request) {
	var input struct{}
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	e := s.isolatedLocal
	if e == nil || e.sourceRoot == "" {
		writeError(w, http.StatusServiceUnavailable, errors.New("public source checkout preparation is unavailable"))
		return
	}
	e.mu.Lock()
	if e.view.State == "preparing" {
		view := e.view
		e.mu.Unlock()
		writeJSON(w, http.StatusAccepted, view)
		return
	}
	e.view.State = "preparing"
	e.view.Reason = "Building and qualifying the public Pi Docker image. Credentials are not used during preparation."
	e.view.RestartRequired = false
	view := e.view
	e.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(s.ctx, 20*time.Minute)
		defer cancel()
		result, err := isolatedenv.Prepare(ctx, e.sourceRoot, e.dataRoot)
		e.mu.Lock()
		defer e.mu.Unlock()
		if err != nil {
			e.view.State = "failed"
			e.view.Reason = err.Error()
			return
		}
		e.view.State = "restart_required"
		e.view.RestartRequired = true
		e.view.ImageID = result.Source.ImageID()
		e.view.Reason = "Environment prepared. Restart Workbench to activate this exact image."
	}()
	writeJSON(w, http.StatusAccepted, view)
}
func (e *isolatedLocalEnvironment) available(ctx context.Context) error {
	if e == nil || e.status().State != "ready" {
		return errors.New("Isolated Local is not ready; prepare the environment and restart Workbench")
	}
	record, err := isolatedenv.Load(ctx, e.dataRoot)
	if err != nil {
		return err
	}
	if record.Source != e.source {
		return errors.New("Isolated Local environment changed; restart required")
	}
	return nil
}
func composeIsolatedLocal(ctx context.Context, composition piComposition, runtimeRoot string, reader storecontract.Reader, e *isolatedLocalEnvironment, authHome string) (piComposition, error) {
	record, err := isolatedenv.Load(ctx, e.dataRoot)
	if err != nil {
		return composition, err
	}
	var adapter *agentpi.Adapter
	if composition.adapter == nil {
		adapter, err = agentpi.New(agentpi.Config{IsolatedSource: record.Source})
	} else if base, ok := composition.adapter.(*agentpi.Adapter); ok {
		adapter, err = base.WithIsolatedSource(record.Source)
	} else {
		err = errors.New("incompatible public Pi adapter composition")
	}
	if err != nil {
		return composition, err
	}
	if authHome == "" {
		home, homeErr := os.UserHomeDir()
		if homeErr != nil {
			return composition, homeErr
		}
		authHome = filepath.Join(home, ".pi", "agent")
	}
	workspace := isolatedWorkspaceCallbacks{reader: reader, source: record.Source, environment: e}
	fp := record.Source.Fingerprint()
	docker, err := dockersupervisor.New(dockersupervisor.Config{
		Runner: record.Runner, RuntimeRoot: filepath.Join(runtimeRoot, "pi-isolated"), ArtifactRoot: filepath.Join(runtimeRoot, "artifacts-isolated"),
		PolicyDigest: record.Source.PolicySHA256(), AttemptImageID: record.Source.ImageID(), RuntimeSourceIdentity: fmt.Sprintf("%x", record.Source.SourceIdentity()),
		CredentialSource: filepath.Join(authHome, "auth.json"), AttemptTimeout: 20 * time.Minute, CapabilityContract: record.Capability, EngineQualification: record.Qualification,
		Workbench: &dockersupervisor.WorkbenchConfig{Arguments: agentpi.IsolatedRPCArguments(), RuntimeVersion: agentpi.IsolatedPiVersion, ObserverSHA256: agentpi.ResourceObserverSHA256(), HelperSHA256: record.Source.HelperSHA256(), RuntimeFingerprint: hex.EncodeToString(fp.Digest[:]), PrepareWorkspace: workspace.prepare, CollectWorkspace: workspace.collect, ReadOnlyWorkspacePaths: workspace.readonly},
	})
	if err != nil {
		return composition, err
	}
	if _, err = docker.Verify(ctx); err != nil {
		return composition, err
	}
	if err = docker.Recover(ctx); err != nil {
		return composition, err
	}
	composition.adapter = adapter
	composition.dockerSupervisor = docker
	composition.attemptImageID = record.Source.ImageID()
	composition.isolatedSource = record.Source
	e.source = record.Source
	e.view.State = "ready"
	e.view.Reason = "Public Pi image and Docker isolation are ready."
	e.view.ImageID = record.Source.ImageID()
	return composition, nil
}

type isolatedWorkspaceCallbacks struct {
	reader      storecontract.Reader
	source      agentpi.IsolatedSource
	environment *isolatedLocalEnvironment
}

func (c isolatedWorkspaceCallbacks) resources(ctx context.Context, inv execution.Invocation) ([]domain.TaskRepositoryResource, error) {
	id, err := domain.ParseAttemptID(inv.Environment()["CHORA_ATTEMPT_ID"])
	if err != nil {
		return nil, err
	}
	attempt, err := c.reader.GetAttempt(ctx, id)
	if err != nil {
		return nil, err
	}
	if attempt.AgentExecutionProfileBinding().Profile() != domain.AgentExecutionProfileIsolatedLocal {
		return nil, errors.New("attempt is not isolated")
	}
	run, err := c.reader.GetRun(ctx, attempt.RunID())
	if err != nil {
		return nil, err
	}
	raw, err := c.reader.GetTaskResourceSnapshot(ctx, run.TaskID())
	if err != nil {
		return nil, err
	}
	var snapshot domain.TaskResourceSnapshot
	if err = json.Unmarshal(raw.CanonicalJSON, &snapshot); err != nil {
		return nil, err
	}
	if _, _, err = snapshot.CanonicalJSON(); err != nil {
		return nil, err
	}
	return snapshot.Resources, nil
}
func (c isolatedWorkspaceCallbacks) readonly(inv execution.Invocation) ([]string, error) {
	resources, err := c.resources(context.Background(), inv)
	if err != nil {
		return nil, err
	}
	return isolatedworkspace.ReadonlyRelativeMountPaths(resources), nil
}
func (c isolatedWorkspaceCallbacks) prepare(ctx context.Context, inv execution.Invocation, dest string) error {
	if err := c.environment.available(ctx); err != nil {
		return err
	}
	resources, err := c.resources(ctx, inv)
	if err != nil {
		return err
	}
	contract, err := isolatedInvocationContract(inv)
	if err != nil {
		return err
	}
	if err = c.source.ValidateContract(contract); err != nil {
		return err
	}
	id, _ := domain.ParseAttemptID(inv.Environment()["CHORA_ATTEMPT_ID"])
	observer, err := agentpi.IsolatedObserverConfig(id, contract)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(observer)
	if hex.EncodeToString(sum[:]) != inv.Environment()["CHORA_CHECK_OBSERVER_CONFIG_SHA256"] {
		return errors.New("observer config drift")
	}
	root := filepath.Dir(filepath.Dir(dest))
	if err = os.WriteFile(filepath.Join(root, "context", "observer.json"), observer, 0444); err != nil {
		return err
	}
	stateRoot := filepath.Join(root, "import-state")
	if err = os.Mkdir(stateRoot, 0700); err != nil {
		return fmt.Errorf("create private import state: %w", err)
	}
	return isolatedworkspace.Prepare(ctx, inv.WorkingRoot(), dest, stateRoot, resources)
}
func (c isolatedWorkspaceCallbacks) collect(ctx context.Context, inv execution.Invocation, dest string) error {
	resources, err := c.resources(ctx, inv)
	if err != nil {
		return err
	}
	return isolatedworkspace.Collect(ctx, inv.WorkingRoot(), dest, filepath.Join(filepath.Dir(filepath.Dir(dest)), "import-state"), resources)
}
func isolatedInvocationContract(inv execution.Invocation) ([]byte, error) {
	lines := bytes.Split(bytes.TrimSpace(inv.Stdin()), []byte{'\n'})
	if len(lines) != 2 {
		return nil, errors.New("invalid isolated RPC input")
	}
	var prompt struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(lines[1], &prompt) != nil {
		return nil, errors.New("invalid isolated prompt")
	}
	message, _, _ := strings.Cut(prompt.Message, "\n\nHuman Review correction for this successor Attempt.")
	_, contract, ok := execution.UnwrapFrozenExecutionPrompt(message)
	if !ok {
		return nil, errors.New("missing frozen isolated contract")
	}
	return contract, nil
}
func isolatedExecutionIdentity(source agentpi.IsolatedSource) speccoding.IsolatedExecutionIdentity {
	return speccoding.IsolatedExecutionIdentity{ImageSHA256: strings.TrimPrefix(source.ImageID(), "sha256:"), SourceSHA256: fmt.Sprintf("%x", source.SourceIdentity()), PolicySHA256: source.PolicySHA256()}
}
