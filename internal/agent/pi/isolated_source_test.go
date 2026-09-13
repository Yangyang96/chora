package pi

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
)

func TestPublicIsolatedSourceCannotSelectHostOrLoseImageBinding(t *testing.T) {
	source, err := NewIsolatedSource(IsolatedSourceParams{ImageID: "sha256:" + strings.Repeat("a", 64), HelperSHA256: strings.Repeat("b", 64), PolicySHA256: strings.Repeat("c", 64)})
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := New(Config{IsolatedSource: source})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := domain.NewAgentExecutionProfileBinding(domain.AgentExecutionProfileTrustedLocal)
	if _, err := adapter.FingerprintForBinding(context.Background(), b); err == nil {
		t.Fatal("isolated-only adapter selected host")
	}
	run, attempt, snapshot := testRunAttemptForProfile(t, domain.AgentExecutionProfileIsolatedLocal)
	contract := []byte(`{"schema_version":"chora.spec-coding-core.v12","task":{"id":"` + run.TaskID().String() + `","resources":[]},"candidate":{"sandbox":{"sha256":"` + strings.Repeat("a", 64) + `"},"runtime":{"config":{"sha256":"` + fmt.Sprintf("%x", source.SourceIdentity()) + `"}}}}`)
	inv, err := adapter.PrepareStart(context.Background(), execution.StartRequest{Run: run, Attempt: attempt, SnapshotDocument: snapshot, ExecutionContractDocument: contract, WorkspaceRoot: t.TempDir(), LaunchToken: execution.LaunchToken{Value: "isolated-launch"}})
	if err != nil {
		t.Fatal(err)
	}
	if inv.Target().ProviderID() != domain.DockerExecutionProvider || inv.Environment()["CHORA_ATTEMPT_IMAGE_ID"] != source.ImageID() {
		t.Fatal("source or Docker target lost")
	}
	if strings.Contains(strings.Join(inv.Arguments(), " "), "session-dir") {
		t.Fatal("host session path entered isolated argv")
	}
}

func TestIsolatedDecoderRejectsModelDriftWithoutRestrictingConnected(t *testing.T) {
	source, _ := NewIsolatedSource(IsolatedSourceParams{ImageID: "sha256:" + strings.Repeat("a", 64), HelperSHA256: strings.Repeat("b", 64), PolicySHA256: strings.Repeat("c", 64)})
	adapter, err := New(Config{IsolatedSource: source})
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{
		`{"type":"message_end","message":{"role":"assistant","provider":"other","model":"deepseek-v4-pro","stopReason":"stop","content":[{"type":"text","text":"done"}]}}`,
		`{"type":"message_end","message":{"role":"assistant","provider":"deepseek","model":"other","stopReason":"stop","content":[{"type":"text","text":"done"}]}}`,
		`{"type":"message_end","message":{"role":"assistant","stopReason":"stop","content":[{"type":"text","text":"done"}]}}`,
		`{"type":"agent_start","provider":"other"}`,
		`{"type":"model_identity","model_id":"other"}`,
		`{"type":"response","command":"get_state","success":true,"data":{"model":{"id":"other","provider":"deepseek"}}}`,
	} {
		_, err := adapter.DecodeEvent(execution.EventChunk{Profile: domain.AgentExecutionProfileIsolatedLocal, Stream: execution.StreamStdout, Data: []byte(line + "\n"), EOF: true})
		if err == nil {
			t.Fatalf("accepted isolated model drift: %s", line)
		}
	}
	line := `{"type":"message_end","message":{"role":"assistant","provider":"deepseek","model":"deepseek-v4-pro","stopReason":"stop","content":[{"type":"text","text":"done"}]}}` + "\n"
	decoded, err := adapter.DecodeEvent(execution.EventChunk{Profile: domain.AgentExecutionProfileIsolatedLocal, Stream: execution.StreamStdout, Data: []byte(line), EOF: true})
	if err != nil || len(decoded.Events) != 1 {
		t.Fatalf("valid model rejected: %#v %v", decoded, err)
	}
	line = strings.ReplaceAll(line, "deepseek-v4-pro", "connected-model")
	if _, err := adapter.DecodeEvent(execution.EventChunk{Profile: domain.AgentExecutionProfileTrustedLocal, Stream: execution.StreamStdout, Data: []byte(line), EOF: true}); err != nil {
		t.Fatalf("connected model restricted: %v", err)
	}
}

func TestIsolatedProxyChangeRejectsFrozenTaskWithoutChangingImage(t *testing.T) {
	p := IsolatedSourceParams{ImageID: "sha256:" + strings.Repeat("a", 64), HelperSHA256: strings.Repeat("b", 64), PolicySHA256: strings.Repeat("c", 64)}
	direct, _ := NewIsolatedSource(p)
	contract := []byte(`{"candidate":{"sandbox":{"sha256":"` + strings.Repeat("a", 64) + `"},"runtime":{"config":{"sha256":"` + fmt.Sprintf("%x", direct.SourceIdentity()) + `"}}}}`)
	p.ProxySHA256 = strings.Repeat("d", 64)
	proxied, err := NewIsolatedSource(p)
	if err != nil {
		t.Fatal(err)
	}
	if direct.ImageID() != proxied.ImageID() || direct.Fingerprint() == proxied.Fingerprint() {
		t.Fatal("proxy not bound separately from image")
	}
	if direct.ValidateContract(contract) != nil || proxied.ValidateContract(contract) == nil {
		t.Fatal("old Task accepted a new proxy")
	}
	newContract := []byte(strings.ReplaceAll(string(contract), fmt.Sprintf("%x", direct.SourceIdentity()), fmt.Sprintf("%x", proxied.SourceIdentity())))
	restarted, _ := NewIsolatedSource(p)
	if restarted != proxied || restarted.ValidateContract(newContract) != nil {
		t.Fatal("same-config restart lost Task binding")
	}
	p.ProxySHA256 = strings.Repeat("e", 64)
	changed, _ := NewIsolatedSource(p)
	if changed.ValidateContract(newContract) == nil {
		t.Fatal("changed proxy accepted old Task")
	}
}
