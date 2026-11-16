package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

// WorkflowDispatchReservation is committed before a provider is contacted.
// Reservations are never refunded, including successful, unknown and crashed
// calls. A pending outcome blocks subsequent dispatch, including after restart.
type WorkflowDispatchReservation struct {
	RequestID     string    `json:"request_id"`
	AttemptID     string    `json:"attempt_id"`
	InputTokens   int       `json:"input_tokens"`
	OutputTokens  int       `json:"output_tokens"`
	PayloadDigest string    `json:"payload_digest"`
	Deadline      time.Time `json:"deadline"`
}

// Unknown or exhausted provider authority also forbids a successor process;
// merely refusing its next model call would still allow unbudgeted local work.
func workflowPiBudgetRoom(ctx context.Context, r storecontract.Reader, w domain.WorkflowExecution) error {
	if w.PiAuthorization == nil {
		return nil
	}
	commands, err := r.ListWorkflowCommands(ctx, w.ID)
	if err != nil {
		return err
	}
	completed := map[string]bool{}
	var after uint64
	for {
		events, e := r.ListWorkflowEvents(ctx, w.ID, after)
		if e != nil {
			return e
		}
		for _, event := range events {
			after = event.Sequence
			if event.Kind == "pi_dispatch_completed" {
				var value struct {
					RequestID string `json:"request_id"`
					Outcome   string `json:"outcome"`
				}
				if json.Unmarshal(event.Data, &value) != nil {
					return ErrInvalidCommand
				}
				if value.Outcome == "response" {
					completed[value.RequestID] = true
				}
			}
		}
		if len(events) < 200 {
			break
		}
	}
	requests, tokens, output := 0, 0, 0
	for _, c := range commands {
		if c.Kind != "pi_dispatch" {
			continue
		}
		var saved WorkflowDispatchReservation
		if json.Unmarshal(c.Receipt, &saved) != nil {
			return ErrInvalidCommand
		}
		if !completed[saved.RequestID] {
			return fmt.Errorf("%w: provider dispatch outcome unknown", ErrUnauthorizedCommand)
		}
		requests++
		tokens += saved.InputTokens + saved.OutputTokens
		output += saved.OutputTokens
	}
	p := w.PiAuthorization
	if requests >= p.MaxRequests || tokens >= p.MaxTokens || output >= p.MaxOutputTokens {
		return fmt.Errorf("%w: Pi budget exhausted", ErrUnauthorizedCommand)
	}
	return nil
}

// ReserveWorkflowDispatch belongs at the physical transport boundary. Replaying
// a reserved request NEVER permits retransmission: it could already be billed.
func (s *Service) ReserveWorkflowDispatch(ctx context.Context, id string, runID domain.RunID, attemptID domain.AttemptID, requestID, transportIdentity, payloadDigest string, inputTokens, outputTokens int) (reservation WorkflowDispatchReservation, err error) {
	if requestID == "" || len(requestID) > 128 || len(payloadDigest) != 64 || inputTokens < 0 || inputTokens > 100000 || outputTokens < 1 || outputTokens > 2048 {
		return reservation, ErrInvalidCommand
	}
	err = s.deps.Store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		w, e := tx.GetWorkflow(ctx, id)
		if e != nil {
			return e
		}
		if w.RunID != runID || w.PiAuthorization == nil || !w.PiAuthorization.Active(time.Now()) || w.PiAuthorization.TransportIdentity != transportIdentity {
			return ErrUnauthorizedCommand
		}
		if e = workflowBoundRunAuthority(ctx, tx, w); e != nil {
			return e
		}
		run, e := tx.GetRun(ctx, runID)
		if e != nil {
			return e
		}
		if run.State() != domain.RunStateRunning {
			return ErrUnauthorizedCommand
		}
		attempt, e := tx.GetCurrentAttempt(ctx, runID)
		if e != nil {
			return e
		}
		p := w.PiAuthorization
		if attempt.ID() != attemptID || attempt.State() != domain.AttemptStateRunning || attempt.ModelBinding().JSON() != p.Model.JSON() || attempt.AgentExecutionProfileBinding().Record() != p.Profile {
			return ErrUnauthorizedCommand
		}
		commands, e := tx.ListWorkflowCommands(ctx, id)
		if e != nil {
			return e
		}
		completed := map[string]bool{}
		var after uint64
		for {
			events, e := tx.ListWorkflowEvents(ctx, id, after)
			if e != nil {
				return e
			}
			for _, event := range events {
				after = event.Sequence
				if event.Kind == "pi_dispatch_completed" {
					var v struct {
						RequestID string `json:"request_id"`
						Outcome   string `json:"outcome"`
					}
					if json.Unmarshal(event.Data, &v) != nil {
						return ErrInvalidCommand
					}
					if v.Outcome == "response" {
						completed[v.RequestID] = true
					}
				}
			}
			if len(events) < 200 {
				break
			}
		}
		requests, tokens, output := 0, 0, 0
		for _, c := range commands {
			if c.Kind != "pi_dispatch" {
				continue
			}
			var previous WorkflowDispatchReservation
			if e = json.Unmarshal(c.Receipt, &previous); e != nil {
				return e
			}
			if previous.RequestID == requestID {
				return storecontract.ErrIdempotencyConflict
			}
			if !completed[previous.RequestID] {
				return fmt.Errorf("%w: previous provider outcome unknown", ErrUnauthorizedCommand)
			}
			requests++
			tokens += previous.InputTokens + previous.OutputTokens
			output += previous.OutputTokens
		}
		if requests >= p.MaxRequests || tokens+inputTokens+outputTokens > p.MaxTokens || output+outputTokens > p.MaxOutputTokens || outputTokens > p.PerRequestOutputTokens {
			return fmt.Errorf("%w: Pi budget exhausted", ErrUnauthorizedCommand)
		}
		reservation = WorkflowDispatchReservation{RequestID: requestID, AttemptID: attemptID.String(), InputTokens: inputTokens, OutputTokens: outputTokens, PayloadDigest: payloadDigest, Deadline: p.Deadline}
		c := workflowCommand(w, "pi_dispatch:"+requestID, reservation)
		c.Kind = "pi_dispatch"
		if e = tx.PutWorkflowCommand(ctx, c); e != nil {
			return e
		}
		if e = tx.RecordWorkflowReceipt(ctx, c, responseBody(reservation)); e != nil {
			return e
		}
		return tx.AppendWorkflowEvent(ctx, id, "pi_dispatch_reserved", responseBody(reservation))
	})
	return
}

