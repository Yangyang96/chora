package localweb

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/speccoding"
)

const taskWorktreeTopicSlugLimit = 64

var errTaskWorktreeNotProven = errors.New("managed Task worktree is not proven")

// taskWorktreeManager owns only the deterministic filesystem and Git boundary
// for durable Task worktrees. Persistence and lifecycle transitions remain in
// the application layer.
type taskWorktreeManager struct {
	repositoryAnchor   string
	repositoryParent   string
	canonicalParent    string
	repositoryIdentity string
	pinnedRevision     string
	rootFingerprint    [32]byte
	legacyFingerprint  [32]byte
	gitCommonDirectory string
}

// newTaskWorktreeManagerForMode keeps the installed-product control-plane
// files bound to installed apply targets while allowing SourceCheckout to use
// the documented clean pinned Git target. SourceCheckout authority is still
// fail-closed: newTaskWorktreeManager proves the exact Git root, pinned commit,
// common directory, and canonical sibling boundary before returning a manager.
func newTaskWorktreeManagerForMode(repositoryAnchor string, repository speccoding.RepositoryIdentity, sourceCheckout bool, validateInstalledApplyTarget func(string) error) (*taskWorktreeManager, error) {
	if !sourceCheckout {
		if validateInstalledApplyTarget == nil {
			return nil, errors.New("installed Task worktree apply-target validator is unavailable")
		}
		if err := validateInstalledApplyTarget(repositoryAnchor); err != nil {
			return nil, fmt.Errorf("installed Task worktree apply target is invalid: %w", err)
		}
	}
	return newTaskWorktreeManager(repositoryAnchor, repository)
}

func newTaskWorktreeManager(repositoryAnchor string, repository speccoding.RepositoryIdentity) (*taskWorktreeManager, error) {
	if !cleanAbsolutePath(repositoryAnchor) {
		return nil, errors.New("Task worktree repository anchor must be an absolute clean path")
	}
	repositoryParent := filepath.Dir(repositoryAnchor)
	if repository.Name == "" || repository.SourceRevision == "" {
		return nil, errors.New("Task worktree repository identity is incomplete")
	}
	anchorTop, err := taskWorktreeGitOutput(context.Background(), repositoryAnchor, "rev-parse", "--show-toplevel")
	if err != nil || filepath.Clean(strings.TrimSpace(string(anchorTop))) != repositoryAnchor {
		return nil, errors.New("Task worktree repository anchor must be the exact root of a Git working tree")
	}
	anchorInfo, err := os.Lstat(repositoryAnchor)
	if err != nil || !anchorInfo.IsDir() || anchorInfo.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("Task worktree repository anchor is unavailable or unsafe")
	}
	resolvedRevision, err := taskWorktreeGitOutput(context.Background(), repositoryAnchor, "rev-parse", "--verify", repository.SourceRevision+"^{commit}")
	if err != nil || strings.TrimSpace(string(resolvedRevision)) != repository.SourceRevision {
		return nil, errors.New("Task worktree pinned revision is unavailable in the repository anchor")
	}
	commonDirectory, err := taskWorktreeCommonDirectory(context.Background(), repositoryAnchor)
	if err != nil {
		return nil, errors.New("Task worktree repository common Git directory is unavailable")
	}
	parentInfo, err := os.Lstat(repositoryParent)
	if err != nil || !parentInfo.IsDir() || parentInfo.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("Task worktree repository parent is unavailable or unsafe")
	}
	canonicalParent, err := filepath.EvalSymlinks(repositoryParent)
	if err != nil || !filepath.IsAbs(canonicalParent) {
		return nil, errors.New("Task worktree repository parent cannot be resolved safely")
	}
	canonicalParent = filepath.Clean(canonicalParent)
	canonicalAnchor, err := filepath.EvalSymlinks(repositoryAnchor)
	if err != nil {
		return nil, errors.New("Task worktree repository anchor cannot be resolved safely")
	}
	if filepath.Dir(filepath.Clean(canonicalAnchor)) != canonicalParent {
		return nil, errors.New("Task worktrees must share the repository's canonical parent directory")
	}
	fingerprint := sha256.Sum256([]byte("chora.task-worktree-siblings.v2\x00" + repositoryAnchor + "\x00" + repository.Name))
	legacyFingerprint := sha256.Sum256([]byte("chora.task-worktrees-root.v1\x00" + filepath.Join(repositoryParent, "worktrees")))
	manager := &taskWorktreeManager{
		repositoryAnchor:   repositoryAnchor,
		repositoryParent:   repositoryParent,
		canonicalParent:    canonicalParent,
		repositoryIdentity: repository.Name,
		pinnedRevision:     repository.SourceRevision,
		rootFingerprint:    fingerprint,
		legacyFingerprint:  legacyFingerprint,
		gitCommonDirectory: commonDirectory,
	}
	// Exercise the domain's complete binding validation at configuration time,
	// including repository-name and pinned-revision syntax.
	if _, err := manager.Plan(domain.NewTaskID(), "configuration probe", time.Unix(1, 0).UTC()); err != nil {
		return nil, fmt.Errorf("invalid Task worktree configuration: %w", err)
	}
	return manager, nil
}

