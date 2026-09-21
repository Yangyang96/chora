package domain

import (
	"crypto/sha256"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestProjectDocumentRevisionFreezesSourceAndExactBody(t *testing.T) {
	now := time.Now().UTC()
	body := "# Design\n\nExact Markdown.\n"
	sourceText := sha256.Sum256([]byte(body))
	source := ProjectDocumentSource{RunID: NewRunID(), AttemptID: NewAttemptID(), ResultID: NewResultID(), AgentReportID: NewAgentReportID(), EventID: NewEventID(), EventSequence: 7, ResultDigest: sha256.Sum256([]byte("result")), SourceTextDigest: sourceText}
	revision, err := NewProjectDocumentRevision(NewProjectDocumentRevisionID(), NewTaskID(), NewRoomID(), 1, ProjectDocumentAgentInitial, body, source, "", "owner", now)
	if err != nil {
		t.Fatal(err)
	}
	if revision.Body != body || revision.BodyDigest != sha256.Sum256([]byte(body)) || revision.Source != source {
		t.Fatalf("revision did not freeze exact input: %#v", revision)
	}
	revision.BodyDigest[0]++
	if _, err := RestoreProjectDocumentRevision(revision); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("tampered digest error = %v", err)
	}
}

func TestProjectDocumentRevisionBoundsAndReviewExactVersion(t *testing.T) {
	now := time.Now().UTC()
	source := ProjectDocumentSource{RunID: NewRunID(), AttemptID: NewAttemptID(), ResultID: NewResultID(), AgentReportID: NewAgentReportID(), EventID: NewEventID(), EventSequence: 1, ResultDigest: sha256.Sum256([]byte("result")), SourceTextDigest: sha256.Sum256([]byte("source"))}
	if _, err := NewProjectDocumentRevision(NewProjectDocumentRevisionID(), NewTaskID(), NewRoomID(), 1, ProjectDocumentAgentInitial, strings.Repeat("x", MaxProjectDocumentBytes+1), source, "", "owner", now); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("oversize body error = %v", err)
	}
	revision, _ := NewProjectDocumentRevision(NewProjectDocumentRevisionID(), NewTaskID(), NewRoomID(), 1, ProjectDocumentAgentInitial, "body", source, "", "owner", now)
	if _, err := NewProjectDocumentReview(NewProjectDocumentReviewID(), revision, 2, ProjectDocumentReviewAccept, "", "owner", "session", NewContextRevisionID(), now); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("stale review error = %v", err)
	}
	if _, err := NewProjectDocumentReview(NewProjectDocumentReviewID(), revision, 1, ProjectDocumentReviewReject, "", "owner", "session", ContextRevisionID{}, now); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("reject without feedback error = %v", err)
	}
}
