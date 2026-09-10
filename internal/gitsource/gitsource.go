package gitsource

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	gitMetadataLimit   = 1 << 20
	gitMetadataTimeout = 10 * time.Second
)

// Inspection is the digest of a local Git worktree at admit time. It is read
// only: Chora never mutates the admitted repository.
type Inspection struct {
	CanonicalPath    string
	CommonGitDir     string
	PhysicalIdentity string
	IsGit            bool
	IsBare           bool
	IsLinkedWorktree bool
	HeadCommit       string
	RootTree         string
	Branch           string
	Unborn           bool
	RemoteURL        string
	Dirty            bool
}

// Source provides repository inspection and cloning. The Default implementation
// shells out to the git executable with argv (never shell interpolation) and
// inherits the user's Git credential helper and SSH agent, so no credentials
// are ever logged or stored by Chora.
type Source interface {
	Inspect(ctx context.Context, path string) (Inspection, error)
	Clone(ctx context.Context, url, dest string) (Inspection, error)
}

var (
	ErrNotGit         = errors.New("path is not a Git worktree")
	ErrBare           = errors.New("path is a bare Git repository")
	ErrLinkedWorktree = errors.New("path is a linked Git worktree")
	ErrMetadataLimit  = errors.New("Git metadata exceeds the supported size limit")
	ErrRevisionDrift  = errors.New("Git revision evidence does not match")
	ErrCloneFailed    = errors.New("Git clone failed")
)

// PathError wraps the OS error produced when a path cannot be resolved.
type PathError struct {
	Path string
	Err  error
}

func (e *PathError) Error() string {
	return fmt.Sprintf("inspect path %s: %v", e.Path, e.Err)
}

func (e *PathError) Unwrap() error { return e.Err }

// Default is the real git-backed Source.
type Default struct{}

func (Default) Inspect(ctx context.Context, path string) (Inspection, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return Inspection{}, err
	}
	canonical, err := filepath.EvalSymlinks(filepath.Clean(absolute))
	if err != nil {
		return Inspection{}, &PathError{Path: path, Err: err}
	}

	bare, err := gitOutput(ctx, canonical, "rev-parse", "--is-bare-repository")
	if err != nil {
		if operationalGitError(err) {
			return Inspection{}, err
		}
		return Inspection{}, ErrNotGit
	}
	if strings.TrimSpace(bare) == "true" {
		return Inspection{}, ErrBare
	}

	topLevel, err := gitOutput(ctx, canonical, "rev-parse", "--show-toplevel")
	if err != nil {
		if operationalGitError(err) {
			return Inspection{}, err
		}
		return Inspection{}, ErrNotGit
	}
	repoRoot, err := filepath.EvalSymlinks(filepath.Clean(strings.TrimSpace(topLevel)))
	if err != nil {
		return Inspection{}, &PathError{Path: topLevel, Err: err}
	}

	linked, commonGitDir, err := inspectGitDirectories(ctx, repoRoot)
	if err != nil {
		if operationalGitError(err) {
			return Inspection{}, err
		}
		return Inspection{}, ErrNotGit
	}
	if linked {
		return Inspection{}, ErrLinkedWorktree
	}
	physicalIdentity, err := repositoryPhysicalIdentity(repoRoot, commonGitDir)
	if err != nil {
		return Inspection{}, err
	}

	branch, branchErr := gitOutput(ctx, repoRoot, "symbolic-ref", "--quiet", "--short", "HEAD")
	if operationalGitError(branchErr) {
		return Inspection{}, branchErr
	}
	branch = strings.TrimSpace(branch)

	head, err := gitOutput(ctx, repoRoot, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		if operationalGitError(err) {
			return Inspection{}, err
		}
		if branch != "" {
			exists, refErr := gitCommandSuccess(ctx, repoRoot, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
			if refErr != nil {
				return Inspection{}, refErr
			}
			if !exists {
				dirty, statusErr := gitStatusDirty(ctx, repoRoot)
				if statusErr != nil {
					return Inspection{}, statusErr
				}
				remote, remoteErr := optionalGitOutput(ctx, repoRoot, "config", "--get", "remote.origin.url")
				if remoteErr != nil {
					return Inspection{}, remoteErr
				}
				return Inspection{
					CanonicalPath: repoRoot, CommonGitDir: commonGitDir, PhysicalIdentity: physicalIdentity,
					IsGit: true, Branch: branch, Unborn: true, RemoteURL: strings.TrimSpace(remote), Dirty: dirty,
				}, nil
			}
		}
		return Inspection{}, ErrNotGit
	}
	tree, err := gitOutput(ctx, repoRoot, "rev-parse", "HEAD^{tree}")
	if err != nil {
		return Inspection{}, err
	}
	remote, err := optionalGitOutput(ctx, repoRoot, "config", "--get", "remote.origin.url")
	if err != nil {
		return Inspection{}, err
	}
	dirty, err := gitStatusDirty(ctx, repoRoot)
	if err != nil {
		return Inspection{}, err
	}

	return Inspection{
		CanonicalPath: repoRoot, CommonGitDir: commonGitDir, PhysicalIdentity: physicalIdentity,
		IsGit: true, HeadCommit: strings.TrimSpace(head), RootTree: strings.TrimSpace(tree),
		Branch: branch, RemoteURL: strings.TrimSpace(remote), Dirty: dirty,
	}, nil
}

