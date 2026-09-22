package domain

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"time"
)

type DelegationSynthesisInput struct {
	TaskID TaskID `json:"taskId"`
	ProjectDocumentSource
	Markdown string `json:"markdown"`
}

// DelegationSynthesis is a one-shot authorization. Its lineage and source set
// can be bound once; later source changes never grant a replacement execution.
type DelegationSynthesis struct {
	ParentTaskID       TaskID
	TaskID             TaskID
	RunID              RunID
	AttemptID          AttemptID
	Inputs             []DelegationSynthesisInput
	ActorID, SessionID string
	CreatedAt          time.Time
}

func (s DelegationSynthesis) Validate() error {
	if !s.ParentTaskID.Valid() || s.CreatedAt.IsZero() || !ValidTrustedContextText(s.ActorID, MaxTrustedContextIdentityRunes, true) || !ValidTrustedContextText(s.SessionID, MaxTrustedContextIdentityRunes, true) {
		return ErrInvalidArgument
	}
	if !s.TaskID.Valid() {
		if s.RunID.Valid() || s.AttemptID.Valid() || len(s.Inputs) != 0 {
			return ErrInvalidArgument
		}
		return nil
	}
	if s.TaskID == s.ParentTaskID || len(s.Inputs) < 1 || len(s.Inputs) > MaxDelegationAssignments || s.AttemptID.Valid() && !s.RunID.Valid() {
		return ErrInvalidArgument
	}
	seen := map[TaskID]bool{}
	for _, i := range s.Inputs {
		if !i.TaskID.Valid() || seen[i.TaskID] || !validProjectDocumentSource(i.ProjectDocumentSource) || sha256.Sum256([]byte(i.Markdown)) != i.SourceTextDigest || len(i.Markdown) > 64<<10 || len(i.Markdown) == 0 {
			return fmt.Errorf("%w: invalid synthesis source", ErrInvalidArgument)
		}
		seen[i.TaskID] = true
	}
	return nil
}

// IDs are opaque domain values, so persistence uses an explicit wire form
// instead of encoding their private representation as empty JSON objects.
type synthesisInputWire struct {
	TaskID           string   `json:"taskId"`
	RunID            string   `json:"runId"`
	AttemptID        string   `json:"attemptId"`
	ResultID         string   `json:"resultId"`
	AgentReportID    string   `json:"agentReportId"`
	EventID          string   `json:"eventId"`
	EventSequence    int64    `json:"eventSequence"`
	ResultDigest     [32]byte `json:"resultDigest"`
	SourceTextDigest [32]byte `json:"sourceTextDigest"`
	Markdown         string   `json:"markdown"`
}

func (i DelegationSynthesisInput) MarshalJSON() ([]byte, error) {
	return json.Marshal(synthesisInputWire{i.TaskID.String(), i.RunID.String(), i.AttemptID.String(), i.ResultID.String(), i.AgentReportID.String(), i.EventID.String(), i.EventSequence, i.ResultDigest, i.SourceTextDigest, i.Markdown})
}
func (i *DelegationSynthesisInput) UnmarshalJSON(raw []byte) error {
	var wire synthesisInputWire
	if err := json.Unmarshal(raw, &wire); err != nil {
		return err
	}
	var err error
	if i.TaskID, err = ParseTaskID(wire.TaskID); err != nil {
		return err
	}
	if i.RunID, err = ParseRunID(wire.RunID); err != nil {
		return err
	}
	if i.AttemptID, err = ParseAttemptID(wire.AttemptID); err != nil {
		return err
	}
	if i.ResultID, err = ParseResultID(wire.ResultID); err != nil {
		return err
	}
	if i.AgentReportID, err = ParseAgentReportID(wire.AgentReportID); err != nil {
		return err
	}
	if i.EventID, err = ParseEventID(wire.EventID); err != nil {
		return err
	}
	i.EventSequence = wire.EventSequence
	i.ResultDigest = wire.ResultDigest
	i.SourceTextDigest = wire.SourceTextDigest
	i.Markdown = wire.Markdown
	return nil
}
