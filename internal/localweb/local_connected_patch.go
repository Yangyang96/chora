package localweb

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
	"github.com/Yangyang96/chora/internal/speccoding"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

const localConnectedReviewPatchLimit = 100 * 1024 * 1024

// localConnectedPatchStore turns the finished Task worktree into one immutable,
// Chora-owned review artifact. Pi never has write authority over this store.
type localConnectedPatchStore struct {
	root     string
	reader   storecontract.Reader
	resolver *taskWorktreeResolver
}

func newLocalConnectedPatchStore(root string, reader storecontract.Reader, resolver *taskWorktreeResolver) (*localConnectedPatchStore, error) {
	if !cleanAbsolutePath(root) || reader == nil || resolver == nil {
		return nil, errors.New("Local Connected Patch store configuration is incomplete")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create Local Connected Patch store: %w", err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return nil, fmt.Errorf("secure Local Connected Patch store: %w", err)
	}
	return &localConnectedPatchStore{root: root, reader: reader, resolver: resolver}, nil
}

func (store *localConnectedPatchStore) MaterializeReviewPatch(ctx context.Context, request app.ReviewPatchMaterializationRequest) (execution.WorkspaceArtifact, error) {
	if store == nil || !request.RunID.Valid() || !request.TaskID.Valid() || !request.AttemptID.Valid() || !cleanAbsolutePath(request.WorkingRoot) {
		return execution.WorkspaceArtifact{}, errors.New("invalid Local Connected Patch request")
	}
	task, binding, err := store.taskRepository(ctx, request.TaskID)
	if err != nil {
		return execution.WorkspaceArtifact{}, err
	}
	worktree, err := store.resolver.ResolveExecutionRoot(ctx, task)
	if err != nil || worktree == "" || worktree != request.WorkingRoot {
		return execution.WorkspaceArtifact{}, errors.New("Task execution worktree cannot be proven")
	}
	head, err := gitTargetOutput(ctx, worktree, nil, "rev-parse", "HEAD")
	if err != nil || strings.TrimSpace(string(head)) != binding.AdmittedBase() {
		return execution.WorkspaceArtifact{}, errors.New("Task execution worktree drifted from its admitted base")
	}
	contract, document, err := store.frozenPatchContract(ctx, task, binding)
	if err != nil {
		return execution.WorkspaceArtifact{}, err
	}
	changed, untracked, err := collectReviewableChangePaths(ctx, worktree, binding.AdmittedBase())
	if err != nil {
		return execution.WorkspaceArtifact{}, errors.New("Task execution worktree status is unavailable")
	}
	for _, relative := range changed {
		if !speccoding.WritableScopeAllows(relative, document.Execution.Boundary.WritableFiles, document.Execution.Boundary.WritableDirectories) {
			return execution.WorkspaceArtifact{}, fmt.Errorf("Task changed a file outside its frozen writable scope: %s", relative)
		}
		if err := validateReviewableTextChange(ctx, worktree, binding.AdmittedBase(), relative); err != nil {
			return execution.WorkspaceArtifact{}, fmt.Errorf("Task change %q is unsupported: %w", relative, err)
		}
	}
	if len(changed) == 0 {
		return execution.WorkspaceArtifact{}, app.ErrNoReviewableChanges
	}
	raw, err := buildScopedReviewPatch(ctx, worktree, binding.AdmittedBase(), changed, untracked)
	if err != nil || len(bytes.TrimSpace(raw)) == 0 {
		return execution.WorkspaceArtifact{}, errors.New("Task Patch could not be materialized from the frozen writable scope")
	}
	if len(raw) > localConnectedReviewPatchLimit {
		return execution.WorkspaceArtifact{}, errors.New("Task Patch exceeds the review artifact limit")
	}
	digest := sha256.Sum256(raw)
	if _, err := app.BuildReviewablePatch(contract, raw, digest); err != nil {
		return execution.WorkspaceArtifact{}, fmt.Errorf("Task Patch is not an ordinary reviewable text change: %w", err)
	}
	locator := request.AttemptID.String() + "/patch.diff"
	if err := store.writeImmutable(locator, raw); err != nil {
		return execution.WorkspaceArtifact{}, err
	}
	return execution.WorkspaceArtifact{
		Locator: locator, Description: "Digest-bound Patch from the Local Connected Task worktree",
		SHA256: hex.EncodeToString(digest[:]), MediaType: "text/x-diff",
	}, nil
}

