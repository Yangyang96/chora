package localweb

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/gitsource"
)

type repositoryEntriesTestProject struct {
	ID string `json:"id"`
}

type repositoryEntriesTestAdmission struct {
	Repository repositoryResourceView `json:"repository"`
}

func TestRepositoryEntriesNestedPaginationSearchAndCursorBinding(t *testing.T) {
	root := newRepositoryEntriesFixture(t)
	server, handler, projectID, repositoryID := newRepositoryEntriesTestServer(t, root)
	_ = server
	base := "/api/v2/projects/" + projectID + "/repositories/" + repositoryID + "/entries"

	var first repositoryEntriesView
	requestJSON(t, handler, http.MethodGet, base+"?path=modules&limit=2&query=a", nil, http.StatusOK, &first)
	if first.Revision == "" || first.Truncated || first.NextCursor == "" || entryNames(first.Entries) != "alpha,bravo" {
		t.Fatalf("first page = %+v", first)
	}
	if _, err := decodeRepositoryEntryCursor(first.NextCursor, "another-repository", first.Revision, "modules", "a"); !errors.Is(err, errRepositoryEntryCursor) {
		t.Fatalf("cursor crossed repository boundary: %v", err)
	}
	var second repositoryEntriesView
	requestJSON(t, handler, http.MethodGet, base+"?path=modules&limit=2&query=a&cursor="+url.QueryEscape(first.NextCursor), nil, http.StatusOK, &second)
	if second.Revision != first.Revision || second.NextCursor != "" || second.Truncated || entryNames(second.Entries) != "charlie,delta" {
		t.Fatalf("second page = %+v", second)
	}
	var nested repositoryEntriesView
	requestJSON(t, handler, http.MethodGet, base+"?path=modules%2Falpha", nil, http.StatusOK, &nested)
	if len(nested.Entries) != 1 || nested.Entries[0].Path != "modules/alpha/inside.txt" || nested.Entries[0].Kind != "file" {
		t.Fatalf("nested page = %+v", nested)
	}
	var nonRecursive repositoryEntriesView
	requestJSON(t, handler, http.MethodGet, base+"?query=inside", nil, http.StatusOK, &nonRecursive)
	if len(nonRecursive.Entries) != 0 {
		t.Fatalf("search escaped current directory: %+v", nonRecursive)
	}
	requestJSON(t, handler, http.MethodGet, base+"?path=modules&limit=2&query=changed&cursor="+url.QueryEscape(first.NextCursor), nil, http.StatusBadRequest, nil)
	requestJSON(t, handler, http.MethodGet, base+"?path=.git", nil, http.StatusBadRequest, nil)
	requestJSON(t, handler, http.MethodGet, base+"?cursor=not-a-cursor", nil, http.StatusBadRequest, nil)
}

func TestRepositoryEntriesMarksSymlinkAndSubmoduleUnsupported(t *testing.T) {
	root := newRepositoryEntriesFixture(t)
	_, handler, projectID, repositoryID := newRepositoryEntriesTestServer(t, root)
	var view repositoryEntriesView
	requestJSON(t, handler, http.MethodGet, "/api/v2/projects/"+projectID+"/repositories/"+repositoryID+"/entries", nil, http.StatusOK, &view)
	kinds := map[string]string{}
	for _, entry := range view.Entries {
		kinds[entry.Name] = entry.Kind
	}
	if kinds["modules"] != "directory" || kinds["README.md"] != "file" || kinds["docs-link"] != "unsupported" || kinds["vendor-module"] != "unsupported" {
		t.Fatalf("entry kinds = %#v", kinds)
	}
}

func TestRepositoryEntriesFailsClosedForIdentityDriftUnbornAndInvalidRevision(t *testing.T) {
	root := newRepositoryEntriesFixture(t)
	server, handler, projectID, repositoryID := newRepositoryEntriesTestServer(t, root)
	path := "/api/v2/projects/" + projectID + "/repositories/" + repositoryID + "/entries"
	inspection, err := (gitsource.Default{}).Inspect(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}

	drifted := inspection
	drifted.PhysicalIdentity = strings.Repeat("f", 64)
	server.repositorySource = repositoryEntriesSource{inspection: drifted}
	requestJSON(t, handler, http.MethodGet, path, nil, http.StatusConflict, nil)

	unborn := inspection
	unborn.HeadCommit, unborn.RootTree, unborn.Branch, unborn.Unborn = "", "", "main", true
	server.repositorySource = repositoryEntriesSource{inspection: unborn}
	requestJSON(t, handler, http.MethodGet, path, nil, http.StatusUnprocessableEntity, nil)

	invalid := inspection
	invalid.HeadCommit = "HEAD"
	server.repositorySource = repositoryEntriesSource{inspection: invalid}
	requestJSON(t, handler, http.MethodGet, path, nil, http.StatusServiceUnavailable, nil)
}

func TestRepositoryEntriesLargeRepositoryIsLazyAndSearchTruncationContinues(t *testing.T) {
	root, revision := newLargeRepositoryEntriesFixture(t, 100, 1_050)
	rootPage, err := readRepositoryEntryPage(context.Background(), root, revision, ".", "", nil, domainPageSizeForTest())
	if err != nil {
		t.Fatal(err)
	}
	if len(rootPage.entries) != 100 || rootPage.hasMore || rootPage.truncated {
		t.Fatalf("lazy root page = entries %d more %v truncated %v", len(rootPage.entries), rootPage.hasMore, rootPage.truncated)
	}

	// One deliberately large directory proves search never scans an unbounded
	// tail in one request. Reuse its tree below 100 roots for over one million paths
	// without asking Git to enumerate the full repository.
	searchRoot, searchRevision := newLargeRepositoryEntriesFixture(t, 100, repositoryEntryScanLimit+50)
	first, err := readRepositoryEntryPage(context.Background(), searchRoot, searchRevision, "module000", "absent-name", nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.entries) != 0 || !first.hasMore || !first.truncated || len(first.resumeAfter) == 0 {
		t.Fatalf("bounded search page = %+v", first)
	}
	second, err := readRepositoryEntryPage(context.Background(), searchRoot, searchRevision, "module000", "absent-name", first.resumeAfter, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.entries) != 0 || second.hasMore || second.truncated {
		t.Fatalf("continued search page = %+v", second)
	}
}

