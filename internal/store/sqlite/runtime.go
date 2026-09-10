package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

func (tx *writeTx) InsertArtifact(ctx context.Context, artifact storecontract.Artifact) error {
	if err := tx.requireActiveRoomForRun(ctx, artifact.RunID); err != nil {
		return err
	}
	if artifact.Role == "" {
		artifact.Role = "output"
	}
	var attempt any
	if artifact.AttemptID != nil {
		attempt = artifact.AttemptID.String()
	}
	var digest any
	if artifact.Digest != nil {
		digest = artifact.Digest[:]
	}
	var event any
	if artifact.SourceEventID != nil {
		event = artifact.SourceEventID.String()
	}
	_, err := tx.tx.ExecContext(ctx, `INSERT INTO artifacts(id,run_id,attempt_id,kind,locator,digest,media_type,created_at,description,position,role,source_event_id) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, artifact.ID.String(), artifact.RunID.String(), attempt, artifact.Kind, artifact.Locator, digest, artifact.MediaType, timeText(artifact.CreatedAt), artifact.Description, artifact.Position, artifact.Role, event)
	return mapWriteError(err)
}

func nullableAttemptID(id *domain.AttemptID) any {
	if id == nil {
		return nil
	}
	return id.String()
}
func parseAttemptID(value sql.NullString) (*domain.AttemptID, error) {
	if !value.Valid {
		return nil, nil
	}
	id, err := domain.ParseAttemptID(value.String)
	if err != nil {
		return nil, err
	}
	return &id, nil
}
func parseEventID(value sql.NullString) (*domain.EventID, error) {
	if !value.Valid {
		return nil, nil
	}
	id, err := domain.ParseEventID(value.String)
	if err != nil {
		return nil, err
	}
	return &id, nil
}

func (tx *writeTx) InsertObservation(ctx context.Context, value storecontract.Observation) error {
	if err := tx.requireActiveRoomForRun(ctx, value.RunID); err != nil {
		return err
	}
	var event any
	if value.SourceEventID != nil {
		event = value.SourceEventID.String()
	}
	_, err := tx.tx.ExecContext(ctx, `INSERT INTO observations(id,run_id,attempt_id,kind,body,source_event_id,created_at,position,role) VALUES(?,?,?,?,?,?,?,?,?)`, value.ID.String(), value.RunID.String(), nullableAttemptID(value.AttemptID), value.Kind, value.Body, event, timeText(value.CreatedAt), value.Position, value.Role)
	return mapWriteError(err)
}
func (tx *writeTx) InsertCheck(ctx context.Context, value storecontract.Check) error {
	if err := tx.requireActiveRoomForRun(ctx, value.RunID); err != nil {
		return err
	}
	var event any
	if value.SourceEventID != nil {
		event = value.SourceEventID.String()
	}
	_, err := tx.tx.ExecContext(ctx, `INSERT INTO checks(id,run_id,attempt_id,criterion_id,position,name,status,evidence,source_event_id,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, value.ID.String(), value.RunID.String(), nullableAttemptID(value.AttemptID), value.CriterionID, value.Position, value.Name, value.Status, value.Evidence, event, timeText(value.CreatedAt))
	return mapWriteError(err)
}
func (tx *writeTx) InsertIntervention(ctx context.Context, value storecontract.Intervention) error {
	if err := tx.requireActiveRoomForRun(ctx, value.RunID); err != nil {
		return err
	}
	_, err := tx.tx.ExecContext(ctx, `INSERT INTO interventions(id,run_id,attempt_id,kind,reason,requested_by,created_at,resolved_at) VALUES(?,?,?,?,?,?,?,?)`, value.ID.String(), value.RunID.String(), nullableAttemptID(value.AttemptID), value.Kind, value.Reason, value.RequestedBy, timeText(value.CreatedAt), nullableTime(value.ResolvedAt))
	return mapWriteError(err)
}
func (tx *writeTx) InsertHandoff(ctx context.Context, value storecontract.Handoff) error {
	if err := tx.requireActiveRoomForRun(ctx, value.RunID); err != nil {
		return err
	}
	var event any
	if value.SourceEventID != nil {
		event = value.SourceEventID.String()
	}
	_, err := tx.tx.ExecContext(ctx, `INSERT INTO handoffs(id,run_id,attempt_id,from_actor,to_actor,reason,status,created_at,resolved_at,source_event_id) VALUES(?,?,?,?,?,?,?,?,?,?)`, value.ID.String(), value.RunID.String(), nullableAttemptID(value.AttemptID), value.FromActor, value.ToActor, value.Reason, value.Status, timeText(value.CreatedAt), nullableTime(value.ResolvedAt), event)
	return mapWriteError(err)
}

func (reader *reader) GetObservation(ctx context.Context, id domain.ObservationID) (storecontract.Observation, error) {
	var idText, runText, kind, body, created string
	var attempt, event sql.NullString
	var position int
	var role string
	err := reader.q.QueryRowContext(ctx, `SELECT id,run_id,attempt_id,kind,body,source_event_id,created_at,position,role FROM observations WHERE id=?`, id.String()).Scan(&idText, &runText, &attempt, &kind, &body, &event, &created, &position, &role)
	if errors.Is(err, sql.ErrNoRows) {
		return storecontract.Observation{}, storecontract.ErrNotFound
	}
	if err != nil {
		return storecontract.Observation{}, err
	}
	parsedID, err := domain.ParseObservationID(idText)
	if err != nil {
		return storecontract.Observation{}, err
	}
	runID, err := domain.ParseRunID(runText)
	if err != nil {
		return storecontract.Observation{}, err
	}
	attemptID, err := parseAttemptID(attempt)
	if err != nil {
		return storecontract.Observation{}, err
	}
	eventID, err := parseEventID(event)
	if err != nil {
		return storecontract.Observation{}, err
	}
	createdAt, err := parseTime(created)
	if err != nil {
		return storecontract.Observation{}, err
	}
	return storecontract.Observation{ID: parsedID, RunID: runID, AttemptID: attemptID, Kind: kind, Body: body, SourceEventID: eventID, Position: position, Role: role, CreatedAt: createdAt}, nil
}
func (reader *reader) GetCheck(ctx context.Context, id domain.CheckID) (storecontract.Check, error) {
	var idText, runText, criterionID, name, status, evidence, created string
	var attempt, event sql.NullString
	var position int
	err := reader.q.QueryRowContext(ctx, `SELECT id,run_id,attempt_id,criterion_id,position,name,status,evidence,source_event_id,created_at FROM checks WHERE id=?`, id.String()).Scan(&idText, &runText, &attempt, &criterionID, &position, &name, &status, &evidence, &event, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return storecontract.Check{}, storecontract.ErrNotFound
	}
	if err != nil {
		return storecontract.Check{}, err
	}
	parsedID, err := domain.ParseCheckID(idText)
	if err != nil {
		return storecontract.Check{}, err
	}
	runID, err := domain.ParseRunID(runText)
	if err != nil {
		return storecontract.Check{}, err
	}
	attemptID, err := parseAttemptID(attempt)
	if err != nil {
		return storecontract.Check{}, err
	}
	eventID, err := parseEventID(event)
	if err != nil {
		return storecontract.Check{}, err
	}
	createdAt, err := parseTime(created)
	if err != nil {
		return storecontract.Check{}, err
	}
	return storecontract.Check{ID: parsedID, RunID: runID, AttemptID: attemptID, CriterionID: criterionID, Position: position, Name: name, Status: status, Evidence: evidence, SourceEventID: eventID, CreatedAt: createdAt}, nil
}
func (reader *reader) GetIntervention(ctx context.Context, id domain.InterventionID) (storecontract.Intervention, error) {
	var idText, runText, kind, reason, requested, created string
	var attempt, resolved sql.NullString
	err := reader.q.QueryRowContext(ctx, `SELECT id,run_id,attempt_id,kind,reason,requested_by,created_at,resolved_at FROM interventions WHERE id=?`, id.String()).Scan(&idText, &runText, &attempt, &kind, &reason, &requested, &created, &resolved)
	if errors.Is(err, sql.ErrNoRows) {
		return storecontract.Intervention{}, storecontract.ErrNotFound
	}
	if err != nil {
		return storecontract.Intervention{}, err
	}
	parsedID, err := domain.ParseInterventionID(idText)
	if err != nil {
		return storecontract.Intervention{}, err
	}
	runID, err := domain.ParseRunID(runText)
	if err != nil {
		return storecontract.Intervention{}, err
	}
	attemptID, err := parseAttemptID(attempt)
	if err != nil {
		return storecontract.Intervention{}, err
	}
	createdAt, err := parseTime(created)
	if err != nil {
		return storecontract.Intervention{}, err
	}
	resolvedAt, err := parseNullableTime(resolved)
	if err != nil {
		return storecontract.Intervention{}, err
	}
	return storecontract.Intervention{ID: parsedID, RunID: runID, AttemptID: attemptID, Kind: kind, Reason: reason, RequestedBy: requested, CreatedAt: createdAt, ResolvedAt: resolvedAt}, nil
}
func (reader *reader) GetHandoff(ctx context.Context, id domain.HandoffID) (storecontract.Handoff, error) {
	var idText, runText, fromActor, toActor, reason, status, created string
	var attempt, resolved, event sql.NullString
	err := reader.q.QueryRowContext(ctx, `SELECT id,run_id,attempt_id,from_actor,to_actor,reason,status,created_at,resolved_at,source_event_id FROM handoffs WHERE id=?`, id.String()).Scan(&idText, &runText, &attempt, &fromActor, &toActor, &reason, &status, &created, &resolved, &event)
	if errors.Is(err, sql.ErrNoRows) {
		return storecontract.Handoff{}, storecontract.ErrNotFound
	}
	if err != nil {
		return storecontract.Handoff{}, err
	}
	parsedID, err := domain.ParseHandoffID(idText)
	if err != nil {
		return storecontract.Handoff{}, err
	}
	runID, err := domain.ParseRunID(runText)
	if err != nil {
		return storecontract.Handoff{}, err
	}
	attemptID, err := parseAttemptID(attempt)
	if err != nil {
		return storecontract.Handoff{}, err
	}
	createdAt, err := parseTime(created)
	if err != nil {
		return storecontract.Handoff{}, err
	}
	resolvedAt, err := parseNullableTime(resolved)
	if err != nil {
		return storecontract.Handoff{}, err
	}
	eventID, err := parseEventID(event)
	if err != nil {
		return storecontract.Handoff{}, err
	}
	return storecontract.Handoff{ID: parsedID, RunID: runID, AttemptID: attemptID, FromActor: fromActor, ToActor: toActor, Reason: reason, Status: status, CreatedAt: createdAt, ResolvedAt: resolvedAt, SourceEventID: eventID}, nil
}

func (tx *writeTx) InsertReview(ctx context.Context, review domain.ReviewDecision) error {
	if err := tx.requireActiveRoomForRun(ctx, review.RunID()); err != nil {
		return err
	}
	_, err := tx.tx.ExecContext(ctx, `INSERT INTO review_decisions(id,run_id,kind,expected_run_version,comment,decided_at) VALUES(?,?,?,?,?,?)`, review.ID().String(), review.RunID().String(), string(review.Kind()), review.ExpectedRunVersion(), review.Comment(), timeText(review.DecidedAt()))
	if err != nil {
		return mapWriteError(err)
	}
	for i, check := range review.Checks() {
		if _, err := tx.tx.ExecContext(ctx, `INSERT INTO review_checks(review_id,position,name,status,evidence) VALUES(?,?,?,?,?)`, review.ID().String(), i, check.Name, check.Status, check.Evidence); err != nil {
			return mapWriteError(err)
		}
	}
	for i, artifact := range review.LinkedArtifacts() {
		if _, err := tx.tx.ExecContext(ctx, `INSERT INTO review_artifacts(review_id,position,artifact_link) VALUES(?,?,?)`, review.ID().String(), i, string(artifact)); err != nil {
			return mapWriteError(err)
		}
	}
	return nil
}

func (reader *reader) GetReview(ctx context.Context, id domain.ReviewDecisionID) (domain.ReviewDecision, error) {
	var idText, runText, kind, note, decided string
	var expected uint64
	err := reader.q.QueryRowContext(ctx, `SELECT id,run_id,kind,expected_run_version,comment,decided_at FROM review_decisions WHERE id=?`, id.String()).Scan(&idText, &runText, &kind, &expected, &note, &decided)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ReviewDecision{}, storecontract.ErrNotFound
	}
	if err != nil {
		return domain.ReviewDecision{}, err
	}
	checkRows, err := reader.q.QueryContext(ctx, `SELECT name,status,evidence FROM review_checks WHERE review_id=? ORDER BY position`, idText)
	if err != nil {
		return domain.ReviewDecision{}, err
	}
	var checks []domain.ReviewedCheck
	for checkRows.Next() {
		var c domain.ReviewedCheck
		if err := checkRows.Scan(&c.Name, &c.Status, &c.Evidence); err != nil {
			checkRows.Close()
			return domain.ReviewDecision{}, err
		}
		checks = append(checks, c)
	}
	if err := checkRows.Err(); err != nil {
		checkRows.Close()
		return domain.ReviewDecision{}, err
	}
	if err := checkRows.Close(); err != nil {
		return domain.ReviewDecision{}, err
	}
	artifactRows, err := reader.q.QueryContext(ctx, `SELECT artifact_link FROM review_artifacts WHERE review_id=? ORDER BY position`, idText)
	if err != nil {
		return domain.ReviewDecision{}, err
	}
	var artifacts []domain.ArtifactLink
	for artifactRows.Next() {
		var value string
		if err := artifactRows.Scan(&value); err != nil {
			artifactRows.Close()
			return domain.ReviewDecision{}, err
		}
		artifacts = append(artifacts, domain.ArtifactLink(value))
	}
	if err := artifactRows.Err(); err != nil {
		artifactRows.Close()
		return domain.ReviewDecision{}, err
	}
	if err := artifactRows.Close(); err != nil {
		return domain.ReviewDecision{}, err
	}
	rid, err := domain.ParseReviewDecisionID(idText)
	if err != nil {
		return domain.ReviewDecision{}, err
	}
	runID, err := domain.ParseRunID(runText)
	if err != nil {
		return domain.ReviewDecision{}, err
	}
	decidedAt, err := parseTime(decided)
	if err != nil {
		return domain.ReviewDecision{}, err
	}
	return domain.RestoreReviewDecision(domain.ReviewDecisionRecord{ID: rid, RunID: runID, Kind: domain.ReviewDecisionKind(kind), ExpectedRunVersion: expected, Comment: note, Checks: checks, LinkedArtifacts: artifacts, DecidedAt: decidedAt})
}

