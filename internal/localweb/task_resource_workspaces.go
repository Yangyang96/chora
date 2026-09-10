package localweb

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/gitsource"
	storecontract "github.com/Yangyang96/chora/internal/store"
	"github.com/Yangyang96/chora/internal/taskdelivery"
)

const taskResourceWorkspaceFingerprintVersion = "chora.task-resource-workspaces.v1"

var errTaskResourceWorkspaceNotProven = errors.New("Task resource workspace is not proven")
var errTaskResourceWorkspacesCleaned = errors.New("Task worktrees have been cleaned up. Review and delivery history remain available.")

// Process-wide locks serialize the two shared authorities involved in worktree
// preparation: one Task root and one repository common Git directory. Git also
// locks its administration files, but serializing here makes retries stable and
// avoids exposing ordinary lock contention as a recovery condition.
var taskResourceWorkspaceLocks = struct {
	sync.Mutex
	values map[string]*sync.Mutex
}{values: make(map[string]*sync.Mutex)}

func taskResourceLock(key string) *sync.Mutex {
	taskResourceWorkspaceLocks.Lock()
	defer taskResourceWorkspaceLocks.Unlock()
	lock := taskResourceWorkspaceLocks.values[key]
	if lock == nil {
		lock = &sync.Mutex{}
		taskResourceWorkspaceLocks.values[key] = lock
	}
	return lock
}

type taskResourceWorkspaceManager struct {
	store           storecontract.Store
	dataRoot        string
	workspaceRoot   string
	rootFingerprint string
}

func newTaskResourceWorkspaceManager(store storecontract.Store, dataRoot string) (app.TaskResourceWorkspaceResolver, error) {
	if store == nil || !cleanAbsolutePath(dataRoot) {
		return nil, fmt.Errorf("%w: invalid manager configuration", errTaskResourceWorkspaceNotProven)
	}
	info, err := os.Lstat(dataRoot)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%w: configured data root is unavailable or unsafe", errTaskResourceWorkspaceNotProven)
	}
	canonical, err := filepath.EvalSymlinks(dataRoot)
	if err != nil || filepath.Clean(canonical) != dataRoot {
		return nil, fmt.Errorf("%w: configured data root is not canonical", errTaskResourceWorkspaceNotProven)
	}
	digest := sha256.Sum256([]byte(taskResourceWorkspaceFingerprintVersion + "\x00" + dataRoot))
	return &taskResourceWorkspaceManager{
		store:           store,
		dataRoot:        dataRoot,
		workspaceRoot:   filepath.Join(dataRoot, "task-workspaces"),
		rootFingerprint: hex.EncodeToString(digest[:]),
	}, nil
}

