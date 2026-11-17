package app

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

// A read-only observation can prove later quiescence of the SAME stopped
// operation, but cannot replace the first command receipt or refund a budget.
func (s *Service) RecordResearchStopObservation(ctx context.Context, c domain.WorkflowCommand, b json.RawMessage) error {
	var v struct {
		Schema, Status string
		Operation      string             `json:"operation_id"`
		Workflow       string             `json:"workflow_id"`
		Digest         string             `json:"input_digest"`
		Closed         bool               `json:"admission_closed"`
		Terminated     bool               `json:"terminated"`
		Idle           bool               `json:"graph_idle"`
		Pending        *[]json.RawMessage `json:"pending_operations"`
	}
	if c.Kind != "stop" || json.Unmarshal(b, &v) != nil || v.Schema != "chora.research-stop-observation.v1" || v.Operation != c.OperationID || v.Workflow != c.WorkflowID || v.Digest != c.InputDigest || !v.Closed || v.Pending == nil {
		return ErrInvalidCommand
	}
	err := s.deps.Store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		w, e := tx.GetWorkflow(ctx, c.WorkflowID)
		if e != nil {
			return e
		}
		if !w.StopRequested {
			return ErrUnauthorizedCommand
		}
		commands, e := tx.ListWorkflowCommands(ctx, c.WorkflowID)
		if e != nil {
			return e
		}
		for _, saved := range commands {
			if saved.Kind == "stop" && saved.OperationID == c.OperationID && saved.InputDigest == c.InputDigest && len(saved.Receipt) > 0 {
				return tx.AppendWorkflowEvent(ctx, w.ID, "research_stop_observation", b)
			}
		}
		return ErrUnauthorizedCommand
	})
	if err != nil {
		return err
	}
	if v.Status != "stopped" || !v.Terminated || !v.Idle || len(*v.Pending) != 0 {
		return errors.New("research admission closed; accepted operation remains pending or unknown")
	}
	return nil
}
