package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/speccoding"
	storecontract "github.com/Yangyang96/chora/internal/store"
	"github.com/Yangyang96/chora/internal/verifier"
)

type verificationRunWire struct {
	ID, RunID, AgentAttemptID string
	Bindings                  domain.VerificationBindings
	State                     domain.VerificationRunState
	CreatedAt                 time.Time
	UpdatedAt                 time.Time
}

type verificationAttemptWire struct {
	ID, VerificationRunID, Predecessor string
	Sequence                           int
	Bindings                           domain.VerificationBindings
	State                              domain.VerificationAttemptState
	CreatedAt                          time.Time
}

type startVerificationWire struct {
	Run             runWire
	VerificationRun verificationRunWire
	Attempt         verificationAttemptWire
}

func wireVerificationRun(run domain.VerificationRun) verificationRunWire {
	return verificationRunWire{ID: run.ID().String(), RunID: run.RunID().String(), AgentAttemptID: run.AgentAttemptID().String(), Bindings: run.Bindings(), State: run.State(), CreatedAt: run.CreatedAt(), UpdatedAt: run.UpdatedAt()}
}

func restoreVerificationRun(wire verificationRunWire) (domain.VerificationRun, error) {
	id, err := domain.ParseVerificationRunID(wire.ID)
	if err != nil {
		return domain.VerificationRun{}, err
	}
	runID, err := domain.ParseRunID(wire.RunID)
	if err != nil {
		return domain.VerificationRun{}, err
	}
	agentAttemptID, err := domain.ParseAttemptID(wire.AgentAttemptID)
	if err != nil {
		return domain.VerificationRun{}, err
	}
	return domain.RestoreVerificationRun(domain.VerificationRunRecord{ID: id, RunID: runID, AgentAttemptID: agentAttemptID, Bindings: wire.Bindings, State: wire.State, CreatedAt: wire.CreatedAt, UpdatedAt: wire.UpdatedAt})
}

func wireVerificationAttempt(attempt domain.VerificationAttempt) verificationAttemptWire {
	predecessor := ""
	if id, ok := attempt.Predecessor(); ok {
		predecessor = id.String()
	}
	return verificationAttemptWire{ID: attempt.ID().String(), VerificationRunID: attempt.VerificationRunID().String(), Predecessor: predecessor,
		Sequence: attempt.Sequence(), Bindings: attempt.Bindings(), State: attempt.State(), CreatedAt: attempt.CreatedAt()}
}

func restoreVerificationAttempt(wire verificationAttemptWire) (domain.VerificationAttempt, error) {
	id, err := domain.ParseVerificationAttemptID(wire.ID)
	if err != nil {
		return domain.VerificationAttempt{}, err
	}
	runID, err := domain.ParseVerificationRunID(wire.VerificationRunID)
	if err != nil {
		return domain.VerificationAttempt{}, err
	}
	var predecessor *domain.VerificationAttemptID
	if wire.Predecessor != "" {
		parsed, err := domain.ParseVerificationAttemptID(wire.Predecessor)
		if err != nil {
			return domain.VerificationAttempt{}, err
		}
		predecessor = &parsed
	}
	return domain.RestoreVerificationAttempt(domain.VerificationAttemptRecord{ID: id, VerificationRunID: runID, Sequence: wire.Sequence,
		Predecessor: predecessor, Bindings: wire.Bindings, State: wire.State, CreatedAt: wire.CreatedAt})
}

func verificationResponse(result StartVerificationResult) (storecontract.Response, error) {
	return jsonResponse(startVerificationWire{Run: wireRun(result.Run), VerificationRun: wireVerificationRun(result.VerificationRun), Attempt: wireVerificationAttempt(result.Attempt)})
}

func replayVerification(response storecontract.Response) (StartVerificationResult, error) {
	var wire startVerificationWire
	if err := decodeResponse(response, &wire); err != nil {
		return StartVerificationResult{}, err
	}
	run, err := restoreRun(wire.Run)
	if err != nil {
		return StartVerificationResult{}, err
	}
	verificationRun, err := restoreVerificationRun(wire.VerificationRun)
	if err != nil {
		return StartVerificationResult{}, err
	}
	attempt, err := restoreVerificationAttempt(wire.Attempt)
	if err != nil {
		return StartVerificationResult{}, err
	}
	return StartVerificationResult{Run: run, VerificationRun: verificationRun, Attempt: attempt, Replayed: true}, nil
}

func (s *Service) StartVerification(ctx context.Context, request StartVerificationRequest) (StartVerificationResult, error) {
	return s.startVerification(ctx, request, false)
}

func (s *Service) RetryVerification(ctx context.Context, request StartVerificationRequest) (StartVerificationResult, error) {
	return s.startVerification(ctx, request, true)
}

