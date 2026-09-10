package app

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

type runWire struct {
	ID, TaskID, CharterID                                          string
	State                                                          domain.RunState
	Version                                                        uint64
	Attempt                                                        int
	CreatedAt, UpdatedAt, StartedAt, ReviewRequestedAt, TerminalAt time.Time
}
type attemptWire struct {
	ID, RunID                                                                 string
	Sequence                                                                  int
	Predecessor                                                               string
	SnapshotID                                                                string
	Digest                                                                    [32]byte
	AdapterID, ExternalSession, RetryReason, InterventionReason, ContextDelta string
	State                                                                     domain.AttemptState
	CreatedAt                                                                 time.Time
	AgentExecutionProfileBinding                                              domain.AgentExecutionProfileBindingRecord
}
type prepareWire struct {
	Run        runWire
	Attempt    attemptWire
	SnapshotID string
}
type sessionWire struct{ ID, Identity string }
type startWire struct {
	Run     runWire
	Attempt attemptWire
	Session *sessionWire
}
type terminalWire struct {
	Run                                                                     runWire
	Attempt                                                                 attemptWire
	Artifacts, Checks, Unknowns                                             []string
	AgentReport, SummaryObservation, ContextConsumptionObservation, Handoff string
}
type reviewWire struct {
	Run          runWire
	DecisionID   string
	Kind         domain.ReviewDecisionKind
	Expected     uint64
	Comment      string `json:"Note"` // Preserve persisted command replay encoding.
	Checks       []domain.ReviewedCheck
	Links        []domain.ArtifactLink
	DecidedAt    time.Time
	CandidateIDs []string
}
type stopWire struct {
	Run     runWire
	Attempt attemptWire
	Handoff string
}
type executionDecisionWire struct {
	ID, RunID, AttemptID, Question, Context, Recommendation, Impact string
	Options                                                         []domain.DecisionGateOption
	Status                                                          domain.DecisionGateStatus
	SelectedOptionID, Note, ActorID, SessionID                      string
	RequestedAt, ResolvedAt                                         time.Time
}

