package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
	"github.com/Yangyang96/chora/internal/speccoding"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

// The managed supervisor caps each persisted stream at 10 MiB. A single Pi
// JSONL event may legitimately exceed 64 KiB (for example a tool result), so
// the application must be able to present the adapter with one complete
// bounded record instead of repeatedly rereading an unconsumable prefix.
const runtimeReadLimit = 10 * 1024 * 1024
const runtimeDrainLimit = 10 * 1024 * 1024

func storeStream(kind execution.StreamKind) (storecontract.RuntimeStream, error) {
	switch kind {
	case execution.StreamStdout:
		return storecontract.RuntimeStreamStdout, nil
	case execution.StreamStderr:
		return storecontract.RuntimeStreamStderr, nil
	default:
		return "", fmt.Errorf("invalid stream")
	}
}

type runtimeContext struct {
	session storecontract.RuntimeSession
	attempt domain.Attempt
	run     domain.AgentRun
	adapter execution.AgentAdapter
}

func (s *Service) runtimeContext(ctx context.Context, sessionID domain.RuntimeSessionID) (runtimeContext, error) {
	session, err := s.deps.Store.Reader().GetRuntimeSession(ctx, sessionID)
	if err != nil {
		return runtimeContext{}, err
	}
	attempt, err := s.deps.Store.Reader().GetAttempt(ctx, session.AttemptID)
	if err != nil {
		return runtimeContext{}, err
	}
	run, err := s.deps.Store.Reader().GetRun(ctx, attempt.RunID())
	if err != nil {
		return runtimeContext{}, err
	}
	adapter, err := s.deps.Agents.Get(session.AdapterID)
	if err != nil {
		return runtimeContext{}, err
	}
	if adapter.ID() != session.AdapterID {
		return runtimeContext{}, fmt.Errorf("adapter registry identity mismatch")
	}
	return runtimeContext{session: session, attempt: attempt, run: run, adapter: adapter}, nil
}
func reconcileHandle(out execution.ReconcileOutcome, expected execution.LaunchToken, required execution.ReconcileOutcomeKind) (execution.RuntimeHandle, error) {
	if out.Kind != required || out.LaunchToken != expected {
		return execution.RuntimeHandle{}, fmt.Errorf("%w: runtime state or launch binding not proven: %s", ErrInvalidCommand, out.Diagnostic)
	}
	if !out.Handle.Valid() {
		return execution.RuntimeHandle{}, fmt.Errorf("reconcile returned no runtime handle")
	}
	return out.Handle, nil
}

func (s *Service) ConsumeStream(ctx context.Context, request ConsumeStreamRequest) error {
	runtime, err := s.runtimeContext(ctx, request.SessionID)
	if err != nil {
		return err
	}
	if err := s.authorize(ctx, request.CommandMeta, "consume_stream", request.SessionID.String(), runtime.run.Version()); err != nil {
		return err
	}
	stream, err := storeStream(request.Stream)
	if err != nil {
		return err
	}
	offset, err := s.deps.Store.Reader().GetRuntimeStreamOffset(ctx, request.SessionID, stream)
	if err != nil {
		return err
	}
	if offset.EOF {
		return nil
	}
	launch := execution.LaunchToken{Value: runtime.session.LaunchToken}
	if !s.reserveStreamProof(request.SessionID, launch, request.Stream, offset.Offset) {
		return fmt.Errorf("%w: stream read requires supervisor notification", ErrInvalidCommand)
	}
	reconciled, err := s.deps.Supervisor.Reconcile(ctx, execution.ProcessIdentity{Value: runtime.session.ProcessIdentity})
	if err != nil {
		return err
	}
	handle, err := reconcileHandle(reconciled, launch, execution.ReconcileAlive)
	if err != nil {
		return err
	}
	chunk, err := s.deps.Supervisor.Read(ctx, handle, request.Stream, offset.Offset, runtimeReadLimit)
	if err != nil {
		return err
	}
	if len(chunk.Data) > runtimeReadLimit || chunk.NextOffset < offset.Offset {
		return fmt.Errorf("invalid bounded stream chunk")
	}
	decoded, err := runtime.adapter.DecodeEvent(execution.EventChunk{Profile: runtime.attempt.AgentExecutionProfileBinding().Profile(), WorkingRoot: runtime.eventWorkingRoot(), Stream: request.Stream, Offset: offset.Offset, Data: chunk.Data, EOF: chunk.EOF})
	if err != nil {
		return err
	}
	if decoded.ConsumedBytes < 0 || decoded.ConsumedBytes > len(chunk.Data) || chunk.EOF && decoded.ConsumedBytes != len(chunk.Data) {
		return fmt.Errorf("invalid adapter consumed byte count")
	}
	var policyViolation bool
	decoded.Events, policyViolation, err = s.projectRuntimePolicy(ctx, runtime, decoded.Events)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrRuntimePolicyViolation, err)
	}
	nextOffset := offset.Offset + int64(decoded.ConsumedBytes)
	committedEOF := chunk.EOF && decoded.ConsumedBytes == len(chunk.Data)
	now := s.deps.Clock.Now()
	err = s.deps.Store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		persisted, err := tx.GetRuntimeSession(ctx, request.SessionID)
		if err != nil {
			return err
		}
		if persisted.AttemptID != runtime.attempt.ID() || persisted.AdapterID != runtime.adapter.ID() {
			return storecontract.ErrVersionConflict
		}
		sessionVersion := persisted.Version
		if err := appendNormalized(ctx, tx, s.deps.IDs, runtime.run.ID(), &persisted, decoded.Events, now); err != nil {
			return err
		}
		if persisted.Version != sessionVersion {
			if err := tx.SaveRuntimeSessionCAS(ctx, sessionVersion, persisted); err != nil {
				return err
			}
		}
		return tx.AdvanceRuntimeStreamOffset(ctx, request.SessionID, stream, offset.Offset, nextOffset, committedEOF)
	})
	if err != nil {
		return err
	}
	s.ackStreamProof(request.SessionID, launch, request.Stream, nextOffset)
	// Real Pi RPC remains alive after agent_settled so it can accept another
	// command. Persist that terminal signal first, then close the held stdin to
	// let this one-shot Attempt exit and deliver its trusted exit notification.
	for _, event := range decoded.Events {
		if event.Type != "agent_settled" {
			continue
		}
		if closer, ok := s.deps.Supervisor.(execution.StdinCloser); ok {
			if err := closer.CloseStdin(ctx, handle); err != nil {
				return err
			}
		}
		break
	}
	if policyViolation {
		return ErrRuntimePolicyViolation
	}
	return nil
}