func (manager *taskWorktreeManager) Plan(taskID domain.TaskID, title string, createdAt time.Time) (domain.TaskWorktreeBinding, error) {
	if manager == nil || !taskID.Valid() || createdAt.IsZero() {
		return domain.TaskWorktreeBinding{}, fmt.Errorf("%w: invalid Task worktree plan", errTaskWorktreeNotProven)
	}
	locator := manager.repositoryIdentity + "-" + taskID.String() + "-" + taskWorktreeTopicSlug(title)
	return domain.NewTaskWorktreeBinding(domain.TaskWorktreeBindingParams{
		TaskID:                    taskID.String(),
		RepositoryIdentity:        manager.repositoryIdentity,
		PinnedBaseRevision:        manager.pinnedRevision,
		RelativeLocator:           locator,
		ConfiguredRootFingerprint: manager.rootFingerprint,
		CreatedAt:                 createdAt,
	})
}

// Ensure proves an existing worktree or provisions a missing worktree while a
// binding is still provisioning. It never repairs or replaces foreign/drifted
// filesystem state and never recreates a missing ready worktree.
func (manager *taskWorktreeManager) Ensure(ctx context.Context, binding domain.TaskWorktreeBinding) error {
	path, exists, err := manager.bindingPath(binding)
	if err != nil {
		return err
	}
	if exists {
		return manager.verifyWorktree(ctx, path, binding)
	}
	if migrated, migrationErr := manager.migrateLegacyWorktree(ctx, path, binding); migrationErr != nil {
		return migrationErr
	} else if migrated {
		return nil
	}
	if binding.State() != domain.TaskWorktreeProvisioning {
		return fmt.Errorf("%w: ready or recovery worktree path is missing", errTaskWorktreeNotProven)
	}
	_, addErr := taskWorktreeGitOutput(ctx, manager.repositoryAnchor, "worktree", "add", "--detach", path, manager.pinnedRevision)
	if verifyErr := manager.verifyWorktree(ctx, path, binding); verifyErr != nil {
		if addErr != nil {
			return fmt.Errorf("%w: git worktree add failed and final state is unproven", errTaskWorktreeNotProven)
		}
		return verifyErr
	}
	return nil
}

// ResolveReady returns the private host path only after re-proving the durable
// ready binding and its exact Git identity.
func (manager *taskWorktreeManager) ResolveReady(ctx context.Context, binding domain.TaskWorktreeBinding) (string, error) {
	if binding.State() != domain.TaskWorktreeReady {
		return "", fmt.Errorf("%w: Task worktree is not ready", errTaskWorktreeNotProven)
	}
	path, exists, err := manager.bindingPath(binding)
	if err != nil {
		return "", err
	}
	if !exists {
		return "", fmt.Errorf("%w: ready Task worktree path is missing", errTaskWorktreeNotProven)
	}
	if err := manager.verifyWorktree(ctx, path, binding); err != nil {
		return "", err
	}
	return path, nil
}

