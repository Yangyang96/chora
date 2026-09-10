package app

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"

	"github.com/Yangyang96/chora/internal/contextcore"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
	"github.com/Yangyang96/chora/internal/speccoding"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

func (s *Service) PrepareRun(ctx context.Context, request PrepareRunRequest) (PrepareRunResult, error) {
	return s.prepare(ctx, request.CommandMeta, request.RunID, request.ExpectedVersion, nil)
}
func (s *Service) PrepareRetry(ctx context.Context, request PrepareRetryRequest) (PrepareRunResult, error) {
	return s.prepare(ctx, request.CommandMeta, request.RunID, request.ExpectedVersion, &retrySpec{Reason: request.Reason, Instructions: request.Instructions, Automatic: request.Automatic})
}

type retrySpec struct {
	Reason, Instructions string
	Automatic            bool
}

func (s *Service) prepare(ctx context.Context, meta CommandMeta, runID domain.RunID, expected uint64, retry *retrySpec) (PrepareRunResult, error) {
	if err := s.require(); err != nil {
		return PrepareRunResult{}, err
	}
	action := "prepare_run"
	if retry != nil {
		action = "prepare_retry"
	}
	if err := s.authorize(ctx, meta, action, runID.String(), expected); err != nil {
		return PrepareRunResult{}, err
	}
	unlock := s.runLock(runID)
	defer unlock()
	if s.deps.TaskWorktrees != nil || s.deps.TaskResourceWorkspaces != nil {
		run, err := s.deps.Store.Reader().GetRun(ctx, runID)
		if err != nil {
			return PrepareRunResult{}, err
		}
		if err := s.ensureExistingTaskWorktree(ctx, run.TaskID()); err != nil {
			return PrepareRunResult{}, err
		}
	}
	now := s.deps.Clock.Now()
	semantic := any(struct{}{})
	if retry != nil {
		semantic = *retry
	}
	key, err := s.commandKey(meta, action, runID.String(), expected, semantic, now)
	if err != nil {
		return PrepareRunResult{}, err
	}
	var result PrepareRunResult
	err = s.deps.Store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		response, replayed, err := tx.LookupCommand(ctx, key)
		if err != nil {
			return err
		}
		if replayed {
			var replayErr error
			result, replayErr = replayPrepare(ctx, response, tx)
			return replayErr
		}
		if e := requireRunWithoutClosure(ctx, tx, runID); e != nil {
			return e
		}
		run, err := tx.GetRun(ctx, runID)
		if err != nil {
			return err
		}
		if run.Version() != expected {
			return storecontract.ErrVersionConflict
		}
		task, err := tx.GetTask(ctx, run.TaskID())
		if err != nil {
			return err
		}
		if err := s.requireWorkbenchRoom(ctx, tx, task.RoomID()); err != nil {
			return err
		}
		planBinding, charter, snapshot, err := loadBoundRunContext(ctx, tx, run, task)
		if err != nil {
			return err
		}
		var currentProfile domain.AgentExecutionProfilePreference
		var currentProfileBinding domain.AgentExecutionProfileBinding
		if charter.AdapterID() == "pi" {
			currentProfile, currentProfileBinding, err = taskAgentExecutionProfileBinding(ctx, tx, task.ID())
			if err != nil {
				return err
			}
			if err := requireProfileAcknowledgement(ctx, tx, currentProfile.Profile(), currentProfile.ActorID()); err != nil {
				return err
			}
		}
		command := domain.CommandPrepareRun
		var predecessor *domain.AttemptID
		var automaticStatus *AutomaticRetry
		var externalSession string
		profile := charter.AgentExecutionProfileBinding()
		if retry == nil && charter.AdapterID() == "pi" && profile != currentProfileBinding {
			return fmt.Errorf("%w: Charter drifted from current Agent execution profile preference", ErrInvalidCommand)
		}
		if retry != nil {
			command = domain.CommandPrepareRetry
			old, err := tx.GetCurrentAttempt(ctx, runID)
			if err != nil {
				return err
			}
			if run.State() == domain.RunStateCancelled {
				events, err := tx.ListRunEvents(ctx, runID)
				if err != nil {
					return err
				}
				if _, allowed := CancelRetryAuthority(events, old.ID()); !allowed {
					return fmt.Errorf("%w: cancelled run has no persisted retry authority", ErrInvalidCommand)
				}
			}
			session, sessionErr := tx.GetRuntimeSessionForAttempt(ctx, old.ID())
			if sessionErr == nil && !runtimeSessionFinalizedForRetry(session) {
				return fmt.Errorf("%w: predecessor runtime is not proven dead and finalized", ErrInvalidCommand)
			}
			if sessionErr != nil && !errors.Is(sessionErr, storecontract.ErrNotFound) {
				return sessionErr
			}
			// Fresh recovery must not inherit the failed Pi session identity.
			// Retain the predecessor link for provenance and explicit resume paths.
			freshRetry := retry.Automatic || old.AdapterID() == "pi" && ((run.State() == domain.RunStateRecoveryRequired && old.State() != domain.AttemptStateInterrupted) || old.AgentExecutionProfileBinding().ExecutionProvider() != domain.TrustedHostExecutionProvider)
			if sessionErr == nil && !freshRetry {
				externalSession = session.ExternalReference
			}
			if old.ContextSnapshotID() != planBinding.SnapshotID() || old.ContextDigest() != planBinding.SnapshotDigest() {
				return fmt.Errorf("%w: predecessor Attempt drifted from immutable planning binding", ErrInvalidCommand)
			}
			if old.AdapterID() == "pi" && old.AgentExecutionProfileBinding() != currentProfileBinding {
				return fmt.Errorf("%w: predecessor Attempt drifted from current Agent execution profile preference", ErrInvalidCommand)
			}
			profile = old.AgentExecutionProfileBinding()
			id := old.ID()
			predecessor = &id
			if retry.Automatic {
				automaticStatus, err = automaticRetryForRun(ctx, tx, runID)
				if err != nil {
					return err
				}
				if automaticStatus == nil || automaticStatus.State != AutomaticRetryPending || automaticStatus.RetriesUsed >= AutomaticRetryMaxRetries {
					return fmt.Errorf("%w: automatic retry is not durably pending", ErrInvalidCommand)
				}
			}
		}
		next, err := run.Transition(command, now)
		if err != nil {
			return err
		}
		attempt, err := domain.NewAttempt(domain.AttemptParams{ID: s.deps.IDs.AttemptID(), RunID: runID, Sequence: next.CurrentAttemptNumber(), Predecessor: predecessor, ContextSnapshotID: snapshot.ID(), ContextDigest: snapshot.Digest(), AdapterID: charter.AdapterID(), AgentExecutionProfileBinding: profile, RetryReason: func() string {
			if retry != nil {
				return retry.Reason
			}
			return ""
		}(), ContextDelta: func() string {
			if retry != nil {
				return retry.Instructions
			}
			return ""
		}(), ExternalSession: externalSession, CreatedAt: now})
		if err != nil {
			return err
		}
		if err := tx.SaveRunCAS(ctx, expected, next); err != nil {
			return err
		}
		if err := tx.InsertAttempt(ctx, attempt); err != nil {
			return err
		}
		eventJSON := responseBody(s.attemptEvent("run.prepared", next, attempt))
		if _, err := tx.AppendRunEvent(ctx, runID, storecontract.EventDraft{ID: s.deps.IDs.EventID(), Type: "run.prepared", Source: "app", OccurredAt: now, RecordedAt: now, NormalizedJSON: eventJSON}); err != nil {
			return err
		}
		persistRetryMarker := attempt.AdapterID() == "pi"
		if retry != nil && !persistRetryMarker {
			events, eventsErr := tx.ListRunEvents(ctx, runID)
			if eventsErr != nil {
				return eventsErr
			}
			state, stateErr := automaticRetryEventState(events)
			if stateErr != nil {
				return stateErr
			}
			persistRetryMarker = state.enabled
		}
		if retry != nil && persistRetryMarker {
			eventType := automaticRetryResetEvent
			payload := automaticRetryEvent{Type: eventType, RunID: runID.String(), AttemptID: attempt.ID().String(), MaxRetries: AutomaticRetryMaxRetries}
			if retry.Automatic {
				eventType = automaticRetryPreparedEvent
				payload.Type = eventType
				payload.PredecessorID = (*predecessor).String()
				payload.FailureReason = retry.Reason
				payload.RetriesUsed = automaticStatus.RetriesUsed + 1
			} else {
				// An explicit human Retry both opts historical Runs in and resets
				// the consecutive automatic budget.
				payload.RetriesUsed = 0
			}
			if _, err := tx.AppendRunEvent(ctx, runID, storecontract.EventDraft{ID: s.deps.IDs.EventID(), Type: eventType, Source: "app", OccurredAt: now, RecordedAt: now, NormalizedJSON: responseBody(payload)}); err != nil {
				return err
			}
		}
		result = PrepareRunResult{Run: next, Attempt: attempt, Snapshot: snapshot}
		response, err = prepareResponse(result)
		if err != nil {
			return err
		}
		if err := tx.SaveCommand(ctx, key, response); err != nil {
			return err
		}
		return nil
	})
	return result, err
}

