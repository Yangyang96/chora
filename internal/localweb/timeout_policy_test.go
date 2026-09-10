package localweb

import (
	"testing"
)

func TestAcceptanceTimeoutPolicyKeepsProductionLimitWithoutAuthority(t *testing.T) {
	policy := acceptanceTimeoutPolicyFor(nil)
	if policy.Controller != nil || policy.AttemptTimeout != productionAttemptTimeout {
		t.Fatalf("policy = %#v", policy)
	}
}
