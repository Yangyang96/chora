package localweb

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/isolatedworkspace"
	"github.com/Yangyang96/chora/internal/speccoding"
)

func TestResourceCheckFingerprintMatchesMaterializedPatchWithoutWritingArtifact(t *testing.T) {
	fixture := newResourcePatchFixture(t, resourcePatchConfig{role: "write"}, resourcePatchConfig{role: "write"})
	resources := fingerprintExecutionResources(fixture.snapshot)
	resource := resources[0]
	resource.Checks = fingerprintNamedChecks(".")
	resources[0] = resource
	repositoryRoot := filepath.Join(fixture.root, resource.RepoID)
	writeResourcePatchFile(t, repositoryRoot, "README.md", []byte("changed tracked\n"))
	writeResourcePatchFile(t, repositoryRoot, "new.txt", []byte("new untracked\n"))

	response := runResourceFingerprintTest(t, resourceFingerprintRequest{
		Config:  resourceFingerprintConfig{Schema: resourceObserverConfigSchema, AttemptID: domain.NewAttemptID().String(), TaskRoot: fixture.root, Resources: resources},
		Command: "cd " + resource.RepoID + " && npm test",
	})
	if entries, err := os.ReadDir(fixture.artifactRoot); err != nil || len(entries) != 0 {
		t.Fatalf("fingerprint helper wrote artifact entries=%d err=%v", len(entries), err)
	}

	attemptID := domain.NewAttemptID()
	patches, err := fixture.materializer.Materialize(context.Background(), fixture.task.ID(), attemptID, fixture.root)
	if err != nil {
		t.Fatal(err)
	}
	var materialized app.ResourceReviewPatch
	for _, patch := range patches {
		if patch.RepoID == resource.RepoID {
			materialized = patch
		}
	}
	wantFingerprint := sha256.Sum256(materialized.Patch.Raw)
	helperRaw, err := os.ReadFile(mustResourceFingerprintExecutable(t))
	if err != nil {
		t.Fatal(err)
	}
	wantHelper := sha256.Sum256(helperRaw)
	if response.Schema != resourceFingerprintSchema || response.Algorithm != resourceFingerprintAlgorithm || response.RepoID != resource.RepoID || response.WorkingDirectory != "." || response.Fingerprint != hex.EncodeToString(wantFingerprint[:]) || response.HelperSHA256 != hex.EncodeToString(wantHelper[:]) {
		t.Fatalf("fingerprint response = %+v, materialized SHA=%x", response, wantFingerprint)
	}
	commandJSON, _ := json.Marshal(struct {
		Argv []string `json:"argv"`
		Cwd  string   `json:"cwd"`
	}{Argv: []string{"npm", "test"}, Cwd: resource.RepoID})
	wantCommand := sha256.Sum256(commandJSON)
	if response.CommandDigest != hex.EncodeToString(wantCommand[:]) {
		t.Fatalf("command digest=%s want=%x over %s", response.CommandDigest, wantCommand, commandJSON)
	}
	original, err := os.ReadFile(filepath.Join(fixture.originals[resource.RepoID], "README.md"))
	if err != nil || string(original) != "initial\n" {
		t.Fatalf("original checkout changed: %q err=%v", original, err)
	}
}