func (manager *taskWorktreeManager) bindingPath(binding domain.TaskWorktreeBinding) (string, bool, error) {
	if manager == nil || binding.RepositoryIdentity() != manager.repositoryIdentity ||
		binding.PinnedBaseRevision() != manager.pinnedRevision ||
		(binding.ConfiguredRootFingerprint() != manager.rootFingerprint && binding.ConfiguredRootFingerprint() != manager.legacyFingerprint) {
		return "", false, fmt.Errorf("%w: binding does not match configured repository authority", errTaskWorktreeNotProven)
	}
	prefix := manager.repositoryIdentity + "-" + binding.TaskID().String() + "-"
	if !strings.HasPrefix(binding.RelativeLocator(), prefix) || strings.ContainsAny(binding.RelativeLocator(), `/\\`) {
		return "", false, fmt.Errorf("%w: binding locator is invalid", errTaskWorktreeNotProven)
	}
	path := filepath.Join(manager.repositoryParent, binding.RelativeLocator())
	if filepath.Clean(path) != path || filepath.Dir(path) != manager.repositoryParent || path == manager.repositoryAnchor {
		return "", false, fmt.Errorf("%w: binding path is not a direct repository sibling", errTaskWorktreeNotProven)
	}
	if err := manager.checkManagedPath(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return path, false, nil
		}
		return "", false, err
	}
	return path, true, nil
}

func (manager *taskWorktreeManager) checkManagedPath(path string) error {
	if filepath.Dir(path) != manager.repositoryParent || path == manager.repositoryAnchor {
		return fmt.Errorf("%w: path is not a direct repository sibling", errTaskWorktreeNotProven)
	}
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return os.ErrNotExist
		}
		return fmt.Errorf("%w: inspect managed path", errTaskWorktreeNotProven)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("%w: managed path is not a real directory", errTaskWorktreeNotProven)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || filepath.Dir(filepath.Clean(resolved)) != manager.canonicalParent {
		return fmt.Errorf("%w: managed path is not a canonical repository sibling", errTaskWorktreeNotProven)
	}
	return nil
}

func (manager *taskWorktreeManager) migrateLegacyWorktree(ctx context.Context, path string, binding domain.TaskWorktreeBinding) (bool, error) {
	if binding.ConfiguredRootFingerprint() != manager.legacyFingerprint {
		return false, nil
	}
	legacyRoot := filepath.Join(manager.repositoryParent, "worktrees")
	legacyName := strings.TrimPrefix(binding.RelativeLocator(), manager.repositoryIdentity+"-")
	legacyPath := filepath.Join(legacyRoot, legacyName)
	if filepath.Dir(legacyPath) != legacyRoot {
		return false, fmt.Errorf("%w: legacy Task worktree path is invalid", errTaskWorktreeNotProven)
	}
	rootInfo, err := os.Lstat(legacyRoot)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil || rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return false, fmt.Errorf("%w: legacy Task worktrees root is unsafe", errTaskWorktreeNotProven)
	}
	legacyInfo, err := os.Lstat(legacyPath)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil || legacyInfo.Mode()&os.ModeSymlink != 0 || !legacyInfo.IsDir() {
		return false, fmt.Errorf("%w: legacy Task worktree is unsafe", errTaskWorktreeNotProven)
	}
	if err := manager.verifyGitWorktreeIdentity(ctx, legacyPath, binding); err != nil {
		return false, err
	}
	if _, err := taskWorktreeGitOutput(ctx, manager.repositoryAnchor, "worktree", "move", legacyPath, path); err != nil {
		return false, fmt.Errorf("%w: move legacy Task worktree to repository sibling", errTaskWorktreeNotProven)
	}
	if err := manager.verifyWorktree(ctx, path, binding); err != nil {
		return false, err
	}
	return true, nil
}

func (manager *taskWorktreeManager) verifyWorktree(ctx context.Context, path string, binding domain.TaskWorktreeBinding) error {
	if err := manager.checkManagedPath(path); err != nil {
		return err
	}
	return manager.verifyGitWorktreeIdentity(ctx, path, binding)
}