func wireRun(run domain.AgentRun) runWire {
	return runWire{ID: run.ID().String(), TaskID: run.TaskID().String(), CharterID: run.CharterID().String(), State: run.State(), Version: run.Version(), Attempt: run.CurrentAttemptNumber(), CreatedAt: run.CreatedAt(), UpdatedAt: run.UpdatedAt(), StartedAt: run.StartedAt(), ReviewRequestedAt: run.ReviewRequestedAt(), TerminalAt: run.TerminalAt()}
}
func restoreRun(w runWire) (domain.AgentRun, error) {
	id, e := domain.ParseRunID(w.ID)
	if e != nil {
		return domain.AgentRun{}, e
	}
	task, e := domain.ParseTaskID(w.TaskID)
	if e != nil {
		return domain.AgentRun{}, e
	}
	charter, e := domain.ParseCharterID(w.CharterID)
	if e != nil {
		return domain.AgentRun{}, e
	}
	return domain.RestoreAgentRun(domain.AgentRunRecord{ID: id, TaskID: task, CharterID: charter, State: w.State, Version: w.Version, CurrentAttemptNumber: w.Attempt, CreatedAt: w.CreatedAt, UpdatedAt: w.UpdatedAt, StartedAt: w.StartedAt, ReviewRequestedAt: w.ReviewRequestedAt, TerminalAt: w.TerminalAt})
}
func wireAttempt(a domain.Attempt) attemptWire {
	pred := ""
	if id, ok := a.Predecessor(); ok {
		pred = id.String()
	}
	return attemptWire{ID: a.ID().String(), RunID: a.RunID().String(), Sequence: a.Sequence(), Predecessor: pred, SnapshotID: a.ContextSnapshotID().String(), Digest: a.ContextDigest(), AdapterID: a.AdapterID(), ExternalSession: a.ExternalSession(), RetryReason: a.RetryReason(), InterventionReason: a.InterventionReason(), ContextDelta: a.ContextDelta(), State: a.State(), CreatedAt: a.CreatedAt(), AgentExecutionProfileBinding: a.AgentExecutionProfileBinding().Record()}
}
func restoreAttempt(w attemptWire) (domain.Attempt, error) {
	id, e := domain.ParseAttemptID(w.ID)
	if e != nil {
		return domain.Attempt{}, e
	}
	run, e := domain.ParseRunID(w.RunID)
	if e != nil {
		return domain.Attempt{}, e
	}
	snapshot, e := domain.ParseContextSnapshotID(w.SnapshotID)
	if e != nil {
		return domain.Attempt{}, e
	}
	var pred *domain.AttemptID
	if w.Predecessor != "" {
		v, e := domain.ParseAttemptID(w.Predecessor)
		if e != nil {
			return domain.Attempt{}, e
		}
		pred = &v
	}
	profile, e := domain.RestoreAgentExecutionProfileBinding(w.AgentExecutionProfileBinding)
	if e != nil {
		return domain.Attempt{}, e
	}
	return domain.RestoreAttempt(domain.AttemptRecord{ID: id, RunID: run, Sequence: w.Sequence, Predecessor: pred, ContextSnapshotID: snapshot, ContextDigest: w.Digest, AdapterID: w.AdapterID, AgentExecutionProfileBinding: profile, ExternalSession: w.ExternalSession, RetryReason: w.RetryReason, InterventionReason: w.InterventionReason, ContextDelta: w.ContextDelta, State: w.State, CreatedAt: w.CreatedAt})
}
func wireExecutionDecision(gate domain.ExecutionDecisionGate) executionDecisionWire {
	return executionDecisionWire{
		ID: gate.ID().String(), RunID: gate.RunID().String(), AttemptID: gate.AttemptID().String(), Question: gate.Question(), Context: gate.Context(),
		Options: gate.Options(), Recommendation: gate.Recommendation(), Impact: gate.Impact(), Status: gate.Status(),
		SelectedOptionID: gate.SelectedOptionID(), Note: gate.Note(), ActorID: gate.ActorID(), SessionID: gate.SessionID(), RequestedAt: gate.RequestedAt(), ResolvedAt: gate.ResolvedAt(),
	}
}
func restoreExecutionDecision(w executionDecisionWire) (domain.ExecutionDecisionGate, error) {
	id, err := domain.ParseDecisionGateID(w.ID)
	if err != nil {
		return domain.ExecutionDecisionGate{}, err
	}
	runID, err := domain.ParseRunID(w.RunID)
	if err != nil {
		return domain.ExecutionDecisionGate{}, err
	}
	attemptID, err := domain.ParseAttemptID(w.AttemptID)
	if err != nil {
		return domain.ExecutionDecisionGate{}, err
	}
	return domain.RestoreExecutionDecisionGate(domain.ExecutionDecisionGateParams{
		ID: id, RunID: runID, AttemptID: attemptID, Question: w.Question, Context: w.Context, Options: w.Options,
		Recommendation: w.Recommendation, Impact: w.Impact, RequestedAt: w.RequestedAt,
	}, w.Status, w.SelectedOptionID, w.Note, w.ActorID, w.SessionID, w.ResolvedAt)
}
func jsonResponse(value any) (storecontract.Response, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return storecontract.Response{}, fmt.Errorf("marshal response: %w", err)
	}
	return storecontract.Response{Status: 200, ContentType: "application/json", Body: body}, nil
}
func decodeResponse(response storecontract.Response, target any) error {
	if len(response.Body) == 0 {
		return fmt.Errorf("empty stored response")
	}
	return json.Unmarshal(response.Body, target)
}

