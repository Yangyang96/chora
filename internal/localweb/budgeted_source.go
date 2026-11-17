package localweb

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sort"

	agentpi "github.com/Yangyang96/chora/internal/agent/pi"
	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/dockersupervisor"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
	"github.com/Yangyang96/chora/internal/isolatedworkspace"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

// BudgetedCandidate is non-secret, explicit installation evidence from the
// existing setup boundary. Nothing is discovered from HOME/auth or guessed.
// Structural assembly does not grant model, permission or Engine qualification.
type BudgetedCandidate struct {
	Identity      domain.RestrictedPiIdentity
	Permissions   agentpi.BudgetedPermissionPlan
	Catalog       domain.ModelCatalog
	Runner        dockersupervisor.CommandRunner
	Capability    dockersupervisor.CapabilityProbeContract
	Qualification dockersupervisor.EngineQualification
}

func composeBudgetedSource(base execution.AgentAdapter, reader storecontract.Reader, root string, c BudgetedCandidate, authority dockersupervisor.BudgetedAuthority) (execution.AgentAdapter, *dockersupervisor.Supervisor, error) {
	if c.Runner == nil {
		return nil, nil, errors.New("restricted source requires explicit Docker command authority")
	}
	c.Permissions = c.Permissions.Clone()
	c.Catalog.Models = slices.Clone(c.Catalog.Models)
	if agentpi.ValidateBudgetedStructure(c.Identity, c.Permissions) != nil || c.Catalog.Validate() != nil || c.Catalog.Digest != c.Identity.Catalog || c.Catalog.AgentID != "pi-budgeted" || c.Catalog.RuntimeIdentity != "sha256:"+c.Identity.Image || c.Catalog.RuntimeVersion != c.Identity.PiVersion || len(c.Catalog.Models) != 1 || c.Catalog.Models[0] != (domain.ModelIdentity{Provider: "deepseek", ModelID: "deepseek-v4-pro"}) {
		return nil, nil, errors.New("restricted installed identity, permissions or model catalog incomplete")
	}
	source, err := agentpi.NewBudgetedSource(c.Identity)
	if err != nil {
		return nil, nil, err
	}
	var original *agentpi.Adapter
	var wrapper *piWorkspaceAdapter
	if w, ok := base.(*piWorkspaceAdapter); ok {
		wrapper = w
		base = w.delegate
	}
	if base != nil {
		var ok bool
		original, ok = base.(*agentpi.Adapter)
		if !ok {
			return nil, nil, errors.New("restricted source requires existing Pi adapter")
		}
	}
	var adapter *agentpi.Adapter
	if original == nil {
		adapter, err = agentpi.New(agentpi.Config{BudgetedSource: source, BudgetedPermissions: c.Permissions.Clone()})
	} else {
		adapter, err = original.WithBudgetedSource(source, c.Permissions)
	}
	if err != nil {
		return nil, nil, err
	}
	adapter, err = adapter.WithModelCatalog(func(ctx context.Context, p domain.AgentExecutionProfile) (domain.ModelCatalog, error) {
		if p == domain.AgentExecutionProfileIsolatedBudgeted {
			return c.Catalog, nil
		}
		if original == nil {
			return domain.ModelCatalog{}, errors.New("original Pi source unavailable")
		}
		return original.DiscoverModelCatalog(ctx, p)
	})
	if err != nil {
		return nil, nil, err
	}
	// This is the real Go authority, not the old no-op fixture dispatch guard.
	guard, ok := authority.(interface {
		Prepare(context.Context, execution.StartRequest) error
	})
	if !ok {
		return nil, nil, errors.New("restricted app preparation authority missing")
	}
	adapter, err = adapter.WithWorkflowDispatchGuard(guard.Prepare)
	if err != nil {
		return nil, nil, err
	}
	workspace := budgetedWorkspace{reader: reader, source: source, permissions: c.Permissions.Clone()}
	args, _ := c.Permissions.Arguments()
	fp := source.Fingerprint()
	supervisor, err := dockersupervisor.New(dockersupervisor.Config{Runner: c.Runner, RuntimeRoot: filepath.Join(root, "pi-budgeted"), ArtifactRoot: filepath.Join(root, "artifacts-budgeted"), PolicyDigest: dockersupervisor.BudgetedPolicyDigest, AttemptImageID: "sha256:" + c.Identity.Image, RuntimeSourceIdentity: c.Identity.RuntimeDigest(), CapabilityContract: c.Capability, EngineQualification: c.Qualification,
		Budgeted:  &dockersupervisor.BudgetedConfig{Identity: c.Identity, Permissions: c.Permissions.Clone(), Authority: authority},
		Workbench: &dockersupervisor.WorkbenchConfig{Arguments: args, RuntimeVersion: c.Identity.PiVersion, ObserverSHA256: agentpi.ResourceObserverSHA256(), HelperSHA256: c.Identity.Helper, RuntimeFingerprint: hex.EncodeToString(fp.Digest[:]), PrepareWorkspace: workspace.prepare, CollectWorkspace: workspace.collect, RecoverWorkspace: isolatedworkspace.Recover, ReadOnlyWorkspacePaths: workspace.readonly}})
	if err != nil {
		return nil, nil, err
	}
	if wrapper != nil {
		copy := *wrapper
		copy.delegate = adapter
		return &copy, supervisor, nil
	}
	return adapter, supervisor, nil
}

