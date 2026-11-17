package pi

import (
	"context"
	"encoding/json"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
	"reflect"
	"strings"
	"testing"
)

func budgetedSourceFixture(t *testing.T) (domain.RestrictedPiIdentity, BudgetedPermissionPlan) {
	t.Helper()
	var i domain.RestrictedPiIdentity
	v := reflect.ValueOf(&i).Elem()
	for n := 0; n < v.NumField(); n++ {
		v.Field(n).SetString(strings.Repeat("a", 64))
	}
	i.Schema = "chora.restricted-pi.v2"
	i.Source = domain.BudgetedPiRuntimeSource
	i.PiVersion = IsolatedPiVersion
	i.NodeVersion = "synthetic"
	i.Endpoint = "deepseek.chat-completions.v1"
	i.CredentialScope = "synthetic-only"
	e := BudgetedExtension{Path: "/opt/chora/permissions/required.mjs", SHA256: strings.Repeat("b", 64), Closure: strings.Repeat("c", 64), Semantics: strings.Repeat("d", 64)}
	p := BudgetedPermissionPlan{Tools: []string{"read", "bash", "edit", "write"}, OriginalRequired: []BudgetedExtension{e}, Installed: []BudgetedExtension{e}, ReadOnlyPaths: []string{"repo/.git"}}
	d := BudgetedAssetDigests()
	i.Shim = d["extension"]
	i.Serializer = d["provider"]
	i.Translator = d["provider"]
	i.Relay = d["relay"]
	i.Entry = BudgetedEntryDigest()
	i.PermissionExtensions = p.Digest()
	b, _ := json.Marshal(p.Tools)
	i.AllowedTools = domain.WorkflowDigest(b)
	args, _ := p.Arguments()
	b, _ = json.Marshal(args)
	i.Arguments = domain.WorkflowDigest(b)
	return i, p
}
func TestBudgetedSourcePreparesFrozenContractAndPreservesRequiredTools(t *testing.T) {
	i, p := budgetedSourceFixture(t)
	run, attempt, snapshot := testRunAttemptForProfile(t, domain.AgentExecutionProfileIsolatedBudgeted)
	contract := []byte(`{"schema_version":"chora.spec-coding-core.v12","task":{"id":"` + run.TaskID().String() + `","resources":[]},"candidate":{"sandbox":{"sha256":"` + i.Image + `"},"runtime":{"config":{"sha256":"` + i.RuntimeDigest() + `"}}}}`)
	i.Contract = domain.WorkflowDigest(contract)
	s, err := NewBudgetedSource(i)
	if err != nil {
		t.Fatal(err)
	}
	a, err := New(Config{BudgetedSource: s, BudgetedPermissions: p})
	if err != nil {
		t.Fatal(err)
	}
	req := execution.StartRequest{Run: run, Attempt: attempt, SnapshotDocument: snapshot, ExecutionContractDocument: contract, WorkspaceRoot: t.TempDir(), LaunchToken: execution.LaunchToken{Value: "budgeted-source"}}
	prepared, err := a.PrepareBoundStart(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	inv := prepared.Invocation
	args, _ := p.Arguments()
	if !reflect.DeepEqual(inv.Arguments(), args) || inv.Executable() != "chora-pi-budgeted" || inv.Target().ProviderID() != domain.DockerExecutionProvider || inv.Environment()["CHORA_RUNTIME_SOURCE_IDENTITY"] != i.RuntimeDigest() {
		t.Fatal("source, tools or required extension drift", inv.Environment())
	}
	req.ExecutionContractDocument = append(contract, ' ')
	if _, err = a.PrepareBoundStart(context.Background(), req); err == nil {
		t.Fatal("contract drift admitted")
	}
	changed := p.Clone()
	changed.Installed = []BudgetedExtension{}
	if _, err = a.WithBudgetedSource(s, changed); err == nil {
		t.Fatal("required extension silently removed")
	}
	for _, mutate := range []func(*BudgetedPermissionPlan){func(p *BudgetedPermissionPlan) { p.Tools = append(p.Tools, "network") }, func(p *BudgetedPermissionPlan) { p.ReadOnlyPaths = nil }, func(p *BudgetedPermissionPlan) {
		p.OriginalRequired[0].Path = "/opt/chora/permissions/sub/a.mjs"
		p.Installed[0].Path = p.OriginalRequired[0].Path
	}} {
		q := p.Clone()
		mutate(&q)
		if q.Validate() == nil {
			t.Fatal("unsupported permission contract admitted", q)
		}
	}
}

func TestBudgetedDecoderRequiresFrozenModelWithoutRestrictingOldIsolated(t *testing.T) {
	i, p := budgetedSourceFixture(t)
	s, _ := NewBudgetedSource(i)
	a, err := New(Config{BudgetedSource: s, BudgetedPermissions: p})
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{`{"type":"message_end","message":{"role":"assistant","provider":"deepseek","model":"deepseek-v4-pro","stopReason":"stop","content":[]}}`, `{"type":"message_end","message":{"role":"assistant","provider":"other","model":"deepseek-v4-pro","stopReason":"stop","content":[]}}`, `{"type":"model_identity","model_id":"foreign"}`} {
		_, err := a.DecodeEvent(execution.EventChunk{Profile: domain.AgentExecutionProfileIsolatedBudgeted, Stream: execution.StreamStdout, Data: []byte(line + "\n"), EOF: true})
		want := strings.Contains(line, `"other"`) || strings.Contains(line, `"foreign"`)
		if (err != nil) != want {
			t.Fatalf("model decoding err=%v: %s", err, line)
		}
	}
}
