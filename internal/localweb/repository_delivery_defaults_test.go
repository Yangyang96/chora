package localweb

import (
	"net/http"
	"testing"
)

func TestRepositoryDeliveryDefaultSuggestionDoesNotGuessWithoutRemoteHEAD(t *testing.T) {
	server, _, selection, _, closeStore := newTaskResourcePathValidationFixture(t)
	defer closeStore()
	var response struct {
		TargetRef, SuggestedTargetRef, Reason string
		Version                               uint64
	}
	requestJSON(t, server.Handler(), http.MethodGet, "/api/v2/repositories/"+selection.RepoID+"/delivery-defaults", nil, http.StatusOK, &response)
	if response.TargetRef != "" || response.SuggestedTargetRef != "" || response.Version != 0 || response.Reason == "" {
		t.Fatalf("unexpected unconfigured default: %+v", response)
	}
}