func loadBoundRunContext(ctx context.Context, reader storecontract.Reader, run domain.AgentRun, task domain.Task) (domain.TechnicalPlanRunBinding, domain.RunCharter, contextcore.Snapshot, error) {
	binding, err := reader.GetTechnicalPlanRunBinding(ctx, run.ID())
	if err != nil {
		if errors.Is(err, storecontract.ErrNotFound) {
			return domain.TechnicalPlanRunBinding{}, domain.RunCharter{}, contextcore.Snapshot{}, fmt.Errorf("%w: Run has no immutable technical plan binding", ErrInvalidCommand)
		}
		return domain.TechnicalPlanRunBinding{}, domain.RunCharter{}, contextcore.Snapshot{}, err
	}
	if binding.TaskID() != task.ID() || binding.TaskID() != run.TaskID() || binding.CharterID() != run.CharterID() {
		return domain.TechnicalPlanRunBinding{}, domain.RunCharter{}, contextcore.Snapshot{}, fmt.Errorf("%w: Run planning identity drift", ErrInvalidCommand)
	}
	revision, err := reader.GetTechnicalPlanRevision(ctx, binding.RevisionID())
	if err != nil || revision.TaskID() != task.ID() {
		return domain.TechnicalPlanRunBinding{}, domain.RunCharter{}, contextcore.Snapshot{}, fmt.Errorf("%w: bound technical plan Revision drift", ErrInvalidCommand)
	}
	acceptance, err := reader.GetCurrentTechnicalPlanAcceptance(ctx, task.ID())
	if err != nil || acceptance.RevisionID() != binding.RevisionID() || acceptance.SnapshotID() != binding.SnapshotID() || acceptance.SnapshotDigest() != binding.SnapshotDigest() {
		return domain.TechnicalPlanRunBinding{}, domain.RunCharter{}, contextcore.Snapshot{}, fmt.Errorf("%w: current technical plan acceptance drift", ErrInvalidCommand)
	}
	selection, err := reader.GetTaskRevisionSelection(ctx, task.ID())
	if err != nil || selection.Digest() != revision.SelectionDigest() {
		return domain.TechnicalPlanRunBinding{}, domain.RunCharter{}, contextcore.Snapshot{}, fmt.Errorf("%w: bound technical plan selection drift", ErrInvalidCommand)
	}
	snapshot, err := reader.GetSnapshot(ctx, binding.SnapshotID())
	if err != nil || snapshot.Digest() != binding.SnapshotDigest() || sha256.Sum256(snapshot.CanonicalJSON()) != binding.SnapshotDigest() || !snapshotMatchesSelection(snapshot, selection) {
		return domain.TechnicalPlanRunBinding{}, domain.RunCharter{}, contextcore.Snapshot{}, fmt.Errorf("%w: bound Context Snapshot drift", ErrInvalidCommand)
	}
	charter, err := reader.GetCharter(ctx, binding.CharterID())
	if err != nil || charter.TaskID() != task.ID() || charter.TaskGoal() != task.Goal() || !sameCriteria(task.Criteria(), charter.Criteria()) || !sameRevisionIDs(charter.ContextRevisionIDs(), selection.SelectedRevisionIDs()) {
		return domain.TechnicalPlanRunBinding{}, domain.RunCharter{}, contextcore.Snapshot{}, fmt.Errorf("%w: bound Run Charter drift", ErrInvalidCommand)
	}
	snapshotCharterID, workspaceRoot, err := specCodingSnapshotBoundary(snapshot)
	if err != nil || snapshotCharterID != charter.ID() || workspaceRoot != charter.WorkspaceRoot() {
		return domain.TechnicalPlanRunBinding{}, domain.RunCharter{}, contextcore.Snapshot{}, fmt.Errorf("%w: Snapshot and Charter boundary drift", ErrInvalidCommand)
	}
	specBinding, err := reader.GetSpecCodingBinding(ctx, task.ID())
	if err == nil {
		if specBinding.Status != storecontract.SpecCodingRegistered || specBinding.SnapshotID != snapshot.ID() || specBinding.SnapshotDigest != snapshot.Digest() || charter.AdapterID() != "pi" {
			return domain.TechnicalPlanRunBinding{}, domain.RunCharter{}, contextcore.Snapshot{}, fmt.Errorf("%w: registered SpecCoding execution binding drift", ErrInvalidCommand)
		}
	} else if !errors.Is(err, storecontract.ErrNotFound) {
		return domain.TechnicalPlanRunBinding{}, domain.RunCharter{}, contextcore.Snapshot{}, err
	} else if charter.AdapterID() != "fake" {
		return domain.TechnicalPlanRunBinding{}, domain.RunCharter{}, contextcore.Snapshot{}, fmt.Errorf("%w: ordinary Task is not bound to Fake Agent", ErrInvalidCommand)
	}
	return binding, charter, snapshot, nil
}