func (store *localConnectedPatchStore) frozenPatchContract(ctx context.Context, task domain.Task, repository domain.RepositoryBinding) (speccoding.CoreContract, speccoding.CoreContractDocument, error) {
	binding, err := store.reader.GetSpecCodingBinding(ctx, task.ID())
	if err != nil || binding.TaskID != task.ID() || binding.Status != storecontract.SpecCodingRegistered ||
		sha256.Sum256(binding.ActiveContractJSON) != binding.ActiveContractDigest {
		return speccoding.CoreContract{}, speccoding.CoreContractDocument{}, errors.New("Task frozen Patch scope is unavailable or drifted")
	}
	contract, err := speccoding.DecodeCoreContract(binding.ActiveContractJSON)
	if err != nil || contract.DigestHex() != hex.EncodeToString(binding.ActiveContractDigest[:]) {
		return speccoding.CoreContract{}, speccoding.CoreContractDocument{}, errors.New("Task frozen Patch scope is unavailable or drifted")
	}
	document := contract.Document()
	if document.Task.ID != task.ID().String() || document.Task.RoomID != task.RoomID().String() ||
		document.Task.Repository.BaseRevision != repository.AdmittedBase() ||
		len(document.Execution.Boundary.WritableFiles) == 0 && len(document.Execution.Boundary.WritableDirectories) == 0 {
		return speccoding.CoreContract{}, speccoding.CoreContractDocument{}, errors.New("Task frozen Patch scope does not match its repository base")
	}
	return contract, document, nil
}

func collectReviewableChangePaths(ctx context.Context, root, base string) ([]string, map[string]struct{}, error) {
	trackedRaw, err := gitTargetOutputBounded(ctx, root, localConnectedDefaultPathLimit, "diff", "--name-only", "-z", "--no-renames", base, "--")
	if err != nil {
		return nil, nil, err
	}
	untrackedRaw, err := gitTargetOutputBounded(ctx, root, localConnectedDefaultPathLimit, "ls-files", "--others", "--exclude-standard", "-z", "--")
	if err != nil {
		return nil, nil, err
	}
	all := make(map[string]struct{})
	untracked := make(map[string]struct{})
	for _, relative := range nulSeparatedPaths(trackedRaw) {
		all[relative] = struct{}{}
	}
	for _, relative := range nulSeparatedPaths(untrackedRaw) {
		all[relative] = struct{}{}
		untracked[relative] = struct{}{}
	}
	paths := make([]string, 0, len(all))
	for relative := range all {
		if !safePatchTargetPath(relative) {
			return nil, nil, errors.New("unsafe changed path")
		}
		paths = append(paths, relative)
	}
	sort.Strings(paths)
	return paths, untracked, nil
}

func nulSeparatedPaths(raw []byte) []string {
	if len(raw) == 0 {
		return nil
	}
	parts := bytes.Split(raw, []byte{0})
	paths := make([]string, 0, len(parts)-1)
	for _, part := range parts {
		if len(part) != 0 {
			paths = append(paths, string(part))
		}
	}
	return paths
}

func validateReviewableTextChange(ctx context.Context, root, base, relative string) error {
	oldMode, oldData, oldExists, err := gitBaseFile(ctx, root, base, relative)
	if err != nil {
		return err
	}
	currentMode, currentData, currentExists, err := workingTreeFile(root, relative)
	if err != nil {
		return err
	}
	if !oldExists && !currentExists {
		return errors.New("changed path has neither base nor worktree content")
	}
	if oldExists && currentExists && oldMode != currentMode {
		return errors.New("executable-mode changes are unsupported")
	}
	if !oldExists && currentMode == "100755" {
		return errors.New("new executable files are unsupported")
	}
	for _, data := range [][]byte{oldData, currentData} {
		if len(data) == 0 {
			continue
		}
		if bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data) {
			return errors.New("binary or non-UTF-8 content is unsupported")
		}
		if bytes.HasPrefix(data, []byte("version https://git-lfs.github.com/spec/v1\n")) {
			return errors.New("Git LFS pointer content is unsupported")
		}
	}
	return nil
}

