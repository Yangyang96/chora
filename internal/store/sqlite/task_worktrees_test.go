package sqlite_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
	"github.com/Yangyang96/chora/internal/store/sqlite"
)

func TestTaskWorktreeBindingPersistsCASAndListsRecoveryCandidates(t *testing.T) {
	ctx := context.Background()
	db, seeded := openSeeded(t)
	binding := newTaskWorktreeBinding(t, seeded.task.ID(), seeded.now)
	if err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error { return tx.InsertTaskWorktreeBinding(ctx, binding) }); err != nil {
		t.Fatal(err)
	}
	got, err := db.Reader().GetTaskWorktreeBinding(ctx, seeded.task.ID())
	if err != nil {
		t.Fatal(err)
	}
	if got.TaskID() != binding.TaskID() || got.RepositoryIdentity() != binding.RepositoryIdentity() || got.PinnedBaseRevision() != binding.PinnedBaseRevision() || got.PinnedBaseTree() != binding.PinnedBaseTree() || got.BaseRef() != binding.BaseRef() || got.StartPolicy() != binding.StartPolicy() || got.RelativeLocator() != binding.RelativeLocator() || got.ConfiguredRootFingerprint() != binding.ConfiguredRootFingerprint() || got.State() != domain.TaskWorktreeProvisioning || got.Version() != 1 {
		t.Fatalf("round trip=%#v", got)
	}
	candidates, err := db.Reader().ListTaskWorktreeRecoveryCandidates(ctx)
	if err != nil || len(candidates) != 1 || candidates[0].TaskID() != seeded.task.ID() || candidates[0].State() != domain.TaskWorktreeProvisioning {
		t.Fatalf("provisioning candidates=%#v err=%v", candidates, err)
	}
	recovery, err := got.RecoveryRequired("provisioning interrupted", seeded.now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		return tx.SaveTaskWorktreeBindingCAS(ctx, got.Version(), recovery)
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		return tx.SaveTaskWorktreeBindingCAS(ctx, got.Version(), recovery)
	}); !errors.Is(err, storecontract.ErrVersionConflict) {
		t.Fatalf("stale CAS err=%v", err)
	}
	candidates, err = db.Reader().ListTaskWorktreeRecoveryCandidates(ctx)
	if err != nil || len(candidates) != 1 || candidates[0].TaskID() != seeded.task.ID() || candidates[0].Reason() != "provisioning interrupted" {
		t.Fatalf("candidates=%#v err=%v", candidates, err)
	}
	retrying, err := recovery.RetryProvisioning(seeded.now.Add(2 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		return tx.SaveTaskWorktreeBindingCAS(ctx, recovery.Version(), retrying)
	}); err != nil {
		t.Fatal(err)
	}
	if candidates, err = db.Reader().ListTaskWorktreeRecoveryCandidates(ctx); err != nil || len(candidates) != 1 || candidates[0].State() != domain.TaskWorktreeProvisioning {
		t.Fatalf("retrying candidates=%#v err=%v", candidates, err)
	}
	ready, err := retrying.Ready(seeded.now.Add(3 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		return tx.SaveTaskWorktreeBindingCAS(ctx, retrying.Version(), ready)
	}); err != nil {
		t.Fatal(err)
	}
	if candidates, err = db.Reader().ListTaskWorktreeRecoveryCandidates(ctx); err != nil || len(candidates) != 0 {
		t.Fatalf("ready candidates=%#v err=%v", candidates, err)
	}
	drifted, err := ready.RecoveryRequired("ready worktree drifted", seeded.now.Add(4*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		return tx.SaveTaskWorktreeBindingCAS(ctx, ready.Version(), drifted)
	}); err != nil {
		t.Fatal(err)
	}
	if candidates, err = db.Reader().ListTaskWorktreeRecoveryCandidates(ctx); err != nil || len(candidates) != 1 || candidates[0].State() != domain.TaskWorktreeRecoveryRequired || !candidates[0].ReadyAt().IsZero() {
		t.Fatalf("drift candidates=%#v err=%v", candidates, err)
	}
	retryingAgain, err := drifted.RetryProvisioning(seeded.now.Add(5 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	readyAgain, err := retryingAgain.Ready(seeded.now.Add(6 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	secondDrift, err := readyAgain.RecoveryRequired("drifted again", seeded.now.Add(7*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		if err := tx.SaveTaskWorktreeBindingCAS(ctx, drifted.Version(), retryingAgain); err != nil {
			return err
		}
		if err := tx.SaveTaskWorktreeBindingCAS(ctx, retryingAgain.Version(), readyAgain); err != nil {
			return err
		}
		return tx.SaveTaskWorktreeBindingCAS(ctx, readyAgain.Version(), secondDrift)
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = sqlite.Open(ctx, seeded.path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	got, err = db.Reader().GetTaskWorktreeBinding(ctx, seeded.task.ID())
	if err != nil || got.State() != domain.TaskWorktreeRecoveryRequired || got.Version() != 8 || got.Reason() != "drifted again" || got.PinnedBaseTree() != binding.PinnedBaseTree() || got.BaseRef() != binding.BaseRef() || got.StartPolicy() != domain.TaskStartPolicyCurrentHEAD || !got.ReadyAt().IsZero() {
		t.Fatalf("reopened=%#v err=%v", got, err)
	}
}

func TestTaskWorktreeRecoveryCandidatesOrderProvisioningAndRecoveryDeterministically(t *testing.T) {
	ctx := context.Background()
	db, seeded := openSeeded(t)
	defer db.Close()
	criterion, err := domain.NewAcceptanceCriterion(domain.NewCriterionID(), "second works", "")
	if err != nil {
		t.Fatal(err)
	}
	secondTask, err := domain.NewTask(domain.NewTaskID(), seeded.room.ID(), "second task", "prove startup ordering", []domain.AcceptanceCriterion{criterion})
	if err != nil {
		t.Fatal(err)
	}
	first := newTaskWorktreeBinding(t, seeded.task.ID(), seeded.now)
	second := newTaskWorktreeBinding(t, secondTask.ID(), seeded.now.Add(time.Second))
	firstRecovery, err := first.RecoveryRequired("interrupted", seeded.now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		if err := tx.InsertTask(ctx, secondTask); err != nil {
			return err
		}
		if err := tx.InsertTaskWorktreeBinding(ctx, first); err != nil {
			return err
		}
		if err := tx.InsertTaskWorktreeBinding(ctx, second); err != nil {
			return err
		}
		return tx.SaveTaskWorktreeBindingCAS(ctx, first.Version(), firstRecovery)
	}); err != nil {
		t.Fatal(err)
	}
	candidates, err := db.Reader().ListTaskWorktreeRecoveryCandidates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 2 || candidates[0].TaskID() != secondTask.ID() || candidates[0].State() != domain.TaskWorktreeProvisioning || candidates[1].TaskID() != seeded.task.ID() || candidates[1].State() != domain.TaskWorktreeRecoveryRequired {
		t.Fatalf("ordered startup candidates=%#v", candidates)
	}
}

func TestTaskWorktreeBindingRejectsDuplicateMutationAndDelete(t *testing.T) {
	ctx := context.Background()
	db, seeded := openSeeded(t)
	defer db.Close()
	binding := newTaskWorktreeBinding(t, seeded.task.ID(), seeded.now)
	if err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error { return tx.InsertTaskWorktreeBinding(ctx, binding) }); err != nil {
		t.Fatal(err)
	}
	if err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error { return tx.InsertTaskWorktreeBinding(ctx, binding) }); !errors.Is(err, storecontract.ErrTaskWorktreeConflict) {
		t.Fatalf("duplicate err=%v", err)
	}
	recovery, err := binding.RecoveryRequired("retry", seeded.now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	forgedParams := domain.TaskWorktreeBindingParams{
		TaskID: binding.TaskID().String(), RepositoryIdentity: "other", PinnedBaseRevision: binding.PinnedBaseRevision(),
		RelativeLocator: "other-" + binding.TaskID().String() + "-other-topic", ConfiguredRootFingerprint: binding.ConfiguredRootFingerprint(),
		State: recovery.State(), Version: recovery.Version(), Reason: recovery.Reason(), CreatedAt: recovery.CreatedAt(), UpdatedAt: recovery.UpdatedAt(),
	}
	forged, err := domain.RestoreTaskWorktreeBinding(forgedParams)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		return tx.SaveTaskWorktreeBindingCAS(ctx, binding.Version(), forged)
	}); !errors.Is(err, storecontract.ErrVersionConflict) {
		t.Fatalf("identity mutation err=%v", err)
	}
	raw, err := sql.Open("sqlite", seeded.path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.ExecContext(ctx, `UPDATE task_worktrees SET repository_identity='other',relative_locator=? WHERE task_id=?`, "other-"+binding.TaskID().String()+"-topic", binding.TaskID().String()); err == nil {
		t.Fatal("direct identity mutation succeeded")
	}
	for _, query := range []string{
		`UPDATE task_worktrees SET pinned_base_tree='` + strings.Repeat("b", 40) + `' WHERE task_id=?`,
		`UPDATE task_worktrees SET base_ref='refs/heads/other' WHERE task_id=?`,
		`UPDATE task_worktrees SET start_policy='legacy_admitted_base',pinned_base_tree='',base_ref='' WHERE task_id=?`,
	} {
		if _, err := raw.ExecContext(ctx, query, binding.TaskID().String()); err == nil {
			t.Fatalf("direct base authority mutation succeeded: %s", query)
		}
	}
	if _, err := raw.ExecContext(ctx, `DELETE FROM task_worktrees WHERE task_id=?`, binding.TaskID().String()); err == nil {
		t.Fatal("direct delete succeeded")
	}
	if _, err := db.Reader().GetTaskWorktreeBinding(ctx, domain.NewTaskID()); !errors.Is(err, storecontract.ErrNotFound) {
		t.Fatalf("missing binding err=%v", err)
	}
}

func newTaskWorktreeBinding(t *testing.T, taskID domain.TaskID, at time.Time) domain.TaskWorktreeBinding {
	t.Helper()
	binding, err := domain.NewTaskWorktreeBinding(domain.TaskWorktreeBindingParams{
		TaskID: taskID.String(), RepositoryIdentity: "chora", PinnedBaseRevision: strings.Repeat("a", 40),
		PinnedBaseTree: strings.Repeat("c", 40), BaseRef: "refs/heads/功能+p2.LOCK", StartPolicy: domain.TaskStartPolicyCurrentHEAD,
		RelativeLocator: "chora-" + taskID.String() + "-topic", ConfiguredRootFingerprint: sha256.Sum256([]byte("configured siblings")), CreatedAt: at,
	})
	if err != nil {
		t.Fatal(err)
	}
	return binding
}
