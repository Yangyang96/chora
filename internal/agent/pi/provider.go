package pi

import (
	"encoding/json"
	"strings"

	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
)

var _ execution.Provider = (*Adapter)(nil)

func (adapter *Adapter) Describe() execution.ProviderDescriptor {
	return execution.ProviderDescriptor{ID: adapter.ID(), Name: "Pi", ProtocolVersion: "pi.rpc.v1", Environments: []string{domain.DockerExecutionProvider, domain.TrustedHostExecutionProvider}, TaskKinds: []string{"code", "document", "research"}, Features: []string{"registered_contract", "native_resources", "agent_checks", "runtime.agent_loop", "runtime.jsonl_rpc", "runtime.file_edit", "runtime.command_execution", "runtime.progress_events", "runtime.terminal_result", "runtime.cooperative_abort", "runtime.allowlisted_event_projection", "runtime.chora_owned_result", "runtime.zero_implicit_context"}, ResumeMode: adapter.Capabilities().ResumeMode, ShutdownMode: "stdin_after_settled", ApprovalMode: "native_noninteractive", ModelMode: "catalog", CheckEvidenceMode: "content_bound"}
}
func (adapter *Adapter) Recover(request execution.RecoveryRequest) execution.RecoveryPolicy {
	local := request.Binding.Bound() && request.Binding.ExecutionProvider() == domain.TrustedHostExecutionProvider && request.Binding.RuntimeSource() == domain.LocalPiRuntimeSource
	return execution.RecoveryPolicy{FreshRetry: request.RunState == domain.RunStateRecoveryRequired && request.AttemptState != domain.AttemptStateInterrupted || request.Binding.ExecutionProvider() != domain.TrustedHostExecutionProvider, StopOnRestart: local && adapter.Capabilities().ResumeMode == execution.ResumeExplicitSession, MissingRouteUncertain: local}
}
func (adapter *Adapter) Complete(request execution.CompletionRequest) (execution.TerminalResult, error) {
	result, err := adapter.DecodeTerminal(request.Files)
	if err == nil && result.Kind != execution.TerminalFailed && request.Attempt.AgentExecutionProfileBinding().RuntimeSource() == domain.LocalPiRuntimeSource && !HasCompleteResult(request.Events) {
		return execution.TerminalResult{Kind: execution.TerminalFailed, FailureReason: "result_contract_invalid"}, nil
	}
	return result, err
}

// HasCompleteResult reads historical Pi events without rewriting their schema.
func HasCompleteResult(events []domain.RunEvent) bool {
	complete, started := false, false
	for _, event := range events {
		if event.Type() == "attempt.started" {
			complete = false
			started = true
		}
		if !started || event.Type() != "assistant_message" || event.Source() != "adapter" {
			continue
		}
		var payload struct {
			Text             string `json:"text"`
			TerminalComplete bool   `json:"terminal_complete"`
			Truncated        bool   `json:"truncated"`
		}
		complete = json.Unmarshal(event.NormalizedJSON(), &payload) == nil && payload.TerminalComplete && !payload.Truncated && strings.TrimSpace(payload.Text) != "" && len(payload.Text) <= 64*1024
	}
	return complete
}
