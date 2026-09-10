package localweb

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/gitsource"
	storecontract "github.com/Yangyang96/chora/internal/store"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFreezeTaskResourcesRejectsCommittedCheckDirectoryTraversal(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, string)
		apply func(*domain.TaskCheckPolicy)
		want  string
	}{
		{
			name: "named check through committed symlink shadowed by directory",
			setup: func(t *testing.T, root string) {
				if err := os.Symlink("../outside", filepath.Join(root, "tools")); err != nil {
					t.Fatal(err)
				}
				commitTaskResourcePaths(t, root, "committed symlink", "tools")
				if err := os.Remove(filepath.Join(root, "tools")); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(filepath.Join(root, "tools"), 0700); err != nil {
					t.Fatal(err)
				}
			},
			apply: func(policy *domain.TaskCheckPolicy) {
				policy.Mode = "named"
				policy.Commands = []domain.TaskCheckCommand{{ID: "custom", Name: "Nested tests", Command: "npm test", WorkingDirectory: "tools"}}
			},
			want: "symlink, submodule",
		},
		{
			name: "preparation through committed symlink shadowed by directory",
			setup: func(t *testing.T, root string) {
				if err := os.Symlink("../outside", filepath.Join(root, "tools")); err != nil {
					t.Fatal(err)
				}
				commitTaskResourcePaths(t, root, "committed symlink", "tools")
				if err := os.Remove(filepath.Join(root, "tools")); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(filepath.Join(root, "tools"), 0700); err != nil {
					t.Fatal(err)
				}
			},
			apply: func(policy *domain.TaskCheckPolicy) {
				policy.Mode = "none"
				policy.Preparation = []domain.TaskCheckCommand{{ID: "prepare", Name: "Prepare", Command: "node --version", WorkingDirectory: "tools"}}
			},
			want: "symlink, submodule",
		},
		{
			name: "named check through committed gitlink shadowed by directory",
			setup: func(t *testing.T, root string) {
				head := strings.TrimSpace(runTaskWorktreeGitTest(t, root, "rev-parse", "HEAD"))
				runTaskWorktreeGitTest(t, root, "update-index", "--add", "--cacheinfo", "160000,"+head+",vendor")
				runTaskWorktreeGitTest(t, root, "commit", "-m", "committed gitlink")
				if err := os.Mkdir(filepath.Join(root, "vendor"), 0700); err != nil {
					t.Fatal(err)
				}
			},
			apply: func(policy *domain.TaskCheckPolicy) {
				policy.Mode = "named"
				policy.Commands = []domain.TaskCheckCommand{{ID: "custom", Name: "Nested tests", Command: "npm test", WorkingDirectory: "vendor"}}
			},
			want: "symlink, submodule",
		},
		{
			name: "ordinary committed nested directory",
			setup: func(t *testing.T, root string) {
				if err := os.Mkdir(filepath.Join(root, "tools"), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, "tools", "marker.txt"), []byte("ordinary\n"), 0600); err != nil {
					t.Fatal(err)
				}
				large := filepath.Join(root, "unchanged-large.bin")
				file, err := os.Create(large)
				if err != nil {
					t.Fatal(err)
				}
				if err = file.Truncate(129 * 1024 * 1024); err != nil {
					_ = file.Close()
					t.Fatal(err)
				}
				if err = file.Close(); err != nil {
					t.Fatal(err)
				}
				commitTaskResourcePaths(t, root, "nested directory and unchanged large binary", "tools/marker.txt", "unchanged-large.bin")
			},
			apply: func(policy *domain.TaskCheckPolicy) {
				policy.Mode = "named"
				policy.Commands = []domain.TaskCheckCommand{{ID: "custom", Name: "Nested tests", Command: "npm test", WorkingDirectory: "tools"}}
				policy.Preparation = []domain.TaskCheckCommand{{ID: "prepare", Name: "Prepare", Command: "node --version", WorkingDirectory: "tools"}}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server, room, selection, root, closeStore := newTaskResourcePathValidationFixture(t)
			defer closeStore()
			test.setup(t, root)
			// Admission happened before setup, so refresh the repository's pinned
			// observation while retaining the same physical identity.
			test.apply(&selection.Checks)
			snapshot, err := server.freezeTaskResources(context.Background(), room, []taskResourceSelection{selection})
			if test.want != "" {
				if err == nil || !strings.Contains(err.Error(), test.want) {
					t.Fatalf("freeze error = %v, want %q", err, test.want)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(snapshot.Resources) != 1 || snapshot.Resources[0].Checks.Commands[0].WorkingDirectory != "tools" || snapshot.Resources[0].Checks.Preparation[0].WorkingDirectory != "tools" {
				t.Fatalf("valid nested check directories not frozen: %+v", snapshot.Resources)
			}
		})
	}
}

