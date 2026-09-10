package localweb

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestPutRepositoryChecksRejectsCommittedSymlinkWorkingDirectory(t *testing.T) {
	server, _, selection, root, closeStore := newTaskResourcePathValidationFixture(t)
	defer closeStore()
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

	projectID := serverProjectIDForRepository(t, server, selection.RepoID)
	requestJSON(t, server.Handler(), http.MethodPut, "/api/v2/projects/"+projectID+"/repositories/"+selection.RepoID+"/checks", map[string]any{
		"checks": []map[string]any{{"id": "nested", "name": "Nested tests", "version": 0, "command": "npm test", "workingDirectory": "tools"}},
	}, http.StatusBadRequest, nil)
}

func TestPutRepositoryChecksAcceptsOrdinaryCommittedNestedDirectory(t *testing.T) {
	server, _, selection, root, closeStore := newTaskResourcePathValidationFixture(t)
	defer closeStore()
	if err := os.Mkdir(filepath.Join(root, "tools"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "tools", "marker.txt"), []byte("ordinary\n"), 0600); err != nil {
		t.Fatal(err)
	}
	commitTaskResourcePaths(t, root, "nested directory", "tools/marker.txt")

	projectID := serverProjectIDForRepository(t, server, selection.RepoID)
	var result struct {
		Checks []struct {
			WorkingDirectory string `json:"workingDirectory"`
			Version          uint64 `json:"version"`
		} `json:"checks"`
	}
	requestJSON(t, server.Handler(), http.MethodPut, "/api/v2/projects/"+projectID+"/repositories/"+selection.RepoID+"/checks", map[string]any{
		"checks": []map[string]any{{"id": "nested", "name": "Nested tests", "version": 0, "command": "npm test", "workingDirectory": "tools"}},
	}, http.StatusOK, &result)
	if len(result.Checks) != 1 || result.Checks[0].WorkingDirectory != "tools" || result.Checks[0].Version != 1 {
		t.Fatalf("saved checks = %+v", result.Checks)
	}
}

func serverProjectIDForRepository(t *testing.T, server *Server, repoID string) string {
	t.Helper()
	var projects struct {
		Projects []struct {
			ID           string                   `json:"id"`
			Repositories []repositoryResourceView `json:"repositories"`
		} `json:"projects"`
	}
	requestJSON(t, server.Handler(), http.MethodGet, "/api/v2/projects?state=active", nil, http.StatusOK, &projects)
	for _, project := range projects.Projects {
		for _, repository := range project.Repositories {
			if repository.RepoID == repoID {
				return project.ID
			}
		}
	}
	t.Fatalf("project for repository %s not found", repoID)
	return ""
}