func (s *Service) FinishWorkflowDispatch(ctx context.Context, id string, reservation WorkflowDispatchReservation, prompt, completion int, known bool) error {
	return s.deps.Store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		w, e := tx.GetWorkflow(ctx, id)
		if e != nil {
			return e
		}
		commands, e := tx.ListWorkflowCommands(ctx, id)
		if e != nil {
			return e
		}
		found := false
		for _, c := range commands {
			if c.OperationID == id+":pi_dispatch:"+reservation.RequestID && c.Kind == "pi_dispatch" {
				var saved WorkflowDispatchReservation
				if json.Unmarshal(c.Receipt, &saved) == nil && saved.RequestID == reservation.RequestID && saved.AttemptID == reservation.AttemptID && saved.InputTokens == reservation.InputTokens && saved.OutputTokens == reservation.OutputTokens && saved.PayloadDigest == reservation.PayloadDigest && saved.Deadline.Equal(reservation.Deadline) {
					found = true
				}
			}
		}
		if !found {
			return ErrUnauthorizedCommand
		}
		outcome := "unknown"
		if known && prompt >= 0 && completion >= 0 && prompt <= reservation.InputTokens && completion <= reservation.OutputTokens && time.Now().Before(reservation.Deadline) {
			outcome = "response"
		}
		// A command receipt makes completion immutable too. Unknown cannot later
		// become available balance by repeating or replacing the result.
		c := workflowCommand(w, "pi_dispatch_result:"+reservation.RequestID, map[string]any{"request_id": reservation.RequestID, "outcome": outcome, "prompt": prompt, "completion": completion})
		if e = tx.PutWorkflowCommand(ctx, c); e != nil {
			return e
		}
		if e = tx.RecordWorkflowReceipt(ctx, c, []byte(c.Payload)); e != nil {
			return e
		}
		if outcome == "unknown" {
			old := w.Version
			w.Version++
			w.Status = "unresolved"
			w.Reason = "Provider dispatch outcome is unknown; its full reservation remains consumed."
			if e = tx.SaveWorkflowCAS(ctx, old, w); e != nil {
				return e
			}
		}
		return tx.AppendWorkflowEvent(ctx, id, "pi_dispatch_completed", []byte(c.Payload))
	})
}

// recordWorkflowLaunch freezes observations from the final prepared invocation,
// rather than attributing a managed-runtime constant to a PATH executable.
func (s *Service) recordWorkflowLaunch(ctx context.Context, run domain.AgentRun, attempt domain.Attempt, invocation execution.Invocation, fp execution.RuntimeFingerprint) (execution.Invocation, error) {
	w, err := s.deps.Store.Reader().GetTaskWorkflow(ctx, run.TaskID())
	if errors.Is(err, storecontract.ErrNotFound) {
		return invocation, nil
	}
	if err != nil {
		return invocation, err
	}
	if w.PiAuthorization != nil {
		p := w.PiAuthorization
		if !p.Active(time.Now()) || attempt.ModelBinding().JSON() != p.Model.JSON() || attempt.AgentExecutionProfileBinding().Record() != p.Profile || fp.Version != p.Model.Record().Catalog.RuntimeVersion {
			return invocation, ErrUnauthorizedCommand
		}
		invocation = invocation.WithDeadline(p.Deadline)
	}
	err = s.deps.Store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		w, e := tx.GetWorkflow(ctx, w.ID)
		if e != nil {
			return e
		}
		if e = workflowAttemptAuthority(ctx, tx, w, attempt); e != nil {
			return e
		}
		body := map[string]any{"attempt_id": attempt.ID().String(), "run_id": run.ID().String(), "model_binding": attempt.ModelBinding(), "profile": attempt.AgentExecutionProfileBinding().Record(), "runtime_version": fp.Version, "runtime_fingerprint": fmt.Sprintf("%x", fp.Digest), "arguments_digest": domain.WorkflowDigest(responseBody(invocation.Arguments()))}
		c := workflowCommand(w, "pi_launch:"+attempt.ID().String(), body)
		if e = tx.PutWorkflowCommand(ctx, c); e != nil {
			return e
		}
		return tx.RecordWorkflowReceipt(ctx, c, []byte(c.Payload))
	})
	return invocation, err
}
