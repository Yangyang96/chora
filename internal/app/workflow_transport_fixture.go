//go:build chora_offline_fixture

package app

import "net/http"

// NewWorkflowChatTransportForFixture exists only in an explicit offline build.
// It deliberately uses an UNPROVEN bound to exercise ledger/HTTP faults with
// synthetic peers. It cannot qualify the production renderer or billing cap.
func NewWorkflowChatTransportForFixture(s *Service, dummy string, peer http.RoundTripper) (*WorkflowChatTransport, error) {
	t, err := NewWorkflowChatTransport(s, dummy)
	if err != nil {
		return nil, err
	}
	t.roundTrip = peer
	t.inputBound = workflowChatEnvelope
	return t, nil
}
