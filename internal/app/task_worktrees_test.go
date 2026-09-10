package app

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
	"github.com/Yangyang96/chora/internal/store/sqlite"
)

type taskWorktreeManagerFake struct {
	err   error
	calls []domain.TaskWorktreeState
}

func (manager *taskWorktreeManagerFake) Plan(context.Context, domain.Task, time.Time) (domain.TaskWorktreeBinding, error) {
	return domain.TaskWorktreeBinding{}, errors.New("not used")
}

func (manager *taskWorktreeManagerFake) Ensure(_ context.Context, binding domain.TaskWorktreeBinding) error {
	manager.calls = append(manager.calls, binding.State())
	return manager.err
}

func (manager *taskWorktreeManagerFake) ResolveExecutionRoot(context.Context, domain.Task) (string, error) {
	return "", nil
}

type taskWorktreeClock struct{ now time.Time }

func (clock *taskWorktreeClock) Now() time.Time {
	clock.now = clock.now.Add(time.Second)
	return clock.now
}

func TestTaskWorktreeStartupRecoveryAndReadyDriftFailClosed(t *testing.T) {
	ctx := context.Background()
	dbRoot := t.TempDir()
	if err := os.Chmod(dbRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := sqlite.Open(ctx, filepath.Join(dbRoot, "chora.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	room, err := domain.NewRoom(domain.RoomParams{ID: domain.NewRoomID(), Name: "room", Description: "test", WorkspaceRoot: t.TempDir(), CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	criterion, _ := domain.NewAcceptanceCriterion(domain.NewCriterionID(), "works", "")
	task, err := domain.NewTask(domain.NewTaskID(), room.ID(), "Task workspace", "goal", []domain.AcceptanceCriterion{criterion})
	if err != nil {
		t.Fatal(err)
	}
	binding, err := domain.NewTaskWorktreeBinding(domain.TaskWorktreeBindingParams{
		TaskID: task.ID().String(), RepositoryIdentity: "chora", PinnedBaseRevision: strings.Repeat("a", 40),
		RelativeLocator: "chora-" + task.ID().String() + "-task-workspace", ConfiguredRootFingerprint: sha256.Sum256([]byte("root")), CreatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		if err := tx.InsertRoom(ctx, room); err != nil {
			return err
		}
		if err := tx.InsertTask(ctx, task); err != nil {
			return err
		}
		return tx.InsertTaskWorktreeBinding(ctx, binding)
	}); err != nil {
		t.Fatal(err)
	}
	manager := &taskWorktreeManagerFake{err: errors.New("git unavailable")}
	service := NewService(Dependencies{Store: db, TaskWorktrees: manager, Clock: &taskWorktreeClock{now: now}})
	if err := service.RecoverStartup(ctx); err != nil {
		t.Fatal(err)
	}
	failed, err := db.Reader().GetTaskWorktreeBinding(ctx, task.ID())
	if err != nil || failed.State() != domain.TaskWorktreeRecoveryRequired {
		t.Fatalf("failed binding=%#v err=%v", failed, err)
	}
	manager.err = nil
	if err := service.RecoverStartup(ctx); err != nil {
		t.Fatal(err)
	}
	ready, err := db.Reader().GetTaskWorktreeBinding(ctx, task.ID())
	if err != nil || ready.State() != domain.TaskWorktreeReady {
		t.Fatalf("ready binding=%#v err=%v", ready, err)
	}
	manager.err = errors.New("ready worktree drifted")
	if err := service.ensureExistingTaskWorktree(ctx, task.ID()); !errors.Is(err, ErrTaskWorktreeUnavailable) {
		t.Fatalf("ensure ready drift err=%v", err)
	}
	drifted, err := db.Reader().GetTaskWorktreeBinding(ctx, task.ID())
	if err != nil || drifted.State() != domain.TaskWorktreeRecoveryRequired || drifted.Reason() == "" {
		t.Fatalf("drifted binding=%#v err=%v", drifted, err)
	}
	if len(manager.calls) != 3 || manager.calls[0] != domain.TaskWorktreeProvisioning || manager.calls[1] != domain.TaskWorktreeProvisioning || manager.calls[2] != domain.TaskWorktreeReady {
		t.Fatalf("Ensure states=%v", manager.calls)
	}
}