func (manager *taskResourceWorkspaceManager) Ensure(ctx context.Context, taskID domain.TaskID) (resultErr error) {
	if manager == nil || !taskID.Valid() {
		return fmt.Errorf("%w: invalid Task", errTaskResourceWorkspaceNotProven)
	}
	lock := taskResourceLock(manager.rootFingerprint + "\x00task\x00" + taskID.String())
	lock.Lock()
	defer lock.Unlock()

	metadataCtx, metadataCancel := context.WithTimeout(context.WithoutCancel(ctx), domain.RepositoryMetadataTimeout)
	defer metadataCancel()
	snapshot, err := manager.snapshot(metadataCtx, taskID)
	if err != nil {
		return err
	}
	rows, err := manager.planRows(metadataCtx, taskID, snapshot)
	if err != nil {
		return err
	}
	created := make([]bool, len(rows))
	defer func() {
		if resultErr != nil {
			resultErr = errors.Join(resultErr, manager.cleanupUnstartedResources(ctx, snapshot.Resources, rows, created))
		}
	}()
	if err := ctx.Err(); err != nil {
		return manager.failRemaining(ctx, rows, 0, "cancelled", "preparation cancelled", err)
	}
	if err := manager.ensureTaskRoot(taskID, snapshot.Resources[0]); err != nil {
		return manager.failRemaining(ctx, rows, 0, "failed", "workspace root unavailable", err)
	}

	for index, resource := range snapshot.Resources {
		if err := ctx.Err(); err != nil {
			return manager.failRemaining(ctx, rows, index, "cancelled", "preparation cancelled", err)
		}
		if rows[index].State == "ready" {
			if err := manager.verifyResourceWorktree(ctx, resource, rows[index]); err != nil {
				return manager.failRemaining(ctx, rows, index, "recovery_required", "ready worktree proof failed", err)
			}
			continue
		}
		if rows[index].State == "recovery_required" {
			return manager.failRemaining(ctx, rows, index, "recovery_required", "worktree requires recovery", errTaskResourceWorkspaceNotProven)
		}

		repositoryLock := taskResourceLock("repository\x00" + resource.PhysicalIdentity)
		repositoryLock.Lock()
		err := manager.prepareResource(ctx, resource, &rows[index], &created[index])
		repositoryLock.Unlock()
		if err != nil {
			state, reason := "failed", "repository preparation failed"
			if errors.Is(err, context.DeadlineExceeded) {
				reason = "repository preparation timed out"
			}
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) && ctx.Err() != nil {
				state, reason = "cancelled", "preparation cancelled"
			}
			return manager.failRemaining(ctx, rows, index, state, reason, err)
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func (manager *taskResourceWorkspaceManager) ResolveExecutionRoot(ctx context.Context, taskID domain.TaskID) (string, error) {
	return manager.resolveRoot(ctx, taskID, false)
}

// ResolveHandoffRoot proves the existing Task branch for an editor/terminal,
// including commits made during delivery. It never grants execution authority.
func (manager *taskResourceWorkspaceManager) ResolveHandoffRoot(ctx context.Context, taskID domain.TaskID) (string, error) {
	return manager.resolveRoot(ctx, taskID, true)
}

func (manager *taskResourceWorkspaceManager) resolveRoot(ctx context.Context, taskID domain.TaskID, allowCommittedHead bool) (string, error) {
	if manager == nil || !taskID.Valid() {
		return "", fmt.Errorf("%w: invalid Task", errTaskResourceWorkspaceNotProven)
	}
	lock := taskResourceLock(manager.rootFingerprint + "\x00task\x00" + taskID.String())
	lock.Lock()
	defer lock.Unlock()

	snapshot, err := manager.snapshot(ctx, taskID)
	if err != nil {
		return "", err
	}
	rows, err := manager.store.Reader().ListTaskRepositoryWorktrees(ctx, taskID)
	if err != nil {
		return "", err
	}
	if len(rows) != len(snapshot.Resources) {
		return "", fmt.Errorf("%w: incomplete worktree records", errTaskResourceWorkspaceNotProven)
	}
	if err := manager.proveTaskRoot(taskID, snapshot.Resources[0]); err != nil {
		return "", err
	}
	var operations []storecontract.DeliveryOperation
	if allowCommittedHead {
		operations, err = manager.store.Reader().ListDeliveryOperations(ctx, taskID)
		if err != nil {
			return "", err
		}
	}
	liveWorktrees := 0
	for index, resource := range snapshot.Resources {
		row := rows[index]
		if row.RepositoryID.String() != resource.RepoID || row.State != "ready" || !manager.rowMatches(taskID, resource, row) {
			return "", fmt.Errorf("%w: worktree record does not match immutable resources", errTaskResourceWorkspaceNotProven)
		}
		// Only external-tool handoff may omit a worktree removed by a proven
		// successful cleanup. Execution still requires every frozen resource.
		cleaned, err := manager.provenResourceCleanup(ctx, taskID, resource, row, operations)
		if err != nil {
			return "", err
		}
		if cleaned {
			continue
		}
		if err := manager.verifyResourceWorktreeHead(ctx, resource, row, allowCommittedHead); err != nil {
			return "", err
		}
		liveWorktrees++
	}
	if liveWorktrees == 0 {
		return "", errTaskResourceWorkspacesCleaned
	}
	return manager.taskRoot(taskID, snapshot.Resources[0]), nil
}

func (manager *taskResourceWorkspaceManager) provenResourceCleanup(ctx context.Context, taskID domain.TaskID, resource domain.TaskRepositoryResource, row domain.TaskRepositoryWorktree, operations []storecontract.DeliveryOperation) (bool, error) {
	if resource.DeliveryMode != domain.TaskResourceDeliveryTaskBranch {
		return false, nil
	}
	expected := taskdelivery.Binding{Root: filepath.Join(manager.dataRoot, row.RelativePath), CommonGitDir: resource.CommonGitDir, BaseCommit: resource.BaseCommit, BaseTree: resource.BaseTree, Branch: resource.TaskBranch, TargetRef: resource.BaseRef}
	for _, operation := range operations {
		if operation.TaskID != taskID || operation.RepositoryID.String() != resource.RepoID || operation.Kind != "cleanup" || operation.State != "succeeded" {
			continue
		}
		var intent struct {
			Binding                                taskdelivery.Binding
			Cleanup                                *taskdelivery.CleanupPreview
			ResultCleanup                          *taskdelivery.ResultCleanupPreview
			ClosedResultID, ResultDigest, ReviewID string
		}
		if err := json.Unmarshal(operation.PreviewJSON, &intent); err != nil || intent.Binding != expected {
			return false, fmt.Errorf("%w: cleanup does not match immutable workspace ownership", errTaskResourceWorkspaceNotProven)
		}
		if intent.ResultCleanup != nil {
			id, err := domain.ParseResultID(intent.ClosedResultID)
			if err != nil || intent.Cleanup != nil || intent.ResultCleanup.Binding != expected {
				return false, errTaskResourceWorkspaceNotProven
			}
			closure, err := manager.store.Reader().GetResultClosure(ctx, id)
			if err != nil || closure.TaskID != taskID || closure.RunID != operation.RunID || hex.EncodeToString(closure.ResultDigest[:]) != intent.ResultDigest || closure.ReviewID != intent.ReviewID {
				return false, errTaskResourceWorkspaceNotProven
			}
			found := false
			for _, repo := range closure.RepositoryIDs {
				if repo == resource.RepoID {
					found = true
				}
			}
			if !found {
				return false, errTaskResourceWorkspaceNotProven
			}
			if err := (taskdelivery.Git{}).ReconcileResultCleanup(ctx, *intent.ResultCleanup); err != nil {
				return false, fmt.Errorf("%w: closed result cleanup cannot be proven: %v", errTaskResourceWorkspaceNotProven, err)
			}
			return true, nil
		}
		if intent.Cleanup == nil || intent.Cleanup.Binding != expected {
			return false, fmt.Errorf("%w: cleanup does not match immutable workspace ownership", errTaskResourceWorkspaceNotProven)
		}
		// Recheck absence and retained registration/branch proof. A replacement
		// directory or symlink must never become an external-tool destination.
		if err := (taskdelivery.Git{}).ReconcileCleanup(ctx, *intent.Cleanup); err != nil {
			return false, fmt.Errorf("%w: cleanup cannot be proven: %v", errTaskResourceWorkspaceNotProven, err)
		}
		return true, nil
	}
	return false, nil
}

func (manager *taskResourceWorkspaceManager) snapshot(ctx context.Context, taskID domain.TaskID) (domain.TaskResourceSnapshot, error) {
	record, err := manager.store.Reader().GetTaskResourceSnapshot(ctx, taskID)
	if err != nil {
		return domain.TaskResourceSnapshot{}, fmt.Errorf("%w: immutable resource snapshot: %v", errTaskResourceWorkspaceNotProven, err)
	}
	var snapshot domain.TaskResourceSnapshot
	decoder := json.NewDecoder(bytes.NewReader(record.CanonicalJSON))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&snapshot); err != nil {
		return snapshot, fmt.Errorf("%w: decode resource snapshot", errTaskResourceWorkspaceNotProven)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return snapshot, fmt.Errorf("%w: trailing resource snapshot data", errTaskResourceWorkspaceNotProven)
	}
	canonical, digest, err := snapshot.CanonicalJSON()
	if err != nil || snapshot.TaskID != taskID.String() || digest != record.Digest || !bytes.Equal(canonical, record.CanonicalJSON) {
		return snapshot, fmt.Errorf("%w: resource snapshot integrity", errTaskResourceWorkspaceNotProven)
	}
	return snapshot, nil
}

func (manager *taskResourceWorkspaceManager) planRows(ctx context.Context, taskID domain.TaskID, snapshot domain.TaskResourceSnapshot) ([]domain.TaskRepositoryWorktree, error) {
	var planned []domain.TaskRepositoryWorktree
	err := manager.store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		existing, err := tx.ListTaskRepositoryWorktrees(ctx, taskID)
		if err != nil {
			return err
		}
		byRepository := make(map[string]domain.TaskRepositoryWorktree, len(existing))
		for _, row := range existing {
			byRepository[row.RepositoryID.String()] = row
		}
		if len(byRepository) != len(existing) || len(existing) > len(snapshot.Resources) {
			return fmt.Errorf("%w: unexpected worktree records", errTaskResourceWorkspaceNotProven)
		}
		now := time.Now().UTC()
		planned = make([]domain.TaskRepositoryWorktree, 0, len(snapshot.Resources))
		for _, resource := range snapshot.Resources {
			repositoryID, _ := domain.ParseRepositoryID(resource.RepoID)
			targetRef := ""
			if resource.DeliveryMode == domain.TaskResourceDeliveryTaskBranch {
				targetRef = resource.BaseRef
			}
			row, found := byRepository[resource.RepoID]
			if !found {
				row = domain.TaskRepositoryWorktree{
					TaskID: taskID, RepositoryID: repositoryID,
					RelativePath: manager.relativePath(taskID, resource), RootFingerprint: manager.rootFingerprint,
					BaseCommit: resource.BaseCommit, BaseTree: resource.BaseTree, DeliveryMode: resource.DeliveryMode, TaskBranch: resource.TaskBranch, TargetRef: targetRef,
					State: "preparing", Version: 1, CreatedAt: now, UpdatedAt: now,
				}
				if err := tx.SaveTaskRepositoryWorktree(ctx, 0, row); err != nil {
					return err
				}
			} else {
				delete(byRepository, resource.RepoID)
				if !manager.rowMatches(taskID, resource, row) {
					return fmt.Errorf("%w: persisted worktree authority differs", errTaskResourceWorkspaceNotProven)
				}
				switch row.State {
				case "failed", "cancelled":
					next := row
					next.State, next.Reason, next.Version, next.UpdatedAt = "preparing", "", row.Version+1, now
					if err := tx.SaveTaskRepositoryWorktree(ctx, row.Version, next); err != nil {
						return err
					}
					row = next
				case "preparing", "ready", "recovery_required":
				default:
					return fmt.Errorf("%w: invalid persisted worktree state", errTaskResourceWorkspaceNotProven)
				}
			}
			planned = append(planned, row)
		}
		if len(byRepository) != 0 {
			return fmt.Errorf("%w: stale worktree records", errTaskResourceWorkspaceNotProven)
		}
		return nil
	})
	return planned, err
}

