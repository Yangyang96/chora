package app

import (
	"context"

	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

type taskArchiveWire struct {
	Task taskWire
}

func taskArchiveResponse(result ArchiveTaskResult) (storecontract.Response, error) {
	return jsonResponse(taskArchiveWire{Task: wireTask(result.Task)})
}

func replayTaskArchive(response storecontract.Response) (ArchiveTaskResult, error) {
	var wire taskArchiveWire
	if err := decodeResponse(response, &wire); err != nil {
		return ArchiveTaskResult{}, err
	}
	task, err := restoreTaskWire(wire.Task)
	if err != nil {
		return ArchiveTaskResult{}, err
	}
	return ArchiveTaskResult{Task: task, Replayed: true}, nil
}

// ArchiveTask hides a Task from active lists without deleting it. It is
// reversible via RestoreTask.
func (s *Service) ArchiveTask(ctx context.Context, request ArchiveTaskRequest) (ArchiveTaskResult, error) {
	return s.changeTaskArchive(ctx, request, true)
}

// RestoreTask returns an archived Task to the active lists.
func (s *Service) RestoreTask(ctx context.Context, request ArchiveTaskRequest) (ArchiveTaskResult, error) {
	return s.changeTaskArchive(ctx, request, false)
}

func (s *Service) changeTaskArchive(ctx context.Context, request ArchiveTaskRequest, archive bool) (ArchiveTaskResult, error) {
	action := "restore_task"
	if archive {
		action = "archive_task"
	}
	if err := s.authorize(ctx, request.CommandMeta, action, request.TaskID.String(), 0); err != nil {
		return ArchiveTaskResult{}, err
	}
	now := s.deps.Clock.Now()
	var result ArchiveTaskResult
	err := s.deps.Store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		task, err := tx.GetTask(ctx, request.TaskID)
		if err != nil {
			return err
		}
		key, err := s.commandKey(request.CommandMeta, action, request.TaskID.String(), 0, struct{}{}, now)
		if err != nil {
			return err
		}
		response, replay, err := tx.LookupCommand(ctx, key)
		if err != nil {
			return err
		}
		if replay {
			result, err = replayTaskArchive(response)
			return err
		}
		var next domain.Task
		if archive {
			if task.Archived() {
				return storecontract.ErrVersionConflict
			}
			next, err = task.Archive(now)
			if err == nil {
				err = tx.ArchiveTask(ctx, task.ID(), now)
			}
		} else {
			if !task.Archived() {
				return storecontract.ErrVersionConflict
			}
			next, err = task.Restore()
			if err == nil {
				err = tx.RestoreTask(ctx, task.ID())
			}
		}
		if err != nil {
			return err
		}
		result = ArchiveTaskResult{Task: next}
		response, err = taskArchiveResponse(result)
		if err != nil {
			return err
		}
		return tx.SaveCommand(ctx, key, response)
	})
	return result, err
}
