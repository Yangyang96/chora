package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
	"github.com/Yangyang96/chora/internal/speccoding"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

// SubmitTerminal is intentionally proof-gated. Only HandleExit can construct
// the drain proof; callers cannot commit terminal state independently.
func (s *Service) SubmitTerminal(ctx context.Context, request SubmitTerminalRequest) (SubmitTerminalResult, error) {
	if !request.proof.sessionID.Valid() {
		return SubmitTerminalResult{}, fmt.Errorf("%w: terminal result requires drained runtime proof", ErrInvalidCommand)
	}
	now := s.deps.Clock.Now()
	var result SubmitTerminalResult
	err := s.deps.Store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		run, err := tx.GetRun(ctx, request.RunID)
		if err != nil {
			return err
		}
		attempt, err := tx.GetCurrentAttempt(ctx, request.RunID)
		if err != nil {
			return err
		}
		result, err = s.submitTerminalTx(ctx, tx, request.CommandMeta, run, attempt, request.ExpectedVersion, request.Result, now)
		return err
	})
	return result, err
}

func (s *Service) submitTerminalTx(ctx context.Context, tx storecontract.WriteTx, meta CommandMeta, run domain.AgentRun, attempt domain.Attempt, expected uint64, terminal execution.TerminalResult, now time.Time, resourceGroups ...*domain.ResourceResultGroup) (SubmitTerminalResult, error) {
	key, err := s.commandKey(meta, "submit_terminal", run.ID().String(), expected, terminal, now)
	if err != nil {
		return SubmitTerminalResult{}, err
	}
	response, replay, err := tx.LookupCommand(ctx, key)
	if err != nil {
		return SubmitTerminalResult{}, err
	}
	if replay {
		return replayTerminal(response)
	}
	result, err := s.applyTerminalTx(ctx, tx, meta, run, attempt, expected, terminal, now, resourceGroups...)
	if err != nil {
		return SubmitTerminalResult{}, err
	}
	response, err = terminalResponse(result)
	if err != nil {
		return SubmitTerminalResult{}, err
	}
	if err := tx.SaveCommand(ctx, key, response); err != nil {
		return SubmitTerminalResult{}, err
	}
	return result, nil
}

