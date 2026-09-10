//go:build !chora_e2e

package localweb

import (
	"net/http"

	agentfake "github.com/Yangyang96/chora/internal/agent/fake"
	"github.com/Yangyang96/chora/internal/domain"
)

type startRunInput struct {
	RevisionID string `json:"revisionId"`
}

func decodeStartRunInput(request *http.Request) (startRunInput, error) {
	var input startRunInput
	err := decodeOptionalJSON(request, &input)
	return input, err
}

func (server *Server) e2eStartRunAdapter(startRunInput, []domain.AcceptanceCriterion, domain.RunID, string, bool) (*agentfake.Adapter, error) {
	return nil, nil
}

func (server *Server) e2eVerifiedRetryAdapter([]domain.AcceptanceCriterion, domain.RunID) (*agentfake.Adapter, error) {
	return nil, nil
}