func (manager *taskResourceWorkspaceManager) prepareResource(parent context.Context, resource domain.TaskRepositoryResource, row *domain.TaskRepositoryWorktree, created *bool) error {
	ctx, cancel := context.WithTimeout(parent, domain.RepositoryPreparationTimeout)
	defer cancel()
	inspection, err := (gitsource.Default{}).Inspect(ctx, resource.Checkout)
	if err != nil || inspection.CanonicalPath != resource.Checkout || inspection.CommonGitDir != resource.CommonGitDir || inspection.PhysicalIdentity != resource.PhysicalIdentity {
		return fmt.Errorf("%w: configured repository identity changed", errTaskResourceWorkspaceNotProven)
	}
	if err := gitsource.ProveRevision(ctx, resource.Checkout, resource.BaseCommit, resource.BaseTree); err != nil {
		return fmt.Errorf("%w: pinned base is unavailable", errTaskResourceWorkspaceNotProven)
	}

	path := filepath.Join(manager.dataRoot, row.RelativePath)
	if _, err := os.Lstat(path); err == nil {
		if err := manager.verifyResourceWorktree(ctx, resource, *row); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: inspect destination", errTaskResourceWorkspaceNotProven)
	} else {
		estimate, err := estimateRevisionBytes(ctx, resource.Checkout, resource.BaseCommit)
		if err != nil {
			return err
		}
		if err := ensureWorkspaceCapacity(manager.dataRoot, estimate); err != nil {
			return err
		}
		var command *exec.Cmd
		if resource.DeliveryMode == domain.TaskResourceDeliveryTaskBranch {
			if err := manager.claimTaskResourceBranch(ctx, resource, *row); err != nil {
				return err
			}
			command = isolatedGitCommand(ctx, resource.Checkout, "worktree", "add", path, resource.TaskBranch)
		} else {
			command = isolatedGitCommand(ctx, resource.Checkout, "worktree", "add", "--detach", path, resource.BaseCommit)
		}
		// Only the absent worktree destination, never an unrelated branch ref,
		// becomes eligible for bounded preparation cleanup.
		*created = true
		command.Stdout = io.Discard
		command.Stderr = &limitedDiscardWriter{remaining: domain.RepositoryMetadataBytes}
		if err := command.Run(); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("%w: git worktree add", errTaskResourceWorkspaceNotProven)
		}
		if err := manager.verifyResourceWorktree(ctx, resource, *row); err != nil {
			return err
		}
	}

	next := *row
	next.State, next.Reason, next.Version, next.UpdatedAt = "ready", "", row.Version+1, time.Now().UTC()
	if err := manager.saveRow(context.WithoutCancel(parent), row.Version, next); err != nil {
		return err
	}
	*row = next
	return nil
}

