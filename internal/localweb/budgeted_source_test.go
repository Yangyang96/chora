package localweb

import (
	"context"
	"encoding/json"
	agentpi "github.com/Yangyang96/chora/internal/agent/pi"
	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/dockersupervisor"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
	"reflect"
	"strings"
	"testing"
)

type noBudgetedCommands struct{ t *testing.T }

func (r noBudgetedCommands) Run(context.Context, dockersupervisor.Command) (dockersupervisor.CommandResult, error) {
	r.t.Fatal("unexpected Docker operation")
	return dockersupervisor.CommandResult{}, nil
}
func (r noBudgetedCommands) Start(context.Context, dockersupervisor.Command) (dockersupervisor.Process, error) {
	r.t.Fatal("unexpected Pi operation")
	return nil, nil
}
func TestBudgetedSourceCompositionRetainsOldSourceAndClosedAdmission(t *testing.T) {
	var i domain.RestrictedPiIdentity
	v := reflect.ValueOf(&i).Elem()
	for n := 0; n < v.NumField(); n++ {
		v.Field(n).SetString(strings.Repeat("a", 64))
	}
	i.Schema = "chora.restricted-pi.v2"
	i.Source = domain.BudgetedPiRuntimeSource
	i.PiVersion = "0.85.1"
	i.NodeVersion = "synthetic"
	i.Endpoint = "deepseek.chat-completions.v1"
	i.CredentialScope = "synthetic-only"
	i.Policy = dockersupervisor.BudgetedPolicyDigest
	catalog, err := domain.NewModelCatalog("pi-budgeted", "sha256:"+i.Image, i.PiVersion, []domain.ModelIdentity{{Provider: "deepseek", ModelID: "deepseek-v4-pro"}})
	if err != nil {
		t.Fatal(err)
	}
	i.Catalog = catalog.Digest
	p := agentpi.BudgetedPermissionPlan{Tools: []string{"read", "bash", "edit", "write"}, OriginalRequired: []agentpi.BudgetedExtension{}, Installed: []agentpi.BudgetedExtension{}, ReadOnlyPaths: []string{}}
	d := agentpi.BudgetedAssetDigests()
	i.Shim = d["extension"]
	i.Serializer = d["provider"]
	i.Translator = d["provider"]
	i.Relay = d["relay"]
	i.Entry = agentpi.BudgetedEntryDigest()
	i.PermissionExtensions = p.Digest()
	raw, _ := json.Marshal(p.Tools)
	i.AllowedTools = domain.WorkflowDigest(raw)
	args, _ := p.Arguments()
	raw, _ = json.Marshal(args)
	i.Arguments = domain.WorkflowDigest(raw)
	oldSource, err := agentpi.NewIsolatedSource(agentpi.IsolatedSourceParams{ImageID: "sha256:" + strings.Repeat("b", 64), HelperSHA256: strings.Repeat("c", 64), PolicySHA256: strings.Repeat("d", 64)})
	if err != nil {
		t.Fatal(err)
	}
	old, err := agentpi.New(agentpi.Config{IsolatedSource: oldSource})
	if err != nil {
		t.Fatal(err)
	}
	contract, err := dockersupervisor.NewCapabilityProbeContract("sha256:"+i.Image, i.Policy)
	if err != nil {
		t.Fatal(err)
	}
	c := BudgetedCandidate{Identity: i, Permissions: p, Catalog: catalog, Runner: noBudgetedCommands{t}, Capability: contract, Qualification: testEngineQualification(t, sourceCheckoutTestEngineIdentity(t, "synthetic-restricted"), contract)}
	authority := app.NewRestrictedExecutionAuthority(&app.Service{})
	wrapped := &piWorkspaceAdapter{delegate: old}
	combined, supervisor, err := composeBudgetedSource(wrapped, nil, t.TempDir(), c, authority)
	if err != nil {
		t.Fatal(err)
	}
	if supervisor == nil {
		t.Fatal("missing dedicated supervisor")
	}
	got, ok := combined.(*piWorkspaceAdapter)
	if !ok {
		t.Fatal("workspace wrapper lost")
	}
	adapter := got.delegate.(*agentpi.Adapter)
	binding, _ := domain.NewAgentExecutionProfileBinding(domain.AgentExecutionProfileIsolatedLocal)
	before, err := old.FingerprintForBinding(context.Background(), binding)
	if err != nil {
		t.Fatal(err)
	}
	after, err := adapter.FingerprintForBinding(context.Background(), binding)
	if err != nil || before != after {
		t.Fatal("old isolated source changed", err)
	}
	selected, _ := domain.NewAgentExecutionProfileBinding(domain.AgentExecutionProfileIsolatedBudgeted)
	if _, err = adapter.FingerprintForBinding(context.Background(), selected); err != nil {
		t.Fatal(err)
	}
	if err = authority.Prepare(context.Background(), execution.StartRequest{}); err != app.ErrBoundUnproven {
		t.Fatal(err)
	}
	c.Permissions.Installed = nil
	if _, _, err = composeBudgetedSource(wrapped, nil, t.TempDir(), c, authority); err == nil {
		t.Fatal("missing required inventory silently accepted")
	}
}
