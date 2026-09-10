package app

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

type sessionChainReader struct {
	storecontract.Reader
	attempts map[domain.AttemptID]domain.Attempt
	sessions map[domain.RuntimeSessionID]storecontract.RuntimeSession
}

func (r sessionChainReader) GetAttempt(_ context.Context, id domain.AttemptID) (domain.Attempt, error) {
	a, ok := r.attempts[id]
	if !ok {
		return a, errors.New("missing attempt")
	}
	return a, nil
}
func (r sessionChainReader) GetRuntimeSession(_ context.Context, id domain.RuntimeSessionID) (storecontract.RuntimeSession, error) {
	s, ok := r.sessions[id]
	if !ok {
		return s, errors.New("missing session")
	}
	return s, nil
}

func TestSessionOwnerSurvivesThirdResumeAndRejectsBrokenChain(t *testing.T) {
	binding, err := domain.NewAgentExecutionProfileBinding(domain.AgentExecutionProfileTrustedLocal)
	if err != nil {
		t.Fatal(err)
	}
	runID := domain.NewRunID()
	snapshot := domain.NewContextSnapshotID()
	digest := sha256.Sum256([]byte("snapshot"))
	ref := "01a07727-e93c-758b-871f-1f47cd6e3de4"
	reader := sessionChainReader{attempts: map[domain.AttemptID]domain.Attempt{}, sessions: map[domain.RuntimeSessionID]storecontract.RuntimeSession{}}
	var attempts []domain.Attempt
	var sessions []storecontract.RuntimeSession
	for i := 0; i < 4; i++ {
		params := domain.AttemptParams{ID: domain.NewAttemptID(), RunID: runID, Sequence: i + 1, ContextSnapshotID: snapshot, ContextDigest: digest, AdapterID: "pi", AgentExecutionProfileBinding: binding, CreatedAt: time.Now().UTC()}
		if i > 0 {
			prev := attempts[i-1].ID()
			params.Predecessor = &prev
			params.ExternalSession = ref
			params.RetryReason = "Resume"
			params.ContextDelta = "Continue explicitly"
		}
		attempt, err := domain.NewAttempt(params)
		if err != nil {
			t.Fatal(err)
		}
		attempts = append(attempts, attempt)
		reader.attempts[attempt.ID()] = attempt
		session := storecontract.RuntimeSession{ID: domain.NewRuntimeSessionID(), AttemptID: attempt.ID(), ExternalReference: ref, AdapterID: "pi", WorkingRoot: "/tmp/owned", RuntimeVersion: "0.85.1", RuntimeFingerprint: digest, SecurityFingerprint: digest}
		if i > 0 {
			prev := sessions[i-1].ID
			session.PredecessorID = &prev
		}
		sessions = append(sessions, session)
		reader.sessions[session.ID] = session
	}
	for i := 1; i < 4; i++ {
		owner, err := sessionOwnerAttempt(context.Background(), reader, sessions[i-1], attempts[i])
		if err != nil || owner != attempts[0].ID() {
			t.Fatalf("resume %d owner=%v err=%v", i, owner, err)
		}
	}
	t.Run("missing", func(t *testing.T) {
		delete(reader.sessions, sessions[0].ID)
		defer func() { reader.sessions[sessions[0].ID] = sessions[0] }()
		if _, err := sessionOwnerAttempt(context.Background(), reader, sessions[2], attempts[3]); err == nil {
			t.Fatal("accepted missing predecessor")
		}
	})
	t.Run("cycle", func(t *testing.T) {
		loop := sessions[1]
		loop.PredecessorID = &loop.ID
		reader.sessions[loop.ID] = loop
		defer func() { reader.sessions[loop.ID] = sessions[1] }()
		if _, err := sessionOwnerAttempt(context.Background(), reader, sessions[2], attempts[3]); err == nil {
			t.Fatal("accepted cyclic predecessor")
		}
	})
	t.Run("drift", func(t *testing.T) {
		drift := sessions[0]
		drift.WorkingRoot = "/tmp/other"
		reader.sessions[drift.ID] = drift
		defer func() { reader.sessions[drift.ID] = sessions[0] }()
		if _, err := sessionOwnerAttempt(context.Background(), reader, sessions[2], attempts[3]); err == nil {
			t.Fatal("accepted workspace drift")
		}
	})
}