func hasRuntimePolicyViolation(events []execution.NormalizedEvent) bool {
	for _, event := range events {
		if event.Type != "error" {
			continue
		}
		var projected struct {
			ErrorClass string `json:"error_class"`
		}
		if json.Unmarshal(event.NormalizedJSON, &projected) == nil && (projected.ErrorClass == "undeclared_path" || projected.ErrorClass == "undeclared_command") {
			return true
		}
	}
	return false
}

func (s *Service) projectRuntimePolicy(ctx context.Context, runtime runtimeContext, events []execution.NormalizedEvent) ([]execution.NormalizedEvent, bool, error) {
	projected := append([]execution.NormalizedEvent(nil), events...)
	violated := hasRuntimePolicyViolation(projected)
	if runtime.adapter.ID() != "pi" {
		return projected, violated, nil
	}
	binding, err := s.deps.Store.Reader().GetSpecCodingBinding(ctx, runtime.run.TaskID())
	if err != nil || binding.Status != storecontract.SpecCodingRegistered || binding.TaskID != runtime.run.TaskID() ||
		sha256.Sum256(binding.ActiveContractJSON) != binding.ActiveContractDigest {
		return nil, false, fmt.Errorf("registered Spec Coding command boundary is unavailable")
	}
	if err := validateSnapshotLineage(ctx, s.deps.Store.Reader(), runtime.attempt, binding); err != nil {
		return nil, false, err
	}
	contract, err := speccoding.DecodeCoreContract(binding.ActiveContractJSON)
	if err != nil {
		return nil, false, fmt.Errorf("registered Spec Coding command boundary is invalid")
	}
	document := contract.Document()
	if document.Task.ID != runtime.run.TaskID().String() || document.Execution.Input.ContextSnapshotID != binding.SnapshotID.String() ||
		document.Execution.Input.ContextSnapshotDigest != fmt.Sprintf("%x", binding.SnapshotDigest) {
		return nil, false, fmt.Errorf("registered Spec Coding command boundary drifted")
	}
	charter, err := s.deps.Store.Reader().GetCharter(ctx, runtime.run.CharterID())
	if err != nil {
		return nil, false, fmt.Errorf("registered Spec Coding execution authority is unavailable")
	}
	if !choraEnforcesDeclaredPiCommands(charter.CapabilityEnvelope()) {
		// Local Connected delegates permission policy to native Pi. Isolated
		// Local instead relies on its Docker filesystem and writeback scope while
		// v1.2 dynamically selects checks. Neither uses the legacy direct-argv
		// whitelist; both retain the frozen contract and Snapshot checks above.
		return projected, false, nil
	}
	policy := declaredCommandPolicy{exact: make(map[string]struct{}, len(document.Execution.Boundary.Commands)), gofmtFiles: map[string]struct{}{}}
	for _, command := range document.Execution.Boundary.Commands {
		policy.exact[strings.Join(command.Argv, " ")] = struct{}{}
		if len(command.Argv) > 2 && command.Argv[0] == "gofmt" && command.Argv[1] == "-w" {
			for _, file := range command.Argv[2:] {
				policy.gofmtFiles[file] = struct{}{}
			}
		}
		if len(command.Argv) == 3 && command.Argv[0] == "go" && command.Argv[1] == "test" && command.Argv[2] == "./..." {
			policy.focusedGoTest = true
		}
	}
	projected, commandViolation := projectDeclaredCommands(projected, policy)
	return projected, violated || commandViolation, nil
}