// autoStartVerification starts independent verification without a human command
// once the Agent has finished and produced a reviewable result. It is fail-closed
// and idempotent: when verification is not applicable (diagnostic Fake tasks, an
// unavailable Verifier, or a version conflict) the Run stays in
// awaiting_verification for a later explicit action. It must only be invoked
// after the terminal transaction has committed.
func (s *Service) autoStartVerification(run domain.AgentRun) {
	if s == nil || s.deps.Store == nil || s.deps.Verifier == nil || run.State() != domain.RunStateAwaitingVerification {
		return
	}
	ctx := context.Background()
	binding, err := s.deps.Store.Reader().GetSpecCodingBinding(ctx, run.TaskID())
	if err != nil || binding.Status != storecontract.SpecCodingRegistered {
		return // diagnostic Fake path: no independent verification
	}
	meta := CommandMeta{
		ActorID:        "system",
		SessionID:      "system",
		IdempotencyKey: fmt.Sprintf("auto-start-verification:%s:%d", run.ID().String(), run.Version()),
	}
	_, _ = s.StartVerification(ctx, StartVerificationRequest{CommandMeta: meta, RunID: run.ID(), ExpectedVersion: run.Version()})
}

func (s *Service) startVerification(ctx context.Context, request StartVerificationRequest, retry bool) (StartVerificationResult, error) {
	if s == nil || s.deps.Store == nil || s.deps.Verifier == nil {
		return StartVerificationResult{}, fmt.Errorf("%w: verification dependencies unavailable", ErrInvalidCommand)
	}
	authority := s.deps.Verifier.Authority()
	if !authority.valid() {
		return StartVerificationResult{}, fmt.Errorf("%w: verification authority unavailable", ErrInvalidCommand)
	}
	action := "start_verification"
	if retry {
		action = "retry_verification"
	}
	if err := s.authorize(ctx, request.CommandMeta, action, request.RunID.String(), request.ExpectedVersion); err != nil {
		return StartVerificationResult{}, err
	}
	unlock := s.runLock(request.RunID)
	defer unlock()
	now := s.deps.Clock.Now()
	var result StartVerificationResult
	var executionRequest VerificationExecutionRequest
	err := s.deps.Store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		run, err := tx.GetRun(ctx, request.RunID)
		if err != nil {
			return err
		}
		key, err := s.commandKey(request.CommandMeta, action, run.ID().String(), request.ExpectedVersion, authority, now)
		if err != nil {
			return err
		}
		response, replay, err := tx.LookupCommand(ctx, key)
		if err != nil {
			return err
		}
		if replay {
			result, err = replayVerification(response)
			return err
		}
		if run.Version() != request.ExpectedVersion {
			return storecontract.ErrVersionConflict
		}
		agentAttempt, err := tx.GetCurrentAttempt(ctx, run.ID())
		if err != nil || agentAttempt.State() != domain.AttemptStateOutputSubmitted {
			return fmt.Errorf("%w: Agent output is not verification-ready", ErrInvalidCommand)
		}
		if _, err := tx.GetAgentReportForAttempt(ctx, agentAttempt.ID()); err != nil {
			return fmt.Errorf("%w: immutable AgentReport unavailable: %v", ErrInvalidCommand, err)
		}
		contract, patchLocator, bindings, err := verificationInputs(ctx, tx, run, agentAttempt, authority)
		if err != nil {
			return err
		}
		charter, err := tx.GetCharter(ctx, run.CharterID())
		if err != nil {
			return err
		}

		var verificationRun domain.VerificationRun
		var verificationAttempt domain.VerificationAttempt
		var runCommand domain.CommandKind
		var verificationEvent domain.VerificationRunEvent
		if !retry {
			if run.State() != domain.RunStateAwaitingVerification {
				return fmt.Errorf("%w: Run is not awaiting verification", ErrInvalidCommand)
			}
			if _, err := tx.GetVerificationRunForAgentAttempt(ctx, agentAttempt.ID()); !errors.Is(err, storecontract.ErrNotFound) {
				if err == nil {
					return fmt.Errorf("%w: verification already exists", ErrInvalidCommand)
				}
				return err
			}
			verificationRun, err = domain.NewVerificationRun(s.deps.IDs.VerificationRunID(), run.ID(), agentAttempt.ID(), bindings, now)
			if err != nil {
				return err
			}
			verificationAttempt, err = domain.NewVerificationAttempt(domain.VerificationAttemptParams{ID: s.deps.IDs.VerificationAttemptID(), VerificationRunID: verificationRun.ID(), Sequence: 1, Bindings: bindings, CreatedAt: now})
			if err != nil {
				return err
			}
			if err := tx.InsertVerificationRun(ctx, verificationRun); err != nil {
				return err
			}
			if err := tx.InsertVerificationAttempt(ctx, verificationAttempt, now); err != nil {
				return err
			}
			runCommand, verificationEvent = domain.CommandStartVerification, domain.VerificationRunStart
		} else {
			verificationRun, err = tx.GetVerificationRunForAgentAttempt(ctx, agentAttempt.ID())
			if err != nil || verificationRun.Bindings() != bindings {
				return fmt.Errorf("%w: frozen verification authority changed", ErrInvalidCommand)
			}
			previous, err := tx.GetCurrentVerificationAttempt(ctx, verificationRun.ID())
			if err != nil || previous.Attempt.Bindings() != bindings || previous.Attempt.State() != domain.VerificationAttemptCancelled && previous.Attempt.State() != domain.VerificationAttemptRecoveryRequired {
				return fmt.Errorf("%w: no retryable verification Attempt", ErrInvalidCommand)
			}
			if run.State() != domain.RunStateAwaitingVerification && run.State() != domain.RunStateVerificationRecoveryRequired {
				return fmt.Errorf("%w: Run is not retryable by Verifier", ErrInvalidCommand)
			}
			predecessor := previous.Attempt.ID()
			verificationAttempt, err = domain.NewVerificationAttempt(domain.VerificationAttemptParams{ID: s.deps.IDs.VerificationAttemptID(), VerificationRunID: verificationRun.ID(),
				Sequence: previous.Attempt.Sequence() + 1, Predecessor: &predecessor, Bindings: bindings, CreatedAt: now})
			if err != nil {
				return err
			}
			if err := tx.InsertVerificationAttempt(ctx, verificationAttempt, now); err != nil {
				return err
			}
			runCommand, verificationEvent = domain.CommandRetryVerification, domain.VerificationRunRetry
		}

		nextRun, err := run.Transition(runCommand, now)
		if err != nil {
			return err
		}
		nextVerificationRun, err := verificationRun.Transition(verificationEvent, now)
		if err != nil {
			return err
		}
		nextAttempt, err := verificationAttempt.Transition(domain.VerificationAttemptStart)
		if err != nil {
			return err
		}
		if err := tx.SaveRunCAS(ctx, run.Version(), nextRun); err != nil {
			return err
		}
		if err := tx.SaveVerificationRunCAS(ctx, verificationRun.State(), nextVerificationRun); err != nil {
			return err
		}
		if err := tx.SaveVerificationAttemptCAS(ctx, verificationAttempt.State(), storecontract.VerificationAttemptRecord{Attempt: nextAttempt, UpdatedAt: now}); err != nil {
			return err
		}
		eventType := "verification.started"
		if retry {
			eventType = "verification.retried"
		}
		if _, err := tx.AppendRunEvent(ctx, run.ID(), storecontract.EventDraft{ID: s.deps.IDs.EventID(), Type: eventType, Source: "app", OccurredAt: now, RecordedAt: now,
			NormalizedJSON: responseBody(map[string]any{"type": eventType, "verification_run_id": nextVerificationRun.ID().String(), "verification_attempt_id": nextAttempt.ID().String(), "sequence": nextAttempt.Sequence()})}); err != nil {
			return err
		}
		result = StartVerificationResult{Run: nextRun, VerificationRun: nextVerificationRun, Attempt: nextAttempt}
		response, err = verificationResponse(result)
		if err != nil {
			return err
		}
		if err := tx.SaveCommand(ctx, key, response); err != nil {
			return err
		}
		executionRequest = VerificationExecutionRequest{RunID: run.ID(), TaskID: run.TaskID(), VerificationRunID: nextVerificationRun.ID(), AttemptID: nextAttempt.ID(),
			Bindings: bindings, Contract: contract, PatchLocator: patchLocator, AgentWorkspace: charter.WorkspaceRoot()}
		return nil
	})
	if err != nil || result.Replayed {
		return result, err
	}
	attemptContext, cancel := context.WithCancel(s.deps.Lifecycle)
	if _, loaded := s.verificationSignals.LoadOrStore(result.Attempt.ID().String(), cancel); loaded {
		cancel()
		_ = s.markVerificationRecovery(context.Background(), result.Run.ID(), result.VerificationRun.ID(), result.Attempt.ID(), "duplicate verification execution identity")
		return StartVerificationResult{}, fmt.Errorf("%w: duplicate verification execution", ErrInvalidCommand)
	}
	go s.executeVerification(attemptContext, executionRequest, cancel)
	return result, nil
}

