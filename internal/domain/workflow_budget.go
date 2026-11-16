package domain

import (
	_ "embed"
	"encoding/hex"
	"fmt"
	"time"
)

//go:embed workflow_budget.go
var workflowBudgetPolicySource []byte

func WorkflowBudgetPolicyIdentity() string { return WorkflowDigest(workflowBudgetPolicySource) }

// WorkflowPiAuthorization is explicit non-secret authority, not a default model
// or permission to spend. The clock starts when authorized, before research.
// It is frozen with the workflow; successor Attempts share these ceilings.
type WorkflowPiAuthorization struct {
	Version                int                                `json:"version"`
	Reference              string                             `json:"reference"`
	AuthorizedAt           time.Time                          `json:"authorizedAt"`
	Deadline               time.Time                          `json:"deadline"`
	MaxRequests            int                                `json:"maxRequests"`
	MaxTokens              int                                `json:"maxTokens"`
	MaxOutputTokens        int                                `json:"maxOutputTokens"`
	PerRequestOutputTokens int                                `json:"perRequestOutputTokens"`
	Model                  ModelBinding                       `json:"modelBinding"`
	Profile                AgentExecutionProfileBindingRecord `json:"profileBinding"`
	TransportIdentity      string                             `json:"transportIdentity"`
}

func (p WorkflowPiAuthorization) Validate() error {
	profile, err := RestoreAgentExecutionProfileBinding(p.Profile)
	_, hexErr := hex.DecodeString(p.TransportIdentity)
	if p.Version != 1 || len(p.Reference) == 0 || len(p.Reference) > 256 || p.AuthorizedAt.IsZero() || !p.Deadline.After(p.AuthorizedAt) || p.Deadline.Sub(p.AuthorizedAt) > 20*time.Minute || p.MaxRequests < 1 || p.MaxRequests > 20 || p.MaxTokens < 1 || p.MaxTokens > 100000 || p.MaxOutputTokens < 1 || p.MaxOutputTokens > p.MaxTokens || p.PerRequestOutputTokens < 1 || p.PerRequestOutputTokens > 2048 || p.PerRequestOutputTokens > p.MaxOutputTokens || !p.Model.Configured() || err != nil || !profile.Bound() || len(p.TransportIdentity) != 64 || hexErr != nil {
		return fmt.Errorf("%w: explicit bounded Pi authorization and runtime/model/transport identities required", ErrInvalidArgument)
	}
	return nil
}

func (p WorkflowPiAuthorization) Active(now time.Time) bool {
	return p.Validate() == nil && !now.Before(p.AuthorizedAt) && now.Before(p.Deadline)
}