func gitBaseFile(ctx context.Context, root, base, relative string) (string, []byte, bool, error) {
	raw, err := gitTargetOutput(ctx, root, nil, "ls-tree", "-z", base, "--", ":(literal)"+relative)
	if err != nil {
		return "", nil, false, errors.New("base tree entry is unavailable")
	}
	if len(raw) == 0 {
		return "", nil, false, nil
	}
	entry := bytes.TrimSuffix(raw, []byte{0})
	if bytes.IndexByte(entry, 0) >= 0 {
		return "", nil, false, errors.New("base tree path is ambiguous")
	}
	tab := bytes.IndexByte(entry, '\t')
	if tab <= 0 {
		return "", nil, false, errors.New("base tree entry is invalid")
	}
	fields := strings.Fields(string(entry[:tab]))
	if len(fields) != 3 || string(entry[tab+1:]) != relative || fields[1] != "blob" || fields[0] != "100644" && fields[0] != "100755" {
		return "", nil, false, errors.New("base path is a symlink, submodule, or special file")
	}
	sizeRaw, sizeErr := gitTargetOutput(ctx, root, nil, "cat-file", "-s", fields[2])
	size, parseErr := strconv.ParseInt(strings.TrimSpace(string(sizeRaw)), 10, 64)
	if sizeErr != nil || parseErr != nil || size < 0 || size > localConnectedRepositoryFileByteLimit {
		return "", nil, false, errors.New("base file exceeds the supported review size")
	}
	data, err := gitTargetOutput(ctx, root, nil, "cat-file", "blob", fields[2])
	if err != nil {
		return "", nil, false, errors.New("base file content is unavailable")
	}
	return fields[0], data, true, nil
}

func workingTreeFile(root, relative string) (string, []byte, bool, error) {
	absolute := root
	parts := strings.Split(relative, "/")
	var leafInfo fs.FileInfo
	for index, part := range parts {
		absolute = filepath.Join(absolute, part)
		info, err := os.Lstat(absolute)
		if errors.Is(err, os.ErrNotExist) {
			return "", nil, false, nil
		}
		if err != nil || info.Mode()&fs.ModeSymlink != 0 || index < len(parts)-1 && !info.IsDir() || index == len(parts)-1 && !info.Mode().IsRegular() {
			return "", nil, false, errors.New("worktree path is a symlink or special file")
		}
		if index == len(parts)-1 {
			leafInfo = info
		}
	}
	if leafInfo == nil || leafInfo.Size() < 0 || leafInfo.Size() > localConnectedRepositoryFileByteLimit {
		return "", nil, false, errors.New("worktree file exceeds the supported review size")
	}
	file, err := os.Open(absolute)
	if err != nil {
		return "", nil, false, errors.New("worktree file content is unavailable")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, localConnectedRepositoryFileByteLimit+1))
	if err != nil || len(data) > localConnectedRepositoryFileByteLimit {
		return "", nil, false, errors.New("worktree file exceeds the supported review size")
	}
	mode := "100644"
	if info, err := os.Lstat(absolute); err != nil || !info.Mode().IsRegular() || info.Mode()&fs.ModeSymlink != 0 {
		return "", nil, false, errors.New("worktree file mode is unavailable")
	} else if info.Mode().Perm()&0o111 != 0 {
		mode = "100755"
	}
	return mode, data, true, nil
}

func buildScopedReviewPatch(ctx context.Context, root, base string, changed []string, untracked map[string]struct{}) ([]byte, error) {
	return buildScopedReviewPatchBounded(ctx, root, base, changed, untracked, localConnectedReviewPatchLimit)
}

func buildScopedReviewPatchBounded(ctx context.Context, root, base string, changed []string, untracked map[string]struct{}, limit int) ([]byte, error) {
	if limit <= 0 {
		return nil, errors.New("Task Patch exceeds the review artifact limit")
	}
	tracked := make([]string, 0, len(changed))
	newFiles := make([]string, 0, len(untracked))
	for _, relative := range changed {
		if _, ok := untracked[relative]; ok {
			newFiles = append(newFiles, relative)
		} else {
			tracked = append(tracked, relative)
		}
	}
	var patch bytes.Buffer
	if len(tracked) != 0 {
		arguments := []string{"-c", "core.quotePath=false", "diff", "--full-index", "--no-ext-diff", "--no-textconv", "--no-renames", base, "--"}
		for _, relative := range tracked {
			arguments = append(arguments, ":(literal)"+relative)
		}
		raw, err := gitTargetOutputBounded(ctx, root, int64(limit), arguments...)
		if err != nil {
			return nil, err
		}
		patch.Write(raw)
	}
	for _, relative := range newFiles {
		remaining := limit - patch.Len()
		if remaining <= 0 {
			return nil, errors.New("Task Patch exceeds the review artifact limit")
		}
		raw, err := gitDiffOutput(ctx, root, int64(remaining), "-c", "core.quotePath=false", "diff", "--no-index", "--full-index", "--no-ext-diff", "--no-textconv", "--", "/dev/null", relative)
		if err != nil {
			return nil, err
		}
		patch.Write(raw)
	}
	return patch.Bytes(), nil
}

