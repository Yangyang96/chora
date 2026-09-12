package localweb

import (
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
	"slices"
	"strings"

	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/speccoding"
)

const (
	resourceObserverConfigSchema       = "chora.resource-observer-config.v1"
	resourceFingerprintSchema          = "chora.resource-fingerprint.v1"
	resourceFingerprintAlgorithm       = "sha256-resource-patch-v1"
	resourceFingerprintInputByteLimit  = 1 << 20
	resourceFingerprintHelperByteLimit = 512 << 20
)

type resourceFingerprintRequest struct {
	Config  resourceFingerprintConfig `json:"config"`
	Command string                    `json:"command"`
}

type resourceFingerprintConfig struct {
	RepositoryLayout string                                   `json:"repositoryLayout,omitempty"`
	Schema           string                                   `json:"schema"`
	AttemptID        string                                   `json:"attemptId"`
	TaskRoot         string                                   `json:"taskRoot"`
	Resources        []speccoding.ExecutionRepositoryResource `json:"resources"`
}

type resourceFingerprintResponse struct {
	Schema           string `json:"schema"`
	Algorithm        string `json:"algorithm"`
	RepoID           string `json:"repoId"`
	WorkingDirectory string `json:"workingDirectory"`
	CommandDigest    string `json:"commandDigest"`
	Fingerprint      string `json:"fingerprint"`
	HelperSHA256     string `json:"helperSha256"`
}

// RunResourceCheckFingerprint reads one bounded observer request and emits one
// digest proof. It only inspects the prepared Task worktrees and never writes a
// Patch artifact or executes the observed command.
func RunResourceCheckFingerprint(parent context.Context, stdin io.Reader, stdout io.Writer) error {
	if parent == nil || stdin == nil || stdout == nil {
		return errors.New("resource fingerprint helper configuration is incomplete")
	}
	ctx, cancel := context.WithTimeout(parent, domain.RepositoryMetadataTimeout)
	defer cancel()

	request, err := decodeResourceFingerprintRequest(stdin)
	if err != nil {
		return err
	}
	response, err := resourceCheckFingerprint(ctx, request)
	if err != nil {
		return err
	}
	if err := json.NewEncoder(stdout).Encode(response); err != nil {
		return errors.New("resource fingerprint response could not be encoded")
	}
	return nil
}