// Only paths absent before this Ensure invocation are eligible for cleanup.
// Existing worktrees may contain prior execution/user work and are never removed.
func (manager *taskResourceWorkspaceManager) cleanupUnstartedResources(parent context.Context, resources []domain.TaskRepositoryResource, rows []domain.TaskRepositoryWorktree, created []bool) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), domain.RepositoryMetadataTimeout)
	defer cancel()
	var failures []error
	for index, owned := range created {
		if !owned {
			continue
		}
		resource, row := resources[index], rows[index]
		lock := taskResourceLock("repository\x00" + resource.PhysicalIdentity)
		lock.Lock()
		cleanupErr := manager.verifyResourceWorktree(ctx, resource, row)
		if cleanupErr == nil {
			command := isolatedGitCommand(ctx, resource.Checkout, "worktree", "remove", filepath.Join(manager.dataRoot, row.RelativePath))
			command.Stdout = io.Discard
			command.Stderr = &limitedDiscardWriter{remaining: domain.RepositoryMetadataBytes}
			cleanupErr = command.Run()
		}
		lock.Unlock()
		next := row
		next.State, next.Reason = "failed", "preparation failed; newly created unused worktree removed"
		if parent.Err() != nil {
			next.State, next.Reason = "cancelled", "preparation cancelled; newly created unused worktree removed"
		}
		if cleanupErr != nil {
			next.State, next.Reason = "recovery_required", "newly created unused worktree cleanup could not be proven; retained for recovery"
			failures = append(failures, fmt.Errorf("%w: cleanup repository %s", errTaskResourceWorkspaceNotProven, resource.RepoID))
		}
		next.Version, next.UpdatedAt = row.Version+1, time.Now().UTC()
		if err := manager.saveRow(ctx, row.Version, next); err != nil {
			failures = append(failures, err)
		} else {
			rows[index] = next
		}
	}
	return errors.Join(failures...)
}