func runtimeSessionFinalizedForRetry(session storecontract.RuntimeSession) bool {
	if session.TerminalAt.IsZero() || session.FinalizedAt.IsZero() {
		return false
	}
	if session.State == "stopped" {
		return true
	}
	return session.State == "failed" && session.ProcessIdentity == "" && session.StartedAt.IsZero()
}

func sameRevisionIDs(left, right []domain.ContextRevisionID) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func (s *Service) StartAttempt(ctx context.Context, request StartAttemptRequest) (StartAttemptResult, error) {
	if err := s.require(); err != nil {
		return StartAttemptResult{}, err
	}
	if err := s.authorize(ctx, request.CommandMeta, "start_attempt", request.RunID.String(), request.ExpectedVersion); err != nil {
		return StartAttemptResult{}, err
	}
	unlock := s.runLock(request.RunID)
	defer unlock()
	now := s.deps.Clock.Now()
	startRun, err := s.deps.Store.Reader().GetRun(ctx, request.RunID)
	if err != nil {
		return StartAttemptResult{}, err
	}
	startCharter, err := s.deps.Store.Reader().GetCharter(ctx, startRun.CharterID())
	if err != nil {
		return StartAttemptResult{}, err
	}
	executionRoot, err := s.executionRootForCharter(ctx, startCharter)
	if err != nil {
		return StartAttemptResult{}, err
	}
	key, err := s.commandKey(request.CommandMeta, "start_attempt", request.RunID.String(), request.ExpectedVersion, request.Mode, now)
	if err != nil {
		return StartAttemptResult{}, err
	}
	var result StartAttemptResult
	err = s.deps.Store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		response, replayed, err := tx.LookupCommand(ctx, key)
		if err != nil {
			return err
		}
		if replayed {
			var replayErr error
			result, replayErr = replayStart(response)
			return replayErr
		}
		run, err := tx.GetRun(ctx, request.RunID)
		if err != nil {
			return err
		}
		if run.Version() != request.ExpectedVersion {
			return storecontract.ErrVersionConflict
		}
		attempt, err := tx.GetCurrentAttempt(ctx, request.RunID)
		if err != nil {
			return err
		}
		if request.Mode != StartFresh && request.Mode != StartResumeRecordedSession {
			return fmt.Errorf("%w: explicit start mode required", ErrInvalidCommand)
		}
		if attempt.Sequence() == 1 && request.Mode != StartFresh {
			return fmt.Errorf("%w: first attempt must start fresh", ErrInvalidCommand)
		}
		charter, err := tx.GetCharter(ctx, run.CharterID())
		if err != nil {
			return err
		}
		if attempt.AdapterID() == "pi" {
			preference, binding, err := taskAgentExecutionProfileBinding(ctx, tx, run.TaskID())
			if err != nil {
				return err
			}
			if attempt.AgentExecutionProfileBinding() != binding {
				return fmt.Errorf("%w: Attempt drifted from current Agent execution profile preference", ErrInvalidCommand)
			}
			if err := requireProfileAcknowledgement(ctx, tx, preference.Profile(), preference.ActorID()); err != nil {
				return err
			}
		}
		next, err := run.Transition(domain.CommandStartAttempt, now)
		if err != nil {
			return err
		}
		nextAttempt, err := attempt.Transition(domain.AttemptEventStartAttempt)
		if err != nil {
			return err
		}
		if err := tx.SaveRunCAS(ctx, request.ExpectedVersion, next); err != nil {
			return err
		}
		if err := tx.SaveAttemptCAS(ctx, attempt.State(), nextAttempt); err != nil {
			return err
		}
		body := responseBody(s.attemptEvent("run.started", next, nextAttempt))
		if _, err := tx.AppendRunEvent(ctx, request.RunID, storecontract.EventDraft{ID: s.deps.IDs.EventID(), Type: "run.started", Source: "app", OccurredAt: now, RecordedAt: now, NormalizedJSON: body}); err != nil {
			return err
		}
		securityMode := charter.SandboxMode()
		if attempt.AgentExecutionProfileBinding().Bound() {
			securityMode = sandboxModeForAgentExecutionProfile(attempt.AgentExecutionProfileBinding())
		}
		intentFingerprint := sha256.Sum256([]byte("security:" + charter.AdapterID() + ":" + securityMode + ":" + executionRoot))
		sessionID := s.deps.IDs.RuntimeSessionID()
		launch := execution.LaunchToken{Value: sessionID.String()}
		var predecessorSession *domain.RuntimeSessionID
		var resumedExternalReference string
		if request.Mode == StartResumeRecordedSession {
			predecessorAttempt, ok := attempt.Predecessor()
			if !ok {
				return fmt.Errorf("%w: resume requires predecessor attempt", ErrInvalidCommand)
			}
			previousAttempt, err := tx.GetAttempt(ctx, predecessorAttempt)
			if err != nil {
				return err
			}
			previous, err := tx.GetRuntimeSessionForAttempt(ctx, predecessorAttempt)
			if err != nil {
				return err
			}
			if attempt.AgentExecutionProfileBinding() != previousAttempt.AgentExecutionProfileBinding() {
				return fmt.Errorf("%w: resumed Attempt changed the predecessor Agent execution profile binding", ErrInvalidCommand)
			}
			id := previous.ID
			predecessorSession = &id
			resumedExternalReference = previous.ExternalReference
		}
		session := storecontract.RuntimeSession{ID: sessionID, AttemptID: attempt.ID(), PredecessorID: predecessorSession, AdapterID: attempt.AdapterID(), RuntimeKind: "process", ExternalReference: resumedExternalReference, LaunchToken: launch.Value, WorkingRoot: executionRoot, SecurityFingerprint: intentFingerprint, State: "starting", CreatedAt: now, UpdatedAt: now}
		if err := tx.InsertRuntimeSession(ctx, session); err != nil {
			return err
		}
		result = StartAttemptResult{Run: next, Attempt: nextAttempt, Session: &RuntimeSessionResult{ID: session.ID}}
		response, err = startResponse(result)
		if err != nil {
			return err
		}
		if err := tx.SaveCommand(ctx, key, response); err != nil {
			return err
		}
		return nil
	})
	if err != nil || result.Replayed {
		return result, err
	}
	launch := execution.LaunchToken{Value: result.Session.ID.String()}
	sink := s.bindRuntimeSink(result.Session.ID, launch)
	charter, err := s.deps.Store.Reader().GetCharter(ctx, result.Run.CharterID())
	if err != nil {
		return result, err
	}
	snapshot, err := s.deps.Store.Reader().GetSnapshot(ctx, result.Attempt.ContextSnapshotID())
	if err != nil {
		return result, err
	}
	adapter, err := s.deps.Agents.Get(charter.AdapterID())
	if err != nil {
		return s.recordStartOutcome(ctx, result, key, execution.StartOutcome{Kind: execution.StartProvenNoChild, LaunchToken: launch, Diagnostic: err.Error()}, "", execution.RuntimeFingerprint{})
	}
	var fingerprint execution.RuntimeFingerprint
	var invocation execution.Invocation
	var executionContract []byte
	currentSession, err := s.deps.Store.Reader().GetRuntimeSession(ctx, result.Session.ID)
	if err != nil {
		return result, err
	}
	if request.Mode == StartFresh && result.Attempt.AgentExecutionProfileBinding().Bound() {
		preparer, ok := adapter.(execution.BoundStartPreparer)
		if !ok {
			return s.recordStartOutcome(ctx, result, key, execution.StartOutcome{Kind: execution.StartProvenNoChild, LaunchToken: launch, Diagnostic: "profile-bound adapter does not support atomic start preparation"}, "", execution.RuntimeFingerprint{})
		}
		if adapter.ID() == "pi" {
			executionContract, err = s.registeredExecutionContract(ctx, result.Run, result.Attempt)
			if err != nil {
				return s.recordStartOutcome(ctx, result, key, execution.StartOutcome{Kind: execution.StartProvenNoChild, LaunchToken: launch, Diagnostic: "registered Spec Coding execution authority unavailable"}, "", execution.RuntimeFingerprint{})
			}
		}
		preparation, prepareErr := preparer.PrepareBoundStart(ctx, execution.StartRequest{
			Run: result.Run, Attempt: result.Attempt, SnapshotDocument: snapshot.CanonicalJSON(),
			ExecutionContractDocument: executionContract, WorkspaceRoot: executionRoot, LaunchToken: launch,
		})
		if prepareErr != nil {
			return s.recordStartOutcome(ctx, result, key, execution.StartOutcome{Kind: execution.StartProvenNoChild, LaunchToken: launch, Diagnostic: prepareErr.Error()}, "", execution.RuntimeFingerprint{})
		}
		if !preparation.Valid() {
			return s.recordStartOutcome(ctx, result, key, execution.StartOutcome{Kind: execution.StartProvenNoChild, LaunchToken: launch, Diagnostic: "adapter returned invalid atomic start preparation"}, "", execution.RuntimeFingerprint{})
		}
		invocation, fingerprint = preparation.Invocation, preparation.RuntimeFingerprint
	} else {
		if contractFingerprinter, ok := adapter.(execution.ContractFingerprinter); ok {
			executionContract, err = s.registeredExecutionContract(ctx, result.Run, result.Attempt)
			if err == nil {
				fingerprint, err = contractFingerprinter.FingerprintForContract(ctx, result.Attempt.AgentExecutionProfileBinding(), executionContract)
			}
		} else if fingerprinter, ok := adapter.(execution.BindingFingerprinter); ok {
			fingerprint, err = fingerprinter.FingerprintForBinding(ctx, result.Attempt.AgentExecutionProfileBinding())
		} else {
			fingerprint, err = adapter.Fingerprint(ctx)
		}
		if err != nil {
			return s.recordStartOutcome(ctx, result, key, execution.StartOutcome{Kind: execution.StartProvenNoChild, LaunchToken: launch, Diagnostic: err.Error()}, "", execution.RuntimeFingerprint{})
		}
		if !fingerprint.Valid() {
			return s.recordStartOutcome(ctx, result, key, execution.StartOutcome{Kind: execution.StartProvenNoChild, LaunchToken: launch, Diagnostic: "adapter returned invalid runtime fingerprint"}, "", execution.RuntimeFingerprint{})
		}
	}
	if request.Mode == StartResumeRecordedSession {
		if adapter.Capabilities().ResumeMode != execution.ResumeExplicitSession || currentSession.PredecessorID == nil {
			return s.recordStartOutcome(ctx, result, key, execution.StartOutcome{Kind: execution.StartProvenNoChild, LaunchToken: launch, Diagnostic: "adapter does not support explicit session resume"}, "", fingerprint)
		}
		previous, loadErr := s.deps.Store.Reader().GetRuntimeSession(ctx, *currentSession.PredecessorID)
		if loadErr != nil {
			return result, loadErr
		}
		if previous.ExternalReference == "" || result.Attempt.ExternalSession() == "" || result.Attempt.ExternalSession() != previous.ExternalReference || previous.RuntimeFingerprint != fingerprint.Digest || previous.RuntimeVersion != fingerprint.Version || previous.WorkingRoot != currentSession.WorkingRoot || previous.SecurityFingerprint != currentSession.SecurityFingerprint {
			return s.recordStartOutcome(ctx, result, key, execution.StartOutcome{Kind: execution.StartProvenNoChild, LaunchToken: launch, Diagnostic: "recorded session binding does not match frozen runtime"}, "", fingerprint)
		}
		ownerAttemptID := previous.AttemptID
		if adapter.ID() == "pi" {
			var chainErr error
			ownerAttemptID, chainErr = sessionOwnerAttempt(ctx, s.deps.Store.Reader(), previous, result.Attempt)
			if chainErr != nil {
				return s.recordStartOutcome(ctx, result, key, execution.StartOutcome{Kind: execution.StartProvenNoChild, LaunchToken: launch, Diagnostic: chainErr.Error()}, "", fingerprint)
			}
		}
		invocation, err = adapter.PrepareResume(ctx, execution.ResumeRequest{ExecutionContractDocument: executionContract, Run: result.Run, Attempt: result.Attempt, Binding: execution.ResumeBinding{ExternalSession: previous.ExternalReference, SessionOwnerAttemptID: ownerAttemptID, RuntimeFingerprint: execution.RuntimeFingerprint{Digest: previous.RuntimeFingerprint, Version: previous.RuntimeVersion}, WorkingRoot: previous.WorkingRoot, SecurityFingerprint: previous.SecurityFingerprint}, SecurityFingerprint: currentSession.SecurityFingerprint, DeltaInstruction: []byte(result.Attempt.ContextDelta()), LaunchToken: launch})
	} else if !result.Attempt.AgentExecutionProfileBinding().Bound() {
		if adapter.ID() == "pi" {
			executionContract, err = s.registeredExecutionContract(ctx, result.Run, result.Attempt)
			if err != nil {
				return s.recordStartOutcome(ctx, result, key, execution.StartOutcome{Kind: execution.StartProvenNoChild, LaunchToken: launch, Diagnostic: "registered Spec Coding execution authority unavailable"}, "", fingerprint)
			}
		}
		invocation, err = adapter.PrepareStart(ctx, execution.StartRequest{Run: result.Run, Attempt: result.Attempt, SnapshotDocument: snapshot.CanonicalJSON(), ExecutionContractDocument: executionContract, WorkspaceRoot: executionRoot, LaunchToken: launch})
	}
	if err != nil {
		return s.recordStartOutcome(ctx, result, key, execution.StartOutcome{Kind: execution.StartProvenNoChild, LaunchToken: launch, Diagnostic: err.Error()}, "", fingerprint)
	}
	expectedTarget, err := executionTargetForAttempt(result.Attempt)
	if err != nil || invocation.LaunchToken() != launch || invocation.Target() != expectedTarget {
		return s.recordStartOutcome(ctx, result, key, execution.StartOutcome{Kind: execution.StartProvenNoChild, LaunchToken: launch, Diagnostic: "adapter returned mismatched launch token or execution target"}, "", fingerprint)
	}
	if err := ctx.Err(); err != nil {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), domain.RepositoryMetadataTimeout)
		defer cancel()
		return s.recordStartOutcome(cleanupCtx, result, key, execution.StartOutcome{Kind: execution.StartProvenNoChild, LaunchToken: launch, Diagnostic: "start cancelled before provider launch"}, "", fingerprint)
	}
	outcome := s.deps.Supervisor.Start(ctx, invocation, sink)
	if !validStartOutcome(outcome, launch) {
		outcome = execution.StartOutcome{Kind: execution.StartReconciliationRequired, LaunchToken: launch, Diagnostic: "malformed supervisor start outcome"}
	}
	if (outcome.Kind == execution.Started || outcome.Kind == execution.StartReconciliationRequired) && outcome.Identity.Valid() {
		if err := s.recordHandshake(ctx, result.Session.ID, outcome.Identity, fingerprint, executionRoot, outcome.Diagnostic); err != nil {
			return result, err
		}
	}
	return s.recordStartOutcome(ctx, result, key, outcome, executionRoot, fingerprint)
}