func (s *Service) applyTerminalTx(ctx context.Context, tx storecontract.WriteTx, meta CommandMeta, run domain.AgentRun, attempt domain.Attempt, expected uint64, terminal execution.TerminalResult, now time.Time, resourceGroups ...*domain.ResourceResultGroup) (SubmitTerminalResult, error) {
	var resourceGroup *domain.ResourceResultGroup
	if len(resourceGroups) == 1 {
		resourceGroup = resourceGroups[0]
	}
	if run.Version() != expected {
		return SubmitTerminalResult{}, storecontract.ErrVersionConflict
	}
	latestGate, err := tx.GetLatestDecisionGateForRun(ctx, run.ID())
	if err == nil && latestGate.Status() == domain.DecisionGateOpen {
		return SubmitTerminalResult{}, fmt.Errorf("%w: terminal result cannot bypass an open execution Decision Gate", ErrInvalidCommand)
	}
	if err != nil && !errors.Is(err, storecontract.ErrNotFound) {
		return SubmitTerminalResult{}, err
	}
	if !terminal.Valid() {
		return SubmitTerminalResult{}, fmt.Errorf("%w: invalid terminal result", ErrInvalidCommand)
	}
	charter, err := tx.GetCharter(ctx, run.CharterID())
	if err != nil {
		return SubmitTerminalResult{}, err
	}
	if terminal.ContextConsumption != nil && !validContextConsumption(ctx, tx, attempt, *terminal.ContextConsumption) {
		terminal = execution.TerminalResult{Kind: execution.TerminalFailed, FailureReason: "context_consumption_invalid"}
	}
	command := domain.CommandSubmitAgentReport
	eventType := "run.awaiting_verification"
	attemptEvent := domain.AttemptEventOutputSubmitted
	switch terminal.Kind {
	case execution.TerminalFailed, execution.TerminalChecksFailed, execution.TerminalChecksIncomplete:
		command = domain.CommandAttemptFailed
		eventType = "attempt.failed"
		attemptEvent = domain.AttemptEventAttemptFailed
	}
	// Local Connected (No Sandbox) has no independent verifier: a review-ready
	// Result moves directly to human Review instead of awaiting verification.
	if terminal.Kind == execution.TerminalReviewReady && s.deps.Verifier == nil && charter.CapabilityEnvelope()[speccoding.LocalConnectedNoSandboxCapability] {
		command = domain.CommandSubmitAgentReportDirectReview
		eventType = "run.awaiting_review"
	}
	if terminal.Kind == execution.TerminalCompletedNoChange || terminal.Kind == execution.TerminalChecksFailed || terminal.Kind == execution.TerminalChecksIncomplete {
		if s.deps.Verifier != nil || !charter.CapabilityEnvelope()[speccoding.LocalConnectedNoSandboxCapability] {
			return SubmitTerminalResult{}, fmt.Errorf("%w: no-change completion requires Local Connected authority", ErrInvalidCommand)
		}
		if terminal.Kind == execution.TerminalCompletedNoChange {
			command = domain.CommandCompleteNoChange
			eventType = "run.completed_no_change"
		}
	}
	directReview := command == domain.CommandSubmitAgentReportDirectReview
	next, err := run.Transition(command, now)
	if err != nil {
		return SubmitTerminalResult{}, err
	}
	nextAttempt, err := attempt.Transition(attemptEvent)
	if err != nil {
		return SubmitTerminalResult{}, err
	}
	if err := tx.SaveRunCAS(ctx, run.Version(), next); err != nil {
		return SubmitTerminalResult{}, err
	}
	if err := tx.SaveAttemptCAS(ctx, attempt.State(), nextAttempt); err != nil {
		return SubmitTerminalResult{}, err
	}
	var agentReport *domain.AgentReport
	if terminal.Kind != execution.TerminalFailed {
		claims := make([]domain.AgentClaimedCheck, 0, len(terminal.Checks))
		for _, claimed := range terminal.Checks {
			criterionID, err := domain.ParseCriterionID(claimed.CriterionID)
			if err != nil {
				return SubmitTerminalResult{}, fmt.Errorf("%w: invalid Agent claimed criterion", ErrInvalidCommand)
			}
			claims = append(claims, domain.AgentClaimedCheck{CriterionID: criterionID, Status: string(claimed.Status), Evidence: claimed.Evidence})
		}
		finalText := terminal.Summary
		events, err := tx.ListRunEvents(ctx, run.ID())
		if err != nil {
			return SubmitTerminalResult{}, err
		}
		if resourceGroup != nil {
			final, visible, complete := domain.CurrentCompleteAssistantEvidence(events)
			if !complete || final != resourceGroup.FinalAssistant {
				return SubmitTerminalResult{}, ErrReviewEvidenceUnavailable
			}
			finalText = visible
		} else if visible, ok := latestAttemptAssistantText(events); ok {
			finalText = visible
		}
		report, err := domain.NewAgentReport(domain.AgentReportParams{ID: s.deps.IDs.AgentReportID(), RunID: run.ID(), AttemptID: attempt.ID(), Summary: terminal.Summary, FinalText: finalText, ClaimedChecks: claims, CompletedAt: now})
		if err != nil {
			return SubmitTerminalResult{}, err
		}
		agentReport = &report
	}
	payload := map[string]any{"type": eventType, "terminal_kind": terminal.Kind, "run_id": run.ID().String(), "attempt_id": attempt.ID().String(), "failure_reason": terminal.FailureReason, "summary": terminal.Summary, "outputs": terminal.Outputs, "artifact_candidates": terminal.ArtifactCandidates, "claimed_checks": terminal.Checks, "claimed_unknowns": terminal.Unknowns, "handoff": terminal.Handoff, "context_consumption": terminal.ContextConsumption}
	if terminal.Kind == execution.TerminalCompletedNoChange || terminal.Kind == execution.TerminalChecksFailed || terminal.Kind == execution.TerminalChecksIncomplete {
		binding, err := tx.GetSpecCodingBinding(ctx, run.TaskID())
		if err != nil || binding.Status != storecontract.SpecCodingRegistered || sha256.Sum256(binding.ActiveContractJSON) != binding.ActiveContractDigest {
			return SubmitTerminalResult{}, fmt.Errorf("%w: no-change contract authority unavailable", ErrInvalidCommand)
		}
		payload["result_id"] = s.deps.IDs.ResultID().String()
		if resourceGroup != nil {
			payload["result_id"] = resourceGroup.ID
		}
		payload["outcome"] = string(terminal.Kind)
		payload["contract_digest"] = hex.EncodeToString(binding.ActiveContractDigest[:])
		payload["context_snapshot_digest"] = hex.EncodeToString(binding.SnapshotDigest[:])
	}
	if agentReport != nil {
		payload["agent_report_id"] = agentReport.ID().String()
	}
	eventID := s.deps.IDs.EventID()
	if _, err := tx.AppendRunEvent(ctx, run.ID(), storecontract.EventDraft{ID: eventID, Type: eventType, Source: "adapter", OccurredAt: now, RecordedAt: now, NormalizedJSON: responseBody(payload)}); err != nil {
		return SubmitTerminalResult{}, err
	}
	result := SubmitTerminalResult{Run: next, Attempt: nextAttempt}
	if agentReport != nil {
		if err := tx.InsertAgentReport(ctx, *agentReport); err != nil {
			return SubmitTerminalResult{}, err
		}
		id := agentReport.ID()
		result.AgentReportID = &id
	}
	attemptID := attempt.ID()
	source := eventID
	if terminal.Summary != "" {
		observationID := s.deps.IDs.ObservationID()
		if err := tx.InsertObservation(ctx, storecontract.Observation{ID: observationID, RunID: run.ID(), AttemptID: &attemptID, Kind: "agent_report_summary", Body: terminal.Summary, SourceEventID: &source, Position: 0, Role: "agent_report_summary", CreatedAt: now}); err != nil {
			return SubmitTerminalResult{}, err
		}
		result.SummaryObservation = &observationID
	}
	if terminal.ContextConsumption != nil {
		body, err := json.Marshal(terminal.ContextConsumption)
		if err != nil {
			return SubmitTerminalResult{}, err
		}
		observationID := s.deps.IDs.ObservationID()
		if err := tx.InsertObservation(ctx, storecontract.Observation{ID: observationID, RunID: run.ID(), AttemptID: &attemptID, Kind: "agent_report_context_consumption", Body: string(body), SourceEventID: &source, Position: 0, Role: "agent_report_context_consumption", CreatedAt: now}); err != nil {
			return SubmitTerminalResult{}, err
		}
		result.ContextConsumptionObservation = &observationID
	}
	var localPatchArtifact *storecontract.Artifact
	for position, output := range terminal.Outputs {
		digest, mediaType, err := artifactIdentity(output)
		if err != nil {
			return SubmitTerminalResult{}, fmt.Errorf("%w: invalid output artifact identity", ErrInvalidCommand)
		}
		artifactID := s.deps.IDs.ArtifactID()
		artifact := storecontract.Artifact{ID: artifactID, RunID: run.ID(), AttemptID: &attemptID, Kind: artifactKind(output), Locator: output.Locator, Digest: digest, MediaType: mediaType, Description: output.Description, Position: position, Role: "output", SourceEventID: &source, CreatedAt: now}
		if err := tx.InsertArtifact(ctx, artifact); err != nil {
			return SubmitTerminalResult{}, err
		}
		if directReview && resourceGroup == nil && artifact.Kind == "patch" {
			if localPatchArtifact != nil {
				return SubmitTerminalResult{}, fmt.Errorf("%w: multiple Local Connected Patch artifacts", ErrInvalidCommand)
			}
			copy := artifact
			localPatchArtifact = &copy
		}
		result.Artifacts = append(result.Artifacts, artifactID)
	}
	if directReview && resourceGroup == nil {
		if agentReport == nil || localPatchArtifact == nil || localPatchArtifact.Digest == nil {
			return SubmitTerminalResult{}, fmt.Errorf("%w: Local Connected review requires one digest-bound Patch and AgentReport", ErrInvalidCommand)
		}
		binding, err := tx.GetSpecCodingBinding(ctx, run.TaskID())
		if err != nil || binding.Status != storecontract.SpecCodingRegistered || sha256.Sum256(binding.ActiveContractJSON) != binding.ActiveContractDigest {
			return SubmitTerminalResult{}, fmt.Errorf("%w: Local Connected contract binding is unavailable", ErrInvalidCommand)
		}
		contract, err := speccoding.DecodeCoreContract(binding.ActiveContractJSON)
		if err != nil || !localConnectedReviewDocument(contract.Document()) {
			return SubmitTerminalResult{}, fmt.Errorf("%w: Local Connected review contract is invalid", ErrInvalidCommand)
		}
		localResult, err := domain.NewLocalReviewResult(domain.LocalReviewResultParams{
			ID: s.deps.IDs.ResultID(), RunID: run.ID(), AgentAttemptID: attempt.ID(), AgentReportID: agentReport.ID(), PatchArtifactID: localPatchArtifact.ID,
			PatchDigest: *localPatchArtifact.Digest, BaselineDigest: localConnectedBaselineDigest(contract.Document()), ContextSnapshotDigest: attempt.ContextDigest(),
			AcceptanceContractDigest: binding.ActiveContractDigest, Outcome: domain.ResultReviewReady, CreatedAt: now,
		})
		if err != nil {
			return SubmitTerminalResult{}, err
		}
		if err := tx.InsertLocalReviewResult(ctx, localResult); err != nil {
			return SubmitTerminalResult{}, err
		}
	}
	if resourceGroup != nil {
		if agentReport == nil {
			return SubmitTerminalResult{}, ErrReviewEvidenceUnavailable
		}
		resourceGroup.AgentReportID = agentReport.ID().String()
		if err := tx.InsertResourceResultGroup(ctx, *resourceGroup); err != nil {
			return SubmitTerminalResult{}, err
		}
	}
	for position, candidate := range terminal.ArtifactCandidates {
		digest, mediaType, err := artifactIdentity(candidate)
		if err != nil {
			return SubmitTerminalResult{}, fmt.Errorf("%w: invalid candidate artifact identity", ErrInvalidCommand)
		}
		artifactID := s.deps.IDs.ArtifactID()
		if err := tx.InsertArtifact(ctx, storecontract.Artifact{ID: artifactID, RunID: run.ID(), AttemptID: &attemptID, Kind: artifactKind(candidate), Locator: candidate.Locator, Digest: digest, MediaType: mediaType, Description: candidate.Description, Position: position, Role: "candidate", SourceEventID: &source, CreatedAt: now}); err != nil {
			return SubmitTerminalResult{}, err
		}
		result.Artifacts = append(result.Artifacts, artifactID)
	}
	for position, unknown := range terminal.Unknowns {
		observationID := s.deps.IDs.ObservationID()
		if err := tx.InsertObservation(ctx, storecontract.Observation{ID: observationID, RunID: run.ID(), AttemptID: &attemptID, Kind: "agent_report_unknown", Body: unknown, SourceEventID: &source, Position: position, Role: "agent_report_unknown", CreatedAt: now}); err != nil {
			return SubmitTerminalResult{}, err
		}
		result.UnknownObservations = append(result.UnknownObservations, observationID)
	}
	if terminal.Kind == execution.TerminalFailed {
		if err := tx.InsertObservation(ctx, storecontract.Observation{ID: s.deps.IDs.ObservationID(), RunID: run.ID(), AttemptID: &attemptID, Kind: "terminal_failure_reason", Body: terminal.FailureReason, SourceEventID: &source, CreatedAt: now}); err != nil {
			return SubmitTerminalResult{}, err
		}
	}
	if terminal.Kind == execution.TerminalCompletedNoChange {
		if err := tx.CloseTaskForCompletedRun(ctx, run.TaskID(), next.ID(), next.Version()); err != nil {
			return SubmitTerminalResult{}, err
		}
	}
	if terminal.Handoff != nil {
		handoffID := s.deps.IDs.HandoffID()
		if err := tx.InsertHandoff(ctx, storecontract.Handoff{ID: handoffID, RunID: run.ID(), AttemptID: &attemptID, FromActor: meta.ActorID, ToActor: charter.ResponsibleHuman(), Reason: terminal.Handoff.Reason, Status: "requested", SourceEventID: &source, CreatedAt: now}); err != nil {
			return SubmitTerminalResult{}, err
		}
		result.Handoff = &handoffID
	}
	return result, nil
}