func prepareResponse(result PrepareRunResult) (storecontract.Response, error) {
	return jsonResponse(prepareWire{Run: wireRun(result.Run), Attempt: wireAttempt(result.Attempt), SnapshotID: result.Snapshot.ID().String()})
}
func replayPrepare(ctx context.Context, response storecontract.Response, reader storecontract.Reader) (PrepareRunResult, error) {
	var w prepareWire
	if err := decodeResponse(response, &w); err != nil {
		return PrepareRunResult{}, err
	}
	run, e := restoreRun(w.Run)
	if e != nil {
		return PrepareRunResult{}, e
	}
	attempt, e := restoreAttempt(w.Attempt)
	if e != nil {
		return PrepareRunResult{}, e
	}
	id, e := domain.ParseContextSnapshotID(w.SnapshotID)
	if e != nil {
		return PrepareRunResult{}, e
	}
	snapshot, e := reader.GetSnapshot(ctx, id)
	return PrepareRunResult{Run: run, Attempt: attempt, Snapshot: snapshot, Replayed: true}, e
}

func startResponse(result StartAttemptResult) (storecontract.Response, error) {
	w := startWire{Run: wireRun(result.Run), Attempt: wireAttempt(result.Attempt)}
	if result.Session != nil {
		w.Session = &sessionWire{ID: result.Session.ID.String(), Identity: result.Session.Identity.Value}
	}
	return jsonResponse(w)
}
func replayStart(response storecontract.Response) (StartAttemptResult, error) {
	var w startWire
	if e := decodeResponse(response, &w); e != nil {
		return StartAttemptResult{}, e
	}
	run, e := restoreRun(w.Run)
	if e != nil {
		return StartAttemptResult{}, e
	}
	attempt, e := restoreAttempt(w.Attempt)
	if e != nil {
		return StartAttemptResult{}, e
	}
	result := StartAttemptResult{Run: run, Attempt: attempt, Replayed: true}
	if w.Session != nil {
		id, e := domain.ParseRuntimeSessionID(w.Session.ID)
		if e != nil {
			return StartAttemptResult{}, e
		}
		result.Session = &RuntimeSessionResult{ID: id, Identity: execution.ProcessIdentity{Value: w.Session.Identity}}
	}
	return result, nil
}
func terminalResponse(result SubmitTerminalResult) (storecontract.Response, error) {
	w := terminalWire{Run: wireRun(result.Run), Attempt: wireAttempt(result.Attempt)}
	if result.AgentReportID != nil {
		w.AgentReport = result.AgentReportID.String()
	}
	for _, id := range result.Artifacts {
		w.Artifacts = append(w.Artifacts, id.String())
	}
	for _, id := range result.Checks {
		w.Checks = append(w.Checks, id.String())
	}
	for _, id := range result.UnknownObservations {
		w.Unknowns = append(w.Unknowns, id.String())
	}
	if result.SummaryObservation != nil {
		w.SummaryObservation = result.SummaryObservation.String()
	}
	if result.ContextConsumptionObservation != nil {
		w.ContextConsumptionObservation = result.ContextConsumptionObservation.String()
	}
	if result.Handoff != nil {
		w.Handoff = result.Handoff.String()
	}
	return jsonResponse(w)
}
func replayTerminal(response storecontract.Response) (SubmitTerminalResult, error) {
	var w terminalWire
	if e := decodeResponse(response, &w); e != nil {
		return SubmitTerminalResult{}, e
	}
	run, e := restoreRun(w.Run)
	if e != nil {
		return SubmitTerminalResult{}, e
	}
	attempt, e := restoreAttempt(w.Attempt)
	if e != nil {
		return SubmitTerminalResult{}, e
	}
	result := SubmitTerminalResult{Run: run, Attempt: attempt, Replayed: true}
	if w.AgentReport != "" {
		id, e := domain.ParseAgentReportID(w.AgentReport)
		if e != nil {
			return result, e
		}
		result.AgentReportID = &id
	}
	for _, value := range w.Artifacts {
		id, e := domain.ParseArtifactID(value)
		if e != nil {
			return result, e
		}
		result.Artifacts = append(result.Artifacts, id)
	}
	for _, value := range w.Checks {
		id, e := domain.ParseCheckID(value)
		if e != nil {
			return result, e
		}
		result.Checks = append(result.Checks, id)
	}
	for _, value := range w.Unknowns {
		id, e := domain.ParseObservationID(value)
		if e != nil {
			return result, e
		}
		result.UnknownObservations = append(result.UnknownObservations, id)
	}
	if w.SummaryObservation != "" {
		id, e := domain.ParseObservationID(w.SummaryObservation)
		if e != nil {
			return result, e
		}
		result.SummaryObservation = &id
	}
	if w.ContextConsumptionObservation != "" {
		id, e := domain.ParseObservationID(w.ContextConsumptionObservation)
		if e != nil {
			return result, e
		}
		result.ContextConsumptionObservation = &id
	}
	if w.Handoff != "" {
		id, e := domain.ParseHandoffID(w.Handoff)
		if e != nil {
			return result, e
		}
		result.Handoff = &id
	}
	return result, nil
}
func executionDecisionResponse(result ExecutionDecisionResult) (storecontract.Response, error) {
	return jsonResponse(wireExecutionDecision(result.Gate))
}
func replayExecutionDecision(response storecontract.Response) (ExecutionDecisionResult, error) {
	var wire executionDecisionWire
	if err := decodeResponse(response, &wire); err != nil {
		return ExecutionDecisionResult{}, err
	}
	gate, err := restoreExecutionDecision(wire)
	return ExecutionDecisionResult{Gate: gate, Replayed: true}, err
}
func reviewResponse(result ReviewResult) (storecontract.Response, error) {
	candidateIDs := make([]string, 0, len(result.Candidates))
	for _, candidate := range result.Candidates {
		candidateIDs = append(candidateIDs, candidate.ID().String())
	}
	return jsonResponse(reviewWire{Run: wireRun(result.Run), DecisionID: result.Decision.ID().String(), Kind: result.Decision.Kind(), Expected: result.Decision.ExpectedRunVersion(), Comment: result.Decision.Comment(), Checks: result.Decision.Checks(), Links: result.Decision.LinkedArtifacts(), DecidedAt: result.Decision.DecidedAt(), CandidateIDs: candidateIDs})
}
func replayReview(ctx context.Context, response storecontract.Response, reader storecontract.Reader) (ReviewResult, error) {
	var w reviewWire
	if e := decodeResponse(response, &w); e != nil {
		return ReviewResult{}, e
	}
	run, e := restoreRun(w.Run)
	if e != nil {
		return ReviewResult{}, e
	}
	id, e := domain.ParseReviewDecisionID(w.DecisionID)
	if e != nil {
		return ReviewResult{}, e
	}
	decision, e := domain.RestoreReviewDecision(domain.ReviewDecisionRecord{ID: id, RunID: run.ID(), Kind: w.Kind, ExpectedRunVersion: w.Expected, Comment: w.Comment, Checks: w.Checks, LinkedArtifacts: w.Links, DecidedAt: w.DecidedAt})
	if e != nil {
		return ReviewResult{}, e
	}
	result := ReviewResult{Run: run, Decision: decision, Replayed: true}
	for _, value := range w.CandidateIDs {
		id, err := domain.ParseCandidateID(value)
		if err != nil {
			return ReviewResult{}, err
		}
		candidate, err := reader.GetCandidate(ctx, id)
		if err != nil {
			return ReviewResult{}, err
		}
		result.Candidates = append(result.Candidates, candidate)
	}
	return result, nil
}
func stopResponse(result StopResult) (storecontract.Response, error) {
	w := stopWire{Run: wireRun(result.Run), Attempt: wireAttempt(result.Attempt)}
	if result.HandoffID != nil {
		w.Handoff = result.HandoffID.String()
	}
	return jsonResponse(w)
}
func replayStop(response storecontract.Response) (StopResult, error) {
	var w stopWire
	if e := decodeResponse(response, &w); e != nil {
		return StopResult{}, e
	}
	run, e := restoreRun(w.Run)
	if e != nil {
		return StopResult{}, e
	}
	attempt, e := restoreAttempt(w.Attempt)
	if e != nil {
		return StopResult{}, e
	}
	result := StopResult{Run: run, Attempt: attempt, Replayed: true}
	if w.Handoff != "" {
		id, e := domain.ParseHandoffID(w.Handoff)
		if e != nil {
			return result, e
		}
		result.HandoffID = &id
	}
	return result, nil
}