func (manager *taskResourceWorkspaceManager) failRemaining(ctx context.Context, rows []domain.TaskRepositoryWorktree, start int, state, reason string, cause error) error {
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), domain.RepositoryMetadataTimeout)
	defer cancel()
	var persistErrors []error
	for index := start; index < len(rows); index++ {
		if rows[index].State == "ready" || rows[index].State == "recovery_required" && state != "recovery_required" {
			continue
		}
		next := rows[index]
		next.State, next.Reason, next.Version, next.UpdatedAt = state, reason, next.Version+1, time.Now().UTC()
		if err := manager.saveRow(persistCtx, rows[index].Version, next); err != nil {
			persistErrors = append(persistErrors, err)
			continue
		}
		rows[index] = next
	}
	if len(persistErrors) > 0 {
		return errors.Join(cause, fmt.Errorf("persist preparation outcome: %w", errors.Join(persistErrors...)))
	}
	return cause
}

func (manager *taskResourceWorkspaceManager) saveRow(ctx context.Context, expected uint64, row domain.TaskRepositoryWorktree) error {
	return manager.store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		return tx.SaveTaskRepositoryWorktree(ctx, expected, row)
	})
}

func (manager *taskResourceWorkspaceManager) verifyResourceWorktree(ctx context.Context, resource domain.TaskRepositoryResource, row domain.TaskRepositoryWorktree) error {
	return manager.verifyResourceWorktreeHead(ctx, resource, row, false)
}

