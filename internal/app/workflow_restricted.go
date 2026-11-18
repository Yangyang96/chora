package app

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

// BindRestrictedLaunch persists the identity observed by the trusted launch
// boundary before releasing a process. It grants no provider proof or runtime
// qualification. Unknown/default sources cannot be upgraded by calling it.
func (s *Service) BindRestrictedLaunch(ctx context.Context, workflowID string, invocation execution.Invocation, observed domain.RestrictedPiIdentity) (b domain.RestrictedLaunch, err error) {
	err = s.deps.Store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		w, e := tx.GetWorkflow(ctx, workflowID)
		if e != nil {
			return e
		}
		p := w.PiAuthorization
		if w.Gate == nil || p == nil || p.Version != 2 || p.RestrictedIdentity == nil || observed.Validate() != nil || observed != *p.RestrictedIdentity || !p.Active(time.Now()) || !invocation.Deadline().Equal(p.Deadline) {
			return ErrUnauthorizedCommand
		}
		if e = workflowBoundRunAuthority(ctx, tx, w); e != nil {
			return e
		}
		run, e := tx.GetRun(ctx, w.RunID)
		if e != nil {
			return e
		}
		a, e := tx.GetCurrentAttempt(ctx, w.RunID)
		if e != nil {
			return e
		}
		if e = workflowAttemptAuthority(ctx, tx, w, a); e != nil {
			return e
		}
		env := invocation.Environment()
		snapshot, e := tx.GetTaskResourceSnapshot(ctx, w.TaskID)
		if e != nil {
			return e
		}
		contract, e := tx.GetSpecCodingBinding(ctx, w.TaskID)
		if e != nil {
			return e
		}
		if observed.Snapshot != domain.WorkflowDigest(snapshot.CanonicalJSON) || observed.Contract != fmt.Sprintf("%x", contract.ActiveContractDigest) {
			return ErrUnauthorizedCommand
		}
		if env["CHORA_RUN_ID"] != run.ID().String() || env["CHORA_ATTEMPT_ID"] != a.ID().String() || env["CHORA_CONTEXT_DIGEST"] != fmt.Sprintf("%x", a.ContextDigest()) || env["CHORA_RUNTIME_SOURCE_IDENTITY"] != observed.RuntimeDigest() || observed.Arguments != domain.WorkflowDigest(responseBody(invocation.Arguments())) || observed.Environment != domain.WorkflowDigest(responseBody(domain.RestrictedStableEnvironment(env))) {
			return ErrUnauthorizedCommand
		}
		b = domain.RestrictedLaunch{Schema: "chora.restricted-launch.v2", Authorization: p.Reference, Workflow: w.ID, Project: w.ProjectID.String(), Task: w.TaskID.String(), Charter: run.CharterID().String(), Run: w.RunID.String(), Attempt: a.ID().String(), Actor: w.ActorID, Gate: w.Gate.ID, Operation: workflowID + ":pi_launch:" + a.ID().String(), InputDigest: w.InputDigest, AuthorityDigest: w.AuthorityDigest, LaunchToken: invocation.LaunchToken().Value, Channel: rand.Text(), SourceDigest: observed.Digest(), ConfigDigest: domain.WorkflowDigest(responseBody(struct {
			Args []string
			Env  map[string]string
		}{invocation.Arguments(), invocation.Environment()})), Epoch: 1, OutputCap: p.PerRequestOutputTokens, AuthorizedAt: p.AuthorizedAt, Deadline: p.Deadline, ObservedAt: time.Now().UTC()}
		return tx.InsertRestrictedLaunch(ctx, b)
	})
	return
}

func restrictedLaunchAuthority(ctx context.Context, r storecontract.Reader, w domain.WorkflowExecution, b domain.RestrictedLaunch) error {
	saved, revoked, err := r.GetRestrictedLaunch(ctx, b.Channel)
	if err != nil {
		return err
	}
	if revoked || saved != b || b.Validate() != nil || time.Now().Before(b.ObservedAt) || !time.Now().Before(b.Deadline) {
		return ErrUnauthorizedCommand
	}
	p := w.PiAuthorization
	if w.Gate == nil || p == nil || p.Version != 2 || p.RestrictedIdentity == nil || p.RestrictedIdentity.Validate() != nil || b.SourceDigest != p.RestrictedIdentity.Digest() || b.OutputCap != p.PerRequestOutputTokens || b.Authorization != p.Reference || b.Workflow != w.ID || b.Task != w.TaskID.String() || b.Project != w.ProjectID.String() || b.Run != w.RunID.String() || b.Actor != w.ActorID || b.Gate != w.Gate.ID || b.InputDigest != w.InputDigest || b.AuthorityDigest != w.AuthorityDigest || !b.Deadline.Equal(p.Deadline) || !b.AuthorizedAt.Equal(p.AuthorizedAt) {
		return ErrUnauthorizedCommand
	}
	if err = workflowRunAuthority(ctx, r, w); err != nil {
		return err
	}
	commands, err := r.ListWorkflowCommands(ctx, w.ID)
	if err != nil {
		return err
	}
	hasBinding := false
	for _, c := range commands {
		if c.Kind == "bind_run" && len(c.Receipt) > 0 {
			hasBinding = true
		}
	}
	if !hasBinding {
		return ErrUnauthorizedCommand
	}
	run, err := r.GetRun(ctx, w.RunID)
	if err != nil {
		return err
	}
	a, err := r.GetCurrentAttempt(ctx, w.RunID)
	if err != nil {
		return err
	}
	charter, err := r.GetCharter(ctx, run.CharterID())
	if err != nil {
		return err
	}
	if b.Attempt != a.ID().String() || b.Charter != run.CharterID().String() || a.ModelBinding().JSON() != p.Model.JSON() || a.AgentExecutionProfileBinding().Record() != p.Profile || charter.ModelBinding().JSON() != p.Model.JSON() || charter.AgentExecutionProfileBinding().Record() != p.Profile {
		return ErrUnauthorizedCommand
	}
	return nil
}