func TestFreezeTaskResourcesValidatesRestrictedScopeAtPinnedRevision(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, string)
		scope domain.TaskRepositoryScope
		want  string
	}{
		{
			name: "writable directory through committed symlink shadowed by directory",
			setup: func(t *testing.T, root string) {
				if err := os.Symlink("../outside", filepath.Join(root, "linked")); err != nil {
					t.Fatal(err)
				}
				commitTaskResourcePaths(t, root, "committed scope symlink", "linked")
				if err := os.Remove(filepath.Join(root, "linked")); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(filepath.Join(root, "linked"), 0700); err != nil {
					t.Fatal(err)
				}
			},
			scope: domain.TaskRepositoryScope{Mode: "restricted", WritableDirectories: []string{"linked"}, MigrationChoice: "not_needed"},
			want:  "symlink, submodule",
		},
		{
			name: "protected directory through committed gitlink shadowed by directory",
			setup: func(t *testing.T, root string) {
				head := strings.TrimSpace(runTaskWorktreeGitTest(t, root, "rev-parse", "HEAD"))
				runTaskWorktreeGitTest(t, root, "update-index", "--add", "--cacheinfo", "160000,"+head+",vendor")
				runTaskWorktreeGitTest(t, root, "commit", "-m", "committed scope gitlink")
				if err := os.Mkdir(filepath.Join(root, "vendor"), 0700); err != nil {
					t.Fatal(err)
				}
			},
			scope: domain.TaskRepositoryScope{Mode: "restricted", WritableFiles: []string{"README.md"}, ProtectedDirectories: []string{"vendor"}, MigrationChoice: "not_needed"},
			want:  "symlink, submodule",
		},
		{
			name: "new paths below ordinary committed directory",
			setup: func(t *testing.T, root string) {
				if err := os.Mkdir(filepath.Join(root, "src"), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, "src", "marker.txt"), []byte("ordinary\n"), 0600); err != nil {
					t.Fatal(err)
				}
				commitTaskResourcePaths(t, root, "ordinary scope parent", "src/marker.txt")
			},
			scope: domain.TaskRepositoryScope{
				Mode: "restricted", WritableFiles: []string{"src/new-file.txt"}, WritableDirectories: []string{"src/new-dir"},
				ProtectedDirectories: []string{"src/new-protected"}, MigrationChoice: "not_needed",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server, room, selection, root, closeStore := newTaskResourcePathValidationFixture(t)
			defer closeStore()
			test.setup(t, root)
			selection.Scope = test.scope
			snapshot, err := server.freezeTaskResources(context.Background(), room, []taskResourceSelection{selection})
			if test.want != "" {
				if err == nil || !strings.Contains(err.Error(), test.want) {
					t.Fatalf("freeze error = %v, want %q", err, test.want)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(snapshot.Resources) != 1 || snapshot.Resources[0].Scope.Mode != "restricted" || len(snapshot.Resources[0].Scope.WritableFiles) != 1 || len(snapshot.Resources[0].Scope.WritableDirectories) != 1 || len(snapshot.Resources[0].Scope.ProtectedDirectories) != 1 {
				t.Fatalf("valid restricted scope not frozen: %+v", snapshot.Resources)
			}
		})
	}
}

