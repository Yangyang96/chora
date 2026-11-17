package domain

import (
	"bytes"
	"embed"
	"encoding/hex"
	"fmt"
	"time"
)

//go:embed workflow_budget.go restricted_pi.go workflow_cost.go
var workflowBudgetPolicySources embed.FS

func WorkflowBudgetPolicyIdentity() string {
	var b bytes.Buffer
	for _, name := range []string{"workflow_budget.go", "restricted_pi.go", "workflow_cost.go"} {
		source, _ := workflowBudgetPolicySources.ReadFile(name)
		b.WriteString(name + "\x00")
		b.Write(source)
		b.WriteByte(0)
	}
	return WorkflowDigest(b.Bytes())
}

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
	RestrictedIdentity     *RestrictedPiIdentity              `json:"restrictedIdentity,omitempty"`
}

func (p WorkflowPiAuthorization) Validate() error {
	profile, err := RestoreAgentExecutionProfileBinding(p.Profile)
	_, hexErr := hex.DecodeString(p.TransportIdentity)
	if (p.Version != 1 && p.Version != 2) || len(p.Reference) == 0 || len(p.Reference) > 256 || p.AuthorizedAt.IsZero() || !p.Deadline.After(p.AuthorizedAt) || p.Deadline.Sub(p.AuthorizedAt) > 20*time.Minute || p.MaxRequests < 1 || p.MaxRequests > 20 || p.MaxTokens < 1 || p.MaxTokens > 100000 || p.MaxOutputTokens < 1 || p.MaxOutputTokens > p.MaxTokens || p.PerRequestOutputTokens < 1 || p.PerRequestOutputTokens > 2048 || p.PerRequestOutputTokens > p.MaxOutputTokens || !p.Model.Configured() || err != nil || !profile.Bound() || len(p.TransportIdentity) != 64 || hexErr != nil {
		return fmt.Errorf("%w: explicit bounded Pi authorization and runtime/model/transport identities required", ErrInvalidArgument)
	}
	if p.Version == 2 {
		if p.RestrictedIdentity == nil || p.RestrictedIdentity.Validate() != nil || p.Profile.Profile != AgentExecutionProfileIsolatedBudgeted || p.RestrictedIdentity.Transport != p.TransportIdentity || p.RestrictedIdentity.Catalog != p.Model.Record().Catalog.Digest {
			return fmt.Errorf("%w: incomplete restricted v2 identity", ErrInvalidArgument)
		}
	} else if p.RestrictedIdentity != nil || p.Profile.Profile == AgentExecutionProfileIsolatedBudgeted {
		return fmt.Errorf("%w: restricted source requires v2 authority", ErrInvalidArgument)
	}
	return nil
}

func (p WorkflowPiAuthorization) Active(now time.Time) bool {
	return p.Validate() == nil && !now.Before(p.AuthorizedAt) && now.Before(p.Deadline)
}
