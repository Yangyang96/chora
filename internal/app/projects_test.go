package app_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/gitsource"
	"github.com/Yangyang96/chora/internal/store/sqlite"
)

const (
	commitSHA = "0123456789abcdef0123456789abcdef01234567"
	treeSHA   = "89abcdef0123456789abcdef0123456789abcdef"
)

type fakeRepositorySource struct {
	mu       sync.Mutex
	byPath   map[string]gitsource.Inspection
	pathErrs map[string]error
	cloneFn  func(url, dest string) (gitsource.Inspection, error)
	calls    int
}

func (f *fakeRepositorySource) Inspect(ctx context.Context, path string) (gitsource.Inspection, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if err, ok := f.pathErrs[path]; ok {
		return gitsource.Inspection{}, err
	}
	if inspection, ok := f.byPath[path]; ok {
		return inspection, nil
	}
	return gitsource.Inspection{}, gitsource.ErrNotGit
}

func (f *fakeRepositorySource) Clone(ctx context.Context, url, dest string) (gitsource.Inspection, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.cloneFn == nil {
		return gitsource.Inspection{}, gitsource.ErrCloneFailed
	}
	return f.cloneFn(url, dest)
}

type projectFixture struct {
	t      *testing.T
	db     *sqlite.Store
	svc    *app.Service
	source *fakeRepositorySource
}

