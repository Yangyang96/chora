package domain

import (
	"fmt"
	"strings"
	"time"
)

type AcceptanceCriterion struct {
	id          CriterionID
	title       string
	description string
}

func NewAcceptanceCriterion(id CriterionID, title, description string) (AcceptanceCriterion, error) {
	if !id.Valid() || strings.TrimSpace(title) == "" {
		return AcceptanceCriterion{}, fmt.Errorf("%w: invalid acceptance criterion", ErrInvalidArgument)
	}
	return AcceptanceCriterion{id: id, title: title, description: description}, nil
}

func (criterion AcceptanceCriterion) ID() CriterionID     { return criterion.id }
func (criterion AcceptanceCriterion) Title() string       { return criterion.title }
func (criterion AcceptanceCriterion) Description() string { return criterion.description }

type TaskState string

const (
	TaskStateOpen   TaskState = "open"
	TaskStateClosed TaskState = "closed"
)

type Task struct {
	id                TaskID
	roomID            RoomID
	predecessorTaskID TaskID
	title             string
	goal              string
	criteria          []AcceptanceCriterion
	state             TaskState
	archived          bool
	archivedAt        time.Time
}

func NewRelatedTask(id TaskID, predecessor Task, title, goal string, criteria []AcceptanceCriterion) (Task, error) {
	task, err := NewTask(id, predecessor.RoomID(), title, goal, criteria)
	if err != nil || !predecessor.ID().Valid() || predecessor.ID() == id {
		return Task{}, fmt.Errorf("%w: invalid related task", ErrInvalidArgument)
	}
	task.predecessorTaskID = predecessor.ID()
	return task, nil
}

func NewTask(id TaskID, roomID RoomID, title, goal string, criteria []AcceptanceCriterion) (Task, error) {
	if !id.Valid() || !roomID.Valid() || strings.TrimSpace(title) == "" || strings.TrimSpace(goal) == "" || len(criteria) == 0 || !validCriteria(criteria) {
		return Task{}, fmt.Errorf("%w: invalid task", ErrInvalidArgument)
	}
	return Task{id: id, roomID: roomID, title: title, goal: goal, criteria: cloneCriteria(criteria), state: TaskStateOpen}, nil
}

func (task Task) ID() TaskID                      { return task.id }
func (task Task) RoomID() RoomID                  { return task.roomID }
func (task Task) PredecessorTaskID() TaskID       { return task.predecessorTaskID }
func (task Task) Title() string                   { return task.title }
func (task Task) Goal() string                    { return task.goal }
func (task Task) Criteria() []AcceptanceCriterion { return cloneCriteria(task.criteria) }
func (task Task) State() TaskState                { return task.state }
func (task Task) Close() Task                     { task.state = TaskStateClosed; return task }
func (task Task) Archived() bool                  { return task.archived }
func (task Task) ArchivedAt() time.Time           { return task.archivedAt }

// Archive hides the Task from active lists without deleting it. Archiving is
// reversible via Restore. An already-archived Task or a zero timestamp is
// rejected.
func (task Task) Archive(at time.Time) (Task, error) {
	if task.archived || at.IsZero() {
		return Task{}, fmt.Errorf("%w: task Archive rejected", ErrInvalidArgument)
	}
	task.archived = true
	task.archivedAt = at
	return task, nil
}

func (task Task) Restore() (Task, error) {
	if !task.archived {
		return Task{}, fmt.Errorf("%w: task Restore rejected", ErrInvalidArgument)
	}
	task.archived = false
	task.archivedAt = time.Time{}
	return task, nil
}

func validCriteria(criteria []AcceptanceCriterion) bool {
	seen := make(map[CriterionID]struct{}, len(criteria))
	for _, criterion := range criteria {
		if !criterion.id.Valid() || strings.TrimSpace(criterion.title) == "" {
			return false
		}
		if _, exists := seen[criterion.id]; exists {
			return false
		}
		seen[criterion.id] = struct{}{}
	}
	return true
}

func cloneCriteria(criteria []AcceptanceCriterion) []AcceptanceCriterion {
	return append([]AcceptanceCriterion(nil), criteria...)
}