// RestrictedDispatchPort is the only business operation exposed to a bound
// Docker pipe. It accepts no actor, URL, headers, key, source, epoch or Run from
// frame data. The supervisor owns the BoundLaunch and revokes it on EOF.
type RestrictedDispatchPort struct {
	transport *WorkflowChatTransport
	service   *Service
}

func (p *RestrictedDispatchPort) ready() error {
	if p.transport == nil || p.transport.inputBound == nil && len(p.transport.proofs) == 0 {
		return ErrBoundUnproven
	}
	return nil
}
func (p *RestrictedDispatchPort) Prepare(ctx context.Context, request execution.StartRequest) error {
	if err := p.ready(); err != nil {
		return err
	}
	w, err := p.service.deps.Store.Reader().GetTaskWorkflow(ctx, request.Run.TaskID())
	if err != nil {
		return err
	}
	if w.PiAuthorization == nil || w.PiAuthorization.Version != 2 || w.PiAuthorization.RestrictedIdentity == nil {
		return ErrUnauthorizedCommand
	}
	return workflowBoundRunAuthority(ctx, p.service.deps.Store.Reader(), w)
}
func (p *RestrictedDispatchPort) Bind(ctx context.Context, inv execution.Invocation) (domain.RestrictedLaunch, error) {
	if err := p.ready(); err != nil {
		return domain.RestrictedLaunch{}, err
	}
	runID, err := domain.ParseRunID(inv.Environment()["CHORA_RUN_ID"])
	if err != nil {
		return domain.RestrictedLaunch{}, err
	}
	run, err := p.service.deps.Store.Reader().GetRun(ctx, runID)
	if err != nil {
		return domain.RestrictedLaunch{}, err
	}
	w, err := p.service.deps.Store.Reader().GetTaskWorkflow(ctx, run.TaskID())
	if err != nil {
		return domain.RestrictedLaunch{}, err
	}
	if w.PiAuthorization == nil || w.PiAuthorization.RestrictedIdentity == nil {
		return domain.RestrictedLaunch{}, ErrUnauthorizedCommand
	}
	return p.service.BindRestrictedLaunch(ctx, w.ID, inv, *w.PiAuthorization.RestrictedIdentity)
}
func (p *RestrictedDispatchPort) Admit(ctx context.Context, b domain.RestrictedLaunch) error {
	if err := p.ready(); err != nil {
		return err
	}
	w, err := p.service.deps.Store.Reader().GetWorkflow(ctx, b.Workflow)
	if err != nil {
		return err
	}
	return restrictedLaunchAuthority(ctx, p.service.deps.Store.Reader(), w, b)
}
func (p *RestrictedDispatchPort) Lease(ctx context.Context, b domain.RestrictedLaunch) error {
	// Model rights are revoked at RPC EOF. A live app may retain this separate
	// lease only while freezing/exporting the already settled workspace. Stop,
	// deadline, SQLite loss or an unknown model outcome still closes PID1 stdin.
	if err := p.ready(); err != nil {
		return err
	}
	r := p.service.deps.Store.Reader()
	saved, _, err := r.GetRestrictedLaunch(ctx, b.Channel)
	if err != nil {
		return err
	}
	if saved != b || b.Validate() != nil || time.Now().Before(b.ObservedAt) || !time.Now().Before(b.Deadline) {
		return ErrUnauthorizedCommand
	}
	w, err := r.GetWorkflow(ctx, b.Workflow)
	if err != nil {
		return err
	}
	if w.RunID.String() != b.Run || w.ActorID != b.Actor {
		return ErrUnauthorizedCommand
	}
	return workflowRunAuthority(ctx, r, w)
}
func (p *RestrictedDispatchPort) Recover(ctx context.Context, b domain.RestrictedLaunch) error {
	return p.Revoke(ctx, b)
}

