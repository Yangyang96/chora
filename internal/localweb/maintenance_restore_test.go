package localweb

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/gitsource"
	"github.com/Yangyang96/chora/internal/pidiscovery"
	storesqlite "github.com/Yangyang96/chora/internal/store/sqlite"
)

func TestStoppedWholeDataRootBackupRestoresMultiRepositoryTaskAtSamePath(t *testing.T) {
	fixtureParent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dataRoot := filepath.Join(fixtureParent, "data")
	backupRoot := filepath.Join(fixtureParent, "backup")
	if err := os.MkdirAll(dataRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	server := newMaintenanceRestoreServer(t, dataRoot)
	handler := server.Handler()
	requestJSONWithHeaders(t, handler, http.MethodPost, "/api/agent-execution/trusted-local-acknowledgements",
		map[string]any{"policyVersion": domain.TrustedLocalDisclosurePolicy},
		map[string]string{"Idempotency-Key": "maintenance-restore-ack"}, http.StatusOK, nil)

	var project resourceProjectTestView
	requestJSON(t, handler, http.MethodPost, "/api/v2/projects", map[string]any{"name": "Restore qualification"}, http.StatusCreated, &project)
	type repositoryFixture struct {
		root   string
		view   repositoryResourceView
		head   string
		status string
	}
	repositories := make([]repositoryFixture, 0, 2)
	for _, name := range []string{"alpha", "beta"} {
		root, _, _ := newTaskWorktreeTestRepository(t)
		if err := os.WriteFile(filepath.Join(root, "same.txt"), []byte(name+" committed\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		runTaskWorktreeGitTest(t, root, "add", "same.txt")
		runTaskWorktreeGitTest(t, root, "-c", "user.name=Chora Restore Test", "-c", "user.email=restore@example.test", "commit", "-m", name)
		if err := os.WriteFile(filepath.Join(root, "user-dirt.txt"), []byte(name+" user dirt\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		var admitted struct {
			Repository repositoryResourceView `json:"repository"`
		}
		requestJSON(t, handler, http.MethodPost, "/api/v2/projects/"+project.ID+"/repositories", map[string]any{"locator": root}, http.StatusOK, &admitted)
		repositories = append(repositories, repositoryFixture{root: root, view: admitted.Repository, head: strings.TrimSpace(runTaskWorktreeGitTest(t, root, "rev-parse", "HEAD")), status: runTaskWorktreeGitTest(t, root, "status", "--porcelain=v1")})
	}

	roomID, err := domain.ParseRoomID(project.DefaultRoomID)
	if err != nil {
		t.Fatal(err)
	}
	revisions, err := server.store.Reader().ListRoomRevisions(context.Background(), roomID)
	if err != nil {
		t.Fatal(err)
	}
	revisionIDs := make([]string, 0, len(revisions))
	for _, revision := range revisions {
		revisionIDs = append(revisionIDs, revision.ID().String())
	}
	selections := []taskResourceSelection{
		{RepoID: repositories[0].view.RepoID, AssociationVersion: repositories[0].view.Version, Role: "write", TargetRef: taskResourceTestTargetRef(t, repositories[0].root), Scope: domain.TaskRepositoryScope{Mode: "repository", MigrationChoice: "not_needed"}, Checks: domain.TaskCheckPolicy{Mode: "auto", SelectionSource: "user"}},
		{RepoID: repositories[1].view.RepoID, AssociationVersion: repositories[1].view.Version, Role: "reference", Scope: domain.TaskRepositoryScope{Mode: "restricted", MigrationChoice: "not_needed"}, Checks: domain.TaskCheckPolicy{Mode: "auto", SelectionSource: "user"}},
	}
	var task taskRefView
	requestJSONWithHeaders(t, handler, http.MethodPost, "/api/v2/rooms/"+project.DefaultRoomID+"/tasks", map[string]any{
		"title": "Restore two repositories", "requirement": "Retain both repositories and their Task workspace.",
		"agentExecutionProfile": string(domain.AgentExecutionProfileTrustedLocal), "resources": selections, "revisionIds": revisionIDs,
	}, map[string]string{"Idempotency-Key": "maintenance-restore-task"}, http.StatusCreated, &task)
	taskID, err := domain.ParseTaskID(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	workspaceManager, err := newTaskResourceWorkspaceManager(server.store, dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := workspaceManager.Ensure(context.Background(), taskID); err != nil {
		t.Fatalf("prepare multi-repository Task workspace: %v", err)
	}
	taskRoot := waitForMaintenanceTaskRoot(t, server, project.ID, taskID)
	if task.ResourceSnapshot == nil || len(task.ResourceSnapshot.Resources) != 2 {
		t.Fatalf("created Task omitted resource snapshot: %#v", task)
	}
	firstWorkspace := task.ResourceSnapshot.Resources[0].WorkspaceDirectory()
	if err := os.WriteFile(filepath.Join(taskRoot, firstWorkspace, "task-dirt.txt"), []byte("retained task dirt\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for path, body := range map[string]string{
		"pi-sessions/session.json":              `{"session":"retained"}\n`,
		"runtime/resource-patches/result.patch": "retained patch\n",
		"pi/installation.marker":                "retained installation\n",
		"projects/legacy-partial-apply.marker":  "legacy partial Apply evidence retained, not replayed\n",
	} {
		full := filepath.Join(dataRoot, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// This is the qualification's shutdown boundary: no database handle or
	// background workspace preparation remains while tar reads the data root.
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	before := maintenanceTreeDigest(t, dataRoot)
	if err := os.MkdirAll(backupRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(backupRoot, "data-root.tar")
	runMaintenanceCommand(t, "/usr/bin/tar", "-C", fixtureParent, "-cpf", archive, filepath.Base(dataRoot))
	archiveBytes, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	archiveDigest := sha256.Sum256(archiveBytes)
	if archiveDigest == ([32]byte{}) {
		t.Fatal("empty archive digest")
	}
	hold := dataRoot + ".before-restore"
	if err := os.Rename(dataRoot, hold); err != nil {
		t.Fatal(err)
	}
	runMaintenanceCommand(t, "/usr/bin/tar", "-C", fixtureParent, "-xpf", archive)
	if got := maintenanceTreeDigest(t, dataRoot); got != before {
		t.Fatalf("restored whole data root digest=%x want=%x", got, before)
	}

	reopened := newMaintenanceRestoreServer(t, dataRoot)
	t.Cleanup(func() { _ = reopened.Close() })
	projectID, err := domain.ParseProjectID(project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if restoredProject, lookupErr := reopened.store.Reader().GetProject(context.Background(), projectID); lookupErr != nil || restoredProject.Name() != "Restore qualification" {
		t.Fatalf("restored Project=%#v err=%v", restoredProject, lookupErr)
	}
	resourceSnapshot, err := reopened.store.Reader().GetTaskResourceSnapshot(context.Background(), taskID)
	var restoredResources domain.TaskResourceSnapshot
	if err == nil {
		err = json.Unmarshal(resourceSnapshot.CanonicalJSON, &restoredResources)
	}
	if err != nil || len(restoredResources.Resources) != 2 || restoredResources.Resources[0].RepoID != repositories[0].view.RepoID || restoredResources.Resources[1].RepoID != repositories[1].view.RepoID {
		t.Fatalf("restored resource snapshot=%#v err=%v", resourceSnapshot, err)
	}
	worktrees, err := reopened.store.Reader().ListTaskRepositoryWorktrees(context.Background(), taskID)
	if err != nil || len(worktrees) != 2 {
		t.Fatalf("restored worktrees=%#v err=%v", worktrees, err)
	}
	if body, err := os.ReadFile(filepath.Join(taskRoot, firstWorkspace, "task-dirt.txt")); err != nil || string(body) != "retained task dirt\n" {
		t.Fatalf("restored Task dirt=%q err=%v", body, err)
	}
	for _, path := range []string{"pi-sessions/session.json", "runtime/resource-patches/result.patch", "pi/installation.marker", "projects/legacy-partial-apply.marker"} {
		if _, err := os.Stat(filepath.Join(dataRoot, filepath.FromSlash(path))); err != nil {
			t.Fatalf("restored component %s: %v", path, err)
		}
	}
	for _, repository := range repositories {
		if got := strings.TrimSpace(runTaskWorktreeGitTest(t, repository.root, "rev-parse", "HEAD")); got != repository.head {
			t.Fatalf("original repository HEAD changed: got %q want %q", got, repository.head)
		}
		if got := runTaskWorktreeGitTest(t, repository.root, "status", "--porcelain=v1"); got != repository.status {
			t.Fatalf("original repository dirt changed: got %q want %q", got, repository.status)
		}
	}
	if _, err := os.Stat(hold); err != nil {
		t.Fatalf("reversible pre-restore hold missing: %v", err)
	}
}

func newMaintenanceRestoreServer(t *testing.T, dataRoot string) *Server {
	t.Helper()
	db, err := storesqlite.Open(context.Background(), filepath.Join(dataRoot, "chora.db"))
	if err != nil {
		t.Fatal(err)
	}
	workspaces, err := newTaskResourceWorkspaceManager(db, dataRoot)
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	lifecycle, cancel := context.WithCancel(context.Background())
	source := gitsource.Default{}
	service := app.NewService(app.Dependencies{RoomCreationMode: app.RoomCreationProjectOnly, Lifecycle: lifecycle, Store: db, Context: app.ContextAssembler{}, Authorizer: localAuthorizer{}, IDs: app.RandomIDs{}, TaskResourceWorkspaces: workspaces, RepositorySource: source, DataRoot: dataRoot,
		SpecCodingEnvelopeResolver: newBoundSpecCodingEnvelopeResolver(db.Reader(), nil, pidiscovery.Result{State: pidiscovery.StateReady, Version: "0.84.2"})})
	return &Server{ctx: lifecycle, cancel: cancel, store: db, service: service, repositorySource: source, pathPiEnabled: true, taskWorktreesReady: true, logger: log.New(io.Discard, "", 0)}
}

func waitForMaintenanceTaskRoot(t *testing.T, server *Server, projectID string, taskID domain.TaskID) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		root, err := server.continuityDirectory(context.Background(), projectID, taskID.String())
		if err == nil && root != "" {
			return root
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("Task workspace did not become ready")
	return ""
}

func runMaintenanceCommand(t *testing.T, name string, args ...string) {
	t.Helper()
	if output, err := exec.Command(name, args...).CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v: %s", name, args, err, output)
	}
}

func maintenanceTreeDigest(t *testing.T, root string) [32]byte {
	t.Helper()
	entries := []string{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		entry := fmt.Sprintf("%s\x00%s\x00%o", relative, info.Mode().Type(), info.Mode().Perm())
		if info.Mode().IsRegular() {
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			entry += fmt.Sprintf("\x00%x", sha256.Sum256(body))
		} else if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			entry += "\x00" + target
		}
		entries = append(entries, entry)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(entries)
	return sha256.Sum256([]byte(strings.Join(entries, "\n")))
}