const maxPersistedAssistantText = 64 * 1024

func latestAttemptAssistantText(events []domain.RunEvent) (string, bool) {
	start := -1
	for index := range events {
		if events[index].Type() == "attempt.started" {
			start = index
		}
	}
	if start < 0 {
		return "", false
	}
	var latest string
	for _, event := range events[start+1:] {
		if event.Type() != "assistant_message" || event.Source() != "adapter" {
			continue
		}
		var payload struct {
			EventType string `json:"event_type"`
			Text      string `json:"text"`
		}
		if json.Unmarshal(event.NormalizedJSON(), &payload) != nil || payload.EventType != "assistant_message" {
			continue
		}
		text := strings.TrimSpace(strings.ToValidUTF8(payload.Text, "\uFFFD"))
		if text == "" || len(text) > maxPersistedAssistantText {
			continue
		}
		latest = text
	}
	return latest, latest != ""
}

func noChangeOutcome(checks []execution.DeclaredCheck, noChecks bool) execution.TerminalResultKind {
	if noChecks {
		return execution.TerminalCompletedNoChange
	}
	incomplete := len(checks) == 0
	for _, check := range checks {
		if check.Status == execution.CheckFail {
			return execution.TerminalChecksFailed
		}
		if check.Status != execution.CheckPass {
			incomplete = true
		}
	}
	if incomplete {
		return execution.TerminalChecksIncomplete
	}
	return execution.TerminalCompletedNoChange
}

