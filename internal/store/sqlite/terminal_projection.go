package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

func (reader *reader) GetTerminalProjection(ctx context.Context, attemptID domain.AttemptID) (storecontract.TerminalProjection, error) {
	if _, err := reader.GetAttempt(ctx, attemptID); err != nil {
		return storecontract.TerminalProjection{}, err
	}
	projection := storecontract.TerminalProjection{}
	rows, err := reader.q.QueryContext(ctx, `SELECT id,run_id,attempt_id,kind,locator,digest,media_type,description,position,role,source_event_id,created_at FROM artifacts WHERE attempt_id=? AND source_event_id IS NOT NULL ORDER BY CASE role WHEN 'output' THEN 0 ELSE 1 END,position`, attemptID.String())
	if err != nil {
		return projection, err
	}
	for rows.Next() {
		var idText, runText, attemptText, kind, locator, mediaType, description, role, eventText, created string
		var digestBytes []byte
		var position int
		if err := rows.Scan(&idText, &runText, &attemptText, &kind, &locator, &digestBytes, &mediaType, &description, &position, &role, &eventText, &created); err != nil {
			rows.Close()
			return projection, err
		}
		id, err := domain.ParseArtifactID(idText)
		if err != nil {
			rows.Close()
			return projection, err
		}
		runID, err := domain.ParseRunID(runText)
		if err != nil {
			rows.Close()
			return projection, err
		}
		attempt, err := domain.ParseAttemptID(attemptText)
		if err != nil {
			rows.Close()
			return projection, err
		}
		event, err := domain.ParseEventID(eventText)
		if err != nil {
			rows.Close()
			return projection, err
		}
		createdAt, err := parseTime(created)
		if err != nil {
			rows.Close()
			return projection, err
		}
		var digest *[32]byte
		if len(digestBytes) > 0 {
			if len(digestBytes) != 32 {
				rows.Close()
				return projection, errors.New("stored artifact digest is invalid")
			}
			value := [32]byte{}
			copy(value[:], digestBytes)
			digest = &value
		}
		projection.Artifacts = append(projection.Artifacts, storecontract.Artifact{ID: id, RunID: runID, AttemptID: &attempt, Kind: kind, Locator: locator, Digest: digest, MediaType: mediaType, Description: description, Position: position, Role: role, SourceEventID: &event, CreatedAt: createdAt})
	}
	if err := rows.Close(); err != nil {
		return projection, err
	}
	if err := rows.Err(); err != nil {
		return projection, err
	}

	checkRows, err := reader.q.QueryContext(ctx, `SELECT id,run_id,attempt_id,criterion_id,position,name,status,evidence,source_event_id,created_at FROM checks WHERE attempt_id=? AND source_event_id IS NOT NULL ORDER BY position`, attemptID.String())
	if err != nil {
		return projection, err
	}
	for checkRows.Next() {
		var idText, runText, attemptText, criterionID, name, status, evidence, eventText, created string
		var position int
		if err := checkRows.Scan(&idText, &runText, &attemptText, &criterionID, &position, &name, &status, &evidence, &eventText, &created); err != nil {
			checkRows.Close()
			return projection, err
		}
		id, err := domain.ParseCheckID(idText)
		if err != nil {
			checkRows.Close()
			return projection, err
		}
		runID, err := domain.ParseRunID(runText)
		if err != nil {
			checkRows.Close()
			return projection, err
		}
		attempt, err := domain.ParseAttemptID(attemptText)
		if err != nil {
			checkRows.Close()
			return projection, err
		}
		event, err := domain.ParseEventID(eventText)
		if err != nil {
			checkRows.Close()
			return projection, err
		}
		createdAt, err := parseTime(created)
		if err != nil {
			checkRows.Close()
			return projection, err
		}
		projection.Checks = append(projection.Checks, storecontract.Check{ID: id, RunID: runID, AttemptID: &attempt, CriterionID: criterionID, Position: position, Name: name, Status: status, Evidence: evidence, SourceEventID: &event, CreatedAt: createdAt})
	}
	if err := checkRows.Close(); err != nil {
		return projection, err
	}
	if err := checkRows.Err(); err != nil {
		return projection, err
	}

	observationRows, err := reader.q.QueryContext(ctx, `SELECT id FROM observations WHERE attempt_id=? AND role IN ('summary','context_consumption','unknown') ORDER BY CASE role WHEN 'summary' THEN 0 WHEN 'context_consumption' THEN 1 ELSE 2 END,position`, attemptID.String())
	if err != nil {
		return projection, err
	}
	for observationRows.Next() {
		var idText string
		if err := observationRows.Scan(&idText); err != nil {
			observationRows.Close()
			return projection, err
		}
		id, err := domain.ParseObservationID(idText)
		if err != nil {
			observationRows.Close()
			return projection, err
		}
		observation, err := reader.GetObservation(ctx, id)
		if err != nil {
			observationRows.Close()
			return projection, err
		}
		if observation.Role == "summary" {
			projection.Summary = observation
		} else if observation.Role == "context_consumption" {
			value := observation
			projection.ContextConsumption = &value
		} else {
			projection.Unknowns = append(projection.Unknowns, observation)
		}
	}
	if err := observationRows.Close(); err != nil {
		return projection, err
	}
	if err := observationRows.Err(); err != nil {
		return projection, err
	}

	var handoffText string
	err = reader.q.QueryRowContext(ctx, `SELECT id FROM handoffs WHERE attempt_id=? AND source_event_id IS NOT NULL ORDER BY created_at DESC LIMIT 1`, attemptID.String()).Scan(&handoffText)
	if err == nil {
		id, parseErr := domain.ParseHandoffID(handoffText)
		if parseErr != nil {
			return projection, parseErr
		}
		handoff, getErr := reader.GetHandoff(ctx, id)
		if getErr != nil {
			return projection, getErr
		}
		projection.Handoff = &handoff
	} else if !errors.Is(err, sql.ErrNoRows) {
		return projection, err
	}
	return projection, nil
}