const maxRuntimeSessionPredecessors = 64

func sessionOwnerAttempt(ctx context.Context, reader storecontract.Reader, newest storecontract.RuntimeSession, successor domain.Attempt) (domain.AttemptID, error) {
	predecessorAttemptID, hasPredecessor := successor.Predecessor()
	if newest.ExternalReference == "" || !hasPredecessor || newest.AttemptID != predecessorAttemptID {
		return domain.AttemptID{}, errors.New("recorded session predecessor chain is inconsistent")
	}
	reference := newest.ExternalReference
	current := newest
	seen := make(map[domain.RuntimeSessionID]struct{}, maxRuntimeSessionPredecessors)
	for depth := 0; depth < maxRuntimeSessionPredecessors; depth++ {
		if _, duplicate := seen[current.ID]; duplicate {
			return domain.AttemptID{}, errors.New("recorded session predecessor chain is cyclic")
		}
		seen[current.ID] = struct{}{}
		attempt, err := reader.GetAttempt(ctx, current.AttemptID)
		if err != nil || attempt.RunID() != successor.RunID() || attempt.AdapterID() != successor.AdapterID() ||
			attempt.AgentExecutionProfileBinding() != successor.AgentExecutionProfileBinding() {
			return domain.AttemptID{}, errors.New("recorded session predecessor chain is inconsistent")
		}
		if current.PredecessorID == nil {
			if attempt.ExternalSession() != "" && attempt.ExternalSession() != reference {
				return domain.AttemptID{}, errors.New("recorded session owner reference is inconsistent")
			}
			return current.AttemptID, nil
		}
		if attempt.ExternalSession() != reference {
			return domain.AttemptID{}, errors.New("recorded resumed session reference is inconsistent")
		}
		previous, err := reader.GetRuntimeSession(ctx, *current.PredecessorID)
		if err != nil {
			return domain.AttemptID{}, errors.New("recorded session predecessor chain is missing")
		}
		predecessorAttempt, ok := attempt.Predecessor()
		if !ok || previous.AttemptID != predecessorAttempt {
			return domain.AttemptID{}, errors.New("recorded session predecessor chain is inconsistent")
		}
		if previous.ExternalReference != reference {
			return current.AttemptID, nil
		}
		if previous.WorkingRoot != newest.WorkingRoot || previous.SecurityFingerprint != newest.SecurityFingerprint ||
			previous.RuntimeFingerprint != newest.RuntimeFingerprint || previous.RuntimeVersion != newest.RuntimeVersion {
			return domain.AttemptID{}, errors.New("recorded session predecessor runtime binding drifted")
		}
		current = previous
	}
	return domain.AttemptID{}, errors.New("recorded session predecessor chain exceeds limit")
}