func choraEnforcesDeclaredPiCommands(capabilities domain.CapabilityEnvelope) bool {
	return !capabilities[speccoding.LocalConnectedNoSandboxCapability] && !capabilities[speccoding.IsolatedLocalCapability]
}

func agentReportedWithoutVerifier(capabilities domain.CapabilityEnvelope) bool {
	return capabilities[speccoding.LocalConnectedNoSandboxCapability] || capabilities[speccoding.IsolatedLocalCapability]
}

func validateSnapshotLineage(ctx context.Context, reader storecontract.Reader, attempt domain.Attempt, binding storecontract.SpecCodingBinding) error {
	currentID, currentDigest := attempt.ContextSnapshotID(), attempt.ContextDigest()
	for depth := 0; depth <= int(attempt.Sequence()); depth++ {
		if currentID == binding.SnapshotID {
			if currentDigest != binding.SnapshotDigest {
				return fmt.Errorf("registered Spec Coding Snapshot lineage digest drifted")
			}
			return nil
		}
		snapshot, err := reader.GetSnapshot(ctx, currentID)
		if err != nil || snapshot.Digest() != currentDigest || sha256.Sum256(snapshot.CanonicalJSON()) != currentDigest {
			return fmt.Errorf("registered Spec Coding Snapshot lineage is unavailable")
		}
		var document struct {
			Task struct {
				ID string `json:"id"`
			} `json:"task"`
			Delta *struct {
				PredecessorSnapshotID string `json:"predecessor_snapshot_id"`
				PredecessorDigest     string `json:"predecessor_digest"`
			} `json:"delta"`
		}
		if json.Unmarshal(snapshot.CanonicalJSON(), &document) != nil || document.Task.ID != binding.TaskID.String() || document.Delta == nil {
			return fmt.Errorf("registered Spec Coding Snapshot lineage is invalid")
		}
		currentID, err = domain.ParseContextSnapshotID(document.Delta.PredecessorSnapshotID)
		if err != nil {
			return fmt.Errorf("registered Spec Coding Snapshot predecessor identity is invalid")
		}
		rawDigest, err := hex.DecodeString(document.Delta.PredecessorDigest)
		if err != nil || len(rawDigest) != sha256.Size {
			return fmt.Errorf("registered Spec Coding Snapshot predecessor digest is invalid")
		}
		copy(currentDigest[:], rawDigest)
	}
	return fmt.Errorf("registered Spec Coding Snapshot lineage did not reach its frozen root")
}

type declaredCommandPolicy struct {
	exact         map[string]struct{}
	gofmtFiles    map[string]struct{}
	focusedGoTest bool
}

type agentClaimEvent struct {
	typ        string
	normalized []byte
}

type agentCommandClaim struct {
	status     execution.CheckStatus
	evidence   string
	generation int
}

type activeAgentCommand struct {
	commandID  string
	generation int
}