func newTaskResourcePathValidationFixture(t *testing.T) (*Server, domain.Room, taskResourceSelection, string, func()) {
	t.Helper()
	db := openTaskWorktreeResolverStore(t)
	source := gitsource.Default{}
	data, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	manager, err := newTaskResourceWorkspaceManager(db, data)
	if err != nil {
		t.Fatal(err)
	}
	service := app.NewService(app.Dependencies{Store: db, DataRoot: data, RepositorySource: source, Authorizer: localAuthorizer{}, TaskResourceWorkspaces: manager, SpecCodingEnvelopeResolver: &boundSpecCodingEnvelopeResolver{reader: db.Reader(), piVersion: "0.84.2"}})
	server := &Server{store: db, service: service, repositorySource: source, pathPiEnabled: true}
	root, _, _ := newTaskWorktreeTestRepository(t)
	runTaskWorktreeGitTest(t, root, "config", "user.name", "Chora Test")
	runTaskWorktreeGitTest(t, root, "config", "user.email", "chora@example.test")
	var project resourceProjectTestView
	requestJSON(t, server.Handler(), http.MethodPost, "/api/v2/projects", map[string]any{"name": "Check path validation"}, http.StatusCreated, &project)
	var added struct {
		Repository repositoryResourceView `json:"repository"`
	}
	requestJSON(t, server.Handler(), http.MethodPost, "/api/v2/projects/"+project.ID+"/repositories", map[string]any{"locator": root}, http.StatusOK, &added)
	roomID, err := domain.ParseRoomID(project.DefaultRoomID)
	if err != nil {
		t.Fatal(err)
	}
	room, err := db.Reader().GetRoom(context.Background(), roomID)
	if err != nil {
		t.Fatal(err)
	}
	selection := taskResourceSelection{
		RepoID: added.Repository.RepoID, AssociationVersion: added.Repository.Version, Role: "write",
		TargetRef: taskResourceTestTargetRef(t, root),
		Scope:     domain.TaskRepositoryScope{Mode: "repository", MigrationChoice: "not_needed"},
		Checks:    domain.TaskCheckPolicy{Mode: "none", SelectionSource: "user"},
	}
	return server, room, selection, root, func() { _ = db.Close() }
}

func commitTaskResourcePaths(t *testing.T, root, message string, paths ...string) {
	t.Helper()
	args := append([]string{"add", "--"}, paths...)
	runTaskWorktreeGitTest(t, root, args...)
	runTaskWorktreeGitTest(t, root, "commit", "-m", message)
}