func (s *Service) registeredExecutionContract(ctx context.Context, run domain.AgentRun, attempt domain.Attempt) ([]byte, error) {
	binding, err := s.deps.Store.Reader().GetSpecCodingBinding(ctx, run.TaskID())
	if err != nil || binding.Status != storecontract.SpecCodingRegistered || binding.TaskID != run.TaskID() ||
		sha256.Sum256(binding.ActiveContractJSON) != binding.ActiveContractDigest {
		return nil, fmt.Errorf("registered Spec Coding execution authority unavailable")
	}
	if err := validateSnapshotLineage(ctx, s.deps.Store.Reader(), attempt, binding); err != nil {
		return nil, err
	}
	contract, err := speccoding.DecodeCoreContract(binding.ActiveContractJSON)
	if err != nil || contract.DigestHex() != fmt.Sprintf("%x", binding.ActiveContractDigest) {
		return nil, fmt.Errorf("registered Spec Coding execution authority invalid")
	}
	document := contract.Document()
	if !registeredExecutionSchema(document.SchemaVersion) || document.Task.ID != run.TaskID().String() ||
		document.Execution.Input.ContextSnapshotID != binding.SnapshotID.String() || document.Execution.Input.ContextSnapshotDigest != fmt.Sprintf("%x", binding.SnapshotDigest) {
		return nil, fmt.Errorf("registered Spec Coding execution authority drifted")
	}
	return append([]byte(nil), binding.ActiveContractJSON...), nil
}