// derivePiAgentReportedChecks converts only exact, contract-declared
// verification command executions into AgentReport claims. These observations
// are intentionally not verifier evidence: callers place them only in
// TerminalResult.Checks, which is the AgentReport input.
func derivePiAgentReportedChecks(document speccoding.CoreContractDocument, events []agentClaimEvent) []execution.DeclaredCheck {
	type commandAuthority struct {
		id string
	}
	summaryAuthorities := make(map[string][]commandAuthority, len(document.Acceptance.VerificationCommands))
	digestAuthorities := make(map[string][]commandAuthority, len(document.Acceptance.VerificationCommands))
	states := make(map[string]agentCommandClaim, len(document.Acceptance.VerificationCommands))
	for _, command := range document.Acceptance.VerificationCommands {
		text, err := speccoding.CanonicalCommandText(command)
		if err != nil {
			continue
		}
		digest := sha256.Sum256([]byte(text))
		digestHex := hex.EncodeToString(digest[:])
		authority := commandAuthority{id: command.ID}
		summaryAuthorities[text] = append(summaryAuthorities[text], authority)
		digestAuthorities[digestHex] = append(digestAuthorities[digestHex], authority)
		states[command.ID] = agentCommandClaim{status: execution.CheckUnknown, evidence: command.ID + " was not observed"}
	}

	active := make(map[string][]activeAgentCommand)
	for _, event := range events {
		var value struct {
			ToolName      string  `json:"tool_name"`
			ToolCallID    string  `json:"tool_call_id"`
			Command       string  `json:"redacted_command_summary"`
			CommandDigest *string `json:"command_digest"`
			ExitCode      *int64  `json:"exit_code"`
			ToolError     *bool   `json:"tool_error"`
		}
		if json.Unmarshal(event.normalized, &value) != nil {
			continue
		}
		switch event.typ {
		case "tool_start":
			if value.ToolName != "bash" {
				continue
			}
			commands := summaryAuthorities[value.Command]
			if value.CommandDigest != nil {
				// A projected digest is authoritative for correlation even when it
				// is malformed or unknown. Falling back to the redacted/truncated
				// summary would let a digest-bearing event claim another command.
				commands = digestAuthorities[*value.CommandDigest]
			}
			if len(commands) == 0 {
				continue
			}
			started := make([]activeAgentCommand, 0, len(commands))
			for _, command := range commands {
				state := states[command.id]
				state.generation++
				state.status = execution.CheckUnknown
				state.evidence = command.id + " has no correlatable end event"
				states[command.id] = state
				started = append(started, activeAgentCommand{commandID: command.id, generation: state.generation})
			}
			if strings.TrimSpace(value.ToolCallID) != "" {
				active[value.ToolCallID] = append(active[value.ToolCallID], started...)
			}
		case "tool_end":
			if strings.TrimSpace(value.ToolCallID) == "" {
				continue
			}
			started := active[value.ToolCallID]
			delete(active, value.ToolCallID)
			for _, executionStart := range started {
				state := states[executionStart.commandID]
				if state.generation != executionStart.generation {
					continue
				}
				switch {
				case value.ToolError != nil && *value.ToolError:
					state.status = execution.CheckFail
					state.evidence = executionStart.commandID + " reported a tool error"
				case value.ExitCode == nil && value.ToolError != nil && !*value.ToolError:
					state.status = execution.CheckPass
					state.evidence = executionStart.commandID + " completed successfully according to Pi's tool result; numeric exit code unavailable"
				case value.ExitCode == nil:
					state.status = execution.CheckUnknown
					state.evidence = executionStart.commandID + " ended without an exit code"
				case *value.ExitCode == 0:
					state.status = execution.CheckPass
					state.evidence = executionStart.commandID + " exited 0"
				default:
					state.status = execution.CheckFail
					state.evidence = fmt.Sprintf("%s exited %d", executionStart.commandID, *value.ExitCode)
				}
				states[executionStart.commandID] = state
			}
		}
	}

	checks := make([]execution.DeclaredCheck, 0, len(document.Acceptance.Criteria))
	for _, criterion := range document.Acceptance.Criteria {
		status := execution.CheckPass
		if len(criterion.VerificationCommandIDs) == 0 {
			status = execution.CheckUnknown
		}
		parts := make([]string, 0, len(criterion.VerificationCommandIDs))
		for _, commandID := range criterion.VerificationCommandIDs {
			state, ok := states[commandID]
			if !ok {
				state = agentCommandClaim{status: execution.CheckUnknown, evidence: commandID + " has no declared command authority"}
			}
			parts = append(parts, state.evidence)
			if state.status == execution.CheckFail {
				status = execution.CheckFail
			} else if state.status == execution.CheckUnknown && status != execution.CheckFail {
				status = execution.CheckUnknown
			}
		}
		if len(criterion.VerificationCommandIDs) == 0 {
			parts = append(parts, "Not run / Unverified: no checks were selected")
		}
		checks = append(checks, execution.DeclaredCheck{
			CriterionID: criterion.ID,
			Status:      status,
			Evidence:    "Agent-reported, non-authoritative: " + strings.Join(parts, "; "),
		})
	}
	return checks
}

func (s *Service) populatePiAgentReportedChecks(ctx context.Context, runtime runtimeContext, terminal execution.TerminalResult) (execution.TerminalResult, error) {
	profile := runtime.attempt.AgentExecutionProfileBinding().Profile()
	if runtime.adapter.ID() != "pi" || (profile != domain.AgentExecutionProfileTrustedLocal && profile != domain.AgentExecutionProfileIsolatedLocal) || terminal.Kind == execution.TerminalFailed {
		return terminal, nil
	}
	binding, err := s.deps.Store.Reader().GetSpecCodingBinding(ctx, runtime.run.TaskID())
	if err != nil {
		return execution.TerminalResult{}, err
	}
	contract, err := speccoding.DecodeCoreContract(binding.ActiveContractJSON)
	if err != nil {
		return execution.TerminalResult{}, fmt.Errorf("decode Local Connected Agent claim authority: %w", err)
	}
	document := contract.Document()
	if !localConnectedReviewDocument(document) {
		return terminal, nil
	}
	persisted, err := s.deps.Store.Reader().ListRunEvents(ctx, runtime.run.ID())
	if err != nil {
		return execution.TerminalResult{}, err
	}
	// A Run may contain earlier retry Attempts. Only events after the current
	// (latest) attempt.started boundary may contribute to this AgentReport.
	attemptStart := -1
	for index := range persisted {
		if persisted[index].Type() == "attempt.started" {
			attemptStart = index
		}
	}
	if attemptStart >= 0 {
		persisted = persisted[attemptStart+1:]
	} else {
		persisted = nil
	}
	events := make([]agentClaimEvent, 0, len(persisted))
	for _, event := range persisted {
		if event.Source() == "adapter" {
			events = append(events, agentClaimEvent{typ: event.Type(), normalized: event.NormalizedJSON()})
		}
	}
	terminal.Checks = derivePiAgentReportedChecks(document, events)
	return terminal, nil
}

