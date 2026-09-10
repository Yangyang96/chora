package domain

import (
	"errors"
	"testing"
)

func TestNewTaskRequiresGoalAndCriteriaAndFreezesCriterionOrder(t *testing.T) {
	first, err := NewAcceptanceCriterion(NewCriterionID(), "tests pass", "all targeted tests pass")
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewAcceptanceCriterion(NewCriterionID(), "reviewed", "human review is available")
	if err != nil {
		t.Fatal(err)
	}
	criteria := []AcceptanceCriterion{first, second}
	task, err := NewTask(NewTaskID(), NewRoomID(), "Domain", "Implement the domain", criteria)
	if err != nil {
		t.Fatalf("NewTask() error = %v", err)
	}

	criteria[0] = second
	got := task.Criteria()
	if got[0].ID() != first.ID() || got[1].ID() != second.ID() {
		t.Fatalf("criterion order mutated: got %v then %v", got[0].ID(), got[1].ID())
	}
	got[0] = second
	if task.Criteria()[0].ID() != first.ID() {
		t.Fatal("returned criteria slice aliases task state")
	}
	if task.State() != TaskStateOpen {
		t.Fatalf("new task state = %q, want open", task.State())
	}
	if closed := task.Close(); closed.State() != TaskStateClosed || task.State() != TaskStateOpen {
		t.Fatal("Close must return a closed value without mutating the original")
	}
}

func TestNewTaskRejectsMissingGoalCriteriaAndDuplicateCriterionIDs(t *testing.T) {
	criterion, err := NewAcceptanceCriterion(NewCriterionID(), "done", "done")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name     string
		goal     string
		criteria []AcceptanceCriterion
	}{
		{"missing goal", "", []AcceptanceCriterion{criterion}},
		{"missing criteria", "goal", nil},
		{"duplicate criterion IDs", "goal", []AcceptanceCriterion{criterion, criterion}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewTask(NewTaskID(), NewRoomID(), "title", test.goal, test.criteria); !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("NewTask() error = %v, want ErrInvalidArgument", err)
			}
		})
	}
}