func verificationInputs(ctx context.Context, reader storecontract.Reader, run domain.AgentRun, attempt domain.Attempt, authority VerificationAuthority) (speccoding.CoreContract, string, domain.VerificationBindings, error) {
	projection, err := reader.GetTerminalProjection(ctx, attempt.ID())
	if err != nil {
		return speccoding.CoreContract{}, "", domain.VerificationBindings{}, err
	}
	var patch *storecontract.Artifact
	for index := range projection.Artifacts {
		artifact := &projection.Artifacts[index]
		if artifact.Kind == "patch" && artifact.Role == "output" {
			if patch != nil {
				return speccoding.CoreContract{}, "", domain.VerificationBindings{}, fmt.Errorf("%w: multiple final Patches", ErrInvalidCommand)
			}
			patch = artifact
		}
	}
	if patch == nil || patch.Digest == nil || strings.TrimSpace(patch.Locator) == "" {
		return speccoding.CoreContract{}, "", domain.VerificationBindings{}, fmt.Errorf("%w: digest-bound final Patch unavailable", ErrInvalidCommand)
	}
	binding, err := reader.GetSpecCodingBinding(ctx, run.TaskID())
	if err != nil || binding.Status != storecontract.SpecCodingRegistered || binding.TaskID != run.TaskID() ||
		sha256.Sum256(binding.ActiveContractJSON) != binding.ActiveContractDigest {
		return speccoding.CoreContract{}, "", domain.VerificationBindings{}, fmt.Errorf("%w: registered contract binding unavailable", ErrInvalidCommand)
	}
	if err := validateSnapshotLineage(ctx, reader, attempt, binding); err != nil {
		return speccoding.CoreContract{}, "", domain.VerificationBindings{}, fmt.Errorf("%w: registered contract binding unavailable", ErrInvalidCommand)
	}
	contract, err := speccoding.DecodeCoreContract(binding.ActiveContractJSON)
	if err != nil || contract.DigestHex() != fmt.Sprintf("%x", binding.ActiveContractDigest) {
		return speccoding.CoreContract{}, "", domain.VerificationBindings{}, fmt.Errorf("%w: active contract identity changed", ErrInvalidCommand)
	}
	document := contract.Document()
	if (document.SchemaVersion != speccoding.CoreContractSchemaVersionV8 && document.SchemaVersion != speccoding.CoreContractSchemaVersionV9 && document.SchemaVersion != speccoding.CoreContractSchemaVersionV10) || document.Task.ID != run.TaskID().String() ||
		document.Execution.Input.ContextSnapshotID != binding.SnapshotID.String() || document.Execution.Input.ContextSnapshotDigest != fmt.Sprintf("%x", binding.SnapshotDigest) {
		return speccoding.CoreContract{}, "", domain.VerificationBindings{}, fmt.Errorf("%w: active contract meaning changed", ErrInvalidCommand)
	}
	bindings := domain.VerificationBindings{BaselineDigest: authority.BaselineDigest, PatchDigest: *patch.Digest, ContextSnapshotDigest: attempt.ContextDigest(),
		AcceptanceContractDigest: binding.ActiveContractDigest, VerifierPolicyVersion: authority.VerifierPolicyVersion, VerifierPolicyDigest: authority.VerifierPolicyDigest}
	return contract, patch.Locator, bindings, nil
}

