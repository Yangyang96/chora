package domain

import (
	"crypto/sha256"
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestSynthesisSourceRoundTripAndIntegrity(t *testing.T) {
	input := DelegationSynthesisInput{TaskID: NewTaskID(), Markdown: "# Original finding", ProjectDocumentSource: ProjectDocumentSource{RunID: NewRunID(), AttemptID: NewAttemptID(), ResultID: NewResultID(), AgentReportID: NewAgentReportID(), EventID: NewEventID(), EventSequence: 1, ResultDigest: sha256.Sum256([]byte("result")), SourceTextDigest: sha256.Sum256([]byte("# Original finding"))}}
	s := DelegationSynthesis{ParentTaskID: NewTaskID(), TaskID: NewTaskID(), Inputs: []DelegationSynthesisInput{input}, ActorID: "human", SessionID: "session", CreatedAt: time.Now().UTC()}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(s.Inputs)
	if err != nil {
		t.Fatal(err)
	}
	var got []DelegationSynthesisInput
	if err = json.Unmarshal(raw, &got); err != nil || !reflect.DeepEqual(got, s.Inputs) {
		t.Fatalf("source identity lost: %s %v", raw, err)
	}
	s.Inputs[0].Markdown = "substituted"
	if s.Validate() == nil {
		t.Fatal("substituted source body accepted")
	}
}

func TestSynthesisEmptyContextDoesNotRelaxOrdinaryCharters(t *testing.T) {
	now := time.Now().UTC()
	task := NewTaskID()
	room := NewRoomID()
	if _, err := NewTaskRevisionSelection(task, room, nil, nil, now); err == nil {
		t.Fatal("ordinary empty selection accepted")
	}
	selection, err := NewSynthesisRevisionSelection(task, room, now)
	if err != nil || !selection.SynthesisOnly() || len(selection.Selected()) != 0 {
		t.Fatal(err)
	}
	params := RunCharterParams{ID: NewCharterID(), TaskID: task, TaskGoal: "Synthesize", Criteria: []AcceptanceCriterion{testCriterion(t)}, WorkspaceRoot: "/tmp/chora", AdapterID: "fake", SandboxMode: "workspace-write", ExpectedOutput: "report", ResponsibleHuman: "human", CapabilityEnvelope: CapabilityEnvelope{"read": true}, Initiator: "human", CreatedAt: now}
	if _, err := NewRunCharter(params); err == nil {
		t.Fatal("ordinary empty charter accepted")
	}
	params.SynthesisOnly = true
	if _, err := NewRunCharter(params); err != nil {
		t.Fatal(err)
	}
	params.ContextRevisionIDs = []ContextRevisionID{NewContextRevisionID()}
	if _, err := NewRunCharter(params); err == nil {
		t.Fatal("synthesis accepted external context")
	}
}