func TestTaskFirstTaskFreezesMultipleRepositoriesAndPreparesPinnedContent(t *testing.T) {
	db := openTaskWorktreeResolverStore(t)
	defer db.Close()
	source := gitsource.Default{}
	data, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	manager, err := newTaskResourceWorkspaceManager(db, data)
	if err != nil {
		t.Fatal(err)
	}
	service := app.NewService(app.Dependencies{Store: db, DataRoot: data, RepositorySource: source, Authorizer: localAuthorizer{}, TaskResourceWorkspaces: manager, SpecCodingEnvelopeResolver: &boundSpecCodingEnvelopeResolver{reader: db.Reader(), piVersion: "0.84.2"}})
	server := &Server{store: db, service: service, repositorySource: source, pathPiEnabled: true}
	handler := server.Handler()
	var project resourceProjectTestView
	requestJSON(t, handler, http.MethodPost, "/api/v2/projects", map[string]any{"name": "Two repos"}, 201, &project)
	selections := []taskResourceSelection{}
	for range 2 {
		root, _, _ := newTaskWorktreeTestRepository(t)
		var added struct {
			Repository repositoryResourceView `json:"repository"`
		}
		requestJSON(t, handler, http.MethodPost, "/api/v2/projects/"+project.ID+"/repositories", map[string]any{"locator": root}, 200, &added)
		if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("original dirty\n"), 0600); err != nil {
			t.Fatal(err)
		}
		selections = append(selections, taskResourceSelection{RepoID: added.Repository.RepoID, AssociationVersion: added.Repository.Version, Role: "write", TargetRef: taskResourceTestTargetRef(t, root), Scope: domain.TaskRepositoryScope{Mode: "repository", MigrationChoice: "not_needed"}, Checks: domain.TaskCheckPolicy{Mode: "auto", SelectionSource: "user"}})
	}
	var options map[string]any
	requestJSON(t, handler, http.MethodGet, "/api/v2/rooms/"+project.DefaultRoomID+"/task-options", nil, 200, &options)
	if len(options["selectedRepoIds"].([]any)) != 0 {
		t.Fatal("silently selected all repos")
	}
	requestJSONWithHeaders(t, handler, http.MethodPost, "/api/agent-execution/trusted-local-acknowledgements", map[string]any{"policyVersion": domain.TrustedLocalDisclosurePolicy}, map[string]string{"Idempotency-Key": "resource-ack"}, http.StatusOK, nil)
	roomID, _ := domain.ParseRoomID(project.DefaultRoomID)
	revisions, err := db.Reader().ListRoomRevisions(context.Background(), roomID)
	if err != nil {
		t.Fatal(err)
	}
	revisionIDs := []string{}
	for _, r := range revisions {
		revisionIDs = append(revisionIDs, r.ID().String())
	}
	var created taskRefView
	requestJSON(t, handler, http.MethodPost, "/api/v2/rooms/"+project.DefaultRoomID+"/tasks", map[string]any{"requirement": "Update README in both repositories", "agentExecutionProfile": "trusted_local", "resources": selections, "revisionIds": revisionIDs}, 201, &created)
	if created.ResourceSnapshot == nil || len(created.ResourceSnapshot.Resources) != 2 {
		t.Fatalf("snapshot missing: %+v", created)
	}
	id, err := domain.ParseTaskID(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := db.Reader().GetTaskResourceSnapshot(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Ensure(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	root, err := manager.ResolveExecutionRoot(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot domain.TaskResourceSnapshot
	if err := json.Unmarshal(persisted.CanonicalJSON, &snapshot); err != nil {
		t.Fatal(err)
	}
	for _, r := range snapshot.Resources {
		ref, err := currentTaskRef(context.Background(), r.Checkout)
		if err != nil || r.BaseRef != ref {
			t.Fatalf("frozen Apply ref %q differs from exact current ref %q: %v", r.BaseRef, ref, err)
		}

		content, err := os.ReadFile(filepath.Join(root, r.WorkspaceDirectory(), "README.md"))
		if err != nil {
			t.Fatal(err)
		}
		if string(content) == "original dirty\n" {
			t.Fatal("copied uncommitted content")
		}
		original, _ := os.ReadFile(filepath.Join(r.Checkout, "README.md"))
		if string(original) != "original dirty\n" {
			t.Fatal("original changed")
		}
	}
}

func TestTaskResourceOptionsResolveLegacyBindingBeyondFirstRepositoryPage(t *testing.T) {
	db := openTaskWorktreeResolverStore(t)
	defer db.Close()
	source := gitsource.Default{}
	data, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	service := app.NewService(app.Dependencies{Store: db, DataRoot: data, RepositorySource: source, Authorizer: localAuthorizer{}})
	server := &Server{store: db, service: service, repositorySource: source, pathPiEnabled: true}
	var project resourceProjectTestView
	requestJSON(t, server.Handler(), http.MethodPost, "/api/v2/projects", map[string]any{"name": "Paged legacy binding"}, http.StatusCreated, &project)
	projectID, err := domain.ParseProjectID(project.ID)
	if err != nil {
		t.Fatal(err)
	}
	dummyRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	err = db.WithinWriteTx(context.Background(), func(tx storecontract.WriteTx) error {
		for index := 0; index < domain.RepositoryMaxPageSize; index++ {
			checkout := filepath.Join(dummyRoot, fmt.Sprintf("dummy-%03d", index))
			id, parseErr := domain.ParseRepositoryID(fmt.Sprintf("repo_00000000-0000-7000-8000-%012x", index+1))
			if parseErr != nil {
				return parseErr
			}
			repository := domain.RepositoryRecord{
				ID: id, Name: fmt.Sprintf("dummy-%03d", index), Checkout: checkout,
				CommonGitDir: filepath.Join(checkout, ".git"), PhysicalIdentity: fmt.Sprintf("%064x", index+1), IdentitySource: "inspected", CreatedAt: now,
			}
			if err := tx.InsertRepository(context.Background(), repository); err != nil {
				return err
			}
			association := domain.ProjectRepository{ProjectID: projectID, Repository: repository, State: "active", Version: 1, AddedAt: now, UpdatedAt: now}
			if err := tx.SaveProjectRepository(context.Background(), 0, association); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	root, _, _ := newTaskWorktreeTestRepository(t)
	roomID, err := domain.ParseRoomID(project.DefaultRoomID)
	if err != nil {
		t.Fatal(err)
	}
	commit := strings.TrimSpace(runTaskWorktreeGitTest(t, root, "rev-parse", "HEAD"))
	tree := strings.TrimSpace(runTaskWorktreeGitTest(t, root, "rev-parse", "HEAD^{tree}"))
	binding, err := domain.NewRepositoryBinding(domain.RepositoryBindingParams{
		RoomID: roomID, Name: "legacy-late", LocalLocator: root, SourceKind: domain.RepositorySourceKindOpen,
		AdmittedBase: commit, BaseIdentity: "sha:" + commit + ":tree:" + tree, TargetWorktree: root, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	settings, err := domain.NewProjectSettings(domain.ProjectSettingsParams{ProjectID: projectID, WritableFiles: []string{"README.md"}, NoChecks: true, UpdatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	err = db.WithinWriteTx(context.Background(), func(tx storecontract.WriteTx) error {
		if err := tx.InsertRepositoryBinding(context.Background(), binding); err != nil {
			return err
		}
		return tx.SaveProjectSettingsCAS(context.Background(), 0, settings)
	})
	if err != nil {
		t.Fatal(err)
	}
	var added struct {
		Repository repositoryResourceView `json:"repository"`
	}
	requestJSON(t, server.Handler(), http.MethodPost, "/api/v2/projects/"+project.ID+"/repositories", map[string]any{"locator": root}, http.StatusOK, &added)

	var options struct {
		Repositories         []repositoryResourceView `json:"repositories"`
		NextRepositoryCursor string                   `json:"nextRepositoryCursor"`
		LegacyScope          struct {
			RepoID string `json:"repoId"`
		} `json:"legacyScope"`
	}
	requestJSON(t, server.Handler(), http.MethodGet, "/api/v2/rooms/"+project.DefaultRoomID+"/task-options", nil, http.StatusOK, &options)
	if options.NextRepositoryCursor == "" || options.LegacyScope.RepoID != added.Repository.RepoID {
		t.Fatalf("paged legacy options = cursor %q scope %+v, want repo %s", options.NextRepositoryCursor, options.LegacyScope, added.Repository.RepoID)
	}
	found := false
	for _, repository := range options.Repositories {
		found = found || repository.RepoID == added.Repository.RepoID
	}
	if !found {
		t.Fatalf("late-page legacy repository %s omitted from task options", added.Repository.RepoID)
	}
}

// The qualification fixture is opt-in so the normal regression does not create
// 100,000 files. R5 supplies the retained R0 fixture explicitly and records timing.
func TestTaskFirstLargeRepositoryQualification(t *testing.T) {
	checkout := os.Getenv("CHORA_LARGE_REPOSITORY")
	if checkout == "" {
		t.Skip("R5 qualification requires the retained large repository fixture")
	}
	db := openTaskWorktreeResolverStore(t)
	defer db.Close()
	data, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	manager, err := newTaskResourceWorkspaceManager(db, data)
	if err != nil {
		t.Fatal(err)
	}
	source := gitsource.Default{}
	service := app.NewService(app.Dependencies{Store: db, DataRoot: data, RepositorySource: source, Authorizer: localAuthorizer{}, TaskResourceWorkspaces: manager, SpecCodingEnvelopeResolver: &boundSpecCodingEnvelopeResolver{reader: db.Reader(), piVersion: "0.84.2"}})
	server := &Server{store: db, service: service, repositorySource: source, pathPiEnabled: true}
	handler := server.Handler()
	before := runTaskWorktreeGitTest(t, checkout, "status", "--porcelain")
	var project resourceProjectTestView
	requestJSON(t, handler, http.MethodPost, "/api/v2/projects", map[string]any{"name": "Large qualification"}, 201, &project)
	started := time.Now()
	var added struct {
		Repository repositoryResourceView `json:"repository"`
	}
	requestJSON(t, handler, http.MethodPost, "/api/v2/projects/"+project.ID+"/repositories", map[string]any{"locator": checkout}, 200, &added)
	admission := time.Since(started)
	started = time.Now()
	var entries repositoryEntriesView
	requestJSON(t, handler, http.MethodGet, "/api/v2/projects/"+project.ID+"/repositories/"+added.Repository.RepoID+"/entries?limit=200", nil, 200, &entries)
	query := time.Since(started)
	if len(entries.Entries) > 200 {
		t.Fatal("unbounded directory result")
	}
	requestJSONWithHeaders(t, handler, http.MethodPost, "/api/agent-execution/trusted-local-acknowledgements", map[string]any{"policyVersion": domain.TrustedLocalDisclosurePolicy}, map[string]string{"Idempotency-Key": "large-ack"}, 200, nil)
	roomID, _ := domain.ParseRoomID(project.DefaultRoomID)
	revisions, err := db.Reader().ListRoomRevisions(context.Background(), roomID)
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, r := range revisions {
		ids = append(ids, r.ID().String())
	}
	selection := []taskResourceSelection{{RepoID: added.Repository.RepoID, AssociationVersion: added.Repository.Version, Role: "write", TargetRef: taskResourceTestTargetRef(t, checkout), Scope: domain.TaskRepositoryScope{Mode: "repository", MigrationChoice: "not_needed"}, Checks: domain.TaskCheckPolicy{Mode: "none", SelectionSource: "user"}}}
	durations := []time.Duration{}
	for i := 0; i < 2; i++ {
		var task taskRefView
		requestJSONWithHeaders(t, handler, http.MethodPost, "/api/v2/rooms/"+project.DefaultRoomID+"/tasks", map[string]any{"requirement": "Inspect one relevant module without whole-repository context loading", "agentExecutionProfile": "trusted_local", "resources": selection, "revisionIds": ids}, map[string]string{"Idempotency-Key": fmt.Sprintf("large-task-%d", i)}, 201, &task)
		id, _ := domain.ParseTaskID(task.ID)
		started = time.Now()
		if err := manager.Ensure(context.Background(), id); err != nil {
			t.Fatal(err)
		}
		durations = append(durations, time.Since(started))
		if _, err := manager.ResolveExecutionRoot(context.Background(), id); err != nil {
			t.Fatal(err)
		}
		started = time.Now()
		if err := manager.Ensure(context.Background(), id); err != nil {
			t.Fatal(err)
		}
		t.Logf("existing preparation verification %d: %s", i, time.Since(started))
	}
	after := runTaskWorktreeGitTest(t, checkout, "status", "--porcelain")
	if before != after {
		t.Fatal("original checkout changed")
	}
	t.Logf("admission=%s directory=%s entries=%d first_worktree=%s second_worktree=%s original_unchanged=true", admission, query, len(entries.Entries), durations[0], durations[1])
}
