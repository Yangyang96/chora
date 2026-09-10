package localweb

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/Yangyang96/chora/internal/app"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/taskdelivery"
)

// Commit and Push use real disposable Git repositories. Only GitHub is
// replaced by this deterministic adapter, with state isolated per Task branch.
type browserDeliveryHosting struct {
	pulls   map[string]taskdelivery.PullRequest
	remotes map[string]string
}

func newBrowserDeliveryHosting() *browserDeliveryHosting {
	return &browserDeliveryHosting{pulls: map[string]taskdelivery.PullRequest{}, remotes: map[string]string{}}
}

func (h *browserDeliveryHosting) Available() bool { return true }

func (h *browserDeliveryHosting) PreviewPR(_ context.Context, p taskdelivery.PushPreview, title, body, marker string) (taskdelivery.HostingPreview, error) {
	return taskdelivery.HostingPreview{
		Kind: "pr", Repository: "browser/" + strings.ReplaceAll(p.Binding.Branch, "/", "-"),
		HeadBranch: p.Binding.Branch, BaseBranch: strings.TrimPrefix(p.Binding.TargetRef, "refs/heads/"),
		Head: p.Head, BaseHead: p.TargetHead, Title: title, Body: body, Marker: marker, Digest: marker,
	}, nil
}

func (h *browserDeliveryHosting) PreviewMerge(_ context.Context, p taskdelivery.HostingPreview, number int) (taskdelivery.HostingPreview, error) {
	pr, ok := h.pulls[p.HeadBranch]
	if !ok || pr.State != "open" || pr.Number != number {
		return p, taskdelivery.ErrConflict
	}
	p.Kind, p.Number, p.MergeMethod = "merge", number, "merge"
	return p, nil
}

