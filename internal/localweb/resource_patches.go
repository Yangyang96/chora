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
	"path/filepath"
	"sort"
	"strings"

	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

type resourcePatchStore struct {
	root             string
	reader           storecontract.Reader
	resolver         app.TaskResourceWorkspaceResolver
	changedPathLimit int
	patchByteLimit   int
}

func newResourcePatchStore(root string, reader storecontract.Reader, resolver app.TaskResourceWorkspaceResolver) (app.ResourceReviewPatchMaterializer, error) {
	if !cleanAbsolutePath(root) || reader == nil || resolver == nil {
		return nil, errors.New("resource Patch store configuration is incomplete")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create resource Patch store: %w", err)
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("resource Patch store root is unavailable or unsafe")
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return nil, fmt.Errorf("secure resource Patch store: %w", err)
	}
	return &resourcePatchStore{
		root: root, reader: reader, resolver: resolver,
		changedPathLimit: domain.TaskChangedEntryLimit,
		patchByteLimit:   domain.TaskPatchBytes,
	}, nil
}

func (store *resourcePatchStore) Materialize(ctx context.Context, taskID domain.TaskID, attemptID domain.AttemptID, workingRoot string) ([]app.ResourceReviewPatch, error) {
	if store == nil || store.reader == nil || store.resolver == nil || !taskID.Valid() || !attemptID.Valid() ||
		!cleanAbsolutePath(workingRoot) || store.changedPathLimit <= 0 || store.patchByteLimit <= 0 {
		return nil, errors.New("invalid resource Patch materialization request")
	}
	snapshot, err := store.resourceSnapshot(ctx, taskID)
	if err != nil {
		return nil, err
	}
	resolved, err := store.resolver.ResolveExecutionRoot(ctx, taskID)
	if err != nil || resolved == "" || resolved != workingRoot {
		return nil, errors.New("Task resource execution root cannot be proven")
	}
	canonicalRoot, err := proveResourceTaskRoot(workingRoot)
	if err != nil {
		return nil, err
	}

	remainingPaths := store.changedPathLimit
	remainingPatchBytes := store.patchByteLimit
	result := make([]app.ResourceReviewPatch, 0, len(snapshot.Resources))
	for _, resource := range snapshot.Resources {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		repositoryRoot, err := proveResourcePatchWorktree(ctx, workingRoot, canonicalRoot, resource)
		if err != nil {
			return nil, fmt.Errorf("Task repository %s cannot be proven: %w", resource.RepoID, err)
		}
		changed, untracked, err := collectResourceReviewPaths(ctx, repositoryRoot, resource.BaseCommit, remainingPaths)
		if err != nil {
			return nil, fmt.Errorf("Task repository %s changed paths are unavailable: %w", resource.RepoID, err)
		}
		remainingPaths -= len(changed)
		item := app.ResourceReviewPatch{RepoID: resource.RepoID}
		if len(changed) == 0 {
			result = append(result, item)
			continue
		}
		if resource.Role != "write" {
			return nil, fmt.Errorf("Task changed reference repository %s", resource.RepoID)
		}
		for _, relative := range changed {
			if !resource.Scope.Allows(relative) {
				return nil, fmt.Errorf("Task repository %s changed a file outside its frozen writable scope: %s", resource.RepoID, relative)
			}
			if err := validateReviewableTextChange(ctx, repositoryRoot, resource.BaseCommit, relative); err != nil {
				return nil, fmt.Errorf("Task repository %s change %q is unsupported: %w", resource.RepoID, relative, err)
			}
		}
		raw, err := buildScopedReviewPatchBounded(ctx, repositoryRoot, resource.BaseCommit, changed, untracked, remainingPatchBytes)
		if err != nil || len(bytes.TrimSpace(raw)) == 0 {
			if err == nil {
				err = errors.New("empty Patch")
			}
			return nil, fmt.Errorf("Task repository %s Patch could not be materialized: %w", resource.RepoID, err)
		}
		remainingPatchBytes -= len(raw)
		digest := sha256.Sum256(raw)
		patch, err := app.BuildResourceReviewablePatch(resource, changed, raw, digest)
		if err != nil {
			return nil, fmt.Errorf("Task repository %s Patch is not an ordinary reviewable text change: %w", resource.RepoID, err)
		}
		item.Patch = patch
		item.Artifact = execution.WorkspaceArtifact{
			Locator:     attemptID.String() + "/" + resource.RepoID + ".diff",
			Description: "Digest-bound Patch from a Task repository worktree",
			SHA256:      hex.EncodeToString(digest[:]), MediaType: "text/x-diff",
		}
		result = append(result, item)
	}

	// Validate every participant before publishing any immutable artifact. This
	// keeps a rejected multi-repository result from exposing a usable prefix.
	for index := range result {
		if len(result[index].Patch.Raw) == 0 {
			continue
		}
		if err := store.writeImmutable(attemptID, result[index].RepoID, result[index].Patch.Raw); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func (store *resourcePatchStore) Read(ctx context.Context, attemptID domain.AttemptID, repoID string, digest [32]byte) ([]byte, error) {
	if store == nil || !attemptID.Valid() || digest == ([32]byte{}) {
		return nil, errors.New("invalid resource Patch read request")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path, err := store.artifactPath(attemptID, repoID)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() <= 0 || info.Size() > int64(domain.TaskPatchBytes) {
		return nil, errors.New("resource Patch artifact is unavailable or unsafe")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("resource Patch artifact is unavailable or unsafe")
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, int64(domain.TaskPatchBytes)+1))
	if err != nil || len(raw) == 0 || len(raw) > domain.TaskPatchBytes || sha256.Sum256(raw) != digest {
		return nil, errors.New("resource Patch artifact digest is unavailable or drifted")
	}
	return raw, nil
}

func (store *resourcePatchStore) resourceSnapshot(ctx context.Context, taskID domain.TaskID) (domain.TaskResourceSnapshot, error) {
	record, err := store.reader.GetTaskResourceSnapshot(ctx, taskID)
	if err != nil || record.TaskID != taskID || record.Digest == ([32]byte{}) {
		return domain.TaskResourceSnapshot{}, errors.New("immutable Task resource snapshot is unavailable")
	}
	var snapshot domain.TaskResourceSnapshot
	decoder := json.NewDecoder(bytes.NewReader(record.CanonicalJSON))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&snapshot); err != nil {
		return snapshot, errors.New("immutable Task resource snapshot is invalid")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return snapshot, errors.New("immutable Task resource snapshot has trailing data")
	}
	canonical, digest, err := snapshot.CanonicalJSON()
	if err != nil || snapshot.TaskID != taskID.String() || digest != record.Digest || !bytes.Equal(canonical, record.CanonicalJSON) {
		return snapshot, errors.New("immutable Task resource snapshot drifted")
	}
	return snapshot, nil
}

func proveResourceTaskRoot(root string) (string, error) {
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("Task resource execution root is unavailable or unsafe")
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil || !cleanAbsolutePath(filepath.Clean(canonical)) {
		return "", errors.New("Task resource execution root cannot be canonicalized")
	}
	return filepath.Clean(canonical), nil
}

func proveResourcePatchWorktree(ctx context.Context, workingRoot, canonicalRoot string, resource domain.TaskRepositoryResource) (string, error) {
	_, err := domain.ParseRepositoryID(resource.RepoID)
	if err != nil {
		return "", errors.New("invalid repository identity")
	}
	root := filepath.Join(workingRoot, resource.WorkspaceDirectory())
	if filepath.Dir(root) != workingRoot {
		return "", errors.New("repository worktree escaped Task root")
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("repository worktree is unavailable or unsafe")
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil || filepath.Clean(canonical) != filepath.Join(canonicalRoot, resource.WorkspaceDirectory()) {
		return "", errors.New("repository worktree escaped Task root")
	}
	canonical = filepath.Clean(canonical)
	top, err := gitTargetOutputBounded(ctx, root, domain.RepositoryMetadataBytes, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", errors.New("repository worktree root is unavailable")
	}
	topCanonical, err := filepath.EvalSymlinks(strings.TrimSpace(string(top)))
	if err != nil || filepath.Clean(topCanonical) != canonical {
		return "", errors.New("repository worktree is not the exact Git root")
	}
	common, err := taskWorktreeCommonDirectory(ctx, root)
	if err != nil || common != resource.CommonGitDir {
		return "", errors.New("repository worktree belongs to a different Git repository")
	}
	head, err := gitTargetOutputBounded(ctx, root, domain.RepositoryMetadataBytes, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil || strings.TrimSpace(string(head)) != resource.BaseCommit {
		return "", errors.New("repository worktree HEAD differs from its frozen base")
	}
	tree, err := gitTargetOutputBounded(ctx, root, domain.RepositoryMetadataBytes, "rev-parse", "--verify", "HEAD^{tree}")
	if err != nil || strings.TrimSpace(string(tree)) != resource.BaseTree {
		return "", errors.New("repository worktree tree differs from its frozen base")
	}
	return root, nil
}

func collectResourceReviewPaths(parent context.Context, root, base string, limit int) ([]string, map[string]struct{}, error) {
	ctx, cancel := context.WithTimeout(parent, domain.RepositoryMetadataTimeout)
	defer cancel()
	all := make(map[string]struct{})
	untracked := make(map[string]struct{})
	metadataBytes := 0
	consume := func(markUntracked bool, arguments ...string) error {
		command := isolatedGitCommand(ctx, root, arguments...)
		stdout, err := command.StdoutPipe()
		if err != nil {
			return err
		}
		command.Stderr = &limitedDiscardWriter{remaining: domain.RepositoryMetadataBytes}
		if err := command.Start(); err != nil {
			return err
		}
		stop := func(err error) error { _ = command.Process.Kill(); _ = command.Wait(); return err }
		reader := bufio.NewReaderSize(stdout, 4096)
		for {
			record, err := readBoundedNullRecord(reader, domain.RepositoryMetadataBytes-metadataBytes)
			if errors.Is(err, io.EOF) && len(record) == 0 {
				break
			}
			if err != nil {
				return stop(fmt.Errorf("changed path metadata unavailable or exceeds %d bytes: %w", domain.RepositoryMetadataBytes, err))
			}
			metadataBytes += len(record) + 1
			if metadataBytes > domain.RepositoryMetadataBytes {
				return stop(fmt.Errorf("changed path metadata exceeds %d bytes", domain.RepositoryMetadataBytes))
			}
			relative := string(record)
			if !safePatchTargetPath(relative) {
				return stop(errors.New("Git returned an unsafe changed path"))
			}
			if _, exists := all[relative]; !exists {
				if len(all) >= limit {
					return stop(fmt.Errorf("Task changes exceed the %d-path limit", domain.TaskChangedEntryLimit))
				}
				all[relative] = struct{}{}
			}
			if markUntracked {
				untracked[relative] = struct{}{}
			}
		}
		if err := command.Wait(); err != nil {
			return fmt.Errorf("Git changed path enumeration failed: %w", err)
		}
		return nil
	}
	if err := consume(false, "diff", "--name-only", "-z", "--no-renames", base, "--"); err != nil {
		return nil, nil, err
	}
	if err := consume(true, "ls-files", "--others", "--exclude-standard", "-z", "--"); err != nil {
		return nil, nil, err
	}
	paths := make([]string, 0, len(all))
	for relative := range all {
		paths = append(paths, relative)
	}
	sort.Strings(paths)
	return paths, untracked, nil
}

func (store *resourcePatchStore) writeImmutable(attemptID domain.AttemptID, repoID string, raw []byte) error {
	path, err := store.artifactPath(attemptID, repoID)
	if err != nil {
		return err
	}
	directory := filepath.Dir(path)
	if err := os.Mkdir(directory, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("create resource Patch directory: %w", err)
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || filepath.Dir(directory) != store.root {
		return errors.New("resource Patch directory is unavailable or unsafe")
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return fmt.Errorf("secure resource Patch directory: %w", err)
	}
	if existingInfo, statErr := os.Lstat(path); statErr == nil {
		if !existingInfo.Mode().IsRegular() || existingInfo.Mode()&os.ModeSymlink != 0 || existingInfo.Size() <= 0 || existingInfo.Size() > int64(domain.TaskPatchBytes) {
			return errors.New("resource Patch artifact identity conflict")
		}
		existing, readErr := os.ReadFile(path)
		if readErr == nil && bytes.Equal(existing, raw) {
			return nil
		}
		return errors.New("resource Patch artifact identity conflict")
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	temporary, err := os.CreateTemp(directory, ".resource-patch-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(raw); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Link(temporaryPath, path); err != nil {
		if existing, readErr := os.ReadFile(path); readErr == nil && bytes.Equal(existing, raw) {
			return nil
		}
		return fmt.Errorf("publish immutable resource Patch: %w", err)
	}
	return nil
}

func (store *resourcePatchStore) artifactPath(attemptID domain.AttemptID, repoID string) (string, error) {
	if store == nil || !cleanAbsolutePath(store.root) || !attemptID.Valid() {
		return "", errors.New("invalid resource Patch locator")
	}
	if _, err := domain.ParseRepositoryID(repoID); err != nil {
		return "", errors.New("invalid resource Patch locator")
	}
	path := filepath.Join(store.root, attemptID.String(), repoID+".diff")
	if filepath.Dir(filepath.Dir(path)) != store.root {
		return "", errors.New("invalid resource Patch locator")
	}
	return path, nil
}

var _ app.ResourceReviewPatchMaterializer = (*resourcePatchStore)(nil)