func newProjectFixture(t *testing.T) *projectFixture {
	t.Helper()
	ctx := context.Background()
	db, err := openLatestSQLiteTestStore(ctx, filepath.Join(t.TempDir(), "data", "chora.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	source := &fakeRepositorySource{byPath: map[string]gitsource.Inspection{}, pathErrs: map[string]error{}}
	dataRoot := filepath.Join(t.TempDir(), "chora-data")
	svc := app.NewService(app.Dependencies{
		Store: db, Context: app.ContextAssembler{}, Authorizer: allowAuthorizer{},
		Clock: &fixedClock{now: time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)}, IDs: app.RandomIDs{},
		RepositorySource: source, DataRoot: dataRoot,
	})
	return &projectFixture{t: t, db: db, svc: svc, source: source}
}

func (f *projectFixture) openResult(locator string) app.OpenProjectResult {
	f.t.Helper()
	result, err := f.svc.OpenProject(context.Background(), app.OpenProjectRequest{CommandMeta: meta("open-"+filepath.Base(locator), "open"), Locator: locator})
	if err != nil {
		f.t.Fatal(err)
	}
	return result
}

func (f *projectFixture) registerRepo(path string, dirty bool) {
	f.source.mu.Lock()
	defer f.source.mu.Unlock()
	f.source.byPath[path] = gitsource.Inspection{CanonicalPath: path, IsGit: true, HeadCommit: commitSHA, RootTree: treeSHA, Dirty: dirty}
}

func TestOpenProjectAdmitsRepositoryAtomically(t *testing.T) {
	fixture := newProjectFixture(t)
	ctx := context.Background()
	repo := t.TempDir()
	fixture.registerRepo(repo, false)
	result := fixture.openResult(repo)
	if result.Replayed {
		t.Fatal("fresh open reported replayed")
	}
	project := result.Project
	if project.RepositoryBinding.RoomID() != project.Room.ID() {
		t.Fatalf("binding room=%q room=%q", project.RepositoryBinding.RoomID(), project.Room.ID())
	}
	if project.RepositoryBinding.LocalLocator() != repo || project.RepositoryBinding.TargetWorktree() != repo {
		t.Fatalf("locator=%q target=%q", project.RepositoryBinding.LocalLocator(), project.RepositoryBinding.TargetWorktree())
	}
	if project.RepositoryBinding.SourceKind() != domain.RepositorySourceKindOpen {
		t.Fatalf("source kind=%q", project.RepositoryBinding.SourceKind())
	}
	if project.RepositoryBinding.AdmittedBase() != commitSHA {
		t.Fatalf("admitted base=%q", project.RepositoryBinding.AdmittedBase())
	}
	if project.Project.Name() != filepath.Base(repo) || project.Room.Name() != "General" {
		t.Fatalf("project name=%q room name=%q", project.Project.Name(), project.Room.Name())
	}
	if project.Room.Description() != "Admitted local Git repository" {
		t.Fatalf("room description=%q", project.Room.Description())
	}
	if project.Room.WorkspaceRoot() != repo {
		t.Fatalf("room workspace=%q", project.Room.WorkspaceRoot())
	}
	// The Room brief revision must exist exactly like CreateRoom.
	revisions, err := fixture.db.Reader().ListRoomRevisions(ctx, project.Room.ID())
	if err != nil {
		t.Fatal(err)
	}
	if len(revisions) != 1 {
		t.Fatalf("room revisions=%d", len(revisions))
	}
}

func TestOpenProjectNamedRoomPersistsAndReopenPreservesIt(t *testing.T) {
	fixture := newProjectFixture(t)
	repo := filepath.Join(t.TempDir(), "frontend")
	fixture.registerRepo(repo, false)
	opened, err := fixture.svc.OpenProject(context.Background(), app.OpenProjectRequest{
		CommandMeta: meta("named-project", "open"), Locator: repo, Name: "  电商平台  ",
	})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := fixture.svc.GetProject(context.Background(), opened.Project.Room.ID())
	if err != nil {
		t.Fatal(err)
	}
	if stored.Project.Name() != "电商平台" || stored.Room.Name() != "General" || stored.RepositoryBinding.Name() != "电商平台" || stored.RepositoryBinding.LocalLocator() != repo {
		t.Fatalf("project name or repository binding not preserved: %+v", stored)
	}
	reopened, err := fixture.svc.OpenProject(context.Background(), app.OpenProjectRequest{
		CommandMeta: meta("reopen-named-project", "open"), Locator: repo, Name: "Different name",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reopened.Replayed || reopened.Project.Room.ID() != stored.Room.ID() || reopened.Project.Project.Name() != "电商平台" {
		t.Fatalf("reopen changed the existing Room: %+v", reopened)
	}
}

func TestOpenProjectRejectsInvalidExplicitName(t *testing.T) {
	for _, name := range []string{"   ", "project\nname", strings.Repeat("界", 121)} {
		t.Run(fmt.Sprintf("%q", name), func(t *testing.T) {
			fixture := newProjectFixture(t)
			repo := filepath.Join(t.TempDir(), "repo")
			fixture.registerRepo(repo, false)
			_, err := fixture.svc.OpenProject(context.Background(), app.OpenProjectRequest{
				CommandMeta: meta("invalid-name", "open"), Locator: repo, Name: name,
			})
			if !errors.Is(err, app.ErrInvalidCommand) {
				t.Fatalf("error=%v want invalid command", err)
			}
		})
	}
}

func TestOpenProjectIdempotentReopen(t *testing.T) {
	fixture := newProjectFixture(t)
	repo := t.TempDir()
	fixture.registerRepo(repo, false)
	first := fixture.openResult(repo)
	second := fixture.openResult(repo)
	if !second.Replayed {
		t.Fatal("reopen not reported replayed")
	}
	if second.Project.Room.ID() != first.Project.Room.ID() {
		t.Fatalf("reopen created a second room: %q != %q", second.Project.Room.ID(), first.Project.Room.ID())
	}
}

func TestOpenProjectOverlapRejected(t *testing.T) {
	fixture := newProjectFixture(t)
	parent := t.TempDir()
	child := filepath.Join(parent, "nested")
	fixture.registerRepo(parent, false)
	fixture.registerRepo(child, false)
	fixture.openResult(parent)
	_, err := fixture.svc.OpenProject(context.Background(), app.OpenProjectRequest{CommandMeta: meta("open-nested", "open-nested"), Locator: child})
	if !errors.Is(err, app.ErrProjectOverlap) {
		t.Fatalf("nested open error = %v, want ErrProjectOverlap", err)
	}
}

func TestArchivedProjectStillReservesRepositoryContainment(t *testing.T) {
	fixture := newProjectFixture(t)
	ctx := context.Background()
	parent := t.TempDir()
	nested := filepath.Join(parent, "nested")
	outer := filepath.Dir(parent)
	fixture.registerRepo(parent, false)
	fixture.registerRepo(nested, false)
	fixture.registerRepo(outer, false)
	opened := fixture.openResult(parent)
	archived, err := fixture.svc.ArchiveProject(ctx, app.ChangeProjectLifecycleRequest{CommandMeta: meta("archive-overlap", "archive"), ProjectID: opened.Project.Project.ID(), ExpectedVersion: opened.Project.Project.Version()})
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []string{nested, outer} {
		if _, err = fixture.svc.OpenProject(ctx, app.OpenProjectRequest{CommandMeta: meta("overlap-"+filepath.Base(candidate), "open"), Locator: candidate}); !errors.Is(err, app.ErrProjectOverlap) {
			t.Fatalf("candidate %q error=%v", candidate, err)
		}
	}
	restored, err := fixture.svc.RestoreProject(ctx, app.ChangeProjectLifecycleRequest{CommandMeta: meta("restore-overlap", "restore"), ProjectID: archived.Project.ID(), ExpectedVersion: archived.Project.Version()})
	if err != nil {
		t.Fatal(err)
	}
	if restored.Project.ID() != opened.Project.Project.ID() {
		t.Fatalf("restored Project=%s", restored.Project.ID())
	}
	projects, err := fixture.svc.ListProjects(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 1 {
		t.Fatalf("projects=%d", len(projects))
	}
}

func TestOpenProjectErrorMapping(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want error
	}{
		{"not git", gitsource.ErrNotGit, app.ErrRepositoryNotGit},
		{"bare", gitsource.ErrBare, app.ErrRepositoryBare},
		{"linked worktree", gitsource.ErrLinkedWorktree, app.ErrRepositoryLinkedWorktree},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newProjectFixture(t)
			path := filepath.Join(t.TempDir(), "repo")
			fixture.source.mu.Lock()
			fixture.source.pathErrs[path] = tc.err
			fixture.source.mu.Unlock()
			_, err := fixture.svc.OpenProject(context.Background(), app.OpenProjectRequest{CommandMeta: meta("open-"+tc.name, "open"), Locator: path})
			if !errors.Is(err, tc.want) {
				t.Fatalf("OpenProject() error = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestOpenProjectMissingPathIsActionable(t *testing.T) {
	fixture := newProjectFixture(t)
	path := filepath.Join(t.TempDir(), "missing")
	fixture.source.mu.Lock()
	fixture.source.pathErrs[path] = &gitsource.PathError{Path: path, Err: os.ErrNotExist}
	fixture.source.mu.Unlock()
	_, err := fixture.svc.OpenProject(context.Background(), app.OpenProjectRequest{CommandMeta: meta("open-missing", "open"), Locator: path})
	if !errors.Is(err, app.ErrRepositoryNotGit) {
		t.Fatalf("missing path error = %v, want ErrRepositoryNotGit", err)
	}
	if !strings.Contains(err.Error(), path) {
		t.Fatalf("missing path error lacks locator: %v", err)
	}
}

func TestOpenProjectDirtyDisclosure(t *testing.T) {
	fixture := newProjectFixture(t)
	repo := t.TempDir()
	fixture.registerRepo(repo, true)
	result := fixture.openResult(repo)
	if !result.Project.RepositoryBinding.DirtyAdmitted() {
		t.Fatal("dirty flag not disclosed")
	}
}

func TestOpenProjectUnavailableWhenSourceNil(t *testing.T) {
	db, err := openLatestSQLiteTestStore(context.Background(), filepath.Join(t.TempDir(), "data", "chora.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	svc := app.NewService(app.Dependencies{Store: db, Context: app.ContextAssembler{}, Authorizer: allowAuthorizer{}, Clock: &fixedClock{now: time.Now().UTC()}, IDs: app.RandomIDs{}})
	_, err = svc.OpenProject(context.Background(), app.OpenProjectRequest{CommandMeta: meta("open-nil", "open"), Locator: t.TempDir()})
	if !errors.Is(err, app.ErrRepositoryUnavailable) {
		t.Fatalf("nil source error = %v, want ErrRepositoryUnavailable", err)
	}
}

func TestCloneProjectClonesAndBinds(t *testing.T) {
	fixture := newProjectFixture(t)
	ctx := context.Background()
	url := "https://example.invalid/owner/repo.git"
	fixture.source.cloneFn = func(url, dest string) (gitsource.Inspection, error) {
		if err := os.MkdirAll(dest, 0o700); err != nil {
			return gitsource.Inspection{}, err
		}
		return gitsource.Inspection{CanonicalPath: dest, IsGit: true, HeadCommit: commitSHA, RootTree: treeSHA}, nil
	}
	result, err := fixture.svc.CloneProject(ctx, app.CloneProjectRequest{CommandMeta: meta("clone-1", "clone"), URL: url})
	if err != nil {
		t.Fatal(err)
	}
	if result.Replayed {
		t.Fatal("fresh clone reported replayed")
	}
	binding := result.Project.RepositoryBinding
	if binding.SourceKind() != domain.RepositorySourceKindClone || binding.CloneURL() != url {
		t.Fatalf("clone kind=%q url=%q", binding.SourceKind(), binding.CloneURL())
	}
	if filepath.Base(binding.LocalLocator()) != "repo" {
		t.Fatalf("clone dest basename=%q want repo", filepath.Base(binding.LocalLocator()))
	}
	if _, err := os.Stat(binding.LocalLocator()); err != nil {
		t.Fatalf("clone destination not created on disk: %v", err)
	}
	// Idempotent on destination.
	second, err := fixture.svc.CloneProject(ctx, app.CloneProjectRequest{CommandMeta: meta("clone-2", "clone"), URL: url})
	if err != nil {
		t.Fatal(err)
	}
	if !second.Replayed || second.Project.Room.ID() != result.Project.Room.ID() {
		t.Fatalf("clone idempotency replayed=%t room match=%t", second.Replayed, second.Project.Room.ID() == result.Project.Room.ID())
	}
}

func TestCloneProjectSameBasenameDifferentURLRejected(t *testing.T) {
	fixture := newProjectFixture(t)
	ctx := context.Background()
	fixture.source.cloneFn = func(url, dest string) (gitsource.Inspection, error) {
		return gitsource.Inspection{CanonicalPath: dest, IsGit: true, HeadCommit: commitSHA, RootTree: treeSHA}, nil
	}
	firstURL := "https://a.example/owner/demo.git"
	if _, err := fixture.svc.CloneProject(ctx, app.CloneProjectRequest{CommandMeta: meta("clone-a", "clone"), URL: firstURL}); err != nil {
		t.Fatal(err)
	}
	secondURL := "https://b.example/owner/demo.git"
	if _, err := fixture.svc.CloneProject(ctx, app.CloneProjectRequest{CommandMeta: meta("clone-b", "clone"), URL: secondURL}); !errors.Is(err, app.ErrProjectOverlap) {
		t.Fatalf("second CloneProject error = %v, want ErrProjectOverlap", err)
	}
}

func TestCloneProjectFailureIsGeneric(t *testing.T) {
	fixture := newProjectFixture(t)
	fixture.source.cloneFn = func(url, dest string) (gitsource.Inspection, error) {
		return gitsource.Inspection{}, gitsource.ErrCloneFailed
	}
	_, err := fixture.svc.CloneProject(context.Background(), app.CloneProjectRequest{CommandMeta: meta("clone-fail", "clone"), URL: "https://user:secret@example.invalid/private.git"})
	if !errors.Is(err, app.ErrCloneFailed) {
		t.Fatalf("clone failure error = %v, want ErrCloneFailed", err)
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatalf("clone failure echoed credentials: %v", err)
	}
}

func TestRemoveProjectLeavesRepositoryUntouched(t *testing.T) {
	fixture := newProjectFixture(t)
	ctx := context.Background()
	repo := t.TempDir()
	marker := filepath.Join(repo, "marker.txt")
	if err := os.WriteFile(marker, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture.registerRepo(repo, false)
	opened := fixture.openResult(repo)
	_, err := fixture.svc.RemoveProject(ctx, app.RemoveProjectRequest{CommandMeta: meta("remove-1", "remove"), RoomID: opened.Project.Room.ID(), ExpectedVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	// Repository directory must be untouched on disk.
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("repository marker removed: %v", err)
	}
	// Binding is removed and the Project becomes effectively archived without
	// rewriting the Room's own lifecycle.
	if _, err := fixture.db.Reader().GetRepositoryBinding(ctx, opened.Project.Room.ID()); err != nil {
		t.Fatal(err)
	}
	active, err := fixture.db.Reader().ListRepositoryBindings(ctx, domain.RepositoryBindingStateActive)
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 0 {
		t.Fatalf("active bindings after remove=%d", len(active))
	}
	room, err := fixture.db.Reader().GetRoom(ctx, opened.Project.Room.ID())
	if err != nil {
		t.Fatal(err)
	}
	if room.State() != domain.RoomStateActive {
		t.Fatalf("room state=%q want active", room.State())
	}
	project, err := fixture.db.Reader().GetProject(ctx, room.ProjectID())
	if err != nil || project.State() != domain.ProjectStateArchived {
		t.Fatalf("project state=%q err=%v", project.State(), err)
	}
}

func TestListProjectsFiltersByNameCaseInsensitive(t *testing.T) {
	fixture := newProjectFixture(t)
	alpha := filepath.Join(t.TempDir(), "AlphaRepo")
	beta := filepath.Join(t.TempDir(), "BetaRepo")
	fixture.registerRepo(alpha, false)
	fixture.registerRepo(beta, false)
	fixture.openResult(alpha)
	fixture.openResult(beta)
	all, err := fixture.svc.ListProjects(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("all projects=%d", len(all))
	}
	filtered, err := fixture.svc.ListProjects(context.Background(), "ALPHA")
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered) != 1 || filtered[0].Project.Name() != "AlphaRepo" {
		t.Fatalf("filtered=%d name=%q", len(filtered), filtered[0].Project.Name())
	}
}

func TestGetProjectNotFound(t *testing.T) {
	fixture := newProjectFixture(t)
	_, err := fixture.svc.GetProject(context.Background(), domain.NewRoomID())
	if !errors.Is(err, app.ErrProjectNotFound) {
		t.Fatalf("GetProject() error = %v, want ErrProjectNotFound", err)
	}
}