func projectDeclaredCommands(events []execution.NormalizedEvent, policy declaredCommandPolicy) ([]execution.NormalizedEvent, bool) {
	projected := append([]execution.NormalizedEvent(nil), events...)
	violated := false
	for index, event := range projected {
		if event.Type != "tool_start" {
			continue
		}
		var value struct {
			ToolName string `json:"tool_name"`
			Command  string `json:"redacted_command_summary"`
		}
		if json.Unmarshal(event.NormalizedJSON, &value) != nil || value.ToolName != "bash" {
			continue
		}
		if declaredCommandAllowed(value.Command, policy) {
			continue
		}
		normalized, _ := json.Marshal(map[string]string{"event_type": "error", "error_class": "undeclared_command"})
		projected[index] = execution.NormalizedEvent{Type: "error", OccurredAt: event.OccurredAt, NormalizedJSON: normalized}
		violated = true
	}
	return projected, violated
}

func declaredCommandAllowed(command string, policy declaredCommandPolicy) bool {
	command = strings.TrimSpace(command)
	if command == "" {
		return false
	}
	if _, ok := policy.exact[command]; ok {
		return true
	}
	argv := strings.Fields(command)
	if len(argv) == 0 || strings.Join(argv, " ") != command {
		return false
	}
	if len(argv) > 2 && argv[0] == "gofmt" && argv[1] == "-w" {
		for _, file := range argv[2:] {
			if _, ok := policy.gofmtFiles[file]; !ok {
				return false
			}
		}
		return true
	}
	if policy.focusedGoTest && len(argv) > 2 && argv[0] == "go" && argv[1] == "test" {
		for _, packagePattern := range argv[2:] {
			if !safeFocusedGoPackage(packagePattern) {
				return false
			}
		}
		return true
	}
	return false
}

func safeFocusedGoPackage(value string) bool {
	if !strings.HasPrefix(value, "./") || value == "./" || strings.ContainsAny(value, "\\\t\r\n ;|&><`$(){}[]*?!'\"") {
		return false
	}
	relative := strings.TrimPrefix(value, "./")
	cleaned := path.Clean(relative)
	return cleaned == relative && cleaned != "." && cleaned != ".." && !strings.HasPrefix(cleaned, "../")
}

