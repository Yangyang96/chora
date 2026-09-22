package domain

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"time"
)

// DelegationProposalSource retains the Agent evidence selected by the human
// start command. It records provenance, never a human acceptance decision.
type DelegationProposalSource struct {
	ParentTaskID TaskID
	ProjectDocumentSource
	PlanDigest [32]byte
	CreatedAt  time.Time
}

func (s DelegationProposalSource) Validate() error {
	if !s.ParentTaskID.Valid() || !validProjectDocumentSource(s.ProjectDocumentSource) || s.PlanDigest == ([32]byte{}) || s.CreatedAt.IsZero() {
		return fmt.Errorf("%w: invalid delegation proposal source", ErrInvalidArgument)
	}
	return nil
}

func DelegationPlanDigest(assignments []DelegationAssignment) [32]byte {
	raw, _ := json.Marshal(DelegationProposal{SchemaVersion: DelegationProposalSchemaV1, Assignments: assignments})
	return sha256.Sum256(raw)
}