func decodeResourceFingerprintRequest(input io.Reader) (resourceFingerprintRequest, error) {
	var request resourceFingerprintRequest
	raw, err := io.ReadAll(io.LimitReader(input, resourceFingerprintInputByteLimit+1))
	if err != nil || len(raw) == 0 || len(raw) > resourceFingerprintInputByteLimit {
		return request, errors.New("resource fingerprint request is unavailable or exceeds its limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return request, errors.New("resource fingerprint request is invalid")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return request, errors.New("resource fingerprint request has trailing data")
	}
	return request, nil
}

func resourceCheckFingerprint(ctx context.Context, request resourceFingerprintRequest) (resourceFingerprintResponse, error) {
	if request.Config.Schema != resourceObserverConfigSchema || strings.TrimSpace(request.Command) == "" || (request.Config.RepositoryLayout != "" && request.Config.RepositoryLayout != "isolated_copy") {
		return resourceFingerprintResponse{}, errors.New("resource fingerprint request authority is invalid")
	}
	if _, err := domain.ParseAttemptID(request.Config.AttemptID); err != nil {
		return resourceFingerprintResponse{}, errors.New("resource fingerprint Attempt identity is invalid")
	}
	if err := validateExecutionFingerprintResources(request.Config.Resources); err != nil {
		return resourceFingerprintResponse{}, err
	}
	if !cleanAbsolutePath(request.Config.TaskRoot) {
		return resourceFingerprintResponse{}, errors.New("resource fingerprint Task root is invalid")
	}
	helperSHA256, err := runningResourceFingerprintHelperSHA256()
	if err != nil {
		return resourceFingerprintResponse{}, err
	}
	canonicalRoot, err := proveResourceTaskRoot(request.Config.TaskRoot)
	if err != nil || canonicalRoot != request.Config.TaskRoot {
		return resourceFingerprintResponse{}, errors.New("resource fingerprint Task root cannot be proven")
	}

	normalized, err := speccoding.NormalizeObservedCheckCommandAtRoot(request.Command, request.Config.TaskRoot)
	if err != nil || !normalized.ExplicitWorkingDirectory {
		return resourceFingerprintResponse{}, errors.New("resource fingerprint command is not an attributable ordinary command")
	}
	repoID, workingDirectory, err := splitResourceFingerprintDirectory(normalized.WorkingDirectory)
	if err != nil {
		return resourceFingerprintResponse{}, err
	}
	var resource *speccoding.ExecutionRepositoryResource
	for index := range request.Config.Resources {
		if request.Config.Resources[index].Locator == repoID {
			resource = &request.Config.Resources[index]
			break
		}
	}
	if resource == nil || !matchesFrozenResourceCheck(*resource, normalized.Argv, workingDirectory) {
		return resourceFingerprintResponse{}, errors.New("resource fingerprint command is outside frozen check authority")
	}
	if request.Config.RepositoryLayout == "isolated_copy" {
		ctx = context.WithValue(ctx, isolatedRepositoryCopyContextKey{}, true)
	}
	repositoryRoot, err := proveResourceFingerprintWorktree(ctx, request.Config.TaskRoot, canonicalRoot, *resource)
	if err != nil {
		return resourceFingerprintResponse{}, err
	}
	if workingDirectory != "." {
		if err := validateRepositoryPathAtRevision(ctx, repositoryRoot, resource.BaseCommit, workingDirectory, true, true); err != nil {
			return resourceFingerprintResponse{}, fmt.Errorf("resource fingerprint working directory cannot be proven: %w", err)
		}
	}

	changed, untracked, err := collectResourceReviewPaths(ctx, repositoryRoot, resource.BaseCommit, domain.TaskChangedEntryLimit)
	if err != nil {
		return resourceFingerprintResponse{}, fmt.Errorf("resource fingerprint changed paths are unavailable: %w", err)
	}
	if len(changed) != 0 && resource.Role != "write" {
		return resourceFingerprintResponse{}, errors.New("resource fingerprint found changes in a reference repository")
	}
	for _, relative := range changed {
		if !resource.Scope.Allows(relative) {
			return resourceFingerprintResponse{}, fmt.Errorf("resource fingerprint change is outside frozen writable scope: %s", relative)
		}
		if err := validateReviewableTextChange(ctx, repositoryRoot, resource.BaseCommit, relative); err != nil {
			return resourceFingerprintResponse{}, fmt.Errorf("resource fingerprint change %q is unsupported: %w", relative, err)
		}
	}
	raw, err := buildScopedReviewPatchBounded(ctx, repositoryRoot, resource.BaseCommit, changed, untracked, domain.TaskPatchBytes)
	if err != nil || len(changed) != 0 && len(bytes.TrimSpace(raw)) == 0 {
		if err == nil {
			err = errors.New("empty Patch")
		}
		return resourceFingerprintResponse{}, fmt.Errorf("resource fingerprint Patch is unavailable: %w", err)
	}
	patchDigest := sha256.Sum256(raw)
	commandAuthority, _ := json.Marshal(struct {
		Argv []string `json:"argv"`
		Cwd  string   `json:"cwd"`
	}{Argv: normalized.Argv, Cwd: normalized.WorkingDirectory})
	commandDigest := sha256.Sum256(commandAuthority)
	return resourceFingerprintResponse{
		Schema: resourceFingerprintSchema, Algorithm: resourceFingerprintAlgorithm, RepoID: resource.RepoID,
		WorkingDirectory: workingDirectory, CommandDigest: hex.EncodeToString(commandDigest[:]),
		Fingerprint: hex.EncodeToString(patchDigest[:]), HelperSHA256: helperSHA256,
	}, nil
}

func runningResourceFingerprintHelperSHA256() (string, error) {
	path, err := os.Executable()
	if err != nil {
		return "", errors.New("resource fingerprint helper identity is unavailable")
	}
	path, err = filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil || !cleanAbsolutePath(path) {
		return "", errors.New("resource fingerprint helper identity is unavailable")
	}
	pathInfo, err := os.Lstat(path)
	if err != nil || !pathInfo.Mode().IsRegular() || pathInfo.Mode()&os.ModeSymlink != 0 || pathInfo.Size() <= 0 || pathInfo.Size() > resourceFingerprintHelperByteLimit {
		return "", errors.New("resource fingerprint helper is unavailable or exceeds its limit")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", errors.New("resource fingerprint helper identity is unavailable")
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil || !os.SameFile(pathInfo, openedInfo) {
		return "", errors.New("resource fingerprint helper identity changed")
	}
	digest := sha256.New()
	read, err := io.Copy(digest, io.LimitReader(file, resourceFingerprintHelperByteLimit+1))
	if err != nil || read != pathInfo.Size() || read > resourceFingerprintHelperByteLimit {
		return "", errors.New("resource fingerprint helper identity could not be read safely")
	}
	afterInfo, err := file.Stat()
	pathAfter, pathErr := os.Stat(path)
	if err != nil || pathErr != nil || !os.SameFile(openedInfo, afterInfo) || !os.SameFile(openedInfo, pathAfter) ||
		afterInfo.Size() != openedInfo.Size() || !afterInfo.ModTime().Equal(openedInfo.ModTime()) {
		return "", errors.New("resource fingerprint helper identity changed while reading")
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func validateExecutionFingerprintResources(resources []speccoding.ExecutionRepositoryResource) error {
	if len(resources) == 0 || len(resources) > domain.TaskRepositoryLimit {
		return errors.New("resource fingerprint resources are invalid")
	}
	validation := domain.TaskResourceSnapshot{
		SchemaVersion: domain.TaskResourceSchemaV2,
		TaskID:        "task_00000000-0000-7000-8000-000000000001", ProjectID: "project_00000000-0000-7000-8000-000000000002",
		RoomID: "room_00000000-0000-7000-8000-000000000003", SelectionSource: "observer_config",
		Resources: make([]domain.TaskRepositoryResource, 0, len(resources)),
	}
	for _, resource := range resources {
		if !domain.ValidRepositoryWorkspaceLocator(resource.RepoID, resource.Locator) {
			return errors.New("resource fingerprint locator is invalid")
		}
		validation.Resources = append(validation.Resources, domain.TaskRepositoryResource{
			RepoID: resource.RepoID, Name: resource.Name, Checkout: "/validation", CommonGitDir: "/validation/.git",
			PhysicalIdentity: resource.TargetIdentityDigest, Role: resource.Role, BaseCommit: resource.BaseCommit,
			BaseTree: resource.BaseTree, BaseRef: resource.BaseRef, AssociationVersion: 1, Scope: resource.Scope, Checks: resource.Checks,
		})
	}
	if err := validation.Validate(); err != nil {
		return errors.New("resource fingerprint resources do not match the frozen v12 contract")
	}
	return nil
}

func splitResourceFingerprintDirectory(value string) (string, string, error) {
	repoID, suffix, found := strings.Cut(value, "/")
	if _, err := domain.ParseRepositoryID(repoID); err != nil && (len(repoID) != 12 || !isLowerHexWorkspaceName(repoID)) {
		return "", "", errors.New("resource fingerprint working directory has no valid repository identity")
	}
	if !found {
		return repoID, ".", nil
	}
	if suffix == "" || !safeProjectSettingsPath(suffix) {
		return "", "", errors.New("resource fingerprint working directory is unsafe")
	}
	return repoID, suffix, nil
}

func matchesFrozenResourceCheck(resource speccoding.ExecutionRepositoryResource, argv []string, cwd string) bool {
	if resource.Checks.Mode == "auto" {
		if cwd == "." {
			return true
		}
		for _, commands := range [][]domain.TaskCheckCommand{resource.Checks.Commands, resource.Checks.Preparation} {
			for _, command := range commands {
				workingDirectory := command.WorkingDirectory
				if workingDirectory == "" {
					workingDirectory = "."
				}
				if workingDirectory == cwd {
					return true
				}
			}
		}
		return false
	}
	for _, commands := range [][]domain.TaskCheckCommand{resource.Checks.Commands, resource.Checks.Preparation} {
		for _, command := range commands {
			workingDirectory := command.WorkingDirectory
			if workingDirectory == "" {
				workingDirectory = "."
			}
			if workingDirectory == cwd && slices.Equal(command.Argv, argv) {
				return true
			}
		}
	}
	return false
}

func proveResourceFingerprintWorktree(ctx context.Context, taskRoot, canonicalRoot string, resource speccoding.ExecutionRepositoryResource) (string, error) {
	_, err := domain.ParseRepositoryID(resource.RepoID)
	if err != nil {
		return "", errors.New("resource fingerprint repository identity is invalid")
	}
	root := filepath.Join(taskRoot, resource.Locator)
	if filepath.Dir(root) != taskRoot {
		return "", errors.New("resource fingerprint repository escaped the Task root")
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("resource fingerprint repository is unavailable or unsafe")
	}
	gitLink, err := os.Lstat(filepath.Join(root, ".git"))
	isolatedCopy := ctx.Value(isolatedRepositoryCopyContextKey{}) == true
	if err != nil || gitLink.Mode()&os.ModeSymlink != 0 || (!isolatedCopy && !gitLink.Mode().IsRegular()) || (isolatedCopy && !gitLink.IsDir()) {
		return "", errors.New("resource fingerprint repository is not a dedicated Git worktree")
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil || filepath.Clean(canonical) != filepath.Join(canonicalRoot, resource.Locator) {
		return "", errors.New("resource fingerprint repository escaped the Task root")
	}
	top, err := gitTargetOutputBounded(ctx, root, domain.RepositoryMetadataBytes, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", errors.New("resource fingerprint repository root is unavailable")
	}
	topCanonical, err := filepath.EvalSymlinks(strings.TrimSpace(string(top)))
	if err != nil || filepath.Clean(topCanonical) != filepath.Clean(canonical) {
		return "", errors.New("resource fingerprint repository is not the exact Git root")
	}
	if common, commonErr := taskWorktreeCommonDirectory(ctx, root); commonErr != nil || !cleanAbsolutePath(common) || (isolatedCopy && common != filepath.Join(root, ".git")) {
		return "", errors.New("resource fingerprint repository common Git directory is unavailable")
	}
	head, err := gitTargetOutputBounded(ctx, root, domain.RepositoryMetadataBytes, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil || strings.TrimSpace(string(head)) != resource.BaseCommit {
		return "", errors.New("resource fingerprint repository HEAD differs from its frozen base")
	}
	tree, err := gitTargetOutputBounded(ctx, root, domain.RepositoryMetadataBytes, "rev-parse", "--verify", "HEAD^{tree}")
	if err != nil || strings.TrimSpace(string(tree)) != resource.BaseTree {
		return "", errors.New("resource fingerprint repository tree differs from its frozen base")
	}
	if isolatedCopy {
		if _, err := gitTargetOutputBounded(ctx, root, domain.RepositoryMetadataBytes, "symbolic-ref", "-q", "HEAD"); err == nil {
			return "", errors.New("isolated repository must retain detached frozen HEAD")
		}
	}
	if err := proveResourceFingerprintHeadBinding(ctx, taskRoot, root, resource); err != nil {
		return "", err
	}
	return root, nil
}

func proveResourceFingerprintHeadBinding(ctx context.Context, taskRoot, root string, resource speccoding.ExecutionRepositoryResource) error {
	symbolic, symbolicErr := gitTargetOutputBounded(ctx, root, domain.RepositoryMetadataBytes, "symbolic-ref", "-q", "HEAD")
	if symbolicErr != nil {
		if len(bytes.TrimSpace(symbolic)) != 0 {
			return errors.New("resource fingerprint repository HEAD binding is ambiguous")
		}
		return nil
	}
	if resource.Role != "write" {
		return errors.New("resource fingerprint reference repository HEAD is not detached")
	}
	ref := strings.TrimSpace(string(symbolic))
	branch, ok := strings.CutPrefix(ref, "refs/heads/")
	kind, _, found := strings.Cut(branch, "/")
	if !ok || !found || strings.Count(branch, "/") != 1 {
		return errors.New("resource fingerprint Task branch identity is invalid")
	}

	witness, err := gitTargetOutputBounded(ctx, root, domain.RepositoryMetadataBytes, "reflog", "show", "-1", "--format=%H%x00%gs", ref)
	witness = bytes.TrimSuffix(witness, []byte{'\n'})
	parts := bytes.SplitN(witness, []byte{0}, 2)
	if err != nil || len(parts) != 2 || string(parts[0]) != resource.BaseCommit {
		return errors.New("resource fingerprint Task branch creation proof is unavailable")
	}
	marker := strings.Split(string(parts[1]), ":")
	if len(marker) != 4 || marker[0] != "chora-task-workspace" || marker[3] != resource.RepoID {
		return errors.New("resource fingerprint Task branch creation proof is invalid")
	}
	taskID, err := domain.ParseTaskID(marker[2])
	dataRoot := filepath.Dir(filepath.Dir(taskRoot))
	rootDigest := sha256.Sum256([]byte(taskResourceWorkspaceFingerprintVersion + "\x00" + dataRoot))
	if err != nil || marker[1] != hex.EncodeToString(rootDigest[:]) ||
		filepath.Join(dataRoot, "task-workspaces", domain.ShortWorkspaceName(taskID.String())) != taskRoot {
		return errors.New("resource fingerprint Task branch root authority is invalid")
	}

	unbound := domain.TaskResourceSnapshot{
		SchemaVersion:   domain.TaskResourceSchemaV2,
		ProjectID:       "project_00000000-0000-7000-8000-000000000002",
		RoomID:          "room_00000000-0000-7000-8000-000000000003",
		SelectionSource: "observer_config",
		Resources: []domain.TaskRepositoryResource{{
			RepoID: resource.RepoID, Name: resource.Name, Checkout: "/validation", CommonGitDir: "/validation/.git",
			PhysicalIdentity: resource.TargetIdentityDigest, Role: resource.Role, BaseCommit: resource.BaseCommit,
			BaseTree: resource.BaseTree, BaseRef: resource.BaseRef, AssociationVersion: 1, Scope: resource.Scope, Checks: resource.Checks,
		}},
	}
	bound, err := domain.BindTaskResourceBranches(unbound, taskID, kind)
	if err != nil || len(bound.Resources) != 1 || ref != "refs/heads/"+bound.Resources[0].TaskBranch {
		return errors.New("resource fingerprint Task branch differs from its frozen Task authority")
	}
	return nil
}

func isLowerHexWorkspaceName(value string) bool {
	raw, err := hex.DecodeString(value)
	return err == nil && len(raw) == 6 && value == strings.ToLower(value)
}