func (s *Service) CancelVerification(ctx context.Context, request CancelVerificationRequest) (CancelVerificationResult, error) {
	if s == nil || s.deps.Store == nil {
		return CancelVerificationResult{}, fmt.Errorf("%w: verification dependencies unavailable", ErrInvalidCommand)
	}
	if err := s.authorize(ctx, request.CommandMeta, "cancel_verification", request.RunID.String(), request.ExpectedVersion); err != nil {
		return CancelVerificationResult{}, err
	}
	unlock := s.runLock(request.RunID)
	defer unlock()
	now := s.deps.Clock.Now()
	var result CancelVerificationResult
	var cancel context.CancelFunc
	err := s.deps.Store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		run, err := tx.GetRun(ctx, request.RunID)
		if err != nil {
			return err
		}
		key, err := s.commandKey(request.CommandMeta, "cancel_verification", run.ID().String(), request.ExpectedVersion, "user_cancel", now)
		if err != nil {
			return err
		}
		response, replay, err := tx.LookupCommand(ctx, key)
		if err != nil {
			return err
		}
		if replay {
			replayed, err := replayVerification(response)
			if err == nil {
				result = CancelVerificationResult{Run: replayed.Run, VerificationRun: replayed.VerificationRun, Attempt: replayed.Attempt, Replayed: true}
			}
			return err
		}
		if run.Version() != request.ExpectedVersion || run.State() != domain.RunStateVerifying {
			return storecontract.ErrVersionConflict
		}
		verificationRun, err := tx.GetVerificationRunForRun(ctx, run.ID())
		if err != nil || verificationRun.State() != domain.VerificationRunVerifying {
			return fmt.Errorf("%w: verification is not running", ErrInvalidCommand)
		}
		attempt, err := tx.GetCurrentVerificationAttempt(ctx, verificationRun.ID())
		if err != nil || attempt.Attempt.State() != domain.VerificationAttemptRunning {
			return fmt.Errorf("%w: verification Attempt is not running", ErrInvalidCommand)
		}
		value, ok := s.verificationSignals.Load(attempt.Attempt.ID().String())
		if !ok {
			return fmt.Errorf("%w: verification process continuity unavailable", ErrInvalidCommand)
		}
		cancel = value.(context.CancelFunc)
		if err := tx.InsertVerificationCancelRequest(ctx, attempt.Attempt.ID(), now); err != nil {
			return err
		}
		result = CancelVerificationResult{Run: run, VerificationRun: verificationRun, Attempt: attempt.Attempt}
		stored, err := verificationResponse(StartVerificationResult{Run: run, VerificationRun: verificationRun, Attempt: attempt.Attempt})
		if err != nil {
			return err
		}
		return tx.SaveCommand(ctx, key, stored)
	})
	if err != nil {
		return result, err
	}
	if cancel != nil {
		cancel()
	}
	return result, nil
}