// Pi result success requires a complete final assistant message for this Attempt.
// An earlier progress message or a truncated response is not a terminal result.
func hasCompletePiResult(events []domain.RunEvent) bool {
	complete := false
	started := false
	for _, event := range events {
		if event.Type() == "attempt.started" {
			complete = false
			started = true
		}
		if !started || event.Type() != "assistant_message" || event.Source() != "adapter" {
			continue
		}
		var payload struct {
			Text             string `json:"text"`
			TerminalComplete bool   `json:"terminal_complete"`
			Truncated        bool   `json:"truncated"`
		}
		complete = json.Unmarshal(event.NormalizedJSON(), &payload) == nil && payload.TerminalComplete && !payload.Truncated && strings.TrimSpace(payload.Text) != "" && len(payload.Text) <= maxPersistedAssistantText
	}
	return complete
}

func artifactIdentity(artifact execution.WorkspaceArtifact) (*[32]byte, string, error) {
	if artifact.SHA256 == "" && artifact.MediaType == "" {
		return nil, "", nil
	}
	if len(artifact.SHA256) != 64 || strings.TrimSpace(artifact.MediaType) == "" {
		return nil, "", errors.New("artifact digest and media type must be complete")
	}
	decoded, err := hex.DecodeString(artifact.SHA256)
	if err != nil || len(decoded) != sha256.Size || strings.ToLower(artifact.SHA256) != artifact.SHA256 {
		return nil, "", errors.New("artifact SHA-256 is invalid")
	}
	var digest [32]byte
	copy(digest[:], decoded)
	return &digest, artifact.MediaType, nil
}