func (manager *taskWorktreeManager) verifyGitWorktreeIdentity(ctx context.Context, path string, binding domain.TaskWorktreeBinding) error {
	top, err := taskWorktreeGitOutput(ctx, path, "rev-parse", "--show-toplevel")
	if err != nil || filepath.Clean(strings.TrimSpace(string(top))) != path {
		return fmt.Errorf("%w: path is not the exact root of its Git worktree", errTaskWorktreeNotProven)
	}
	commonDirectory, err := taskWorktreeCommonDirectory(ctx, path)
	if err != nil || commonDirectory != manager.gitCommonDirectory {
		return fmt.Errorf("%w: worktree belongs to a foreign Git repository", errTaskWorktreeNotProven)
	}
	head, err := taskWorktreeGitOutput(ctx, path, "rev-parse", "HEAD")
	if err != nil || strings.TrimSpace(string(head)) != binding.PinnedBaseRevision() {
		return fmt.Errorf("%w: worktree HEAD differs from pinned revision", errTaskWorktreeNotProven)
	}
	symbolic, symbolicErr := taskWorktreeGitOutput(ctx, path, "symbolic-ref", "-q", "HEAD")
	if symbolicErr == nil || len(bytes.TrimSpace(symbolic)) != 0 {
		return fmt.Errorf("%w: worktree HEAD is not detached", errTaskWorktreeNotProven)
	}
	registered, err := manager.registeredWorktree(ctx, path, binding.PinnedBaseRevision())
	if err != nil || !registered {
		return fmt.Errorf("%w: worktree is not registered with the repository anchor", errTaskWorktreeNotProven)
	}
	return nil
}

func (manager *taskWorktreeManager) registeredWorktree(ctx context.Context, path, head string) (bool, error) {
	data, err := taskWorktreeGitOutput(ctx, manager.repositoryAnchor, "worktree", "list", "--porcelain")
	if err != nil {
		return false, err
	}
	var candidatePath, candidateHead string
	detached := false
	flush := func() bool {
		return candidatePath == path && candidateHead == head && detached
	}
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" {
			if flush() {
				return true, nil
			}
			candidatePath, candidateHead, detached = "", "", false
			continue
		}
		switch {
		case strings.HasPrefix(line, "worktree "):
			candidatePath = filepath.Clean(strings.TrimPrefix(line, "worktree "))
		case strings.HasPrefix(line, "HEAD "):
			candidateHead = strings.TrimPrefix(line, "HEAD ")
		case line == "detached":
			detached = true
		}
	}
	return flush(), nil
}

func taskWorktreeTopicSlug(title string) string {
	var builder strings.Builder
	separator := false
	for _, char := range strings.ToLower(strings.TrimSpace(title)) {
		if char <= unicode.MaxASCII && ((char >= 'a' && char <= 'z') || (char >= '0' && char <= '9')) {
			if separator && builder.Len() > 0 && builder.Len() < taskWorktreeTopicSlugLimit {
				builder.WriteByte('-')
			}
			separator = false
			if builder.Len() < taskWorktreeTopicSlugLimit {
				builder.WriteRune(char)
			}
			continue
		}
		separator = builder.Len() > 0
	}
	value := strings.Trim(builder.String(), "-")
	if value == "" {
		return "task"
	}
	return value
}

func taskWorktreeCommonDirectory(ctx context.Context, root string) (string, error) {
	data, err := taskWorktreeGitOutput(ctx, root, "rev-parse", "--git-common-dir")
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(data))
	if !filepath.IsAbs(value) {
		value = filepath.Join(root, value)
	}
	value, err = filepath.EvalSymlinks(filepath.Clean(value))
	if err != nil {
		return "", err
	}
	return filepath.Clean(value), nil
}

func taskWorktreeGitOutput(ctx context.Context, root string, arguments ...string) ([]byte, error) {
	command := isolatedGitCommand(ctx, root, arguments...)
	var stdout bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &bytes.Buffer{}
	if err := command.Run(); err != nil {
		return nil, err
	}
	return stdout.Bytes(), nil
}

func cleanAbsolutePath(path string) bool {
	return path != "" && filepath.IsAbs(path) && filepath.Clean(path) == path
}

func pathWithin(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func pathsOverlap(first, second string) bool {
	return pathWithin(first, second) || pathWithin(second, first)
}

var _ interface {
	Plan(domain.TaskID, string, time.Time) (domain.TaskWorktreeBinding, error)
	Ensure(context.Context, domain.TaskWorktreeBinding) error
} = (*taskWorktreeManager)(nil)