// Startup composes the whole route with credential-free app authority. Recovery
// runs before route restoration and never starts Pi. The public profile remains
// unavailable; compiled proof admission must also pass before any Start effect.
func (s *Server) installBudgetedSource(ctx context.Context, c BudgetedCandidate) error {
	base, _ := s.registry.Get(agentpi.AdapterID)
	authority := app.NewRestrictedExecutionAuthority(s.service)
	adapter, supervisor, err := composeBudgetedSource(base, s.store.Reader(), s.runtimeRoot, c, authority)
	if err != nil {
		return err
	}
	if err = supervisor.Recover(ctx); err != nil {
		return err
	}
	s.supervisor.mu.Lock()
	s.supervisor.budgeted = supervisor
	s.supervisor.mu.Unlock()
	s.registry.Set(adapter)
	candidates, err := s.store.Reader().ListStartupCandidates(ctx)
	if err != nil {
		return err
	}
	for _, candidate := range candidates {
		if candidate.Attempt == nil || candidate.Attempt.AgentExecutionProfileBinding().Profile() != domain.AgentExecutionProfileIsolatedBudgeted {
			continue
		}
		session, err := s.store.Reader().GetRuntimeSessionForAttempt(ctx, candidate.Attempt.ID())
		if errors.Is(err, storecontract.ErrNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		if !session.FinalizedAt.IsZero() {
			continue
		}
		identity := execution.ProcessIdentity{Value: session.ProcessIdentity}
		launch := execution.LaunchToken{Value: session.LaunchToken}
		if err = supervisor.RegisterRecoveredDead(identity, launch); err != nil {
			return err
		}
		if err = s.supervisor.RestoreBudgeted(ctx, identity, launch); err != nil {
			return err
		}
	}
	return nil
}

type budgetedWorkspace struct {
	reader      storecontract.Reader
	source      agentpi.BudgetedSource
	permissions agentpi.BudgetedPermissionPlan
}

func (w budgetedWorkspace) resources(ctx context.Context, inv execution.Invocation) ([]domain.TaskRepositoryResource, error) {
	id, err := domain.ParseAttemptID(inv.Environment()["CHORA_ATTEMPT_ID"])
	if err != nil {
		return nil, err
	}
	a, err := w.reader.GetAttempt(ctx, id)
	if err != nil {
		return nil, err
	}
	if a.AgentExecutionProfileBinding().Profile() != domain.AgentExecutionProfileIsolatedBudgeted {
		return nil, errors.New("restricted workspace Attempt binding mismatch")
	}
	run, err := w.reader.GetRun(ctx, a.RunID())
	if err != nil {
		return nil, err
	}
	raw, err := w.reader.GetTaskResourceSnapshot(ctx, run.TaskID())
	if err != nil {
		return nil, err
	}
	var snapshot domain.TaskResourceSnapshot
	if json.Unmarshal(raw.CanonicalJSON, &snapshot) != nil {
		return nil, errors.New("restricted Task resource snapshot missing")
	}
	if _, _, err = snapshot.CanonicalJSON(); err != nil {
		return nil, err
	}
	actual := isolatedworkspace.ReadonlyRelativeMountPaths(snapshot.Resources)
	expected := slices.Clone(w.permissions.ReadOnlyPaths)
	sort.Strings(actual)
	sort.Strings(expected)
	if !slices.Equal(actual, expected) {
		return nil, errors.New("permission_contract_unsupported: original resource masks changed")
	}
	return snapshot.Resources, nil
}
func (w budgetedWorkspace) readonly(inv execution.Invocation) ([]string, error) {
	resources, err := w.resources(context.Background(), inv)
	if err != nil {
		return nil, err
	}
	paths := isolatedworkspace.ReadonlyRelativeMountPaths(resources)
	if paths == nil {
		paths = []string{}
	}
	return paths, nil
}
func (w budgetedWorkspace) prepare(ctx context.Context, inv execution.Invocation, dest string) error {
	resources, err := w.resources(ctx, inv)
	if err != nil {
		return err
	}
	contract, err := isolatedInvocationContract(inv)
	if err != nil {
		return err
	}
	if err = w.source.ValidateContract(contract); err != nil {
		return err
	}
	id, _ := domain.ParseAttemptID(inv.Environment()["CHORA_ATTEMPT_ID"])
	observer, err := agentpi.IsolatedObserverConfig(id, contract)
	if err != nil {
		return err
	}
	if domain.WorkflowDigest(observer) != inv.Environment()["CHORA_CHECK_OBSERVER_CONFIG_SHA256"] {
		return errors.New("restricted observer identity drift")
	}
	root := filepath.Dir(filepath.Dir(dest))
	if err = os.WriteFile(filepath.Join(root, "context", "observer.json"), observer, 0444); err != nil {
		return err
	}
	state := filepath.Join(root, "import-state")
	if err = os.Mkdir(state, 0700); err != nil {
		return err
	}
	if len(resources) == 0 {
		return isolatedworkspace.PrepareDocument(inv.WorkingRoot(), dest, state)
	}
	return isolatedworkspace.Prepare(ctx, inv.WorkingRoot(), dest, state, resources)
}
func (w budgetedWorkspace) collect(ctx context.Context, inv execution.Invocation, dest string) error {
	resources, err := w.resources(ctx, inv)
	if err != nil {
		return err
	}
	var state string
	// Use the same host-owned import journal prepared beside the runtime copy.
	// The caller's exported directory is <attempt>/export/repository.
	state = filepath.Join(filepath.Dir(filepath.Dir(dest)), "import-state")
	if len(resources) == 0 {
		return isolatedworkspace.CollectDocument(inv.WorkingRoot(), dest, state)
	}
	return isolatedworkspace.Collect(ctx, inv.WorkingRoot(), dest, state, resources)
}