func (manager *taskResourceWorkspaceManager) verifyResourceWorktreeHead(ctx context.Context, resource domain.TaskRepositoryResource, row domain.TaskRepositoryWorktree, allowCommittedHead bool) error {
	if !manager.rowMatches(row.TaskID, resource, row) {
		return fmt.Errorf("%w: worktree row authority differs", errTaskResourceWorkspaceNotProven)
	}
	inspection, err := (gitsource.Default{}).Inspect(ctx, resource.Checkout)
	if err != nil || inspection.CanonicalPath != resource.Checkout || inspection.CommonGitDir != resource.CommonGitDir || inspection.PhysicalIdentity != resource.PhysicalIdentity {
		return fmt.Errorf("%w: configured repository identity changed", errTaskResourceWorkspaceNotProven)
	}
	if err := gitsource.ProveRevision(ctx, resource.Checkout, resource.BaseCommit, resource.BaseTree); err != nil {
		return fmt.Errorf("%w: pinned base is unavailable", errTaskResourceWorkspaceNotProven)
	}
	path := filepath.Join(manager.dataRoot, row.RelativePath)
	if err := manager.proveManagedDirectory(row.TaskID, resource, path); err != nil {
		return err
	}
	top, err := gitTargetOutputBounded(ctx, path, domain.RepositoryMetadataBytes, "rev-parse", "--show-toplevel")
	if err != nil || filepath.Clean(strings.TrimSpace(string(top))) != path {
		return fmt.Errorf("%w: destination is not the exact worktree root", errTaskResourceWorkspaceNotProven)
	}
	common, err := taskWorktreeCommonDirectory(ctx, path)
	if err != nil || common != resource.CommonGitDir {
		return fmt.Errorf("%w: worktree belongs to a different repository", errTaskResourceWorkspaceNotProven)
	}
	if allowCommittedHead && resource.DeliveryMode == domain.TaskResourceDeliveryTaskBranch {
		if _, err := gitTargetOutputBounded(ctx, path, domain.RepositoryMetadataBytes, "merge-base", "--is-ancestor", resource.BaseCommit, "HEAD"); err != nil {
			return fmt.Errorf("%w: Task branch no longer contains its pinned base", errTaskResourceWorkspaceNotProven)
		}
	} else {
		head, err := gitTargetOutputBounded(ctx, path, domain.RepositoryMetadataBytes, "rev-parse", "--verify", "HEAD^{commit}")
		if err != nil || strings.TrimSpace(string(head)) != resource.BaseCommit {
			return fmt.Errorf("%w: worktree HEAD differs from pinned base", errTaskResourceWorkspaceNotProven)
		}
		tree, err := gitTargetOutputBounded(ctx, path, domain.RepositoryMetadataBytes, "rev-parse", "--verify", "HEAD^{tree}")
		if err != nil || strings.TrimSpace(string(tree)) != resource.BaseTree {
			return fmt.Errorf("%w: worktree tree differs from pinned base", errTaskResourceWorkspaceNotProven)
		}
	}
	symbolic, symbolicErr := gitTargetOutputBounded(ctx, path, domain.RepositoryMetadataBytes, "symbolic-ref", "-q", "HEAD")
	if resource.DeliveryMode == domain.TaskResourceDeliveryTaskBranch {
		if symbolicErr != nil || strings.TrimSpace(string(symbolic)) != "refs/heads/"+resource.TaskBranch {
			return fmt.Errorf("%w: worktree is not on the exact Task branch", errTaskResourceWorkspaceNotProven)
		}
	} else if symbolicErr == nil || len(bytes.TrimSpace(symbolic)) != 0 {
		return fmt.Errorf("%w: legacy worktree HEAD is not detached", errTaskResourceWorkspaceNotProven)
	}
	return nil
}

func (manager *taskResourceWorkspaceManager) rowMatches(taskID domain.TaskID, resource domain.TaskRepositoryResource, row domain.TaskRepositoryWorktree) bool {
	targetRef := ""
	if resource.DeliveryMode == domain.TaskResourceDeliveryTaskBranch {
		targetRef = resource.BaseRef
	}
	return row.TaskID == taskID && row.RepositoryID.String() == resource.RepoID &&
		row.RelativePath == manager.relativePath(taskID, resource) &&
		row.RootFingerprint == manager.rootFingerprint && row.BaseCommit == resource.BaseCommit && row.BaseTree == resource.BaseTree &&
		row.DeliveryMode == resource.DeliveryMode && row.TaskBranch == resource.TaskBranch && row.TargetRef == targetRef &&
		row.Version > 0 && !row.CreatedAt.IsZero() && !row.UpdatedAt.Before(row.CreatedAt)
}

func (manager *taskResourceWorkspaceManager) relativePath(taskID domain.TaskID, resource domain.TaskRepositoryResource) string {
	return filepath.Join("task-workspaces", resource.TaskWorkspaceDirectory(taskID), resource.WorkspaceDirectory())
}

func (manager *taskResourceWorkspaceManager) taskRoot(taskID domain.TaskID, resource domain.TaskRepositoryResource) string {
	return filepath.Join(manager.workspaceRoot, resource.TaskWorkspaceDirectory(taskID))
}

