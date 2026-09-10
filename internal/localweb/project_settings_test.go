package localweb

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/gitsource"
)

func TestProjectSettingsAPIRequiresExplicitNoCheckAndPersistsCAS(t *testing.T) {
	db := openTaskWorktreeResolverStore(t)
	defer db.Close()
	root, _, _ := newTaskWorktreeTestRepository(t)
	if err := os.Mkdir(filepath.Join(root, "docs"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "docs", ".keep"), []byte("docs\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runTaskWorktreeGitTest(t, root, "add", "docs")
	runTaskWorktreeGitTest(t, root, "-c", "user.name=Chora Test", "-c", "user.email=chora@example.test", "commit", "-m", "add committed check directory")
	service := app.NewService(app.Dependencies{Store: db, RepositorySource: gitsource.Default{}, Authorizer: localAuthorizer{}})
	server := &Server{store: db, service: service, repositorySource: gitsource.Default{}, pathPiEnabled: true}
	handler := server.Handler()
	var project projectView
	requestJSON(t, handler, http.MethodPost, "/api/projects", map[string]any{"locator": root, "name": "Settings project"}, http.StatusOK, &project)

	var detected projectSettingsView
	requestJSON(t, handler, http.MethodGet, "/api/projects/"+project.ID+"/settings", nil, http.StatusOK, &detected)
	if detected.Version != 0 || detected.NoChecks || len(detected.VerificationCommands) != 0 || !reflect.DeepEqual(detected.WritableFiles, []string{"README.md", "docs/.keep"}) {
		t.Fatalf("detected settings=%+v", detected)
	}
	requestJSON(t, handler, http.MethodPut, "/api/projects/"+project.ID+"/settings", detected, http.StatusBadRequest, nil)

	noChecks := projectSettingsView{Version: 0, WritableFiles: []string{"README.md", "docs/new.md"}, WritableDirectories: []string{"docs"}, NoChecks: true}
	var created projectSettingsView
	requestJSON(t, handler, http.MethodPut, "/api/projects/"+project.ID+"/settings", noChecks, http.StatusOK, &created)
	if created.Version != 1 || !created.NoChecks || !reflect.DeepEqual(created.WritableDirectories, []string{"docs"}) {
		t.Fatalf("created=%+v", created)
	}
	requestJSON(t, handler, http.MethodPut, "/api/projects/"+project.ID+"/settings", noChecks, http.StatusConflict, nil)

	custom := projectSettingsView{
		Version: 1, WritableFiles: []string{"README.md"}, WritableDirectories: []string{"docs"},
		VerificationCommands: []projectVerificationCommandView{{Argv: []string{"go", "test", "./..."}, WorkingDirectory: "docs"}},
	}
	var updated projectSettingsView
	requestJSON(t, handler, http.MethodPut, "/api/projects/"+project.ID+"/settings", custom, http.StatusOK, &updated)
	if updated.Version != 2 || updated.NoChecks || !reflect.DeepEqual(updated.VerificationCommands, custom.VerificationCommands) {
		t.Fatalf("updated=%+v", updated)
	}
	var loaded projectSettingsView
	requestJSON(t, handler, http.MethodGet, "/api/projects/"+project.ID+"/settings", nil, http.StatusOK, &loaded)
	if !reflect.DeepEqual(loaded, updated) {
		t.Fatalf("loaded=%+v updated=%+v", loaded, updated)
	}
	requestJSON(t, handler, http.MethodGet, "/api/projects/"+project.Room.ID+"/settings", nil, http.StatusBadRequest, nil)
}

func TestProjectSettingsAPIRejectsSymlinkScopeAndWorkingDirectory(t *testing.T) {
	db := openTaskWorktreeResolverStore(t)
	defer db.Close()
	root, _, _ := newTaskWorktreeTestRepository(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	service := app.NewService(app.Dependencies{Store: db, RepositorySource: gitsource.Default{}, Authorizer: localAuthorizer{}})
	server := &Server{store: db, service: service, repositorySource: gitsource.Default{}, pathPiEnabled: true}
	handler := server.Handler()
	var project projectView
	requestJSON(t, handler, http.MethodPost, "/api/projects", map[string]any{"locator": root}, http.StatusOK, &project)

	for _, body := range []projectSettingsView{
		{WritableFiles: []string{"README.md"}, WritableDirectories: []string{"linked"}, NoChecks: true},
		{WritableFiles: []string{"linked/new.md"}, NoChecks: true},
		{WritableFiles: []string{"README.md"}, VerificationCommands: []projectVerificationCommandView{{Argv: []string{"go", "test"}, WorkingDirectory: "linked"}}},
	} {
		requestJSON(t, handler, http.MethodPut, "/api/projects/"+project.ID+"/settings", body, http.StatusBadRequest, nil)
	}
	var detected projectSettingsView
	requestJSON(t, handler, http.MethodGet, "/api/projects/"+project.ID+"/settings", nil, http.StatusOK, &detected)
	if detected.Version != 0 {
		t.Fatal("unsafe update persisted")
	}
}

func TestProjectSettingsDetectsRepositoryFileSizeBeforeLaunch(t *testing.T) {
	root, _, _ := newTaskWorktreeTestRepository(t)
	large := filepath.Join(root, "large.txt")
	file, err := os.Create(large)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(localConnectedRepositoryFileByteLimit + 1); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	runTaskWorktreeGitTest(t, root, "add", "large.txt")
	runTaskWorktreeGitTest(t, root, "-c", "user.name=Chora Test", "-c", "user.email=chora@example.test", "commit", "-m", "large fixture")
	detected, err := detectedProjectSettings(context.Background(), domain.NewProjectID(), root)
	if err != nil || !reflect.DeepEqual(detected.WritableFiles(), []string{"README.md"}) {
		t.Fatalf("unrelated large file changed detected scope: %+v, %v", detected, err)
	}
	selected, err := domain.NewProjectSettings(domain.ProjectSettingsParams{
		ProjectID: domain.NewProjectID(), WritableFiles: []string{"large.txt"}, NoChecks: true, UpdatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	err = validateProjectSettingsForRepository(context.Background(), root, selected)
	if err == nil || !strings.Contains(err.Error(), "text-file limit") {
		t.Fatalf("size error=%v", err)
	}
}

func TestProjectSettingsRejectsScopedBinaryAndLFSPointerBeforeLaunch(t *testing.T) {
	root, _, _ := newTaskWorktreeTestRepository(t)
	if err := os.Mkdir(filepath.Join(root, "assets"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "assets", "binary.dat"), []byte{'a', 0, 'b'}, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "pointer.txt"), []byte("version https://git-lfs.github.com/spec/v1\noid sha256:0123456789abcdef\nsize 3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runTaskWorktreeGitTest(t, root, "add", "assets/binary.dat", "pointer.txt")
	runTaskWorktreeGitTest(t, root, "-c", "user.name=Chora Test", "-c", "user.email=chora@example.test", "commit", "-m", "unsupported files")
	for _, test := range []struct {
		name        string
		files, dirs []string
		want        string
	}{
		{name: "binary directory", dirs: []string{"assets"}, want: "binary or non-UTF-8"},
		{name: "lfs exact file", files: []string{"pointer.txt"}, want: "Git LFS pointer"},
	} {
		t.Run(test.name, func(t *testing.T) {
			settings, err := domain.NewProjectSettings(domain.ProjectSettingsParams{
				ProjectID: domain.NewProjectID(), WritableFiles: test.files, WritableDirectories: test.dirs,
				NoChecks: true, UpdatedAt: time.Now().UTC(),
			})
			if err != nil {
				t.Fatal(err)
			}
			err = validateProjectSettingsForRepository(context.Background(), root, settings)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("classification error=%v", err)
			}
		})
	}
}
