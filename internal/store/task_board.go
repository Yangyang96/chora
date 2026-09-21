package store

import (
	"context"
	"github.com/Yangyang96/chora/internal/domain"
	"time"
)

// TaskBoardSnapshot contains only persisted summary evidence, from one read
// transaction. It deliberately excludes logs, patches, credentials and paths.
type TaskBoardSnapshot struct {
	Project      domain.Project
	Rooms        []domain.Room
	Repositories []TaskBoardRepository
	Tasks        []TaskBoardFacts
}
type TaskBoardReader interface {
	GetTaskBoardSnapshot(context.Context, domain.ProjectID) (TaskBoardSnapshot, error)
}
type TaskBoardRepository struct {
	RepoID string `json:"repoId"`
	Name   string `json:"name"`
}
type TaskBoardFacts struct {
	OutcomeKind, DocumentStatus                           string
	Item                                                  TaskWorkspaceItem
	TaskID, RoomID, Title, State, Invalid                 string
	Archived                                              bool
	Activity                                              time.Time
	AttemptID, AttemptState, GateID, GateQuestion         string
	ResourceResultID, ResourceOutcome, ResourceReviewKind string
	ResourceSnapshot                                      bool
	Repositories                                          []TaskBoardRepositoryFacts
	Closure                                               bool
	FailureReason                                         string
	RetryFinalized                                        bool
	RetryEvents                                           []TaskBoardRetryEvent
}
type TaskBoardRetryEvent struct {
	Type     string
	Payload  []byte
	Sequence int64
}
type TaskBoardRepositoryFacts struct {
	RepoID, Name, Role, Mode, Preparation string
	ResultPresent                         bool
	Changed                               int
	ChecksMode                            string
	ChecksNotApplicable                   bool
	ChecksStatus                          string
	Closed                                bool
	ApplyState                            string
	Operations                            []TaskBoardOperation
}
type TaskBoardOperation struct {
	ID, Kind, State, PRState, MergeCommit, Commit string
	Version                                       uint64
	UpdatedAt                                     time.Time
}