func appendNormalized(ctx context.Context, tx storecontract.WriteTx, ids IDGenerator, runID domain.RunID, session *storecontract.RuntimeSession, events []execution.NormalizedEvent, now time.Time) error {
	for _, event := range events {
		if event.ExternalSession != "" {
			if session.ExternalReference != "" && session.ExternalReference != event.ExternalSession {
				return fmt.Errorf("%w: external session reference changed", ErrInvalidCommand)
			}
			if session.ExternalReference == "" {
				session.ExternalReference = event.ExternalSession
				session.Version++
				session.UpdatedAt = now
			}
		}
		occurred := event.OccurredAt
		if occurred.IsZero() || occurred.After(now) {
			occurred = now
		}
		normalized := event.NormalizedJSON
		if len(normalized) == 0 {
			normalized = []byte(`{}`)
		}
		if _, err := tx.AppendRunEvent(ctx, runID, storecontract.EventDraft{ID: ids.EventID(), Type: event.Type, Source: "adapter", OccurredAt: occurred, RecordedAt: now, NormalizedJSON: normalized, RawJSON: event.RawJSON}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) drainToEOF(ctx context.Context, runtime runtimeContext, handle execution.RuntimeHandle) (execution.TerminalFiles, error) {
	var terminal execution.TerminalFiles
	for {
		offsets := execution.StreamOffsets{}
		persisted := make(map[execution.StreamKind]storecontract.RuntimeStreamOffset, 2)
		allPersistedEOF := true
		for _, kind := range []execution.StreamKind{execution.StreamStdout, execution.StreamStderr} {
			stream, _ := storeStream(kind)
			offset, err := s.deps.Store.Reader().GetRuntimeStreamOffset(ctx, runtime.session.ID, stream)
			if err != nil {
				return execution.TerminalFiles{}, err
			}
			persisted[kind] = offset
			offsets[kind] = offset.Offset
			if !offset.EOF {
				allPersistedEOF = false
			}
		}
		drained, err := s.deps.Supervisor.Drain(ctx, handle, offsets, runtimeDrainLimit)
		if err != nil {
			return execution.TerminalFiles{}, err
		}
		if allPersistedEOF {
			return drained.TerminalFiles, nil
		}
		total := 0
		progress := false
		decoded := make(map[execution.StreamKind]execution.DecodedChunk, 2)
		committedOffsets := make(map[execution.StreamKind]int64, 2)
		committedEOF := make(map[execution.StreamKind]bool, 2)
		for _, kind := range []execution.StreamKind{execution.StreamStdout, execution.StreamStderr} {
			offset := persisted[kind]
			if offset.EOF {
				continue
			}
			data := drained.Chunks[kind]
			total += len(data)
			next, ok := drained.Offsets[kind]
			if !ok || next != offset.Offset+int64(len(data)) {
				return execution.TerminalFiles{}, fmt.Errorf("invalid drain offset")
			}
			result, err := runtime.adapter.DecodeEvent(execution.EventChunk{Profile: runtime.attempt.AgentExecutionProfileBinding().Profile(), WorkingRoot: runtime.eventWorkingRoot(), Stream: kind, Offset: offset.Offset, Data: data, EOF: drained.EOF[kind]})
			if errors.Is(err, execution.ErrEventStreamInvalid) {
				// Death was independently proven before draining. Malformed provider
				// output must fail the Attempt, not prevent its terminal transition.
				// Persist the failure with the consumed offset so retries/restarts
				// cannot promote a successful result after discarding these bytes.
				result = execution.DecodedChunk{ConsumedBytes: len(data), Events: []execution.NormalizedEvent{{
					Type: "error", NormalizedJSON: []byte(`{"event_type":"error","error_class":"runtime_stream_invalid"}`),
				}}}
			} else if err != nil {
				return execution.TerminalFiles{}, err
			}
			if result.ConsumedBytes < 0 || result.ConsumedBytes > len(data) || drained.EOF[kind] && result.ConsumedBytes != len(data) {
				return execution.TerminalFiles{}, fmt.Errorf("invalid adapter consumed byte count")
			}
			decoded[kind] = result
			committedOffsets[kind] = offset.Offset + int64(result.ConsumedBytes)
			committedEOF[kind] = drained.EOF[kind] && result.ConsumedBytes == len(data)
			if result.ConsumedBytes > 0 || committedEOF[kind] {
				progress = true
			}
		}
		if total > runtimeDrainLimit {
			return execution.TerminalFiles{}, fmt.Errorf("drain chunk exceeds limit")
		}
		if !progress {
			return execution.TerminalFiles{}, fmt.Errorf("drain made no progress")
		}
		now := s.deps.Clock.Now()
		if err := s.deps.Store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
			session, err := tx.GetRuntimeSession(ctx, runtime.session.ID)
			if err != nil {
				return err
			}
			sessionVersion := session.Version
			for _, kind := range []execution.StreamKind{execution.StreamStdout, execution.StreamStderr} {
				offset := persisted[kind]
				if offset.EOF {
					continue
				}
				if err := appendNormalized(ctx, tx, s.deps.IDs, runtime.run.ID(), &session, decoded[kind].Events, now); err != nil {
					return err
				}
				stream, _ := storeStream(kind)
				if err := tx.AdvanceRuntimeStreamOffset(ctx, runtime.session.ID, stream, offset.Offset, committedOffsets[kind], committedEOF[kind]); err != nil {
					return err
				}
			}
			if session.Version != sessionVersion {
				if err := tx.SaveRuntimeSessionCAS(ctx, sessionVersion, session); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return execution.TerminalFiles{}, err
		}
		// Real Pi RPC keeps running until stdin reaches EOF. Once the terminal
		// signal (agent_settled) is observed, close stdin so the process exits
		// cleanly and the drain reaches EOF.
		if s.deps.Supervisor != nil {
			for _, kind := range []execution.StreamKind{execution.StreamStdout, execution.StreamStderr} {
				for _, event := range decoded[kind].Events {
					if event.Type == "agent_settled" {
						if closer, ok := s.deps.Supervisor.(execution.StdinCloser); ok {
							_ = closer.CloseStdin(ctx, handle)
						}
					}
				}
			}
		}
		terminal = drained.TerminalFiles
		done := true
		for _, kind := range []execution.StreamKind{execution.StreamStdout, execution.StreamStderr} {
			if !persisted[kind].EOF && !committedEOF[kind] {
				done = false
			}
		}
		if done {
			return terminal, nil
		}
	}
}

func (s *Service) HandleExit(ctx context.Context, request ExitRequest) (SubmitTerminalResult, error) {
	runtime, err := s.runtimeContext(ctx, request.SessionID)
	if err != nil {
		return SubmitTerminalResult{}, err
	}
	if err := s.authorize(ctx, request.CommandMeta, "handle_exit", request.SessionID.String(), request.ExpectedVersion); err != nil {
		return SubmitTerminalResult{}, err
	}
	unlock := s.runLock(runtime.run.ID())
	defer unlock()
	now := s.deps.Clock.Now()
	key, err := s.commandKey(request.CommandMeta, "handle_exit", request.SessionID.String(), request.ExpectedVersion, struct{}{}, now)
	if err != nil {
		return SubmitTerminalResult{}, err
	}
	var replay *SubmitTerminalResult
	err = s.deps.Store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		response, found, err := tx.LookupCommand(ctx, key)
		if err != nil {
			return err
		}
		if !found {
			return nil
		}
		value, err := replayTerminal(response)
		if err != nil {
			return err
		}
		replay = &value
		return nil
	})
	if err != nil {
		return SubmitTerminalResult{}, err
	}
	if replay != nil {
		return *replay, nil
	}
	launch := execution.LaunchToken{Value: runtime.session.LaunchToken}
	if !s.consumeExitProof(request.SessionID, launch) {
		return SubmitTerminalResult{}, fmt.Errorf("%w: exit requires trusted runtime signal", ErrInvalidCommand)
	}
	reconciled, err := s.deps.Supervisor.Reconcile(ctx, execution.ProcessIdentity{Value: runtime.session.ProcessIdentity})
	if err != nil {
		return SubmitTerminalResult{}, err
	}
	handle, err := reconcileHandle(reconciled, launch, execution.ReconcileDead)
	if err != nil {
		return SubmitTerminalResult{}, err
	}
	terminalFiles, err := s.drainToEOF(ctx, runtime, handle)
	if err != nil {
		return SubmitTerminalResult{}, err
	}
	terminalFiles.Profile = runtime.attempt.AgentExecutionProfileBinding().Profile()
	events, err := s.deps.Store.Reader().ListRunEvents(ctx, runtime.run.ID())
	if err != nil {
		return SubmitTerminalResult{}, err
	}
	terminal := execution.TerminalResult{}
	if attemptHasRuntimeFailure(events, "runtime_stream_invalid") && terminalFiles.TerminationCause != execution.TerminationOutputLimit {
		terminal = execution.TerminalResult{Kind: execution.TerminalFailed, FailureReason: "runtime_stream_invalid"}
	} else {
		switch terminalFiles.TerminationCause {
		case execution.TerminationOutputLimit:
			terminal = execution.TerminalResult{Kind: execution.TerminalFailed, FailureReason: "runtime_output_limit_exceeded"}
		case execution.TerminationDeadlineExceeded:
			terminal = execution.TerminalResult{Kind: execution.TerminalFailed, FailureReason: "attempt_timeout"}
		case execution.TerminationExitNonzero:
			terminal = execution.TerminalResult{Kind: execution.TerminalFailed, FailureReason: "runtime_exit_nonzero"}
		case execution.TerminationNone, "":
			if terminalFiles.ExitCode != 0 {
				// Compatibility for non-managed supervisors that have not adopted the
				// typed cause yet. Managed Docker always returns a trusted cause.
				terminal = execution.TerminalResult{Kind: execution.TerminalFailed, FailureReason: "runtime_exit_nonzero"}
				break
			}
			terminal, err = runtime.adapter.DecodeTerminal(terminalFiles)
			if errors.Is(err, execution.ErrResultContractInvalid) {
				terminal = execution.TerminalResult{Kind: execution.TerminalFailed, FailureReason: "result_contract_invalid"}
				err = nil
			}
			if err != nil {
				return SubmitTerminalResult{}, err
			}
		default:
			return SubmitTerminalResult{}, fmt.Errorf("%w: invalid trusted termination cause", ErrInvalidCommand)
		}
	}
	if terminal.Kind != execution.TerminalFailed && attemptHasModelFailure(events) {
		terminal = execution.TerminalResult{Kind: execution.TerminalFailed, FailureReason: "agent_model_error"}
	}
	if terminal.Kind != execution.TerminalFailed && runtime.adapter.ID() == "pi" && runtime.attempt.AgentExecutionProfileBinding().RuntimeSource() == "local_pi" {
		if !hasCompletePiResult(events) {
			terminal = execution.TerminalResult{Kind: execution.TerminalFailed, FailureReason: "result_contract_invalid"}
		}
	}
	var resourceGroup *domain.ResourceResultGroup
	if _, resourceErr := s.deps.Store.Reader().GetTaskResourceSnapshot(ctx, runtime.run.TaskID()); resourceErr == nil {
		terminal, resourceGroup, err = s.collectResourceTerminal(ctx, runtime, terminal)
		if err != nil {
			return SubmitTerminalResult{}, err
		}
	} else if !errors.Is(resourceErr, storecontract.ErrNotFound) {
		return SubmitTerminalResult{}, resourceErr
	}
	terminal, err = s.populatePiAgentReportedChecks(ctx, runtime, terminal)
	if err != nil {
		return SubmitTerminalResult{}, err
	}
	if !terminal.Valid() {
		terminal = execution.TerminalResult{Kind: execution.TerminalFailed, FailureReason: "result_contract_invalid"}
	}
	if resourceGroup == nil && terminal.Kind == execution.TerminalReviewReady && s.deps.PatchMaterializer != nil && !terminalHasReviewPatch(terminal) {
		artifact, materializeErr := s.deps.PatchMaterializer.MaterializeReviewPatch(ctx, ReviewPatchMaterializationRequest{
			RunID: runtime.run.ID(), TaskID: runtime.run.TaskID(), AttemptID: runtime.attempt.ID(), WorkingRoot: runtime.session.WorkingRoot,
		})
		if errors.Is(materializeErr, ErrNoReviewableChanges) && strings.TrimSpace(terminal.Summary) != "" {
			binding, bindingErr := s.deps.Store.Reader().GetSpecCodingBinding(ctx, runtime.run.TaskID())
			if bindingErr != nil {
				return SubmitTerminalResult{}, bindingErr
			}
			contract, contractErr := speccoding.DecodeCoreContract(binding.ActiveContractJSON)
			if contractErr != nil {
				return SubmitTerminalResult{}, contractErr
			}
			terminal.Kind = noChangeOutcome(terminal.Checks, contract.Document().Acceptance.NoChecks)
			if terminal.Kind != execution.TerminalCompletedNoChange {
				terminal.FailureReason = string(terminal.Kind)
			}
		} else if materializeErr != nil {
			terminal = execution.TerminalResult{Kind: execution.TerminalFailed, FailureReason: "review_patch_materialization_failed"}
		} else {
			terminal.Outputs = append(terminal.Outputs, artifact)
		}
	}
	if err := s.deps.Supervisor.Finalize(ctx, handle, execution.RetentionPolicy{}); err != nil {
		return SubmitTerminalResult{}, err
	}
	var result SubmitTerminalResult
	err = s.deps.Store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		response, found, err := tx.LookupCommand(ctx, key)
		if err != nil {
			return err
		}
		if found {
			result, err = replayTerminal(response)
			return err
		}
		session, err := tx.GetRuntimeSession(ctx, request.SessionID)
		if err != nil {
			return err
		}
		attempt, err := tx.GetAttempt(ctx, session.AttemptID)
		if err != nil {
			return err
		}
		run, err := tx.GetRun(ctx, attempt.RunID())
		if err != nil {
			return err
		}
		if run.Version() != request.ExpectedVersion {
			return storecontract.ErrVersionConflict
		}
		result, err = s.submitTerminalTx(ctx, tx, request.CommandMeta, run, attempt, request.ExpectedVersion, terminal, now, resourceGroup)
		if err != nil {
			return err
		}
		sessionVersion := session.Version
		session.Version++
		session.State = "stopped"
		session.UpdatedAt = now
		session.TerminalAt = now
		session.FinalizedAt = now
		if err := tx.SaveRuntimeSessionCAS(ctx, sessionVersion, session); err != nil {
			return err
		}
		response, err = terminalResponse(result)
		if err != nil {
			return err
		}
		return tx.SaveCommand(ctx, key, response)
	})
	if err != nil {
		return SubmitTerminalResult{}, err
	}
	s.cleanupRuntimeSignals(request.SessionID)
	if s.deps.AutoStartVerification && result.AgentReportID != nil && result.Run.State() == domain.RunStateAwaitingVerification {
		go s.autoStartVerification(result.Run)
	}
	return result, nil
}