func gitDiffOutput(ctx context.Context, root string, limit int64, arguments ...string) ([]byte, error) {
	if limit <= 0 {
		return nil, errors.New("Task Patch exceeds the review artifact limit")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	command := isolatedGitCommand(ctx, root, arguments...)
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, err
	}
	command.Stderr = io.Discard
	if err := command.Start(); err != nil {
		return nil, err
	}
	output, readErr := io.ReadAll(io.LimitReader(stdout, limit+1))
	if readErr != nil || int64(len(output)) > limit {
		cancel()
	}
	waitErr := command.Wait()
	if int64(len(output)) > limit {
		return nil, errors.New("Task Patch exceeds the review artifact limit")
	}
	if readErr != nil {
		return nil, readErr
	}
	if waitErr == nil {
		return output, nil
	}
	var status interface{ ExitCode() int }
	if !errors.As(waitErr, &status) || status.ExitCode() != 1 {
		return nil, waitErr
	}
	return output, nil
}

func (store *localConnectedPatchStore) ReadReviewPatch(_ context.Context, locator string) ([]byte, error) {
	path, err := store.locatorPath(locator)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() <= 0 || info.Size() > localConnectedReviewPatchLimit {
		return nil, errors.New("review Patch artifact is unavailable or unsafe")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, localConnectedReviewPatchLimit+1))
	if err != nil || len(raw) == 0 || len(raw) > localConnectedReviewPatchLimit {
		return nil, errors.New("review Patch artifact cannot be read safely")
	}
	return raw, nil
}

func (store *localConnectedPatchStore) taskRepository(ctx context.Context, taskID domain.TaskID) (domain.Task, domain.RepositoryBinding, error) {
	if store == nil || store.reader == nil {
		return domain.Task{}, domain.RepositoryBinding{}, errors.New("Local Connected Patch store is unavailable")
	}
	task, err := store.reader.GetTask(ctx, taskID)
	if err != nil {
		return domain.Task{}, domain.RepositoryBinding{}, err
	}
	binding, err := store.reader.GetRepositoryBinding(ctx, task.RoomID())
	if err != nil || binding.State() != domain.RepositoryBindingStateActive {
		return domain.Task{}, domain.RepositoryBinding{}, errors.New("active Task repository binding is unavailable")
	}
	base, err := store.reader.GetTaskWorktreeBinding(ctx, taskID)
	if err != nil {
		return domain.Task{}, domain.RepositoryBinding{}, err
	}
	binding, err = repositoryForTaskBase(ctx, binding, base)
	return task, binding, err
}

