package app

import (
	"context"
	"sort"
	"time"

	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

type RoomSummary struct {
	Room                domain.Room
	LastActivityAt      time.Time
	TaskCounts          storecontract.RoomTaskCounts
	HumanActionRequired bool
}

type RoomDirectory struct {
	ActiveRooms   []RoomSummary
	ArchivedRooms []RoomSummary
}

type TaskSummary struct {
	Task                      domain.Task
	CreatedAt, UpdatedAt      time.Time
	LastActivityAt            time.Time
	RunCount                  int
	CurrentPlanRevisionID     domain.TechnicalPlanRevisionID
	CurrentPlanRevisionNumber uint64
	LatestRun                 *domain.AgentRun
	CurrentAction             CurrentAction
}

type RoomWorkspace struct {
	Room  RoomSummary
	Tasks []TaskSummary
}

type TaskRunHistory struct {
	RoomID domain.RoomID
	TaskID domain.TaskID
	Runs   []storecontract.RunSummary
}

func (s *Service) ListRoomDirectory(ctx context.Context) (RoomDirectory, error) {
	active, err := s.deps.Store.Reader().ListRoomDirectory(ctx, domain.RoomStateActive)
	if err != nil {
		return RoomDirectory{}, err
	}
	archived, err := s.deps.Store.Reader().ListRoomDirectory(ctx, domain.RoomStateArchived)
	if err != nil {
		return RoomDirectory{}, err
	}
	return RoomDirectory{ActiveRooms: roomSummaries(active), ArchivedRooms: roomSummaries(archived)}, nil
}

func (s *Service) GetRoomWorkspace(ctx context.Context, roomID domain.RoomID) (RoomWorkspace, error) {
	projection, err := s.deps.Store.Reader().GetRoomWorkspace(ctx, roomID)
	if err != nil {
		return RoomWorkspace{}, err
	}
	result := RoomWorkspace{Room: roomSummary(projection.Room), Tasks: make([]TaskSummary, 0, len(projection.Tasks))}
	for _, item := range projection.Tasks {
		action := deriveCurrentAction(projection.Room.Room.State(), item)
		if !item.Task.Archived() && currentActionNeedsHuman(action.Kind) {
			result.Room.HumanActionRequired = true
		}
		result.Tasks = append(result.Tasks, TaskSummary{
			Task: item.Task, CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt, LastActivityAt: item.LastActivityAt,
			RunCount: item.RunCount, CurrentPlanRevisionID: item.LatestRevisionID, CurrentPlanRevisionNumber: item.LatestRevisionNumber,
			LatestRun: item.LatestRun, CurrentAction: action,
		})
	}
	sortTaskSummaries(result.Tasks)
	return result, nil
}

func sortTaskSummaries(tasks []TaskSummary) {
	sort.SliceStable(tasks, func(i, j int) bool {
		left, right := tasks[i], tasks[j]
		leftRank, rightRank := taskSummaryRank(left), taskSummaryRank(right)
		if leftRank != rightRank {
			return leftRank < rightRank
		}
		if !left.LastActivityAt.Equal(right.LastActivityAt) {
			return left.LastActivityAt.After(right.LastActivityAt)
		}
		return left.Task.ID().String() < right.Task.ID().String()
	})
}

func (s *Service) ListTaskRunHistory(ctx context.Context, roomID domain.RoomID, taskID domain.TaskID) (TaskRunHistory, error) {
	runs, err := s.deps.Store.Reader().ListTaskRunHistory(ctx, roomID, taskID)
	if err != nil {
		return TaskRunHistory{}, err
	}
	return TaskRunHistory{RoomID: roomID, TaskID: taskID, Runs: runs}, nil
}

func (s *Service) GetTaskRun(ctx context.Context, roomID domain.RoomID, taskID domain.TaskID, runID domain.RunID) (domain.AgentRun, error) {
	return s.deps.Store.Reader().GetTaskRun(ctx, roomID, taskID, runID)
}

func roomSummaries(items []storecontract.RoomDirectoryItem) []RoomSummary {
	result := make([]RoomSummary, 0, len(items))
	for _, item := range items {
		summary := roomSummary(item)
		for _, task := range item.Tasks {
			if task.Task.Archived() {
				continue
			}
			if currentActionNeedsHuman(deriveCurrentAction(item.Room.State(), task).Kind) {
				summary.HumanActionRequired = true
				break
			}
		}
		result = append(result, summary)
	}
	return result
}

func roomSummary(item storecontract.RoomDirectoryItem) RoomSummary {
	return RoomSummary{Room: item.Room, LastActivityAt: item.LastActivityAt, TaskCounts: item.TaskCounts}
}

func currentActionNeedsHuman(kind CurrentActionKind) bool {
	switch kind {
	case CurrentActionEditPlan, CurrentActionReviewPlan, CurrentActionStartRun, CurrentActionStartAttempt,
		CurrentActionReviewResult, CurrentActionRetryImplementation, CurrentActionContinueSuccessorPlan, CurrentActionOpenRelatedTask:
		return true
	default:
		return false
	}
}

func taskSummaryRank(summary TaskSummary) int {
	if currentActionNeedsHuman(summary.CurrentAction.Kind) {
		return 0
	}
	switch summary.CurrentAction.Kind {
	case CurrentActionMonitorRun, CurrentActionRecoverRun, CurrentActionRecoverVerification:
		return 1
	}
	if summary.Task.State() == domain.TaskStateOpen && summary.CurrentAction.Kind != CurrentActionViewTerminal {
		return 2
	}
	return 3
}