func artifactKind(artifact execution.WorkspaceArtifact) string {
	if artifact.MediaType == "text/x-diff" {
		return "patch"
	}
	return "workspace"
}

func validContextConsumption(ctx context.Context, reader storecontract.Reader, attempt domain.Attempt, evidence execution.ContextConsumption) bool {
	if !evidence.Valid() || evidence.SnapshotID != attempt.ContextSnapshotID().String() || evidence.SnapshotDigest != fmt.Sprintf("%x", attempt.ContextDigest()) {
		return false
	}
	snapshot, err := reader.GetSnapshot(ctx, attempt.ContextSnapshotID())
	if err != nil || snapshot.Digest() != attempt.ContextDigest() {
		return false
	}
	selection, ok := snapshot.Selection()
	if !ok {
		return false
	}
	selected := selection.Selected()
	included := make([]string, 0, len(selected))
	for _, item := range selected {
		included = append(included, item.RevisionID.String())
	}
	excludedSelection := selection.Excluded()
	excluded := make([]string, 0, len(excludedSelection))
	for _, item := range excludedSelection {
		excluded = append(excluded, item.RevisionID.String())
	}
	if !sameStrings(included, evidence.IncludedRevisionIDs) || !sameStrings(excluded, evidence.ExcludedRevisionIDs) {
		return false
	}
	candidateID, err := domain.ParseContextRevisionID(evidence.CandidateRevisionID)
	if err != nil {
		return false
	}
	revision, err := reader.GetRoomRevision(ctx, candidateID)
	if err != nil || revision.Provenance().Kind != domain.RevisionProvenanceRunCandidate {
		return false
	}
	return evidence.Derivation == execution.DeriveConfirmedContext(candidateID.String(), revision.Revision().Title(), revision.Revision().Body())
}

func sameStrings(left, right []string) bool {
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