func (reader *reader) GetLatestReviewForRun(ctx context.Context, runID domain.RunID) (domain.ReviewDecision, error) {
	var idText string
	err := reader.q.QueryRowContext(ctx, `SELECT id FROM review_decisions WHERE run_id=? ORDER BY decided_at DESC LIMIT 1`, runID.String()).Scan(&idText)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ReviewDecision{}, storecontract.ErrNotFound
	}
	if err != nil {
		return domain.ReviewDecision{}, err
	}
	id, err := domain.ParseReviewDecisionID(idText)
	if err != nil {
		return domain.ReviewDecision{}, err
	}
	return reader.GetReview(ctx, id)
}

func (reader *reader) ListReviewsForRun(ctx context.Context, runID domain.RunID) ([]domain.ReviewDecision, error) {
	rows, err := reader.q.QueryContext(ctx, `SELECT id FROM review_decisions WHERE run_id=? ORDER BY decided_at,id`, runID.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var reviews []domain.ReviewDecision
	for rows.Next() {
		var idText string
		if err := rows.Scan(&idText); err != nil {
			return nil, err
		}
		id, err := domain.ParseReviewDecisionID(idText)
		if err != nil {
			return nil, err
		}
		review, err := reader.GetReview(ctx, id)
		if err != nil {
			return nil, err
		}
		reviews = append(reviews, review)
	}
	return reviews, rows.Err()
}

func (tx *writeTx) InsertRuntimeSession(ctx context.Context, session storecontract.RuntimeSession) error {
	if err := tx.requireActiveRoomForAttempt(ctx, session.AttemptID); err != nil {
		return err
	}
	var predecessor any
	if session.PredecessorID != nil {
		predecessor = session.PredecessorID.String()
	}
	fingerprint := session.SecurityFingerprint
	var runtimeFingerprint any
	if session.RuntimeFingerprint != ([32]byte{}) {
		runtimeFingerprint = session.RuntimeFingerprint[:]
	}
	_, err := tx.tx.ExecContext(ctx, `INSERT INTO runtime_sessions(id,attempt_id,predecessor_session_id,adapter_id,runtime_kind,external_reference,version,working_root,security_fingerprint,process_identity,state,created_at,updated_at,started_at,terminal_at,stop_intent,diagnostic,finalized_at,launch_token,runtime_version,runtime_fingerprint) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, session.ID.String(), session.AttemptID.String(), predecessor, session.AdapterID, session.RuntimeKind, session.ExternalReference, session.Version, session.WorkingRoot, fingerprint[:], session.ProcessIdentity, session.State, timeText(session.CreatedAt), timeText(session.UpdatedAt), nullableTime(session.StartedAt), nullableTime(session.TerminalAt), session.StopIntent, session.Diagnostic, nullableTime(session.FinalizedAt), session.LaunchToken, session.RuntimeVersion, runtimeFingerprint)
	if err != nil {
		return mapWriteError(err)
	}
	for _, stream := range []storecontract.RuntimeStream{storecontract.RuntimeStreamStdout, storecontract.RuntimeStreamStderr} {
		if _, err := tx.tx.ExecContext(ctx, `INSERT INTO runtime_stream_offsets(session_id,stream,offset,eof) VALUES(?,?,0,0)`, session.ID.String(), string(stream)); err != nil {
			return mapWriteError(err)
		}
	}
	return nil
}

func (tx *writeTx) AdvanceRuntimeStreamOffset(ctx context.Context, sessionID domain.RuntimeSessionID, stream storecontract.RuntimeStream, expected, next int64, eof bool) error {
	if err := tx.requireActiveRoomForRuntimeSession(ctx, sessionID); err != nil {
		return preserveWriteConflict(err, storecontract.ErrVersionConflict)
	}
	if stream != storecontract.RuntimeStreamStdout && stream != storecontract.RuntimeStreamStderr || expected < 0 || next < expected {
		return storecontract.ErrVersionConflict
	}
	result, err := tx.tx.ExecContext(ctx, `UPDATE runtime_stream_offsets SET offset=?,eof=? WHERE session_id=? AND stream=? AND offset=? AND eof=0`, next, eof, sessionID.String(), string(stream), expected)
	if err != nil {
		return mapWriteError(err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return storecontract.ErrVersionConflict
	}
	return nil
}
func (reader *reader) GetRuntimeStreamOffset(ctx context.Context, sessionID domain.RuntimeSessionID, stream storecontract.RuntimeStream) (storecontract.RuntimeStreamOffset, error) {
	if stream != storecontract.RuntimeStreamStdout && stream != storecontract.RuntimeStreamStderr {
		return storecontract.RuntimeStreamOffset{}, fmt.Errorf("invalid runtime stream %q", stream)
	}
	var sessionText, streamText string
	var offset int64
	var eof bool
	err := reader.q.QueryRowContext(ctx, `SELECT session_id,stream,offset,eof FROM runtime_stream_offsets WHERE session_id=? AND stream=?`, sessionID.String(), string(stream)).Scan(&sessionText, &streamText, &offset, &eof)
	if errors.Is(err, sql.ErrNoRows) {
		return storecontract.RuntimeStreamOffset{}, storecontract.ErrNotFound
	}
	if err != nil {
		return storecontract.RuntimeStreamOffset{}, err
	}
	parsed, err := domain.ParseRuntimeSessionID(sessionText)
	if err != nil {
		return storecontract.RuntimeStreamOffset{}, err
	}
	return storecontract.RuntimeStreamOffset{SessionID: parsed, Stream: storecontract.RuntimeStream(strings.ToLower(streamText)), Offset: offset, EOF: eof}, nil
}

func (reader *reader) GetRuntimeSessionForAttempt(ctx context.Context, attemptID domain.AttemptID) (storecontract.RuntimeSession, error) {
	var id string
	err := reader.q.QueryRowContext(ctx, `SELECT id FROM runtime_sessions WHERE attempt_id=? ORDER BY created_at DESC LIMIT 1`, attemptID.String()).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return storecontract.RuntimeSession{}, storecontract.ErrNotFound
	}
	if err != nil {
		return storecontract.RuntimeSession{}, err
	}
	parsed, err := domain.ParseRuntimeSessionID(id)
	if err != nil {
		return storecontract.RuntimeSession{}, err
	}
	return reader.GetRuntimeSession(ctx, parsed)
}

func (reader *reader) GetRuntimeSession(ctx context.Context, sessionID domain.RuntimeSessionID) (storecontract.RuntimeSession, error) {
	var idText, attemptText, adapter, kind, external, root, identity, state, created, updated, launch, runtimeVersion string
	var stopIntent, diagnostic string
	var predecessor sql.NullString
	var version uint64
	var fingerprint, runtimeFingerprint []byte
	var started, terminal, finalized sql.NullString
	err := reader.q.QueryRowContext(ctx, `SELECT id,attempt_id,predecessor_session_id,adapter_id,runtime_kind,external_reference,version,working_root,security_fingerprint,process_identity,state,created_at,updated_at,started_at,terminal_at,stop_intent,diagnostic,finalized_at,launch_token,runtime_version,runtime_fingerprint FROM runtime_sessions WHERE id=?`, sessionID.String()).Scan(&idText, &attemptText, &predecessor, &adapter, &kind, &external, &version, &root, &fingerprint, &identity, &state, &created, &updated, &started, &terminal, &stopIntent, &diagnostic, &finalized, &launch, &runtimeVersion, &runtimeFingerprint)
	if errors.Is(err, sql.ErrNoRows) {
		return storecontract.RuntimeSession{}, storecontract.ErrNotFound
	}
	if err != nil {
		return storecontract.RuntimeSession{}, err
	}
	id, err := domain.ParseRuntimeSessionID(idText)
	if err != nil {
		return storecontract.RuntimeSession{}, err
	}
	aid, err := domain.ParseAttemptID(attemptText)
	if err != nil {
		return storecontract.RuntimeSession{}, err
	}
	var pred *domain.RuntimeSessionID
	if predecessor.Valid {
		v, e := domain.ParseRuntimeSessionID(predecessor.String)
		if e != nil {
			return storecontract.RuntimeSession{}, e
		}
		pred = &v
	}
	var fp [32]byte
	if len(fingerprint) != 32 {
		return storecontract.RuntimeSession{}, fmt.Errorf("invalid fingerprint")
	}
	copy(fp[:], fingerprint)
	var runtimeFP [32]byte
	if len(runtimeFingerprint) != 0 && len(runtimeFingerprint) != 32 {
		return storecontract.RuntimeSession{}, fmt.Errorf("invalid runtime fingerprint")
	}
	copy(runtimeFP[:], runtimeFingerprint)
	createdAt, e := parseTime(created)
	if e != nil {
		return storecontract.RuntimeSession{}, e
	}
	updatedAt, e := parseTime(updated)
	if e != nil {
		return storecontract.RuntimeSession{}, e
	}
	startedAt, e := parseNullableTime(started)
	if e != nil {
		return storecontract.RuntimeSession{}, e
	}
	terminalAt, e := parseNullableTime(terminal)
	if e != nil {
		return storecontract.RuntimeSession{}, e
	}
	finalizedAt, e := parseNullableTime(finalized)
	if e != nil {
		return storecontract.RuntimeSession{}, e
	}
	return storecontract.RuntimeSession{ID: id, AttemptID: aid, PredecessorID: pred, AdapterID: adapter, RuntimeKind: kind, ExternalReference: external, LaunchToken: launch, Version: version, WorkingRoot: root, SecurityFingerprint: fp, RuntimeVersion: runtimeVersion, RuntimeFingerprint: runtimeFP, ProcessIdentity: identity, State: state, StopIntent: stopIntent, Diagnostic: diagnostic, CreatedAt: createdAt, UpdatedAt: updatedAt, StartedAt: startedAt, TerminalAt: terminalAt, FinalizedAt: finalizedAt}, nil
}

func (tx *writeTx) SaveRuntimeSessionCAS(ctx context.Context, expected uint64, next storecontract.RuntimeSession) error {
	if err := tx.requireActiveRoomForRuntimeSession(ctx, next.ID); err != nil {
		return err
	}
	current, err := tx.GetRuntimeSession(ctx, next.ID)
	if err != nil {
		return err
	}
	if current.Version != expected || next.Version != expected+1 || current.ID != next.ID || current.AttemptID != next.AttemptID || current.AdapterID != next.AdapterID || current.LaunchToken != next.LaunchToken || current.CreatedAt != next.CreatedAt || current.WorkingRoot != next.WorkingRoot || current.SecurityFingerprint != next.SecurityFingerprint || current.ExternalReference != "" && current.ExternalReference != next.ExternalReference || current.RuntimeFingerprint != ([32]byte{}) && current.RuntimeFingerprint != next.RuntimeFingerprint || current.RuntimeVersion != "" && current.RuntimeVersion != next.RuntimeVersion {
		return storecontract.ErrVersionConflict
	}
	var runtimeFingerprint any
	if next.RuntimeFingerprint != ([32]byte{}) {
		runtimeFingerprint = next.RuntimeFingerprint[:]
	}
	result, err := tx.tx.ExecContext(ctx, `UPDATE runtime_sessions SET version=?,external_reference=?,working_root=?,security_fingerprint=?,process_identity=?,state=?,updated_at=?,started_at=?,terminal_at=?,stop_intent=?,diagnostic=?,finalized_at=?,launch_token=?,runtime_version=?,runtime_fingerprint=? WHERE id=? AND version=?`, next.Version, next.ExternalReference, next.WorkingRoot, next.SecurityFingerprint[:], next.ProcessIdentity, next.State, timeText(next.UpdatedAt), nullableTime(next.StartedAt), nullableTime(next.TerminalAt), next.StopIntent, next.Diagnostic, nullableTime(next.FinalizedAt), next.LaunchToken, next.RuntimeVersion, runtimeFingerprint, next.ID.String(), expected)
	if err != nil {
		return mapWriteError(err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return storecontract.ErrVersionConflict
	}
	return nil
}

func (reader *reader) ListStartupCandidates(ctx context.Context) ([]storecontract.StartupCandidate, error) {
	rows, err := reader.q.QueryContext(ctx, `SELECT DISTINCT r.id,a.id FROM runs r LEFT JOIN attempts a ON a.run_id=r.id AND a.sequence=r.current_attempt_number LEFT JOIN runtime_sessions s ON s.attempt_id=a.id WHERE r.state IN ('running','stopping') OR a.state IN ('starting','running') OR (r.state='recovery_required' AND s.id IS NOT NULL AND s.finalized_at IS NULL AND s.state IN ('starting','running','stopping','stopped','lost')) ORDER BY r.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []storecontract.StartupCandidate
	for rows.Next() {
		var runText string
		var attemptText sql.NullString
		if err := rows.Scan(&runText, &attemptText); err != nil {
			return nil, err
		}
		runID, err := domain.ParseRunID(runText)
		if err != nil {
			return nil, err
		}
		run, err := reader.GetRun(ctx, runID)
		if err != nil {
			return nil, err
		}
		candidate := storecontract.StartupCandidate{Run: run}
		if attemptText.Valid {
			attemptID, err := domain.ParseAttemptID(attemptText.String)
			if err != nil {
				return nil, err
			}
			attempt, err := reader.GetAttempt(ctx, attemptID)
			if err != nil {
				return nil, err
			}
			candidate.Attempt = &attempt
		}
		result = append(result, candidate)
	}
	return result, rows.Err()
}