func TestRepositoryEntriesHonorsCancellation(t *testing.T) {
	root := newRepositoryEntriesFixture(t)
	revision := repositoryEntriesGit(t, root, "rev-parse", "HEAD")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := readRepositoryEntryPage(ctx, root, revision, ".", "", nil, 20)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error = %v", err)
	}
}

type repositoryEntriesSource struct {
	inspection gitsource.Inspection
	err        error
}

func (source repositoryEntriesSource) Inspect(context.Context, string) (gitsource.Inspection, error) {
	return source.inspection, source.err
}
func (source repositoryEntriesSource) Clone(context.Context, string, string) (gitsource.Inspection, error) {
	return gitsource.Inspection{}, errors.New("not implemented")
}

func newRepositoryEntriesTestServer(t *testing.T, root string) (*Server, http.Handler, string, string) {
	t.Helper()
	db := openTaskWorktreeResolverStore(t)
	t.Cleanup(func() { _ = db.Close() })
	source := gitsource.Default{}
	service := app.NewService(app.Dependencies{Store: db, DataRoot: t.TempDir(), RepositorySource: source, Authorizer: localAuthorizer{}})
	server := &Server{store: db, service: service, repositorySource: source, pathPiEnabled: true}
	handler := server.Handler()
	var project repositoryEntriesTestProject
	requestJSON(t, handler, http.MethodPost, "/api/v2/projects", map[string]any{"name": "Directory browser"}, http.StatusCreated, &project)
	var admitted repositoryEntriesTestAdmission
	requestJSON(t, handler, http.MethodPost, "/api/v2/projects/"+project.ID+"/repositories", map[string]any{"locator": root}, http.StatusOK, &admitted)
	return server, handler, project.ID, admitted.Repository.RepoID
}

func newRepositoryEntriesFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	repositoryEntriesRunGit(t, root, "init", "-q")
	repositoryEntriesRunGit(t, root, "config", "user.name", "Chora Test")
	repositoryEntriesRunGit(t, root, "config", "user.email", "chora@example.invalid")
	for _, directory := range []string{"alpha", "bravo", "charlie", "delta"} {
		path := filepath.Join(root, "modules", directory)
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "inside.txt"), []byte(directory), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("README.md", filepath.Join(root, "docs-link")); err != nil {
		t.Fatal(err)
	}
	repositoryEntriesRunGit(t, root, "add", "--all")
	repositoryEntriesRunGit(t, root, "commit", "-qm", "fixture")
	head := repositoryEntriesGit(t, root, "rev-parse", "HEAD")
	repositoryEntriesRunGit(t, root, "update-index", "--add", "--cacheinfo", "160000,"+head+",vendor-module")
	repositoryEntriesRunGit(t, root, "commit", "-qm", "add gitlink")
	return root
}

func newLargeRepositoryEntriesFixture(t *testing.T, directories, filesPerDirectory int) (string, string) {
	t.Helper()
	root := t.TempDir()
	repositoryEntriesRunGit(t, root, "init", "-q")
	repositoryEntriesRunGit(t, root, "config", "user.name", "Chora Test")
	repositoryEntriesRunGit(t, root, "config", "user.email", "chora@example.invalid")
	blob := repositoryEntriesGitInput(t, root, "x", "hash-object", "-w", "--stdin")
	var module strings.Builder
	for index := 0; index < filesPerDirectory; index++ {
		fmt.Fprintf(&module, "100644 blob %s\tfile%05d.txt\n", blob, index)
	}
	moduleTree := repositoryEntriesGitInput(t, root, module.String(), "mktree")
	var top strings.Builder
	for index := 0; index < directories; index++ {
		fmt.Fprintf(&top, "040000 tree %s\tmodule%03d\n", moduleTree, index)
	}
	topTree := repositoryEntriesGitInput(t, root, top.String(), "mktree")
	commit := repositoryEntriesGitInput(t, root, "large fixture\n", "commit-tree", topTree)
	repositoryEntriesRunGit(t, root, "update-ref", "HEAD", commit)
	return root, commit
}

func repositoryEntriesRunGit(t *testing.T, root string, arguments ...string) {
	t.Helper()
	command := exec.Command("/usr/bin/git", append([]string{"-C", root}, arguments...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", arguments, err, output)
	}
}

func repositoryEntriesGit(t *testing.T, root string, arguments ...string) string {
	t.Helper()
	command := exec.Command("/usr/bin/git", append([]string{"-C", root}, arguments...)...)
	output, err := command.Output()
	if err != nil {
		t.Fatalf("git %v: %v", arguments, err)
	}
	return strings.TrimSpace(string(output))
}

func repositoryEntriesGitInput(t *testing.T, root, input string, arguments ...string) string {
	t.Helper()
	command := exec.Command("/usr/bin/git", append([]string{"-C", root}, arguments...)...)
	command.Stdin = strings.NewReader(input)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", arguments, err, output)
	}
	return strings.TrimSpace(string(output))
}

func entryNames(entries []repositoryEntryView) string {
	names := make([]string, len(entries))
	for index, entry := range entries {
		names[index] = entry.Name
	}
	return strings.Join(names, ",")
}

func domainPageSizeForTest() int { return 200 }