func executionTargetForAttempt(attempt domain.Attempt) (execution.ExecutionTarget, error) {
	provider := attempt.AdapterID()
	binding := attempt.AgentExecutionProfileBinding()
	if binding.Bound() {
		provider = binding.ExecutionProvider()
	}
	return execution.NewExecutionTarget(attempt.AdapterID(), provider)
}

func registeredExecutionSchema(schema string) bool {
	return schema == speccoding.CoreContractSchemaVersionV5 || schema == speccoding.CoreContractSchemaVersionV6 ||
		schema == speccoding.CoreContractSchemaVersionV7 || schema == speccoding.CoreContractSchemaVersionV8 ||
		schema == speccoding.CoreContractSchemaVersionV9 || schema == speccoding.CoreContractSchemaVersionV10 ||
		schema == speccoding.CoreContractSchemaVersionV11 || schema == speccoding.CoreContractSchemaVersionV12
}

func (s *Service) recordHandshake(ctx context.Context, sessionID domain.RuntimeSessionID, identity execution.ProcessIdentity, fingerprint execution.RuntimeFingerprint, workingRoot, diagnostic string) error {
	now := s.deps.Clock.Now()
	return s.deps.Store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		session, err := tx.GetRuntimeSession(ctx, sessionID)
		if err != nil {
			return err
		}
		version := session.Version
		session.Version++
		session.UpdatedAt = now
		session.ProcessIdentity = identity.Value
		session.RuntimeFingerprint = fingerprint.Digest
		session.RuntimeVersion = fingerprint.Version
		if workingRoot != "" {
			session.WorkingRoot = workingRoot
		}
		session.Diagnostic = diagnostic
		return tx.SaveRuntimeSessionCAS(ctx, version, session)
	})
}