func (s *Service) executeVerification(ctx context.Context, request VerificationExecutionRequest, cancel context.CancelFunc) {
	defer cancel()
	defer s.verificationSignals.Delete(request.AttemptID.String())
	evidence, err := s.deps.Verifier.Verify(ctx, request)
	if err != nil {
		_ = s.markVerificationRecovery(context.Background(), request.RunID, request.VerificationRunID, request.AttemptID, "verifier execution error: "+err.Error())
		return
	}
	switch evidence.Classification {
	case verifier.AttemptCompleted:
		if err := s.commitVerificationEvidence(context.Background(), request, evidence); err != nil {
			_ = s.markVerificationRecovery(context.Background(), request.RunID, request.VerificationRunID, request.AttemptID, "verification persistence uncertain: "+err.Error())
		}
	case verifier.AttemptCancelled:
		if err := s.commitVerificationCancellation(context.Background(), request, evidence); err != nil {
			_ = s.markVerificationRecovery(context.Background(), request.RunID, request.VerificationRunID, request.AttemptID, "verification cancellation persistence uncertain: "+err.Error())
		}
	default:
		if err := s.commitVerificationRecoveryEvidence(context.Background(), request, evidence); err != nil {
			_ = s.markVerificationRecovery(context.Background(), request.RunID, request.VerificationRunID, request.AttemptID, "verification recovery evidence persistence uncertain: "+err.Error())
		}
	}
}

type normalizedVerificationEvidence struct {
	commands          []domain.VerificationCommandEvidence
	checks            []domain.AcceptanceCheck
	workspaceIdentity string
}

type verificationEvidenceMode uint8

const (
	verificationEvidenceComplete verificationEvidenceMode = iota
	verificationEvidenceCancel
	verificationEvidenceRecovery
)

func (s *Service) normalizeVerificationEvidence(request VerificationExecutionRequest, evidence verifier.AttemptEvidence, mode verificationEvidenceMode) (normalizedVerificationEvidence, error) {
	if mode != verificationEvidenceRecovery && !evidence.CleanupProven || mode != verificationEvidenceRecovery && len(evidence.Commands) == 0 {
		return normalizedVerificationEvidence{}, fmt.Errorf("cleanup or command evidence incomplete")
	}
	document := request.Contract.Document()
	expectedCriteria := map[string][]string{}
	criterionOrder := make([]string, 0, len(document.Acceptance.Criteria))
	for _, criterion := range document.Acceptance.Criteria {
		criterionOrder = append(criterionOrder, criterion.ID)
		for _, commandID := range criterion.VerificationCommandIDs {
			expectedCriteria[commandID] = append(expectedCriteria[commandID], criterion.ID)
		}
	}
	expectedCommands := make([]speccoding.BoundedCommand, 0, len(expectedCriteria))
	for _, command := range document.Acceptance.VerificationCommands {
		if _, ok := expectedCriteria[command.ID]; ok {
			expectedCommands = append(expectedCommands, command)
		}
	}
	if len(evidence.Commands) > len(expectedCommands) || mode == verificationEvidenceComplete && len(evidence.Commands) != len(expectedCommands) {
		return normalizedVerificationEvidence{}, fmt.Errorf("verification command coverage drift")
	}
	normalized := normalizedVerificationEvidence{}
	evidenceByCriterion := map[string][]domain.VerificationCommandEvidenceID{}
	statusByCriterion := map[string]domain.AcceptanceCheckStatus{}
	for index, command := range evidence.Commands {
		expected := expectedCommands[index]
		if command.CommandID != expected.ID || !slices.Equal(command.Argv, expected.Argv) || !slices.Equal(command.CriterionIDs, expectedCriteria[expected.ID]) || !matchingEvidenceIdentity(command.Identity, request) {
			return normalizedVerificationEvidence{}, fmt.Errorf("verification evidence authority drift")
		}
		if normalized.workspaceIdentity == "" {
			normalized.workspaceIdentity = command.Identity.WorkspaceIdentity
		} else if normalized.workspaceIdentity != command.Identity.WorkspaceIdentity {
			return normalizedVerificationEvidence{}, fmt.Errorf("verification Workspace identity drift")
		}
		classification := domain.VerificationCommandClassification(command.Kind)
		stdout := domain.VerificationStreamLog{FullSHA256: command.Stdout.FullSHA256, TotalBytes: command.Stdout.TotalBytes, RetainedBody: command.Stdout.RetainedBody,
			Truncated: command.Stdout.Truncated, TruncationBoundary: command.Stdout.TruncationBoundary, RedactionPolicyVersion: command.Stdout.RedactionPolicyVersion}
		stderr := domain.VerificationStreamLog{FullSHA256: command.Stderr.FullSHA256, TotalBytes: command.Stderr.TotalBytes, RetainedBody: command.Stderr.RetainedBody,
			Truncated: command.Stderr.Truncated, TruncationBoundary: command.Stderr.TruncationBoundary, RedactionPolicyVersion: command.Stderr.RedactionPolicyVersion}
		criterionIDs := make([]domain.CriterionID, 0, len(command.CriterionIDs))
		for _, value := range command.CriterionIDs {
			criterionID, err := domain.ParseCriterionID(value)
			if err != nil {
				return normalizedVerificationEvidence{}, err
			}
			criterionIDs = append(criterionIDs, criterionID)
		}
		domainEvidence, err := domain.NewVerificationCommandEvidence(domain.VerificationCommandEvidenceParams{ID: s.deps.IDs.VerificationCommandEvidenceID(), VerificationAttemptID: request.AttemptID,
			CommandID: command.CommandID, CriterionIDs: criterionIDs, Argv: command.Argv, StartedAt: command.StartedAt, EndedAt: command.EndedAt,
			Classification: classification, ExitCode: command.ExitCode, Stdout: stdout, Stderr: stderr, VerifierIdentity: command.VerifierIdentity,
			WorkspaceIdentity: command.Identity.WorkspaceIdentity})
		if err != nil {
			return normalizedVerificationEvidence{}, err
		}
		normalized.commands = append(normalized.commands, domainEvidence)
		for _, criterionID := range command.CriterionIDs {
			evidenceByCriterion[criterionID] = append(evidenceByCriterion[criterionID], domainEvidence.ID())
			if mode != verificationEvidenceComplete {
				continue
			}
			status, err := mergeCriterionStatus(statusByCriterion[criterionID], command.Kind, command.ExitCode)
			if err != nil {
				return normalizedVerificationEvidence{}, err
			}
			statusByCriterion[criterionID] = status
		}
	}
	if mode == verificationEvidenceCancel {
		last := evidence.Commands[len(evidence.Commands)-1]
		if last.Kind != verifier.CommandCancelled {
			return normalizedVerificationEvidence{}, fmt.Errorf("cancelled Attempt lacks cancelled command proof")
		}
		return normalized, nil
	}
	if mode == verificationEvidenceRecovery {
		return normalized, nil
	}
	for _, criterionText := range criterionOrder {
		criterionID, err := domain.ParseCriterionID(criterionText)
		if err != nil || len(evidenceByCriterion[criterionText]) == 0 {
			return normalizedVerificationEvidence{}, fmt.Errorf("criterion evidence incomplete")
		}
		check, err := domain.NewAcceptanceCheck(s.deps.IDs.CheckID(), criterionID, statusByCriterion[criterionText], domain.VerificationEvidenceTrusted, evidenceByCriterion[criterionText])
		if err != nil {
			return normalizedVerificationEvidence{}, err
		}
		normalized.checks = append(normalized.checks, check)
	}
	return normalized, nil
}