func (manager *taskResourceWorkspaceManager) ensureTaskRoot(taskID domain.TaskID, resource domain.TaskRepositoryResource) error {
	if err := ensureRealDirectory(manager.workspaceRoot); err != nil {
		return fmt.Errorf("%w: task workspace parent: %v", errTaskResourceWorkspaceNotProven, err)
	}
	root := manager.taskRoot(taskID, resource)
	if resource.WorkspaceName != "" {
		if err := os.Mkdir(root, 0o700); err == nil {
			if err := os.WriteFile(filepath.Join(root, ".chora-task"), []byte(taskID.String()), 0o600); err != nil {
				return err
			}
		} else if !errors.Is(err, os.ErrExist) {
			return err
		}
	}
	if err := ensureRealDirectory(root); err != nil {
		return fmt.Errorf("%w: Task root: %v", errTaskResourceWorkspaceNotProven, err)
	}
	return manager.proveTaskRootOnly(taskID, resource)
}

func (manager *taskResourceWorkspaceManager) proveTaskRoot(taskID domain.TaskID, resource domain.TaskRepositoryResource) error {
	if err := manager.proveTaskRootOnly(taskID, resource); err != nil {
		return err
	}
	return nil
}

func (manager *taskResourceWorkspaceManager) proveTaskRootOnly(taskID domain.TaskID, resource domain.TaskRepositoryResource) error {
	parentInfo, err := os.Lstat(manager.workspaceRoot)
	if err != nil || !parentInfo.IsDir() || parentInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: Task workspace parent is unavailable or unsafe", errTaskResourceWorkspaceNotProven)
	}
	parentCanonical, err := filepath.EvalSymlinks(manager.workspaceRoot)
	if err != nil || filepath.Clean(parentCanonical) != manager.workspaceRoot || filepath.Dir(manager.workspaceRoot) != manager.dataRoot {
		return fmt.Errorf("%w: Task workspace parent escaped configured storage", errTaskResourceWorkspaceNotProven)
	}
	root := manager.taskRoot(taskID, resource)
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: Task root is unavailable or unsafe", errTaskResourceWorkspaceNotProven)
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil || filepath.Clean(canonical) != root || filepath.Dir(root) != manager.workspaceRoot {
		return fmt.Errorf("%w: Task root escaped configured storage", errTaskResourceWorkspaceNotProven)
	}
	if resource.WorkspaceName != "" {
		owner := filepath.Join(root, ".chora-task")
		info, err := os.Lstat(owner)
		if err != nil || !info.Mode().IsRegular() || info.Size() != int64(len(taskID.String())) {
			return fmt.Errorf("%w: Task root owner unavailable", errTaskResourceWorkspaceNotProven)
		}
		content, err := os.ReadFile(owner)
		if err != nil || string(content) != taskID.String() {
			return fmt.Errorf("%w: Task root belongs to another Task", errTaskResourceWorkspaceNotProven)
		}
	}
	return nil
}

