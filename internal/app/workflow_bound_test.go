package app

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/domain"
)

type syntheticBoundProof struct {
	evidence workflowBoundEvidence
	input    int
}

func (p syntheticBoundProof) Evidence() workflowBoundEvidence     { return p.evidence }
func (p syntheticBoundProof) InputUpperBound([]byte) (int, error) { return p.input, nil }
func TestTrustedBoundStrategyRequiresExactEvidenceAndExpiry(t *testing.T) {
	now := time.Now().UTC()
	var identity domain.RestrictedPiIdentity
	v := reflect.ValueOf(&identity).Elem()
	for n := 0; n < v.NumField(); n++ {
		v.Field(n).SetString(strings.Repeat("a", 64))
	}
	identity.Schema = "chora.restricted-pi.v2"
	identity.Source = domain.BudgetedPiRuntimeSource
	identity.PiVersion = "synthetic"
	identity.NodeVersion = "synthetic"
	identity.Endpoint = "deepseek.chat-completions.v1"
	identity.CredentialScope = "synthetic"
	c, _ := domain.NewModelCatalog("synthetic", "synthetic", "synthetic", []domain.ModelIdentity{{Provider: "deepseek", ModelID: "deepseek-v4-pro"}})
	m, _ := domain.NewModelBinding(c, c.Models[0], now)
	identity.Catalog = c.Digest
	profile, _ := domain.NewAgentExecutionProfileBinding(domain.AgentExecutionProfileIsolatedBudgeted)
	p := domain.WorkflowPiAuthorization{Version: 2, Reference: "synthetic", AuthorizedAt: now, Deadline: now.Add(time.Minute), MaxRequests: 1, MaxTokens: 100, MaxOutputTokens: 8, PerRequestOutputTokens: 8, Model: m, Profile: profile.Record(), TransportIdentity: identity.Transport, RestrictedIdentity: &identity}
	proof := syntheticBoundProof{workflowBoundEvidence{identity.BoundProof, identity.BillingProof, identity.Serializer, identity.Catalog, identity.Endpoint, p.Deadline}, 10}
	body := []byte(`{"model":"deepseek-v4-pro","messages":[{"role":"user","content":"fixture"}],"max_tokens":8,"thinking":{"type":"disabled"},"stream":false}`)
	if input, output, err := workflowProvenBound([]workflowBoundStrategy{proof}, &p, body); err != nil || input != 10 || output != 8 {
		t.Fatalf("synthetic strategy seam: %d %d %v", input, output, err)
	}
	for _, mode := range []string{"missing", "billing", "serializer", "catalog", "expiry", "over"} {
		bad := proof
		strategies := []workflowBoundStrategy{bad}
		switch mode {
		case "missing":
			strategies = nil
		case "billing":
			bad.evidence.BillingProof = "unknown"
		case "serializer":
			bad.evidence.Serializer = "other"
		case "catalog":
			bad.evidence.Catalog = "other"
		case "expiry":
			bad.evidence.Expires = now
		case "over":
			bad.input = 101
		}
		if mode != "missing" {
			strategies[0] = bad
		}
		if _, _, err := workflowProvenBound(strategies, &p, body); err == nil {
			t.Fatal("unproved strategy admitted", mode)
		}
	}
	if len(trustedWorkflowBoundStrategies()) != 0 {
		t.Fatal("fixture was installed as production proof")
	}
}