func matchingEvidenceIdentity(identity verifier.EvidenceIdentity, request VerificationExecutionRequest) bool {
	bindings := request.Bindings
	return identity.RunID == request.RunID.String() && identity.VerificationRunID == request.VerificationRunID.String() && identity.AttemptID == request.AttemptID.String() &&
		identity.TaskID == request.TaskID.String() && validVerificationSHAIdentity(identity.WorkspaceIdentity) && validVerificationSHAIdentity(identity.VerifierImage) &&
		identity.VerifierPolicyVersion == bindings.VerifierPolicyVersion && identity.BaselineDigest == bindings.BaselineDigest && identity.PatchDigest == bindings.PatchDigest &&
		identity.ContextSnapshotDigest == bindings.ContextSnapshotDigest && identity.AcceptanceContractDigest == bindings.AcceptanceContractDigest && identity.VerifierPolicyDigest == bindings.VerifierPolicyDigest
}

func validVerificationSHAIdentity(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != len("sha256:")+64 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}

func mergeCriterionStatus(current domain.AcceptanceCheckStatus, kind verifier.CommandKind, exitCode *int) (domain.AcceptanceCheckStatus, error) {
	next := domain.AcceptanceCheckUnknown
	switch kind {
	case verifier.CommandExited:
		if exitCode == nil {
			return "", fmt.Errorf("exited command lacks code")
		}
		if *exitCode == 0 {
			next = domain.AcceptanceCheckPassed
		} else {
			next = domain.AcceptanceCheckFailed
		}
	case verifier.CommandTimedOut, verifier.CommandUnavailable:
		next = domain.AcceptanceCheckUnknown
	default:
		return "", fmt.Errorf("untrusted completed command classification")
	}
	if current == domain.AcceptanceCheckFailed || next == domain.AcceptanceCheckFailed {
		return domain.AcceptanceCheckFailed, nil
	}
	if current == domain.AcceptanceCheckUnknown || next == domain.AcceptanceCheckUnknown {
		return domain.AcceptanceCheckUnknown, nil
	}
	return domain.AcceptanceCheckPassed, nil
}