func NewRestrictedExecutionAuthority(s *Service) *RestrictedDispatchPort {
	return &RestrictedDispatchPort{service: s}
}

func NewRestrictedDispatchPort(t *WorkflowChatTransport) (*RestrictedDispatchPort, error) {
	if t == nil {
		return nil, ErrInvalidCommand
	}
	return &RestrictedDispatchPort{transport: t, service: t.service}, nil
}
func (p *RestrictedDispatchPort) Revoke(ctx context.Context, b domain.RestrictedLaunch) error {
	return p.service.deps.Store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		saved, _, err := tx.GetRestrictedLaunch(ctx, b.Channel)
		if err != nil {
			return err
		}
		if saved != b {
			return ErrUnauthorizedCommand
		}
		if err = tx.RevokeRestrictedLaunch(ctx, b.Channel); err != nil {
			return err
		}
		if err = workflowPiDispatchQuiescent(ctx, tx, b.Workflow); err != nil {
			w, e := tx.GetWorkflow(ctx, b.Workflow)
			if e != nil {
				return e
			}
			old := w.Version
			w.Version++
			w.Status = "unresolved"
			w.Reason = "Restricted channel closed with a pending or unknown provider reservation."
			if e = tx.SaveWorkflowCAS(ctx, old, w); e != nil {
				return e
			}
		}
		return tx.AppendWorkflowEvent(ctx, b.Workflow, "pi_channel_revoked", responseBody(map[string]string{"channel": b.Channel}))
	})
}
func (p *RestrictedDispatchPort) Dispatch(ctx context.Context, b domain.RestrictedLaunch, id string, raw []byte) (json.RawMessage, error) {
	if err := p.ready(); err != nil {
		return nil, err
	}
	run, err := domain.ParseRunID(b.Run)
	if err != nil {
		return nil, ErrInvalidCommand
	}
	attempt, err := domain.ParseAttemptID(b.Attempt)
	if err != nil {
		return nil, ErrInvalidCommand
	}
	return p.transport.dispatch(ctx, b.Workflow, run, attempt, id, raw, &b)
}

// No result replay can resend. This method returns the ORIGINAL immutable
// receipt (including unknown); response bytes are deliberately not in public
// event logs. A response that was not retained is explicitly unreplayable.
func (s *Service) restrictedReplay(ctx context.Context, b domain.RestrictedLaunch, id, digest string) error {
	return s.deps.Store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		w, err := tx.GetWorkflow(ctx, b.Workflow)
		if err != nil {
			return err
		}
		saved, _, e := tx.GetRestrictedLaunch(ctx, b.Channel)
		if e != nil {
			return e
		}
		if saved != b || b.Workflow != w.ID || b.Actor != w.ActorID || b.InputDigest != w.InputDigest || b.AuthorityDigest != w.AuthorityDigest {
			return ErrUnauthorizedCommand
		}
		commands, err := tx.ListWorkflowCommands(ctx, b.Workflow)
		if err != nil {
			return err
		}
		for _, c := range commands {
			if c.Kind != "pi_dispatch" {
				continue
			}
			var r WorkflowDispatchReservation
			if json.Unmarshal(c.Receipt, &r) != nil {
				return ErrInvalidCommand
			}
			if r.RequestID == id {
				if r.Channel != b.Channel || r.PayloadDigest != digest {
					return storecontract.ErrIdempotencyConflict
				}
				for _, result := range commands {
					if result.OperationID == b.Workflow+":pi_dispatch_result:"+id {
						return &RestrictedReplayError{Receipt: result.Receipt}
					}
				}
				return &RestrictedReplayError{Receipt: json.RawMessage(`{"outcome":"pending_or_unknown"}`)}
			}
		}
		return nil
	})
}

type RestrictedReplayError struct{ Receipt json.RawMessage }

func (e *RestrictedReplayError) Error() string {
	return "dispatch already reserved: original result unreplayable; no retransmission"
}
func (e *RestrictedReplayError) ChannelError() string { return "result_unreplayable_no_retransmission" }

// Stop/DB loss cancels an accepted HTTP call independently of the research PG.
// The full reservation remains consumed; cancellation does not prove that the
// provider stopped generation or billing. Polling bounds observation latency,
// and is not an atomic transaction with the remote socket.
func (s *Service) watchRestrictedDispatch(ctx context.Context, cancel context.CancelFunc, b domain.RestrictedLaunch) {
	timer := time.NewTicker(50 * time.Millisecond)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			w, err := s.deps.Store.Reader().GetWorkflow(ctx, b.Workflow)
			if err == nil {
				err = restrictedLaunchAuthority(ctx, s.deps.Store.Reader(), w, b)
			}
			if err != nil {
				cancel()
				return
			}
		}
	}
}

var _ error = (*RestrictedReplayError)(nil)