func (h *browserDeliveryHosting) Confirm(ctx context.Context, p taskdelivery.HostingPreview) (taskdelivery.PullRequest, error) {
	if p.Kind == "pr" {
		pr := taskdelivery.PullRequest{
			Repository: p.Repository, HeadBranch: p.HeadBranch, BaseBranch: p.BaseBranch,
			Head: p.Head, BaseHead: p.BaseHead, URL: fmt.Sprintf("https://github.com/%s/pull/1", p.Repository),
			State: "open", Number: 1,
		}
		h.pulls[p.HeadBranch] = pr
		return pr, nil
	}
	pr, ok := h.pulls[p.HeadBranch]
	if !ok || pr.State != "open" || pr.Number != p.Number {
		return taskdelivery.PullRequest{}, taskdelivery.ErrConflict
	}
	// Simulate GitHub with an actual two-parent merge in the disposable bare
	// remote. The original checkout and its local main must not move.
	remote := h.remotes[p.HeadBranch]
	if remote == "" || p.MergeMethod != "merge" {
		return taskdelivery.PullRequest{}, taskdelivery.ErrConflict
	}
	git := func(args ...string) (string, error) {
		command := exec.CommandContext(ctx, "git", append([]string{"-C", remote, "-c", "user.name=Hosting fixture", "-c", "user.email=hosting@example.test"}, args...)...)
		out, err := command.CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	tree, err := git("rev-parse", p.Head+"^{tree}")
	if err != nil {
		return taskdelivery.PullRequest{}, err
	}
	merged, err := git("commit-tree", tree, "-p", p.BaseHead, "-p", p.Head, "-m", "Merge reviewed Task branch")
	if err != nil {
		return taskdelivery.PullRequest{}, err
	}
	if _, err = git("update-ref", "refs/heads/"+p.BaseBranch, merged, p.BaseHead); err != nil {
		return taskdelivery.PullRequest{}, err
	}
	pr.State, pr.MergeCommit = "merged", merged
	h.pulls[p.HeadBranch] = pr
	return pr, nil
}

func (h *browserDeliveryHosting) Observe(_ context.Context, p taskdelivery.HostingPreview) (taskdelivery.PullRequest, error) {
	pr, ok := h.pulls[p.HeadBranch]
	if !ok {
		return taskdelivery.PullRequest{}, taskdelivery.ErrRecovery
	}
	return pr, nil
}

func TestTaskBranchBrowserFullDeliveryPreservesOriginalsAndRepositoryIsolation(t *testing.T) {
	if os.Getenv("CHORA_TASK_DELIVERY_BROWSER") != "1" {
		t.Skip("set CHORA_TASK_DELIVERY_BROWSER=1 after building output/task-branch-web")
	}
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	webRoot := filepath.Join(repositoryRoot, "output", "task-branch-web")
	if custom := os.Getenv("CHORA_DELIVERY_TEST_WEB"); custom != "" {
		webRoot = custom
	}
	if _, err := os.Stat(filepath.Join(webRoot, "index.html")); err != nil {
		t.Fatalf("task branch browser assets are unavailable at %s: %v", webRoot, err)
	}

	var taskRoot string
	result := runResourceTerminalScenario(t, resourceTerminalScenario{
		futureDelivery: true, name: "task branch full delivery browser", repositoryCount: 2, checkMode: "none", completeAssistant: true,
		wantRunState: domain.RunStateAwaitingReview,
		change: func(t *testing.T, root string, resources []domain.TaskRepositoryResource) {
			taskRoot = root
			for _, resource := range resources {
				writeResourceTerminalFile(t, filepath.Join(root, resource.WorkspaceDirectory()), "same.txt", []byte("reviewed browser change for "+resource.RepoID+"\n"))
			}
		},
	})
	result.server.webRoot = webRoot
	result.server.commitMessages.generate = func(_ context.Context, input string) (commitMessageSuggestion, error) {
		var content struct {
			app.DeliveryDraftContext
			Feedback string `json:"feedback"`
		}
		if err := json.Unmarshal([]byte(input), &content); err != nil {
			return commitMessageSuggestion{}, err
		}
		if content.Kind == "pr" {
			body := "- Update same.txt with the reviewed repository-specific content.\n\nTests not run: no checks were selected."
			if content.Feedback == "简化成一句话" {
				body = "Update the reviewed repository content; tests were not run."
			}
			return commitMessageSuggestion{Title: "fix: update reviewed repository content", Body: body, Provider: "browser-fixture", Model: "deterministic"}, nil
		}
		return commitMessageSuggestion{Message: "fix: update reviewed repository content\n\nUpdate same.txt with the reviewed repository-specific content.", Provider: "browser-fixture", Model: "deterministic"}, nil
	}
	hosting := newBrowserDeliveryHosting()
	installDeliveryTestService(t, result, taskRoot, hosting)
	result.server.pathPiEnabled = true // Exercise the production Run projection too.

	originalHeads := make(map[string]string, len(result.resources))
	originalIndexes := make(map[string]string, len(result.resources))
	originalFiles := make(map[string]string, len(result.resources))
	taskHeads := make(map[string]string, len(result.resources))
	remotes := make(map[string]string, len(result.resources))
	for _, resource := range result.resources {
		original := result.originals[resource.RepoID]
		runTaskWorktreeGitTest(t, original, "config", "user.name", "Chora Browser Test")
		runTaskWorktreeGitTest(t, original, "config", "user.email", "browser@example.test")
		bare := filepath.Join(t.TempDir(), resource.RepoID+".git")
		runTaskWorktreeGitTest(t, original, "init", "--bare", bare)
		runTaskWorktreeGitTest(t, original, "remote", "add", "origin", bare)
		runTaskWorktreeGitTest(t, original, "push", "origin", resource.BaseRef+":"+resource.BaseRef)
		remotes[resource.RepoID] = bare
		hosting.remotes[resource.TaskBranch] = bare

		writeResourceTerminalFile(t, original, "unrelated.txt", []byte("preexisting staged browser witness for "+resource.RepoID+"\n"))
		runTaskWorktreeGitTest(t, original, "add", "unrelated.txt")
		originalFiles[resource.RepoID] = "original checkout unsaved browser witness for " + resource.RepoID + "\n"
		writeResourceTerminalFile(t, original, "same.txt", []byte(originalFiles[resource.RepoID]))
		originalHeads[resource.RepoID] = runTaskWorktreeGitTest(t, original, "rev-parse", "HEAD")
		originalIndexes[resource.RepoID] = runTaskWorktreeGitTest(t, original, "ls-files", "--stage")

		taskWorktree := filepath.Join(taskRoot, resource.WorkspaceDirectory())
		taskHeads[resource.RepoID] = strings.TrimSpace(runTaskWorktreeGitTest(t, taskWorktree, "rev-parse", "HEAD"))
		taskContent, readErr := os.ReadFile(filepath.Join(taskWorktree, "same.txt"))
		wantTaskContent := "reviewed browser change for " + resource.RepoID + "\n"
		if taskHeads[resource.RepoID] != resource.BaseCommit || readErr != nil || string(taskContent) != wantTaskContent {
			t.Fatalf("fixture changed Task worktree authority before Review for %s: head=%s content=%q err=%v", resource.RepoID, taskHeads[resource.RepoID], taskContent, readErr)
		}
	}

	httpServer := httptest.NewServer(result.server.Handler())
	defer httpServer.Close()
	script := filepath.Join(repositoryRoot, "e2e", "task-delivery-browser.mjs")
	route := "/rooms/" + result.run.Room.ID + "/tasks/" + result.run.Task.ID + "/runs/" + result.run.ID
	command := exec.Command("node", script, httpServer.URL, route, result.resources[0].RepoID, result.resources[1].RepoID, filepath.Join(taskRoot, result.resources[0].WorkspaceDirectory()), filepath.Join(taskRoot, result.resources[1].WorkspaceDirectory()))
	command.Dir = repositoryRoot
	command.Env = os.Environ()
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("task branch full-delivery browser failed: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "TASK_BRANCH_FULL_DELIVERY_BROWSER_PASS") {
		t.Fatalf("browser omitted success witness: %s", output)
	}
	// A fresh service must recognize durable cleanup without recreating paths.
	installDeliveryTestService(t, result, taskRoot, hosting)
	handler := result.server.Handler()
	var continuity map[string]any
	requestJSON(t, handler, "GET", "/api/projects/"+result.run.Room.ID+"/continuity?taskId="+result.run.Task.ID, nil, 200, &continuity)
	if continuity["cleanedUp"] != true || continuity["ready"] != false || continuity["path"] != nil || continuity["applications"] != nil {
		t.Fatalf("cleaned continuity after restart: %#v", continuity)
	}
	opener := &recordingDirectoryOpener{}
	result.server.externalOpener = opener
	requestJSON(t, handler, "POST", "/api/projects/"+result.run.Room.ID+"/open-external", map[string]any{"taskId": result.run.Task.ID, "application": "terminal"}, 409, nil)
	if len(opener.paths) != 0 {
		t.Fatalf("cleaned Task opened another directory: %v", opener.paths)
	}
	var retained runView
	requestJSON(t, handler, "GET", "/api/runs/"+result.run.ID, nil, 200, &retained)
	for _, blocker := range retained.Blockers {
		if strings.Contains(blocker, "managed worktree is unavailable") {
			t.Fatalf("cleaned resource Task inherited legacy recovery advice: %s", blocker)
		}
	}

	for _, resource := range result.resources {
		original := result.originals[resource.RepoID]
		if got := runTaskWorktreeGitTest(t, original, "rev-parse", "HEAD"); got != originalHeads[resource.RepoID] {
			t.Fatalf("delivery changed original HEAD for %s: %s != %s", resource.RepoID, got, originalHeads[resource.RepoID])
		}
		if got := runTaskWorktreeGitTest(t, original, "ls-files", "--stage"); got != originalIndexes[resource.RepoID] {
			t.Fatalf("delivery changed original index for %s", resource.RepoID)
		}
		content, readErr := os.ReadFile(filepath.Join(original, "same.txt"))
		if readErr != nil || string(content) != originalFiles[resource.RepoID] {
			t.Fatalf("delivery changed original dirty file for %s: %q err=%v", resource.RepoID, content, readErr)
		}
		if _, statErr := os.Stat(filepath.Join(taskRoot, resource.WorkspaceDirectory())); !os.IsNotExist(statErr) {
			t.Fatalf("merged clean Task worktree for %s still exists: %v", resource.RepoID, statErr)
		}
		remoteHead := strings.TrimSpace(runTaskWorktreeGitTest(t, remotes[resource.RepoID], "rev-parse", "refs/heads/"+resource.TaskBranch))
		if message := runTaskWorktreeGitTest(t, remotes[resource.RepoID], "show", "-s", "--format=%B", remoteHead); !strings.Contains(message, "\n\nUpdate same.txt") {
			t.Fatalf("commit lost generated body: %q", message)
		}
		if remoteHead == "" || remoteHead == taskHeads[resource.RepoID] {
			t.Fatalf("Task branch was not committed and pushed for %s", resource.RepoID)
		}
		merged := strings.TrimSpace(runTaskWorktreeGitTest(t, remotes[resource.RepoID], "rev-parse", resource.BaseRef))
		if merged == resource.BaseCommit || merged == remoteHead {
			t.Fatalf("remote main has no merge commit for %s", resource.RepoID)
		}
		parents := strings.Fields(runTaskWorktreeGitTest(t, remotes[resource.RepoID], "show", "-s", "--format=%P", merged))
		if len(parents) != 2 || parents[0] != resource.BaseCommit || parents[1] != remoteHead {
			t.Fatalf("wrong merge ancestry: %v", parents)
		}
		if got := strings.TrimSpace(runTaskWorktreeGitTest(t, remotes[resource.RepoID], "show", merged+":same.txt")); got != "reviewed browser change for "+resource.RepoID {
			t.Fatalf("remote main lost reviewed change: %q", got)
		}
		if got := strings.TrimSpace(runTaskWorktreeGitTest(t, original, "rev-parse", resource.BaseRef)); got != resource.BaseCommit {
			t.Fatalf("original local main moved: %s", got)
		}
		if got := strings.TrimSpace(runTaskWorktreeGitTest(t, original, "rev-parse", "refs/heads/"+resource.TaskBranch)); got != remoteHead {
			t.Fatalf("local Task branch lost: %s", got)
		}
		if pr := hosting.pulls[resource.TaskBranch]; pr.State != "merged" || pr.Head != remoteHead || pr.MergeCommit != merged {
			t.Fatalf("mock provider did not retain merged PR for %s: %#v", resource.RepoID, pr)
		}
	}
}
