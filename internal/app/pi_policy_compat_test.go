package app

import (
	"github.com/Yangyang96/chora/internal/agent/pi"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
	"github.com/Yangyang96/chora/internal/speccoding"
)

type agentClaimEvent struct {
	typ        string
	normalized []byte
}

func derivePiAgentReportedChecks(document speccoding.CoreContractDocument, events []agentClaimEvent) []execution.DeclaredCheck {
	normalized := make([]execution.NormalizedEvent, 0, len(events))
	for _, e := range events {
		normalized = append(normalized, execution.NormalizedEvent{Type: e.typ, NormalizedJSON: e.normalized})
	}
	return pi.DeriveAgentReportedChecks(document, normalized)
}
func hasCompletePiResult(events []domain.RunEvent) bool { return pi.HasCompleteResult(events) }
