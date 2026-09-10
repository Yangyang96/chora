package localweb

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Yangyang96/chora/internal/acceptanceauthority"
)

func TestO4ResidueEndpointIsDefaultDisabledAndFailureIsSafe(t *testing.T) {
	request := newLocalRequest(http.MethodGet, "/api/o4/residue", nil)
	disabled := httptest.NewRecorder()
	(&Server{}).Handler().ServeHTTP(disabled, request)
	if disabled.Code != http.StatusNotFound {
		t.Fatalf("default-disabled status = %d", disabled.Code)
	}

	enabled := httptest.NewRecorder()
	(&Server{o4AcceptanceAuthority: &acceptanceauthority.Controller{}}).Handler().ServeHTTP(enabled, request)
	if enabled.Code != http.StatusConflict || enabled.Body.String() != "{\"error\":\"o4_residue_proof_unavailable\"}\n" || strings.Contains(enabled.Body.String(), "Docker") {
		t.Fatalf("safe failure = status %d body %q", enabled.Code, enabled.Body.String())
	}
}

func TestSafeStringAggregateReturnsOnlyCountAndDigest(t *testing.T) {
	proof := safeStringAggregate([]string{"private-generation-id"})
	if proof.Count != 1 || len(proof.AggregateSHA256) != 64 || strings.Contains(proof.AggregateSHA256, "private-generation-id") {
		t.Fatalf("unsafe reference proof = %#v", proof)
	}
}