func attemptHasModelFailure(events []domain.RunEvent) bool {
	return attemptHasRuntimeFailure(events, "model_error")
}

func attemptHasRuntimeFailure(events []domain.RunEvent, errorClass string) bool {
	start := -1
	for index := range events {
		if events[index].Type() == "attempt.started" {
			start = index
		}
	}
	if start < 0 {
		return false
	}
	for _, event := range events[start+1:] {
		if event.Type() != "error" || event.Source() != "adapter" {
			continue
		}
		var payload struct {
			EventType  string `json:"event_type"`
			ErrorClass string `json:"error_class"`
		}
		if json.Unmarshal(event.NormalizedJSON(), &payload) == nil && payload.EventType == "error" && payload.ErrorClass == errorClass {
			return true
		}
	}
	return false
}

func terminalHasReviewPatch(terminal execution.TerminalResult) bool {
	for _, artifact := range terminal.Outputs {
		if artifact.MediaType == "text/x-diff" && artifact.SHA256 != "" {
			return true
		}
	}
	return false
}

func (s *Service) finalizeRuntime(ctx context.Context, sessionID domain.RuntimeSessionID) error {
	session, err := s.deps.Store.Reader().GetRuntimeSession(ctx, sessionID)
	if err != nil {
		return err
	}
	if !session.FinalizedAt.IsZero() {
		return nil
	}
	launch := execution.LaunchToken{Value: session.LaunchToken}
	var reconciled execution.ReconcileOutcome
	if session.ProcessIdentity == "" {
		reconciled, err = s.deps.Supervisor.ReconcileLaunch(ctx, launch)
	} else {
		reconciled, err = s.deps.Supervisor.Reconcile(ctx, execution.ProcessIdentity{Value: session.ProcessIdentity})
	}
	if err != nil {
		return err
	}
	handle, err := reconcileHandle(reconciled, launch, execution.ReconcileDead)
	if err != nil {
		return err
	}
	if err := s.deps.Supervisor.Finalize(ctx, handle, execution.RetentionPolicy{}); err != nil {
		return err
	}
	return s.markFinalized(ctx, sessionID)
}
func (s *Service) markFinalized(ctx context.Context, sessionID domain.RuntimeSessionID) error {
	now := s.deps.Clock.Now()
	err := s.deps.Store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		session, err := tx.GetRuntimeSession(ctx, sessionID)
		if err != nil {
			return err
		}
		if !session.FinalizedAt.IsZero() {
			return nil
		}
		version := session.Version
		session.Version++
		session.UpdatedAt = now
		session.FinalizedAt = now
		return tx.SaveRuntimeSessionCAS(ctx, version, session)
	})
	if err == nil {
		s.cleanupRuntimeSignals(sessionID)
	}
	return err
}

// Event paths are normalized against the execution boundary selected by Chora,
// rather than a provider-supplied cwd or the host writeback workspace.
func (r runtimeContext) eventWorkingRoot() string {
	if r.attempt.AgentExecutionProfileBinding().Profile() == domain.AgentExecutionProfileIsolatedLocal {
		return "/workspace/repository"
	}
	return r.session.WorkingRoot
}
