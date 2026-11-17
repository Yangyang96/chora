package domain

import (
	"math"
	"reflect"
	"strings"
	"testing"
	"time"
)

func restrictedIdentityFixture() RestrictedPiIdentity {
	var i RestrictedPiIdentity
	v := reflect.ValueOf(&i).Elem()
	for n := 0; n < v.NumField(); n++ {
		v.Field(n).SetString(strings.Repeat("a", 64))
	}
	i.Schema = "chora.restricted-pi.v2"
	i.Source = BudgetedPiRuntimeSource
	i.PiVersion = "synthetic-pi"
	i.NodeVersion = "synthetic-node"
	i.Endpoint = "deepseek.chat-completions.v1"
	i.CredentialScope = "synthetic-only"
	return i
}
func TestRestrictedIdentityRejectsEveryMissingFieldAndKeepsOldProfile(t *testing.T) {
	i := restrictedIdentityFixture()
	if err := i.Validate(); err != nil {
		t.Fatal(err)
	}
	v := reflect.ValueOf(&i).Elem()
	for n := 0; n < v.NumField(); n++ {
		old := v.Field(n).String()
		v.Field(n).SetString("")
		if i.Validate() == nil {
			t.Fatalf("missing field %s admitted", v.Type().Field(n).Name)
		}
		v.Field(n).SetString(old)
	}
	old, _ := NewAgentExecutionProfileBinding(AgentExecutionProfileIsolatedLocal)
	r := old.Record()
	r.Profile = AgentExecutionProfileIsolatedBudgeted
	if _, err := RestoreAgentExecutionProfileBinding(r); err == nil {
		t.Fatal("old binding silently upgraded")
	}
}
func TestRestrictedAuthorizationAndCostUnknownExpiryRoundingOverflow(t *testing.T) {
	now := time.Now().UTC()
	catalog, _ := NewModelCatalog("synthetic-budgeted", "synthetic-image", "synthetic-pi", []ModelIdentity{{"deepseek", "deepseek-v4-pro"}})
	m, err := NewModelBinding(catalog, catalog.Models[0], now)
	if err != nil {
		t.Fatal(err)
	}
	profile, _ := NewAgentExecutionProfileBinding(AgentExecutionProfileIsolatedBudgeted)
	i := restrictedIdentityFixture()
	i.Catalog = catalog.Digest
	p := WorkflowPiAuthorization{Version: 2, Reference: "synthetic", AuthorizedAt: now, Deadline: now.Add(time.Minute), MaxRequests: 2, MaxTokens: 3, MaxOutputTokens: 2, PerRequestOutputTokens: 1, Model: m, Profile: profile.Record(), TransportIdentity: i.Transport, RestrictedIdentity: &i}
	if err = p.Validate(); err != nil {
		t.Fatal(err)
	}
	p.Version = 1
	if p.Validate() == nil {
		t.Fatal("partial/legacy source upgraded")
	}
	p.Version = 2
	s := WorkflowPriceSchedule{Reference: "not-price-evidence", EvidenceSHA256: strings.Repeat("b", 64), Currency: "synthetic-minor-unit", ValidUntil: p.Deadline, InputPerMillion: 1, OutputPerMillion: 1, FixedPerRequest: 2, AllFeesCovered: true}
	cost, err := WorkflowCostCeiling(p, s, now)
	if err != nil || cost != 5 {
		t.Fatalf("rounded=%d %v", cost, err)
	}
	s.AllFeesCovered = false
	if _, err = WorkflowCostCeiling(p, s, now); err == nil {
		t.Fatal("unknown fees treated as zero")
	}
	s.AllFeesCovered = true
	s.ValidUntil = now
	if _, err = WorkflowCostCeiling(p, s, now); err == nil {
		t.Fatal("expired price admitted")
	}
	s.ValidUntil = p.Deadline
	s.FixedPerRequest = math.MaxUint64
	if _, err = WorkflowCostCeiling(p, s, now); err == nil {
		t.Fatal("price overflow wrapped")
	}
}

func TestRestrictedIdentityRuntimeScopeDoesNotCreateContractHashCycle(t *testing.T) {
	i := restrictedIdentityFixture()
	full, runtime := i.Digest(), i.RuntimeDigest()
	i.Snapshot = strings.Repeat("b", 64)
	i.Contract = strings.Repeat("c", 64)
	if i.Digest() == full || i.RuntimeDigest() != runtime {
		t.Fatal("Task scope not independently frozen")
	}
	i.Helper = strings.Repeat("d", 64)
	if i.RuntimeDigest() == runtime {
		t.Fatal("runtime closure drift ignored")
	}
}
