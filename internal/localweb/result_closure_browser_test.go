package localweb

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

func TestResultClosureBrowserTwoRepositories(t *testing.T) {
	if os.Getenv("CHORA_RESULT_CLOSURE_BROWSER") != "1" {
		t.Skip("run explicit result closure browser gate with built web assets")
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	web := os.Getenv("CHORA_DELIVERY_TEST_WEB")
	if web == "" {
		web = filepath.Join(root, "web", "dist")
	}
	if _, err := os.Stat(filepath.Join(web, "index.html")); err != nil {
		t.Fatal(err)
	}
	var taskRoot string
	f := runResourceTerminalScenario(t, resourceTerminalScenario{
		futureDelivery: true, name: "unreviewed closure browser", repositoryCount: 2, checkMode: "none", completeAssistant: true, wantRunState: domain.RunStateAwaitingReview,
		change: func(t *testing.T, path string, repos []domain.TaskRepositoryResource) {
			taskRoot = path
			for _, repo := range repos {
				writeResourceTerminalFile(t, filepath.Join(path, repo.WorkspaceDirectory()), "same.txt", []byte("unwanted browser result\n"))
			}
		},
	})
	f.server.webRoot = web
	f.server.pathPiEnabled = true
	hosting := newBrowserDeliveryHosting()
	installDeliveryTestService(t, f, taskRoot, hosting)
	server := httptest.NewServer(f.server.Handler())
	defer server.Close()
	route := "/rooms/" + f.run.Room.ID + "/tasks/" + f.run.Task.ID + "/runs/" + f.run.ID
	command := exec.Command("node", filepath.Join(root, "e2e", "result-closure-browser.mjs"), server.URL, route, filepath.Join(taskRoot, f.resources[0].WorkspaceDirectory()), filepath.Join(taskRoot, f.resources[1].WorkspaceDirectory()))
	command.Dir, command.Env = root, os.Environ()
	output, err := command.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "RESULT_CLOSURE_BROWSER_PASS") {
		t.Fatalf("browser closure: %v\n%s", err, output)
	}
	installDeliveryTestService(t, f, taskRoot, hosting)
	var after app.ResultClosureView
	requestJSON(t, f.server.Handler(), "GET", "/api/v2/runs/"+f.run.ID+"/closure", nil, 200, &after)
	if after.ClosedAt == nil || len(after.Entries) != 2 {
		t.Fatalf("closure lost after service recomposition: %#v", after)
	}
	for _, entry := range after.Entries {
		if entry.Status != "closed" || entry.Cleanup == nil || entry.Cleanup.Status != "succeeded" || entry.Achievement != "" {
			t.Fatalf("false delivery or lost cleanup: %#v", entry)
		}
	}
	runID, _ := domain.ParseRunID(f.run.ID)
	if _, err := f.server.store.Reader().GetLatestReviewForRun(context.Background(), runID); !errors.Is(err, storecontract.ErrNotFound) {
		t.Fatalf("closing invented Review: %v", err)
	}
	for _, repo := range f.resources {
		if _, err := os.Stat(filepath.Join(taskRoot, repo.WorkspaceDirectory())); !os.IsNotExist(err) {
			t.Fatal("Task worktree remains", err)
		}
		if got := runTaskWorktreeGitTest(t, f.originals[repo.RepoID], "rev-parse", "HEAD"); got != repo.BaseCommit+"\n" {
			t.Fatal("original main changed")
		}
		if got := runTaskWorktreeGitTest(t, f.originals[repo.RepoID], "rev-parse", "refs/heads/"+repo.TaskBranch); got != repo.BaseCommit+"\n" {
			t.Fatal("Task branch changed")
		}
		if string(mustReadResourceClosure(t, filepath.Join(f.originals[repo.RepoID], "same.txt"))) != "original "+repo.RepoID+"\n" {
			t.Fatal("original file changed")
		}
	}
}