func (s *Service) recordStartOutcome(ctx context.Context, result StartAttemptResult, key storecontract.CommandKey, outcome execution.StartOutcome, workingRoot string, fingerprint execution.RuntimeFingerprint) (StartAttemptResult, error) {
	now := s.deps.Clock.Now()
	err := s.deps.Store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		run, err := tx.GetRun(ctx, result.Run.ID())
		if err != nil {
			return err
		}
		attempt, err := tx.GetCurrentAttempt(ctx, result.Run.ID())
		if err != nil {
			return err
		}
		var command domain.CommandKind
		var event string
		var nextAttempt = attempt
		switch outcome.Kind {
		case execution.Started:
			command = domain.CommandAttemptStarted
			event = "attempt.started"
			nextAttempt, err = attempt.Transition(domain.AttemptEventAttemptStarted)
		case execution.StartProvenNoChild:
			command = domain.CommandStartFailed
			event = "attempt.start_failed"
			nextAttempt, err = attempt.Transition(domain.AttemptEventStartFailed)
		case execution.StartReconciliationRequired:
			command = domain.CommandStartReconciliationRequired
			event = "attempt.reconciliation_required"
		}
		if err != nil {
			return err
		}
		next, err := run.Transition(command, now)
		if err != nil {
			return err
		}
		if err := tx.SaveRunCAS(ctx, run.Version(), next); err != nil {
			return err
		}
		if nextAttempt.State() != attempt.State() {
			if err := tx.SaveAttemptCAS(ctx, attempt.State(), nextAttempt); err != nil {
				return err
			}
		}
		if result.Session == nil {
			return storecontract.ErrNotFound
		}
		session, err := tx.GetRuntimeSession(ctx, result.Session.ID)
		if err != nil {
			return err
		}
		expectedSessionVersion := session.Version
		if session.LaunchToken != outcome.LaunchToken.Value {
			return storecontract.ErrVersionConflict
		}
		session.Version++
		session.UpdatedAt = now
		if workingRoot != "" {
			session.WorkingRoot = workingRoot
		}
		session.Diagnostic = outcome.Diagnostic
		switch outcome.Kind {
		case execution.Started:
			session.State = "running"
			if session.ProcessIdentity == "" {
				session.ProcessIdentity = outcome.Identity.Value
				session.RuntimeFingerprint = fingerprint.Digest
				session.RuntimeVersion = fingerprint.Version
			}
			session.StartedAt = now
			result.Session.Identity = outcome.Identity
		case execution.StartReconciliationRequired:
			session.State = "lost"
			if session.ProcessIdentity == "" {
				session.ProcessIdentity = outcome.Identity.Value
				session.RuntimeFingerprint = fingerprint.Digest
				session.RuntimeVersion = fingerprint.Version
			}
			session.StartedAt = now
			result.Session.Identity = outcome.Identity
		case execution.StartProvenNoChild:
			session.State = "failed"
			session.TerminalAt = now
			session.FinalizedAt = now
		}
		if err := tx.SaveRuntimeSessionCAS(ctx, expectedSessionVersion, session); err != nil {
			return err
		}
		body := responseBody(s.attemptEvent(event, next, nextAttempt))
		persistedEvent, err := tx.AppendRunEvent(ctx, next.ID(), storecontract.EventDraft{ID: s.deps.IDs.EventID(), Type: event, Source: "app", OccurredAt: now, RecordedAt: now, NormalizedJSON: body})
		if err != nil {
			return err
		}
		if outcome.Kind == execution.StartProvenNoChild {
			attemptID := nextAttempt.ID()
			sourceEventID := persistedEvent.ID()
			if err := tx.InsertObservation(ctx, storecontract.Observation{
				ID: s.deps.IDs.ObservationID(), RunID: next.ID(), AttemptID: &attemptID,
				Kind: "terminal_unknown", Body: "The managed Agent did not start; no child process was created.",
				SourceEventID: &sourceEventID, Role: "unknown", CreatedAt: now,
			}); err != nil {
				return err
			}
		}
		result.Run = next
		result.Attempt = nextAttempt
		response, err := startResponse(result)
		if err != nil {
			return err
		}
		return tx.UpdateCommand(ctx, key, response)
	})
	if err == nil && outcome.Kind == execution.StartProvenNoChild && result.Session != nil {
		s.cleanupRuntimeSignals(result.Session.ID)
	}
	return result, err
}