func (Default) Clone(ctx context.Context, url, dest string) (Inspection, error) {
	if _, err := os.Lstat(dest); err == nil {
		return Inspection{}, fmt.Errorf("%w: destination already exists", ErrCloneFailed)
	}
	cmd := exec.CommandContext(ctx, "git", "clone", url, dest)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		// Deliberately discard git's output: it may contain a credential in a
		// rewritten URL. Only a generic failure escapes. Remove any partial
		// clone left behind so a retry is not blocked by a half-written dest.
		_ = os.RemoveAll(dest)
		return Inspection{}, fmt.Errorf("%w", ErrCloneFailed)
	}
	return Default{}.Inspect(ctx, dest)
}

// isLinkedWorktree reports whether a worktree root is a linked worktree. A
// linked worktree has a per-worktree git directory distinct from the shared
// common directory; a plain checkout has them equal.
func isLinkedWorktree(ctx context.Context, root string) (bool, error) {
	linked, _, err := inspectGitDirectories(ctx, root)
	return linked, err
}

func inspectGitDirectories(ctx context.Context, root string) (bool, string, error) {
	gitDir, err := gitOutput(ctx, root, "rev-parse", "--git-dir")
	if err != nil {
		return false, "", err
	}
	commonDir, err := gitOutput(ctx, root, "rev-parse", "--git-common-dir")
	if err != nil {
		return false, "", err
	}
	gitDir = resolveGitDir(root, strings.TrimSpace(gitDir))
	commonDir = resolveGitDir(root, strings.TrimSpace(commonDir))
	commonDir, err = filepath.EvalSymlinks(commonDir)
	if err != nil {
		return false, "", &PathError{Path: commonDir, Err: err}
	}
	commonDir = filepath.Clean(commonDir)
	if gitDir != commonDir {
		return true, commonDir, nil
	}
	if info, err := os.Stat(gitDir); err == nil && info.Mode().IsRegular() {
		return true, commonDir, nil
	}
	return false, commonDir, nil
}

func resolveGitDir(root, value string) string {
	if value == "" {
		return value
	}
	if filepath.IsAbs(value) {
		return filepath.Clean(value)
	}
	return filepath.Clean(filepath.Join(root, value))
}

func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	commandCtx, cancel := context.WithTimeout(ctx, gitMetadataTimeout)
	defer cancel()
	cmd := exec.CommandContext(commandCtx, "git", append([]string{"-C", dir}, args...)...)
	stdout := &boundedBuffer{limit: gitMetadataLimit}
	cmd.Stdout = stdout
	cmd.Stderr = io.Discard
	err := cmd.Run()
	if stdout.exceeded {
		return "", ErrMetadataLimit
	}
	if commandCtx.Err() != nil {
		return "", commandCtx.Err()
	}
	if err != nil {
		return "", err
	}
	return stdout.String(), nil
}

func optionalGitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	value, err := gitOutput(ctx, dir, args...)
	if err == nil {
		return value, nil
	}
	if operationalGitError(err) {
		return "", err
	}
	return "", nil
}

func gitCommandSuccess(ctx context.Context, dir string, args ...string) (bool, error) {
	commandCtx, cancel := context.WithTimeout(ctx, gitMetadataTimeout)
	defer cancel()
	cmd := exec.CommandContext(commandCtx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	err := cmd.Run()
	if commandCtx.Err() != nil {
		return false, commandCtx.Err()
	}
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return false, nil
	}
	return false, err
}

var errStatusPresent = errors.New("Git status entry present")

type presenceWriter struct{ present bool }

func (writer *presenceWriter) Write(value []byte) (int, error) {
	if len(value) == 0 {
		return 0, nil
	}
	writer.present = true
	return 0, errStatusPresent
}

func gitStatusDirty(ctx context.Context, dir string) (bool, error) {
	commandCtx, cancel := context.WithTimeout(ctx, gitMetadataTimeout)
	defer cancel()
	cmd := exec.CommandContext(commandCtx, "git", "-C", dir, "status", "--porcelain=v1", "-z", "--untracked-files=normal")
	status := &presenceWriter{}
	cmd.Stdout = status
	cmd.Stderr = io.Discard
	err := cmd.Run()
	if status.present {
		return true, nil
	}
	if commandCtx.Err() != nil {
		return false, commandCtx.Err()
	}
	if err != nil {
		return false, err
	}
	return false, nil
}

type boundedBuffer struct {
	buffer   bytes.Buffer
	limit    int
	exceeded bool
}

func (buffer *boundedBuffer) Write(value []byte) (int, error) {
	remaining := buffer.limit - buffer.buffer.Len()
	if len(value) <= remaining {
		return buffer.buffer.Write(value)
	}
	if remaining > 0 {
		_, _ = buffer.buffer.Write(value[:remaining])
	}
	buffer.exceeded = true
	return 0, ErrMetadataLimit
}

func (buffer *boundedBuffer) String() string { return buffer.buffer.String() }

func operationalGitError(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrMetadataLimit)
}
