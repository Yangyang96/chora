package app

import (
	"time"

	"github.com/Yangyang96/chora/internal/domain"
)

// Implementations are compiled, reviewed strategies, never JSON/plugin supplied
// functions. Their evidence binds the WHOLE renderer and billing contract.
// There is deliberately no public registration or environment discovery port.
type workflowBoundEvidence struct {
	InputProof, BillingProof, Serializer, Catalog string
	Endpoint                                      string
	Expires                                       time.Time
}
type workflowBoundStrategy interface {
	Evidence() workflowBoundEvidence
	InputUpperBound(finalCanonicalBody []byte) (int, error)
}

func trustedWorkflowBoundStrategies() []workflowBoundStrategy {
	// No supported strategy has complete provider renderer/billing evidence yet.
	// Adding one requires a code change, proof artifact and independent review.
	return nil
}
func workflowProvenBound(strategies []workflowBoundStrategy, p *domain.WorkflowPiAuthorization, body []byte) (int, int, error) {
	if p == nil || p.Validate() != nil || p.Version != 2 || p.RestrictedIdentity == nil {
		return 0, 0, ErrBoundUnproven
	}
	i := p.RestrictedIdentity
	for _, strategy := range strategies {
		e := strategy.Evidence()
		if e.InputProof != i.BoundProof || e.BillingProof != i.BillingProof || e.Serializer != i.Serializer || e.Catalog != i.Catalog || e.Endpoint != i.Endpoint || e.Expires.Before(p.Deadline) || !time.Now().Before(e.Expires) {
			continue
		}
		// Shape/output policy is independent of a proved input strategy. Its byte
		// estimate is ignored: it cannot become a proof by selecting this path.
		_, output, err := workflowChatEnvelope(body)
		if err != nil {
			return 0, 0, err
		}
		input, err := strategy.InputUpperBound(body)
		if err != nil || input < 0 || input > p.MaxTokens || output > p.PerRequestOutputTokens {
			return 0, 0, ErrBoundUnproven
		}
		return input, output, nil
	}
	return 0, 0, ErrBoundUnproven
}
