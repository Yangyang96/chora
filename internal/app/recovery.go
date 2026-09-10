package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

func (s *Service) RecoverStartup(ctx context.Context) error {
	if err := s.recoverTaskWorktrees(ctx); err != nil {
		return err
	}
	candidates, err := s.deps.Store.Reader().ListStartupCandidates(ctx)
	if err != nil {
		return err
	}
	for _, candidate := range candidates {
		if candidate.Attempt == nil {
			continue
		}
		now := s.deps.Clock.Now()
		var recovery domain.AgentRun
		if err := s.deps.Store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
			run, err := tx.GetRun(ctx, candidate.Run.ID())
			if err != nil {
				return err
			}
			if run.State() == domain.RunStateRecoveryRequired {
				recovery = run
				return nil
			}
			next, err := run.Transition(domain.CommandRecoveryDetected, now)
			if err != nil {
				return err
			}
			if err := tx.SaveRunCAS(ctx, run.Version(), next); err != nil {
				return err
			}
			body := responseBody(s.event("run.recovery_required", next))
			_, err = tx.AppendRunEvent(ctx, run.ID(), storecontract.EventDraft{ID: s.deps.IDs.EventID(), Type: "run.recovery_required", Source: "app", OccurredAt: now, RecordedAt: now, NormalizedJSON: body})
			recovery = next
			return err
		}); err != nil {
			return err
		}
		session, err := s.deps.Store.Reader().GetRuntimeSessionForAttempt(ctx, candidate.Attempt.ID())
		if errors.Is(err, storecontract.ErrNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		launch := execution.LaunchToken{Value: session.LaunchToken}
		if !launch.Valid() {
			continue
		}
		var outcome execution.ReconcileOutcome
		if session.ProcessIdentity == "" {
			outcome, err = s.deps.Supervisor.ReconcileLaunch(ctx, launch)
		} else {
			outcome, err = s.deps.Supervisor.Reconcile(ctx, execution.ProcessIdentity{Value: session.ProcessIdentity})
		}
		if err != nil {
			return err
		}
		binding := candidate.Attempt.AgentExecutionProfileBinding()
		pathPiBinding := session.AdapterID == "pi" && binding.Bound() && binding.ExecutionProvider() == domain.TrustedHostExecutionProvider && binding.RuntimeSource() == domain.LocalPiRuntimeSource
		if outcome.Kind == execution.ReconcileUncertain && pathPiBinding && outcome.LaunchToken != launch {
			// A missing PATH Pi route after restart proves neither liveness nor
			// ownership. Keep the durable recovery_required state readable and
			// never signal or replay an unbound process.
			continue
		}
		if outcome.LaunchToken != launch || outcome.Kind != execution.ReconcileDead && outcome.Kind != execution.ReconcileAlive && outcome.Kind != execution.ReconcileUncertain {
			return fmt.Errorf("malformed reconcile outcome")
		}
		if outcome.Kind == execution.ReconcileAlive && pathPiBinding {
			adapter, adapterErr := s.deps.Agents.Get(session.AdapterID)
			if adapterErr != nil || adapter.Capabilities().ResumeMode != execution.ResumeExplicitSession {
				continue
			}
			if !outcome.Handle.Valid() {
				continue
			}
			stopped, stopErr := s.deps.Supervisor.Stop(ctx, outcome.Handle, execution.StopIntent{Kind: execution.StopForCancel, Reason: "backend restart interrupted PATH Pi continuity"})
			if stopErr != nil || stopped.Kind != execution.StopConfirmed {
				continue
			}
			var confirmed execution.ReconcileOutcome
			var reconcileErr error
			if session.ProcessIdentity == "" {
				confirmed, reconcileErr = s.deps.Supervisor.ReconcileLaunch(ctx, launch)
			} else {
				confirmed, reconcileErr = s.deps.Supervisor.Reconcile(ctx, execution.ProcessIdentity{Value: session.ProcessIdentity})
			}
			if reconcileErr != nil || confirmed.Kind != execution.ReconcileDead || confirmed.LaunchToken != launch || !confirmed.Handle.Valid() {
				continue
			}
			outcome = confirmed
		}
		if outcome.Kind == execution.ReconcileDead {
			if !outcome.Handle.Valid() {
				return fmt.Errorf("reconciled dead runtime has no drain handle")
			}
			if session.State == "stopped" {
				for _, kind := range []execution.StreamKind{execution.StreamStdout, execution.StreamStderr} {
					stream, _ := storeStream(kind)
					offset, err := s.deps.Store.Reader().GetRuntimeStreamOffset(ctx, session.ID, stream)
					if err != nil {
						return err
					}
					if !offset.EOF {
						return fmt.Errorf("stopped runtime has undrained stream")
					}
				}
				if err := s.deps.Supervisor.Finalize(ctx, outcome.Handle, execution.RetentionPolicy{}); err != nil {
					return err
				}
				if err := s.markFinalized(ctx, session.ID); err != nil {
					return err
				}
				continue
			}
			adapter, err := s.deps.Agents.Get(session.AdapterID)
			if err != nil {
				return err
			}
			runtime := runtimeContext{session: session, attempt: *candidate.Attempt, run: recovery, adapter: adapter}
			terminalFiles, err := s.drainToEOF(ctx, runtime, outcome.Handle)
			if err != nil {
				return err
			}
			if err := s.deps.Store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
				attempt, err := tx.GetCurrentAttempt(ctx, candidate.Run.ID())
				if err != nil {
					return err
				}
				next, err := attempt.Transition(domain.AttemptEventStopConfirmedForRevision)
				if err != nil {
					return err
				}
				if err := tx.SaveAttemptCAS(ctx, attempt.State(), next); err != nil {
					return err
				}
				persistedSession, err := tx.GetRuntimeSession(ctx, session.ID)
				if err != nil {
					return err
				}
				version := persistedSession.Version
				persistedSession.Version++
				persistedSession.State = "stopped"
				persistedSession.UpdatedAt = now
				persistedSession.TerminalAt = now
				if err := tx.SaveRuntimeSessionCAS(ctx, version, persistedSession); err != nil {
					return err
				}
				reason := ""
				switch terminalFiles.TerminationCause {
				case execution.TerminationOutputLimit:
					reason = "runtime_output_limit_exceeded"
				case execution.TerminationDeadlineExceeded:
					reason = "attempt_timeout"
				case execution.TerminationExitNonzero:
					reason = "runtime_exit_nonzero"
				case execution.TerminationNone, "":
					if terminalFiles.ExitCode != 0 {
						reason = "runtime_exit_nonzero"
					}
				}
				if reason == "" {
					events, err := tx.ListRunEvents(ctx, candidate.Run.ID())
					if err != nil {
						return err
					}
					if attemptHasRuntimeFailure(events, "runtime_stream_invalid") {
						reason = "runtime_stream_invalid"
					}
				}
				body := responseBody(map[string]any{"type": "attempt.reconciled_dead", "attempt_id": attempt.ID().String(), "failure_reason": reason})
				_, err = tx.AppendRunEvent(ctx, candidate.Run.ID(), storecontract.EventDraft{ID: s.deps.IDs.EventID(), Type: "attempt.reconciled_dead", Source: "app", OccurredAt: now, RecordedAt: now, NormalizedJSON: body})
				return err
			}); err != nil {
				return err
			}
			if err := s.deps.Supervisor.Finalize(ctx, outcome.Handle, execution.RetentionPolicy{}); err != nil {
				return err
			}
			if err := s.markFinalized(ctx, session.ID); err != nil {
				return err
			}
		}
		_ = recovery
	}
	verificationCandidates, err := s.deps.Store.Reader().ListVerificationStartupCandidates(ctx)
	if err != nil {
		return err
	}
	for _, candidate := range verificationCandidates {
		if err := s.markVerificationRecovery(ctx, candidate.Run.ID(), candidate.VerificationRun.ID(), candidate.Attempt.ID(), "backend restart made verification continuity unprovable"); err != nil {
			return err
		}
	}
	return s.recoverPatchApplications(ctx)
}
