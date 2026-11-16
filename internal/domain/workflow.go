package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
)

const ResearchWorkflowVersion = "research-to-pi.v1"

// WorkflowExecution is Go's business authority. Graph checkpoints remain in PG.
// Operation identities never include worker, epoch or delivery attempt.
type WorkflowExecution struct {
	ID              string                  `json:"id"`
	TaskID          TaskID                  `json:"taskId"`
	ProjectID       ProjectID               `json:"projectId"`
	ActorID         string                  `json:"actorId"`
	SessionID       string                  `json:"-"`
	Version         uint64                  `json:"version"`
	GraphVersion    string                  `json:"graphVersion"`
	InputDigest     string                  `json:"inputDigest"`
	AuthorityDigest string                  `json:"authorityDigest"`
	RevisionID      TechnicalPlanRevisionID `json:"revisionId"`
	Question        string                  `json:"question"`
	Configuration   json.RawMessage         `json:"configuration"`
	Status          string                  `json:"status"`
	Reason          string                  `json:"reason,omitempty"`
	StopRequested   bool                    `json:"stopRequested"`
	RunID           RunID                   `json:"runId,omitempty"`
	Gate            *WorkflowGate           `json:"gate,omitempty"`
	Projection      json.RawMessage         `json:"projection,omitempty"`
	CreatedAt       time.Time               `json:"createdAt"`
}

type WorkflowGate struct {
	ID           string          `json:"id"`
	InputDigest  string          `json:"inputDigest"`
	Version      uint64          `json:"version"`
	Report       json.RawMessage `json:"report"`
	Decision     string          `json:"decision,omitempty"`
	Instructions string          `json:"instructions,omitempty"`
}

type WorkflowCommand struct {
	OperationID string          `json:"operation_id"`
	WorkflowID  string          `json:"workflow_id"`
	Kind        string          `json:"kind"`
	InputDigest string          `json:"input_digest"`
	Payload     string          `json:"payload"`
	Receipt     json.RawMessage `json:"receipt,omitempty"`
}

type WorkflowEvent struct {
	Sequence uint64          `json:"sequence"`
	Kind     string          `json:"kind"`
	Data     json.RawMessage `json:"data"`
}

// Domain IDs deliberately do not expose JSON serialization globally.
type workflowJSON WorkflowExecution
type workflowWire struct {
	*workflowJSON
	Task     string `json:"taskId"`
	Project  string `json:"projectId"`
	Revision string `json:"revisionId"`
	Run      string `json:"runId,omitempty"`
}

func (w WorkflowExecution) MarshalJSON() ([]byte, error) {
	a := workflowJSON(w)
	return json.Marshal(workflowWire{&a, w.TaskID.String(), w.ProjectID.String(), w.RevisionID.String(), w.RunID.String()})
}
func (w *WorkflowExecution) UnmarshalJSON(b []byte) error {
	a := workflowJSON{}
	v := workflowWire{workflowJSON: &a}
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	var err error
	if a.TaskID, err = ParseTaskID(v.Task); err != nil {
		return err
	}
	if a.ProjectID, err = ParseProjectID(v.Project); err != nil {
		return err
	}
	if a.RevisionID, err = ParseTechnicalPlanRevisionID(v.Revision); err != nil {
		return err
	}
	if v.Run != "" {
		if a.RunID, err = ParseRunID(v.Run); err != nil {
			return err
		}
	}
	*w = WorkflowExecution(a)
	return nil
}

func WorkflowDigest(body []byte) string {
	d := sha256.Sum256(body)
	return hex.EncodeToString(d[:])
}

func (w WorkflowExecution) Active() bool {
	switch w.Status {
	case "stopped", "rejected", "failed", "awaiting_review":
		return false
	}
	return true
}