func (store *localConnectedPatchStore) writeImmutable(locator string, raw []byte) error {
	path, err := store.locatorPath(locator)
	if err != nil {
		return err
	}
	directory := filepath.Dir(path)
	if err := os.Mkdir(directory, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("create review Patch directory: %w", err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return fmt.Errorf("secure review Patch directory: %w", err)
	}
	if existing, readErr := os.ReadFile(path); readErr == nil {
		if bytes.Equal(existing, raw) {
			return nil
		}
		return errors.New("review Patch artifact identity conflict")
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return readErr
	}
	temporary, err := os.CreateTemp(directory, ".patch-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(raw); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Link(temporaryPath, path); err != nil {
		if existing, readErr := os.ReadFile(path); readErr == nil && bytes.Equal(existing, raw) {
			return nil
		}
		return fmt.Errorf("publish immutable review Patch: %w", err)
	}
	return nil
}

func (store *localConnectedPatchStore) locatorPath(locator string) (string, error) {
	parts := strings.Split(locator, "/")
	if store == nil || !cleanAbsolutePath(store.root) || len(parts) != 2 || parts[1] != "patch.diff" {
		return "", errors.New("invalid review Patch locator")
	}
	if _, err := domain.ParseAttemptID(parts[0]); err != nil {
		return "", errors.New("invalid review Patch locator")
	}
	path := filepath.Join(store.root, parts[0], parts[1])
	if filepath.Dir(filepath.Dir(path)) != store.root {
		return "", errors.New("invalid review Patch locator")
	}
	return path, nil
}

// repositoryBoundGitPatchTarget applies an accepted Task Patch only to the
// Project's shared original checkout. The Task worktree is a Patch source. One
// server owns this target and its Apply mutex across every topic Room.
type repositoryBoundGitPatchTarget struct {
	reader  storecontract.Reader
	applyMu sync.Mutex
}

func (target *repositoryBoundGitPatchTarget) Inspect(ctx context.Context, request app.PatchTargetRequest) (app.PatchTargetInspection, error) {
	resolved, binding, err := target.resolve(ctx, request.TaskID)
	if err != nil {
		return app.PatchTargetInspection{}, fmt.Errorf("%w: original repository binding is unavailable", app.ErrPatchTargetRecovery)
	}
	inspection, inspectErr := resolved.Inspect(ctx, request)
	if inspectErr != nil {
		return inspection, inspectErr
	}
	if inspection.BaseRevision != binding.AdmittedBase() {
		inspection.Applicable = false
		inspection.AlreadyApplied = false
		inspection.Reason = "The original checkout HEAD drifted from the admitted Task base."
		return inspection, fmt.Errorf("%w: original checkout HEAD drifted; restore the Task base or create a new Task", app.ErrPatchTargetConflict)
	}
	base, err := target.reader.GetTaskWorktreeBinding(ctx, request.TaskID)
	if err != nil {
		return inspection, err
	}
	if base.StartPolicy() == domain.TaskStartPolicyCurrentHEAD {
		ref, err := currentTaskRef(ctx, binding.TargetWorktree())
		if err != nil || ref != base.BaseRef() {
			inspection.Applicable = false
			inspection.AlreadyApplied = false
			inspection.Reason = "The original checkout branch drifted from the Task base; restore the selected branch or create a new Task."
			return inspection, fmt.Errorf("%w: original checkout branch drifted; restore the selected branch or create a new Task", app.ErrPatchTargetConflict)
		}
	}
	return inspection, nil
}

func (target *repositoryBoundGitPatchTarget) Apply(ctx context.Context, request app.PatchTargetRequest, inspection app.PatchTargetInspection) (app.PatchTargetEvidence, error) {
	if !target.applyMu.TryLock() {
		return app.PatchTargetEvidence{}, fmt.Errorf("%w: another Apply is in progress; wait and retry", app.ErrPatchTargetConflict)
	}
	defer target.applyMu.Unlock()
	current, err := target.Inspect(ctx, request)
	if err != nil || !current.Applicable || current.TargetIdentity != inspection.TargetIdentity || current.BaseRevision != inspection.BaseRevision || current.StateDigest != inspection.StateDigest {
		return app.PatchTargetEvidence{}, fmt.Errorf("%w: target changed after review; restore the Task base or create a new Task", app.ErrPatchTargetConflict)
	}
	resolved, _, err := target.resolve(ctx, request.TaskID)
	if err != nil {
		return app.PatchTargetEvidence{}, fmt.Errorf("%w: original repository binding is unavailable", app.ErrPatchTargetRecovery)
	}
	return resolved.Apply(ctx, request, inspection)
}

func (target *repositoryBoundGitPatchTarget) resolve(ctx context.Context, taskID domain.TaskID) (*gitPatchTarget, domain.RepositoryBinding, error) {
	if target == nil || target.reader == nil || !taskID.Valid() {
		return nil, domain.RepositoryBinding{}, errors.New("invalid Task repository target")
	}
	task, err := target.reader.GetTask(ctx, taskID)
	if err != nil {
		return nil, domain.RepositoryBinding{}, err
	}
	binding, err := target.reader.GetRepositoryBinding(ctx, task.RoomID())
	if err != nil || binding.State() != domain.RepositoryBindingStateActive {
		return nil, domain.RepositoryBinding{}, errors.New("active repository binding is unavailable; restore the Project")
	}
	base, err := target.reader.GetTaskWorktreeBinding(ctx, taskID)
	if err != nil {
		return nil, domain.RepositoryBinding{}, err
	}
	binding, err = repositoryForTaskBase(ctx, binding, base)
	if err != nil {
		return nil, domain.RepositoryBinding{}, err
	}
	resolved, err := newGitPatchTarget(binding.TargetWorktree(), nil)
	if err != nil {
		return nil, domain.RepositoryBinding{}, err
	}
	identity := sha256.Sum256([]byte("chora.repository-patch-target.v1\x00" + task.RoomID().String() + "\x00" + binding.TargetWorktree() + "\x00" + binding.BaseIdentity()))
	resolved.identity = "sha256:" + hex.EncodeToString(identity[:])
	return resolved, binding, nil
}

var _ app.ReviewPatchMaterializer = (*localConnectedPatchStore)(nil)
var _ app.ReviewPatchSource = (*localConnectedPatchStore)(nil)
var _ app.PatchApplicationTarget = (*repositoryBoundGitPatchTarget)(nil)