func (manager *taskResourceWorkspaceManager) proveManagedDirectory(taskID domain.TaskID, resource domain.TaskRepositoryResource, path string) error {
	if err := manager.proveTaskRoot(taskID, resource); err != nil {
		return err
	}
	expected := filepath.Join(manager.taskRoot(taskID, resource), resource.WorkspaceDirectory())
	if path != expected || filepath.Dir(path) != manager.taskRoot(taskID, resource) {
		return fmt.Errorf("%w: repository path is outside the Task root", errTaskResourceWorkspaceNotProven)
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: repository worktree is unavailable or unsafe", errTaskResourceWorkspaceNotProven)
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil || filepath.Clean(canonical) != path {
		return fmt.Errorf("%w: repository worktree path is not canonical", errTaskResourceWorkspaceNotProven)
	}
	return nil
}

func ensureRealDirectory(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(path, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		info, err = os.Lstat(path)
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("path is not a real directory")
	}
	return nil
}

func estimateRevisionBytes(ctx context.Context, root, commit string) (uint64, error) {
	command := isolatedGitCommand(ctx, root, "ls-tree", "-r", "-l", "-z", commit)
	stdout, err := command.StdoutPipe()
	if err != nil {
		return 0, err
	}
	command.Stderr = &limitedDiscardWriter{remaining: domain.RepositoryMetadataBytes}
	if err := command.Start(); err != nil {
		return 0, err
	}
	reader := bufio.NewReaderSize(stdout, 64<<10)
	var total uint64
	for {
		record, readErr := readBoundedNullRecord(reader, domain.RepositoryMetadataBytes)
		if len(record) > 0 {
			tab := bytes.IndexByte(record, '\t')
			if tab < 0 {
				_ = command.Process.Kill()
				_ = command.Wait()
				return 0, fmt.Errorf("%w: malformed tree metadata", errTaskResourceWorkspaceNotProven)
			}
			fields := strings.Fields(string(record[:tab]))
			if len(fields) >= 4 && fields[1] == "blob" && fields[3] != "-" {
				size, parseErr := strconv.ParseUint(fields[3], 10, 64)
				if parseErr != nil || ^uint64(0)-total < size {
					_ = command.Process.Kill()
					_ = command.Wait()
					return 0, fmt.Errorf("%w: repository size overflow", errTaskResourceWorkspaceNotProven)
				}
				total += size
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			_ = command.Process.Kill()
			_ = command.Wait()
			return 0, readErr
		}
	}
	if err := command.Wait(); err != nil {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		return 0, fmt.Errorf("%w: estimate repository checkout", errTaskResourceWorkspaceNotProven)
	}
	return total, nil
}

func readBoundedNullRecord(reader *bufio.Reader, limit int) ([]byte, error) {
	var record []byte
	for {
		part, err := reader.ReadSlice(0)
		if len(record)+len(part) > limit+1 {
			return nil, fmt.Errorf("%w: tree entry metadata exceeds limit", errTaskResourceWorkspaceNotProven)
		}
		record = append(record, part...)
		if err == nil {
			return bytes.TrimSuffix(record, []byte{0}), nil
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return bytes.TrimSuffix(record, []byte{0}), err
	}
}

func ensureWorkspaceCapacity(path string, estimate uint64) error {
	if ^uint64(0)-estimate < uint64(domain.RepositoryDiskReserveBytes) {
		return fmt.Errorf("%w: disk estimate overflow", errTaskResourceWorkspaceNotProven)
	}
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return fmt.Errorf("%w: inspect free disk capacity", errTaskResourceWorkspaceNotProven)
	}
	blockSize := uint64(stat.Bsize)
	availableBlocks := uint64(stat.Bavail)
	available := availableBlocks * blockSize
	if blockSize != 0 && available/blockSize != availableBlocks {
		available = ^uint64(0)
	}
	if available < estimate+uint64(domain.RepositoryDiskReserveBytes) {
		return fmt.Errorf("%w: insufficient disk capacity", errTaskResourceWorkspaceNotProven)
	}
	return nil
}

type limitedDiscardWriter struct{ remaining int }

func (writer *limitedDiscardWriter) Write(value []byte) (int, error) {
	written := len(value)
	if len(value) > writer.remaining {
		writer.remaining = 0
		return written, nil
	}
	writer.remaining -= len(value)
	return written, nil
}

var _ app.TaskResourceWorkspaceResolver = (*taskResourceWorkspaceManager)(nil)

func (manager *taskResourceWorkspaceManager) taskBranchCreationMarker(row domain.TaskRepositoryWorktree) string {
	return "chora-task-workspace:" + manager.rootFingerprint + ":" + row.TaskID.String() + ":" + row.RepositoryID.String()
}

// A matching commit is insufficient ownership proof: an unrelated branch can
// point at the same base. Atomic ref creation leaves a durable reflog witness so
// a crash before worktree add can be recovered without adopting somebody's ref.
func (manager *taskResourceWorkspaceManager) claimTaskResourceBranch(ctx context.Context, resource domain.TaskRepositoryResource, row domain.TaskRepositoryWorktree) error {
	ref := "refs/heads/" + resource.TaskBranch
	marker := manager.taskBranchCreationMarker(row)
	create := isolatedGitCommand(ctx, resource.Checkout, "-c", "user.name=Chora", "-c", "user.email=chora@local.invalid", "update-ref", "--create-reflog", "-m", marker, ref, resource.BaseCommit, strings.Repeat("0", 40))
	create.Stdout = io.Discard
	create.Stderr = &limitedDiscardWriter{remaining: domain.RepositoryMetadataBytes}
	if err := create.Run(); err == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	head, err := gitTargetOutputBounded(ctx, resource.Checkout, domain.RepositoryMetadataBytes, "rev-parse", "--verify", ref+"^{commit}")
	if err != nil || strings.TrimSpace(string(head)) != resource.BaseCommit {
		return fmt.Errorf("%w: Task branch collides with an unrelated ref", errTaskResourceWorkspaceNotProven)
	}
	witness, err := gitTargetOutputBounded(ctx, resource.Checkout, domain.RepositoryMetadataBytes, "reflog", "show", "-1", "--format=%H%x00%gs", ref)
	expected := resource.BaseCommit + "\x00" + marker
	if err != nil || strings.TrimSpace(string(witness)) != expected {
		return fmt.Errorf("%w: existing Task branch has no matching creation proof", errTaskResourceWorkspaceNotProven)
	}
	return nil
}