func (s *Service) commitVerificationEvidence(ctx context.Context, request VerificationExecutionRequest, evidence verifier.AttemptEvidence) error {
	normalized, err := s.normalizeVerificationEvidence(request, evidence, verificationEvidenceComplete)
	if err != nil {
		return err
	}
	unlock := s.runLock(request.RunID)
	defer unlock()
	now := s.deps.Clock.Now()
	return s.deps.Store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		run, verificationRun, attempt, err := loadRunningVerification(ctx, tx, request)
		if err != nil {
			return err
		}
		cancelRequested, err := tx.VerificationCancelRequested(ctx, attempt.Attempt.ID())
		if err != nil {
			return err
		}
		for _, command := range normalized.commands {
			if err := tx.InsertVerificationCommandEvidence(ctx, command); err != nil {
				return err
			}
		}
		if cancelRequested {
			return s.finishVerificationCancellationTx(ctx, tx, run, verificationRun, attempt, normalized.workspaceIdentity, "user_cancelled_after_verifier_exit", now)
		}
		for _, check := range normalized.checks {
			if err := tx.InsertVerificationAcceptanceCheck(ctx, attempt.Attempt.ID(), check); err != nil {
				return err
			}
		}
		nextAttempt, err := attempt.Attempt.Transition(domain.VerificationAttemptEvidenceCompleted)
		if err != nil {
			return err
		}
		if err := tx.SaveVerificationAttemptCAS(ctx, attempt.Attempt.State(), storecontract.VerificationAttemptRecord{Attempt: nextAttempt, EvidenceComplete: true, CleanupProven: true, WorkspaceIdentity: normalized.workspaceIdentity, Reason: evidence.Reason, UpdatedAt: now}); err != nil {
			return err
		}
		result, err := domain.NewVerificationResult(s.deps.IDs.ResultID(), verificationRun, nextAttempt, normalized.checks, domain.VerificationCompletionProof{EvidenceComplete: true, CleanupProven: true}, now)
		if err != nil {
			return err
		}
		if err := tx.InsertVerificationResult(ctx, result); err != nil {
			return err
		}
		nextVerificationRun, err := verificationRun.Transition(domain.VerificationRunEvidenceCompleted, now)
		if err != nil {
			return err
		}
		runCommand := domain.CommandVerificationPassed
		eventType := "verification.review_ready"
		if result.Outcome() == domain.ResultNeedsRevision {
			runCommand, eventType = domain.CommandVerificationNeedsRevision, "verification.needs_revision"
		}
		nextRun, err := run.Transition(runCommand, now)
		if err != nil {
			return err
		}
		if err := tx.SaveVerificationRunCAS(ctx, verificationRun.State(), nextVerificationRun); err != nil {
			return err
		}
		if err := tx.SaveRunCAS(ctx, run.Version(), nextRun); err != nil {
			return err
		}
		_, err = tx.AppendRunEvent(ctx, run.ID(), storecontract.EventDraft{ID: s.deps.IDs.EventID(), Type: eventType, Source: "verifier", OccurredAt: now, RecordedAt: now,
			NormalizedJSON: responseBody(map[string]any{"type": eventType, "verification_run_id": verificationRun.ID().String(), "verification_attempt_id": nextAttempt.ID().String(), "result_id": result.ID().String(), "outcome": result.Outcome()})})
		return err
	})
}

func (s *Service) commitVerificationCancellation(ctx context.Context, request VerificationExecutionRequest, evidence verifier.AttemptEvidence) error {
	normalized, err := s.normalizeVerificationEvidence(request, evidence, verificationEvidenceCancel)
	if err != nil {
		return err
	}
	unlock := s.runLock(request.RunID)
	defer unlock()
	now := s.deps.Clock.Now()
	return s.deps.Store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		run, verificationRun, attempt, err := loadRunningVerification(ctx, tx, request)
		if err != nil {
			return err
		}
		for _, command := range normalized.commands {
			if err := tx.InsertVerificationCommandEvidence(ctx, command); err != nil {
				return err
			}
		}
		return s.finishVerificationCancellationTx(ctx, tx, run, verificationRun, attempt, normalized.workspaceIdentity, "user_cancelled", now)
	})
}

func (s *Service) finishVerificationCancellationTx(ctx context.Context, tx storecontract.WriteTx, run domain.AgentRun, verificationRun domain.VerificationRun, attempt storecontract.VerificationAttemptRecord, workspaceIdentity, reason string, now time.Time) error {
	nextAttempt, err := attempt.Attempt.Transition(domain.VerificationAttemptCancelConfirmed)
	if err != nil {
		return err
	}
	nextVerificationRun, err := verificationRun.Transition(domain.VerificationRunCancelConfirmed, now)
	if err != nil {
		return err
	}
	nextRun, err := run.Transition(domain.CommandVerificationCancelConfirmed, now)
	if err != nil {
		return err
	}
	if err := tx.SaveVerificationAttemptCAS(ctx, attempt.Attempt.State(), storecontract.VerificationAttemptRecord{Attempt: nextAttempt, CleanupProven: true, WorkspaceIdentity: workspaceIdentity, Reason: reason, UpdatedAt: now}); err != nil {
		return err
	}
	if err := tx.SaveVerificationRunCAS(ctx, verificationRun.State(), nextVerificationRun); err != nil {
		return err
	}
	if err := tx.SaveRunCAS(ctx, run.Version(), nextRun); err != nil {
		return err
	}
	_, err = tx.AppendRunEvent(ctx, run.ID(), storecontract.EventDraft{ID: s.deps.IDs.EventID(), Type: "verification.cancelled", Source: "verifier", OccurredAt: now, RecordedAt: now,
		NormalizedJSON: responseBody(map[string]any{"type": "verification.cancelled", "verification_attempt_id": nextAttempt.ID().String(), "workspace_identity": workspaceIdentity, "cleanup_proven": true})})
	return err
}