func mustResourceFingerprintExecutable(t *testing.T) string {
	t.Helper()
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	path, err = filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func TestResourceCheckFingerprintTracksEmptyUntrackedAndContentDrift(t *testing.T) {
	fixture := newResourcePatchFixture(t, resourcePatchConfig{role: "write"})
	resource := fingerprintExecutionResources(fixture.snapshot)[0]
	resource.Checks = fingerprintNamedChecks(".")
	request := resourceFingerprintRequest{
		Config:  resourceFingerprintConfig{Schema: resourceObserverConfigSchema, AttemptID: domain.NewAttemptID().String(), TaskRoot: fixture.root, Resources: []speccoding.ExecutionRepositoryResource{resource}},
		Command: "cd " + resource.RepoID + " && npm test",
	}
	empty := runResourceFingerprintTest(t, request)
	wantEmpty := sha256.Sum256(nil)
	if empty.Fingerprint != hex.EncodeToString(wantEmpty[:]) {
		t.Fatalf("empty fingerprint=%s want=%x", empty.Fingerprint, wantEmpty)
	}
	path := filepath.Join(fixture.root, resource.RepoID, "new.txt")
	writeResourcePatchFile(t, filepath.Dir(path), filepath.Base(path), []byte("first\n"))
	first := runResourceFingerprintTest(t, request)
	writeResourcePatchFile(t, filepath.Dir(path), filepath.Base(path), []byte("second\n"))
	second := runResourceFingerprintTest(t, request)
	if first.Fingerprint == empty.Fingerprint || second.Fingerprint == first.Fingerprint {
		t.Fatalf("content drift was not reflected: empty=%s first=%s second=%s", empty.Fingerprint, first.Fingerprint, second.Fingerprint)
	}
}

func TestResourceCheckFingerprintAcceptsAbsoluteTaskBoundCommand(t *testing.T) {
	fixture := newResourcePatchFixture(t, resourcePatchConfig{role: "write"}, resourcePatchConfig{role: "write"})
	resources := fingerprintExecutionResources(fixture.snapshot)
	for index := range resources {
		resources[index].Checks = fingerprintNamedChecks(".")
	}
	resource := resources[1]
	absolute := filepath.Join(fixture.root, resource.Locator)
	response := runResourceFingerprintTest(t, resourceFingerprintRequest{
		Config:  resourceFingerprintConfig{Schema: resourceObserverConfigSchema, AttemptID: domain.NewAttemptID().String(), TaskRoot: fixture.root, Resources: resources},
		Command: "cd " + speccodingQuoteForTest(absolute) + " && npm 'test'",
	})
	if response.RepoID != resource.RepoID || response.WorkingDirectory != "." {
		t.Fatalf("absolute response=%+v", response)
	}
	commandJSON, _ := json.Marshal(struct {
		Argv []string `json:"argv"`
		Cwd  string   `json:"cwd"`
	}{[]string{"npm", "test"}, resource.Locator})
	digest := sha256.Sum256(commandJSON)
	if response.CommandDigest != hex.EncodeToString(digest[:]) {
		t.Fatalf("absolute command digest=%s want=%x", response.CommandDigest, digest)
	}

	assertResourceFingerprintError(t, fixture.root, resource, "cd "+fixture.root+"-sibling/"+resource.Locator+" && npm test", "not an attributable ordinary command")
	assertResourceFingerprintError(t, fixture.root, resource, "cd "+absolute+" && npm test && npm lint", "not an attributable ordinary command")
}

func speccodingQuoteForTest(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func TestResourceCheckFingerprintAutoCommandsUseOnlyFrozenWorkingDirectories(t *testing.T) {
	t.Run("new ordinary command at repository root", func(t *testing.T) {
		fixture := newResourcePatchFixture(t, resourcePatchConfig{role: "write"})
		resource := fingerprintExecutionResources(fixture.snapshot)[0]
		resource.Checks = domain.TaskCheckPolicy{Mode: "auto", SelectionSource: "committed_configuration"}
		response := runResourceFingerprintTest(t, resourceFingerprintRequest{
			Config:  resourceFingerprintConfig{Schema: resourceObserverConfigSchema, AttemptID: domain.NewAttemptID().String(), TaskRoot: fixture.root, Resources: []speccoding.ExecutionRepositoryResource{resource}},
			Command: "cd " + resource.RepoID + " && go test ./...",
		})
		if response.RepoID != resource.RepoID || response.WorkingDirectory != "." {
			t.Fatalf("root auto response=%+v", response)
		}
	})

	t.Run("new ordinary command at frozen proven nested directory", func(t *testing.T) {
		fixture := newResourcePatchFixture(t, resourcePatchConfig{role: "write"})
		resource := fingerprintExecutionResources(fixture.snapshot)[0]
		commitFingerprintWorkingDirectory(t, fixture.root, &resource, "tools")
		resource.Checks = domain.TaskCheckPolicy{Mode: "auto", SelectionSource: "committed_configuration", Commands: []domain.TaskCheckCommand{{
			ID: "discovered", Name: "Discovered tests", Command: "npm test", Argv: []string{"npm", "test"}, WorkingDirectory: "tools", Source: "auto",
		}}}
		response := runResourceFingerprintTest(t, resourceFingerprintRequest{
			Config:  resourceFingerprintConfig{Schema: resourceObserverConfigSchema, AttemptID: domain.NewAttemptID().String(), TaskRoot: fixture.root, Resources: []speccoding.ExecutionRepositoryResource{resource}},
			Command: "cd " + resource.RepoID + "/tools && go test ./...",
		})
		if response.WorkingDirectory != "tools" {
			t.Fatalf("nested auto response=%+v", response)
		}
	})

	t.Run("new ordinary command at unfrozen nested directory", func(t *testing.T) {
		fixture := newResourcePatchFixture(t, resourcePatchConfig{role: "write"})
		resource := fingerprintExecutionResources(fixture.snapshot)[0]
		commitFingerprintWorkingDirectory(t, fixture.root, &resource, "tools")
		resource.Checks = domain.TaskCheckPolicy{Mode: "auto", SelectionSource: "committed_configuration"}
		assertResourceFingerprintError(t, fixture.root, resource, "cd "+resource.RepoID+"/tools && go test ./...", "outside frozen check authority")
	})

	t.Run("new named command", func(t *testing.T) {
		fixture := newResourcePatchFixture(t, resourcePatchConfig{role: "write"})
		resource := fingerprintExecutionResources(fixture.snapshot)[0]
		resource.Checks = fingerprintNamedChecks(".")
		assertResourceFingerprintError(t, fixture.root, resource, "cd "+resource.RepoID+" && go test ./...", "outside frozen check authority")
	})
}

func TestResourceCheckFingerprintRejectsUnsafeAuthorityAndWorkspaceState(t *testing.T) {
	t.Run("outside restricted scope", func(t *testing.T) {
		fixture := newResourcePatchFixture(t, resourcePatchConfig{role: "write", scope: domain.TaskRepositoryScope{Mode: "restricted", WritableFiles: []string{"allowed.txt"}, MigrationChoice: "not_needed"}})
		resource := fingerprintExecutionResources(fixture.snapshot)[0]
		resource.Checks = fingerprintNamedChecks(".")
		writeResourcePatchFile(t, filepath.Join(fixture.root, resource.RepoID), "denied.txt", []byte("denied\n"))
		assertResourceFingerprintError(t, fixture.root, resource, "cd "+resource.RepoID+" && npm test", "outside frozen writable scope")
	})

	t.Run("changed reference repository", func(t *testing.T) {
		fixture := newResourcePatchFixture(t, resourcePatchConfig{role: "reference"})
		resource := fingerprintExecutionResources(fixture.snapshot)[0]
		resource.Checks = fingerprintNamedChecks(".")
		writeResourcePatchFile(t, filepath.Join(fixture.root, resource.RepoID), "new.txt", []byte("changed\n"))
		assertResourceFingerprintError(t, fixture.root, resource, "cd "+resource.RepoID+" && npm test", "reference repository")
	})

	t.Run("proposed working directory", func(t *testing.T) {
		fixture := newResourcePatchFixture(t, resourcePatchConfig{role: "write"})
		resource := fingerprintExecutionResources(fixture.snapshot)[0]
		resource.Checks = fingerprintNamedChecks(".")
		assertResourceFingerprintError(t, fixture.root, resource, "cd "+resource.RepoID+"/proposed && npm test", "outside frozen check authority")
	})

	t.Run("head drift", func(t *testing.T) {
		fixture := newResourcePatchFixture(t, resourcePatchConfig{role: "write"})
		resource := fingerprintExecutionResources(fixture.snapshot)[0]
		resource.Checks = fingerprintNamedChecks(".")
		repositoryRoot := filepath.Join(fixture.root, resource.RepoID)
		writeResourcePatchFile(t, repositoryRoot, "later.txt", []byte("later\n"))
		runTaskWorktreeGitTest(t, repositoryRoot, "add", "later.txt")
		runTaskWorktreeGitTest(t, repositoryRoot, "-c", "user.name=Chora Test", "-c", "user.email=chora@example.test", "commit", "-m", "drift")
		assertResourceFingerprintError(t, fixture.root, resource, "cd "+resource.RepoID+" && npm test", "HEAD differs")
	})

	t.Run("noncanonical Task root", func(t *testing.T) {
		fixture := newResourcePatchFixture(t, resourcePatchConfig{role: "write"})
		resource := fingerprintExecutionResources(fixture.snapshot)[0]
		resource.Checks = fingerprintNamedChecks(".")
		alias := filepath.Join(canonicalTempDir(t), "task-root-alias")
		if err := os.Symlink(fixture.root, alias); err != nil {
			t.Fatal(err)
		}
		assertResourceFingerprintError(t, alias, resource, "cd "+resource.RepoID+" && npm test", "Task root cannot be proven")
	})

	t.Run("symlinked absolute working directory", func(t *testing.T) {
		fixture := newResourcePatchFixture(t, resourcePatchConfig{role: "write"})
		resource := fingerprintExecutionResources(fixture.snapshot)[0]
		repositoryRoot := filepath.Join(fixture.root, resource.Locator)
		writeResourcePatchFile(t, repositoryRoot, "tools/marker.txt", []byte("committed\n"))
		if err := os.Symlink("tools", filepath.Join(repositoryRoot, "linked-tools")); err != nil {
			t.Fatal(err)
		}
		runTaskWorktreeGitTest(t, repositoryRoot, "add", "tools", "linked-tools")
		runTaskWorktreeGitTest(t, repositoryRoot, "-c", "user.name=Chora Test", "-c", "user.email=chora@example.test", "commit", "-m", "symlink fixture")
		resource.BaseCommit = strings.TrimSpace(runTaskWorktreeGitTest(t, repositoryRoot, "rev-parse", "HEAD"))
		resource.BaseTree = strings.TrimSpace(runTaskWorktreeGitTest(t, repositoryRoot, "rev-parse", "HEAD^{tree}"))
		resource.Checks = fingerprintNamedChecks("linked-tools")
		command := "cd " + filepath.Join(repositoryRoot, "linked-tools") + " && npm test"
		assertResourceFingerprintError(t, fixture.root, resource, command, "working directory cannot be proven")
	})
}

func TestResourceCheckFingerprintEnforcesReviewTextLimit(t *testing.T) {
	fixture := newResourcePatchFixture(t, resourcePatchConfig{role: "write"})
	resource := fingerprintExecutionResources(fixture.snapshot)[0]
	resource.Checks = fingerprintNamedChecks(".")
	path := filepath.Join(fixture.root, resource.RepoID, "huge.txt")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(int64(domain.RepositoryTextBytes + 1)); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	assertResourceFingerprintError(t, fixture.root, resource, "cd "+resource.RepoID+" && npm test", "supported review size")
}

func fingerprintExecutionResources(snapshot domain.TaskResourceSnapshot) []speccoding.ExecutionRepositoryResource {
	resources := make([]speccoding.ExecutionRepositoryResource, 0, len(snapshot.Resources))
	for _, resource := range snapshot.Resources {
		resources = append(resources, speccoding.ExecutionRepositoryResource{
			RepoID: resource.RepoID, Name: resource.Name, Locator: resource.RepoID, TargetIdentityDigest: resource.PhysicalIdentity,
			Role: resource.Role, BaseCommit: resource.BaseCommit, BaseTree: resource.BaseTree, BaseRef: resource.BaseRef,
			Scope: resource.Scope, Checks: resource.Checks,
		})
	}
	return resources
}

func fingerprintNamedChecks(workingDirectory string) domain.TaskCheckPolicy {
	return domain.TaskCheckPolicy{Mode: "named", SelectionSource: "user", Commands: []domain.TaskCheckCommand{{
		ID: "test", Name: "Tests", Command: "npm test", Argv: []string{"npm", "test"}, WorkingDirectory: workingDirectory, Source: "user",
	}}}
}

func commitFingerprintWorkingDirectory(t *testing.T, taskRoot string, resource *speccoding.ExecutionRepositoryResource, relative string) {
	t.Helper()
	repositoryRoot := filepath.Join(taskRoot, resource.RepoID)
	writeResourcePatchFile(t, repositoryRoot, filepath.Join(relative, "marker.txt"), []byte("committed\n"))
	runTaskWorktreeGitTest(t, repositoryRoot, "add", relative)
	runTaskWorktreeGitTest(t, repositoryRoot, "-c", "user.name=Chora Test", "-c", "user.email=chora@example.test", "commit", "-m", "nested check directory")
	resource.BaseCommit = strings.TrimSpace(runTaskWorktreeGitTest(t, repositoryRoot, "rev-parse", "HEAD"))
	resource.BaseTree = strings.TrimSpace(runTaskWorktreeGitTest(t, repositoryRoot, "rev-parse", "HEAD^{tree}"))
}

func runResourceFingerprintTest(t *testing.T, request resourceFingerprintRequest) resourceFingerprintResponse {
	t.Helper()
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := RunResourceCheckFingerprint(context.Background(), bytes.NewReader(raw), &output); err != nil {
		t.Fatal(err)
	}
	var response resourceFingerprintResponse
	decoder := json.NewDecoder(&output)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&response); err != nil {
		t.Fatal(err)
	}
	return response
}

func assertResourceFingerprintError(t *testing.T, taskRoot string, resource speccoding.ExecutionRepositoryResource, command, want string) {
	t.Helper()
	raw, err := json.Marshal(resourceFingerprintRequest{
		Config:  resourceFingerprintConfig{Schema: resourceObserverConfigSchema, AttemptID: domain.NewAttemptID().String(), TaskRoot: taskRoot, Resources: []speccoding.ExecutionRepositoryResource{resource}},
		Command: command,
	})
	if err != nil {
		t.Fatal(err)
	}
	err = RunResourceCheckFingerprint(context.Background(), bytes.NewReader(raw), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("fingerprint error=%v want %q", err, want)
	}
}

func TestResourceCheckFingerprintShortLocatorRetainsFullRepositoryID(t *testing.T) {
	fixture := newResourcePatchFixture(t, resourcePatchConfig{role: "write"})
	resource := fingerprintExecutionResources(fixture.snapshot)[0]
	resource.Locator = domain.ShortWorkspaceName(resource.RepoID)
	resource.Checks = fingerprintNamedChecks(".")
	originalRoot := filepath.Join(fixture.root, resource.RepoID)
	shortRoot := filepath.Join(fixture.root, resource.Locator)
	runTaskWorktreeGitTest(t, originalRoot, "worktree", "move", originalRoot, shortRoot)
	writeResourcePatchFile(t, shortRoot, "README.md", []byte("short path change\n"))
	response := runResourceFingerprintTest(t, resourceFingerprintRequest{
		Config:  resourceFingerprintConfig{Schema: resourceObserverConfigSchema, AttemptID: domain.NewAttemptID().String(), TaskRoot: fixture.root, Resources: []speccoding.ExecutionRepositoryResource{resource}},
		Command: "cd " + resource.Locator + " && npm test",
	})
	if response.RepoID != resource.RepoID || response.Fingerprint == "" {
		t.Fatalf("lost resource identity: %+v", response)
	}
	commandJSON, _ := json.Marshal(struct {
		Argv []string `json:"argv"`
		Cwd  string   `json:"cwd"`
	}{[]string{"npm", "test"}, resource.Locator})
	digest := sha256.Sum256(commandJSON)
	if response.CommandDigest != hex.EncodeToString(digest[:]) {
		t.Fatal("command digest used ID instead of actual locator")
	}
	assertResourceFingerprintError(t, fixture.root, resource, "cd "+resource.RepoID+" && npm test", "outside frozen check authority")
	absolute := runResourceFingerprintTest(t, resourceFingerprintRequest{
		Config:  resourceFingerprintConfig{Schema: resourceObserverConfigSchema, AttemptID: domain.NewAttemptID().String(), TaskRoot: fixture.root, Resources: []speccoding.ExecutionRepositoryResource{resource}},
		Command: "cd " + shortRoot + " && npm test",
	})
	if absolute.RepoID != resource.RepoID || absolute.CommandDigest != response.CommandDigest {
		t.Fatalf("absolute short locator lost authority: relative=%+v absolute=%+v", response, absolute)
	}
}

func TestResourceCheckFingerprintProvesAttachedTaskBranchAuthority(t *testing.T) {
	t.Run("valid Task branch", func(t *testing.T) {
		fixture := newResourcePatchFixture(t, resourcePatchConfig{role: "write"})
		resource := fingerprintExecutionResources(fixture.snapshot)[0]
		resource.Checks = fingerprintNamedChecks(".")
		taskRoot, repositoryRoot := attachResourceFingerprintTaskBranch(t, fixture, &resource)
		writeResourcePatchFile(t, repositoryRoot, "README.md", []byte("Task branch change\n"))
		response := runResourceFingerprintTest(t, resourceFingerprintRequest{
			Config:  resourceFingerprintConfig{Schema: resourceObserverConfigSchema, AttemptID: domain.NewAttemptID().String(), TaskRoot: taskRoot, Resources: []speccoding.ExecutionRepositoryResource{resource}},
			Command: "cd " + repositoryRoot + " && npm test",
		})
		if response.RepoID != resource.RepoID || response.Fingerprint == "" {
			t.Fatalf("Task branch response=%+v", response)
		}
	})

	t.Run("wrong branch", func(t *testing.T) {
		fixture := newResourcePatchFixture(t, resourcePatchConfig{role: "write"})
		resource := fingerprintExecutionResources(fixture.snapshot)[0]
		resource.Checks = fingerprintNamedChecks(".")
		taskRoot, repositoryRoot := attachResourceFingerprintTaskBranch(t, fixture, &resource)
		runTaskWorktreeGitTest(t, repositoryRoot, "switch", "-c", "feature/000000000000")
		assertResourceFingerprintError(t, taskRoot, resource, "cd "+repositoryRoot+" && npm test", "Task branch")
	})

	t.Run("reference remains detached", func(t *testing.T) {
		fixture := newResourcePatchFixture(t, resourcePatchConfig{role: "write"})
		resource := fingerprintExecutionResources(fixture.snapshot)[0]
		resource.Checks = fingerprintNamedChecks(".")
		taskRoot, repositoryRoot := attachResourceFingerprintTaskBranch(t, fixture, &resource)
		resource.Role = "reference"
		assertResourceFingerprintError(t, taskRoot, resource, "cd "+repositoryRoot+" && npm test", "reference repository HEAD is not detached")
	})
}

func attachResourceFingerprintTaskBranch(t *testing.T, fixture *resourcePatchFixture, resource *speccoding.ExecutionRepositoryResource) (string, string) {
	t.Helper()
	unbound := fixture.snapshot
	unbound.TaskID = ""
	unbound.BranchType = ""
	for index := range unbound.Resources {
		if unbound.Resources[index].Role == "write" {
			unbound.Resources[index].BaseRef = "refs/heads/main"
		}
		if unbound.Resources[index].RepoID == resource.RepoID {
			resource.BaseRef = unbound.Resources[index].BaseRef
		}
	}
	bound, err := domain.BindTaskResourceBranches(unbound, fixture.task.ID(), "feature")
	if err != nil {
		t.Fatal(err)
	}
	var taskBranch string
	for _, candidate := range bound.Resources {
		if candidate.RepoID == resource.RepoID {
			taskBranch = candidate.TaskBranch
			resource.Locator = candidate.WorkspaceDirectory()
		}
	}
	if taskBranch == "" {
		t.Fatal("missing Task branch")
	}
	dataRoot := filepath.Dir(filepath.Dir(fixture.root))
	rootDigest := sha256.Sum256([]byte(taskResourceWorkspaceFingerprintVersion + "\x00" + dataRoot))
	marker := "chora-task-workspace:" + hex.EncodeToString(rootDigest[:]) + ":" + fixture.task.ID().String() + ":" + resource.RepoID
	originalRoot := filepath.Join(fixture.root, resource.RepoID)
	runTaskWorktreeGitTest(t, originalRoot, "update-ref", "--create-reflog", "-m", marker, "refs/heads/"+taskBranch, resource.BaseCommit, strings.Repeat("0", 40))
	runTaskWorktreeGitTest(t, originalRoot, "switch", taskBranch)
	taskRoot := filepath.Join(filepath.Dir(fixture.root), domain.ShortWorkspaceName(fixture.task.ID().String()))
	if err := os.MkdirAll(taskRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	repositoryRoot := filepath.Join(taskRoot, resource.Locator)
	runTaskWorktreeGitTest(t, originalRoot, "worktree", "move", originalRoot, repositoryRoot)
	return taskRoot, repositoryRoot
}

func TestIsolatedCopyFingerprintMatchesTaskWorktreeAndRejectsImplicitLayout(t *testing.T) {
	fixture := newResourcePatchFixture(t, resourcePatchConfig{role: "write"})
	resource := fingerprintExecutionResources(fixture.snapshot)[0]
	resource.Checks = fingerprintNamedChecks(".")
	writeResourcePatchFile(t, filepath.Join(fixture.root, resource.Locator), "README.md", []byte("isolated edit\n"))
	dest, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := isolatedworkspace.Prepare(context.Background(), fixture.root, dest, state, fixture.snapshot.Resources); err != nil {
		t.Fatal(err)
	}
	request := resourceFingerprintRequest{Config: resourceFingerprintConfig{Schema: resourceObserverConfigSchema, AttemptID: domain.NewAttemptID().String(), TaskRoot: dest, Resources: []speccoding.ExecutionRepositoryResource{resource}}, Command: "cd " + resource.Locator + " && npm test"}
	if _, err := resourceCheckFingerprint(context.Background(), request); err == nil {
		t.Fatal("standalone repository admitted without explicit isolated layout")
	}
	request.Config.RepositoryLayout = "isolated_copy"
	isolated, err := resourceCheckFingerprint(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	request.Config.RepositoryLayout = ""
	request.Config.TaskRoot = fixture.root
	host, err := resourceCheckFingerprint(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if isolated.Fingerprint != host.Fingerprint || isolated.CommandDigest != host.CommandDigest {
		t.Fatalf("isolated proof differs from writeback worktree: isolated=%+v host=%+v", isolated, host)
	}
}