func loadRunningVerification(ctx context.Context, reader storecontract.Reader, request VerificationExecutionRequest) (domain.AgentRun, domain.VerificationRun, storecontract.VerificationAttemptRecord, error) {
	run, err := reader.GetRun(ctx, request.RunID)
	if err != nil || run.State() != domain.RunStateVerifying {
		return domain.AgentRun{}, domain.VerificationRun{}, storecontract.VerificationAttemptRecord{}, fmt.Errorf("verification Run is no longer running")
	}
	verificationRun, err := reader.GetVerificationRun(ctx, request.VerificationRunID)
	if err != nil || verificationRun.RunID() != run.ID() || verificationRun.State() != domain.VerificationRunVerifying || verificationRun.Bindings() != request.Bindings {
		return domain.AgentRun{}, domain.VerificationRun{}, storecontract.VerificationAttemptRecord{}, fmt.Errorf("verification run authority changed")
	}
	attempt, err := reader.GetVerificationAttempt(ctx, request.AttemptID)
	if err != nil || attempt.Attempt.VerificationRunID() != verificationRun.ID() || attempt.Attempt.State() != domain.VerificationAttemptRunning || attempt.Attempt.Bindings() != request.Bindings {
		return domain.AgentRun{}, domain.VerificationRun{}, storecontract.VerificationAttemptRecord{}, fmt.Errorf("verification Attempt authority changed")
	}
	return run, verificationRun, attempt, nil
}

func (s *Service) markVerificationRecovery(ctx context.Context, runID domain.RunID, verificationRunID domain.VerificationRunID, attemptID domain.VerificationAttemptID, reason string) error {
	return s.persistVerificationRecovery(ctx, runID, verificationRunID, attemptID, nil, "", reason)
}

func (s *Service) commitVerificationRecoveryEvidence(ctx context.Context, request VerificationExecutionRequest, evidence verifier.AttemptEvidence) error {
	normalized, err := s.normalizeVerificationEvidence(request, evidence, verificationEvidenceRecovery)
	if err != nil {
		return err
	}
	return s.persistVerificationRecovery(ctx, request.RunID, request.VerificationRunID, request.AttemptID, normalized.commands, normalized.workspaceIdentity, evidence.Reason)
}

func (s *Service) persistVerificationRecovery(ctx context.Context, runID domain.RunID, verificationRunID domain.VerificationRunID, attemptID domain.VerificationAttemptID, commands []domain.VerificationCommandEvidence, workspaceIdentity, reason string) error {
	unlock := s.runLock(runID)
	defer unlock()
	now := s.deps.Clock.Now()
	if strings.TrimSpace(reason) == "" {
		reason = "verification continuity or evidence trust unavailable"
	}
	return s.deps.Store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		if _, err := tx.GetVerificationResultForRun(ctx, runID); err == nil {
			return nil
		} else if !errors.Is(err, storecontract.ErrNotFound) {
			return err
		}
		run, err := tx.GetRun(ctx, runID)
		if err != nil {
			return err
		}
		if run.State() == domain.RunStateVerificationRecoveryRequired {
			return nil
		}
		verificationRun, err := tx.GetVerificationRun(ctx, verificationRunID)
		if err != nil {
			return err
		}
		attempt, err := tx.GetVerificationAttempt(ctx, attemptID)
		if err != nil {
			return err
		}
		if run.State() != domain.RunStateVerifying || verificationRun.State() != domain.VerificationRunVerifying || attempt.Attempt.State() != domain.VerificationAttemptRunning {
			return fmt.Errorf("verification recovery source state changed")
		}
		for _, command := range commands {
			if err := tx.InsertVerificationCommandEvidence(ctx, command); err != nil {
				return err
			}
		}
		nextAttempt, err := attempt.Attempt.Transition(domain.VerificationAttemptIntegrityUncertain)
		if err != nil {
			return err
		}
		nextVerificationRun, err := verificationRun.Transition(domain.VerificationRunIntegrityUncertain, now)
		if err != nil {
			return err
		}
		nextRun, err := run.Transition(domain.CommandVerificationRecoveryRequired, now)
		if err != nil {
			return err
		}
		if err := tx.SaveVerificationAttemptCAS(ctx, attempt.Attempt.State(), storecontract.VerificationAttemptRecord{Attempt: nextAttempt, WorkspaceIdentity: workspaceIdentity, Reason: reason, UpdatedAt: now}); err != nil {
			return err
		}
		if err := tx.SaveVerificationRunCAS(ctx, verificationRun.State(), nextVerificationRun); err != nil {
			return err
		}
		if err := tx.SaveRunCAS(ctx, run.Version(), nextRun); err != nil {
			return err
		}
		_, err = tx.AppendRunEvent(ctx, run.ID(), storecontract.EventDraft{ID: s.deps.IDs.EventID(), Type: "verification.recovery_required", Source: "app", OccurredAt: now, RecordedAt: now,
			NormalizedJSON: responseBody(map[string]any{"type": "verification.recovery_required", "verification_attempt_id": attemptID.String(), "workspace_identity": workspaceIdentity, "command_evidence_count": len(commands), "reason": reason})})
		return err
	})
}
