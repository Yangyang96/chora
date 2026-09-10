package localweb

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
	"github.com/Yangyang96/chora/internal/taskdelivery"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Hosting is simulated; Commit and Push still operate on real disposable Git.
type deliveryTestHosting struct {
	pr                    taskdelivery.PullRequest
	writes                int
	noMutation            bool
	uncertain             bool
	uncertainUnobservable bool
}

func (h *deliveryTestHosting) Available() bool { return true }
func (h *deliveryTestHosting) PreviewPR(ctx context.Context, p taskdelivery.PushPreview, title, body, marker string) (taskdelivery.HostingPreview, error) {
	return taskdelivery.HostingPreview{Kind: "pr", Repository: "test/repo", HeadBranch: p.Binding.Branch, BaseBranch: "main", Head: p.Head, BaseHead: p.TargetHead, Title: title, Body: body, Marker: marker, Digest: marker}, nil
}
func (h *deliveryTestHosting) PreviewMerge(ctx context.Context, p taskdelivery.HostingPreview, n int) (taskdelivery.HostingPreview, error) {
	if h.pr.State != "open" {
		return p, taskdelivery.ErrConflict
	}
	p.Kind = "merge"
	p.Number = n
	p.MergeMethod = "merge"
	return p, nil
}
func (h *deliveryTestHosting) Confirm(ctx context.Context, p taskdelivery.HostingPreview) (taskdelivery.PullRequest, error) {
	if h.noMutation {
		h.noMutation = false
		return taskdelivery.PullRequest{}, taskdelivery.ErrHostingMutationNotStarted
	}
	h.writes++
	if h.uncertainUnobservable {
		h.uncertainUnobservable = false
		return taskdelivery.PullRequest{}, taskdelivery.ErrRecovery
	}
	if p.Kind == "pr" {
		h.pr = taskdelivery.PullRequest{Repository: p.Repository, HeadBranch: p.HeadBranch, BaseBranch: p.BaseBranch, Head: p.Head, BaseHead: p.BaseHead, URL: "https://github.com/test/repo/pull/1", State: "open", Number: 1}
	} else {
		h.pr.State = "merged"
		h.pr.MergeCommit = p.Head
	}
	if h.uncertain {
		h.uncertain = false
		return taskdelivery.PullRequest{}, taskdelivery.ErrRecovery
	}
	return h.pr, nil
}
func (h *deliveryTestHosting) Observe(ctx context.Context, p taskdelivery.HostingPreview) (taskdelivery.PullRequest, error) {
	if h.pr.Number == 0 {
		return h.pr, taskdelivery.ErrRecovery
	}
	return h.pr, nil
}
func installDeliveryTestService(t *testing.T, result resourceTerminalScenarioResult, root string, hosting taskdelivery.Hosting) {
	t.Helper()
	data := filepath.Dir(filepath.Dir(root))
	w, err := newTaskResourceWorkspaceManager(result.server.store, data)
	if err != nil {
		t.Fatal(err)
	}
	patches, err := newResourcePatchStore(filepath.Join(data, "resource-patches"), result.server.store.Reader(), w)
	if err != nil {
		t.Fatal(err)
	}
	result.server.service = app.NewService(app.Dependencies{Store: result.server.store, TaskResourceWorkspaces: w, ResourcePatchMaterializer: patches, ResourceReviewVerifier: newResourceReviewVerifier(w), TaskDeliveryWorkspaces: newTaskDeliveryWorkspaceResolver(w), TaskDeliveryGit: taskdelivery.Git{}, TaskDeliveryHosting: hosting, Presence: localPresence{}, Authorizer: localAuthorizer{}})
}

func TestTaskDeliveryNoHostingMutationAllowsFreshExplicitConfirmation(t *testing.T) {
	var root string
	result := runResourceTerminalScenario(t, resourceTerminalScenario{futureDelivery: true, name: "hosting-no-mutation", repositoryCount: 1, checkMode: "none", completeAssistant: true, wantRunState: domain.RunStateAwaitingReview, change: func(t *testing.T, taskRoot string, resources []domain.TaskRepositoryResource) {
		root = taskRoot
		writeResourceTerminalFile(t, filepath.Join(root, resources[0].WorkspaceDirectory()), "same.txt", []byte("hosting no mutation\n"))
	}})
	host := &deliveryTestHosting{}
	installDeliveryTestService(t, result, root, host)
	repo := result.resources[0]
	original := result.originals[repo.RepoID]
	runTaskWorktreeGitTest(t, original, "config", "user.name", "Test")
	runTaskWorktreeGitTest(t, original, "config", "user.email", "test@example.test")
	bare := filepath.Join(t.TempDir(), "remote.git")
	runTaskWorktreeGitTest(t, original, "init", "--bare", bare)
	runTaskWorktreeGitTest(t, original, "remote", "add", "origin", bare)
	runTaskWorktreeGitTest(t, original, "push", "origin", repo.BaseRef+":"+repo.BaseRef)

	h := result.server.Handler()
	prefix := "/api/v2/runs/" + result.run.ID
	var accepted runView
	requestJSONWithHeaders(t, h, http.MethodPost, prefix+"/review", map[string]any{"expectedVersion": result.run.Version, "kind": "accept", "resultDigest": result.run.ResourceResult.Digest}, map[string]string{"Idempotency-Key": "no-mutation-accept"}, 200, &accepted)
	args := func() map[string]any {
		return map[string]any{"expectedVersion": accepted.Version, "resultDigest": accepted.ResourceResult.Digest, "repoId": repo.RepoID}
	}
	preview := func(kind, key string) app.DeliveryOperationView {
		body := args()
		body["kind"] = kind
		body["message"] = "Reviewed commit"
		body["remote"] = "origin"
		body["title"] = "Reviewed PR"
		body["body"] = "reviewed changes"
		var operation app.DeliveryOperationView
		requestJSONWithHeaders(t, h, http.MethodPost, prefix+"/delivery/preview", body, map[string]string{"Idempotency-Key": key}, 200, &operation)
		return operation
	}
	confirm := func(operation app.DeliveryOperationView) app.DeliveryOperationView {
		body := args()
		body["operationId"] = operation.ID
		var outcome app.DeliveryOperationView
		requestJSONWithHeaders(t, h, http.MethodPost, prefix+"/delivery/confirm", body, map[string]string{"Idempotency-Key": "confirm-" + operation.ID}, 200, &outcome)
		return outcome
	}
	if outcome := confirm(preview("commit", "no-mutation-commit")); outcome.Status != "succeeded" {
		t.Fatalf("commit %#v", outcome)
	}
	if outcome := confirm(preview("push", "no-mutation-push")); outcome.Status != "succeeded" {
		t.Fatalf("push %#v", outcome)
	}

	first := preview("pr", "no-mutation-pr")
	host.noMutation = true
	failed := confirm(first)
	if failed.Status != "failed" || !strings.Contains(failed.Reason, "provider mutation was not started") || !strings.Contains(failed.Reason, "fresh preview") || host.writes != 0 {
		t.Fatalf("no-mutation outcome %#v writes=%d", failed, host.writes)
	}
	if duplicate := confirm(first); duplicate.Status != "failed" || host.writes != 0 {
		t.Fatalf("failed confirmation replayed %#v writes=%d", duplicate, host.writes)
	}
	var delivery app.TaskDeliveryView
	requestJSON(t, h, http.MethodGet, prefix+"/delivery", nil, 200, &delivery)
	if len(delivery.Repositories) != 1 || delivery.Repositories[0].Reason != failed.Reason {
		t.Fatalf("current failure is not visible: %#v", delivery)
	}
	second := preview("pr", "no-mutation-pr-retry")
	if second.ID == first.ID {
		t.Fatal("fresh preview reused failed operation")
	}
	if succeeded := confirm(second); succeeded.Status != "succeeded" || succeeded.PRState != "open" || host.writes != 1 {
		t.Fatalf("fresh explicit confirmation %#v writes=%d", succeeded, host.writes)
	}
	assertRecovered := func() {
		t.Helper()
		var delivery app.TaskDeliveryView
		requestJSON(t, result.server.Handler(), http.MethodGet, prefix+"/delivery", nil, 200, &delivery)
		if len(delivery.Repositories) != 1 || delivery.Repositories[0].Status != "pr_open" || delivery.Repositories[0].Reason != "" {
			t.Fatalf("recovered repository still shows a failure: %#v", delivery)
		}
		for _, operation := range delivery.Repositories[0].Operations {
			if operation.ID == first.ID && operation.Status == "failed" && operation.Reason == failed.Reason {
				return
			}
		}
		t.Fatal("recovery erased the original failed operation history")
	}
	assertRecovered()
	installDeliveryTestService(t, result, root, host)
	assertRecovered()
	h = result.server.Handler()
	merge := preview("merge", "no-mutation-merge")
	host.noMutation = true
	mergeFailed := confirm(merge)
	if mergeFailed.Status != "failed" {
		t.Fatalf("merge should fail before mutation: %#v", mergeFailed)
	}
	if outcome := confirm(preview("push", "lower-stage-push")); outcome.Status != "succeeded" {
		t.Fatalf("duplicate push: %#v", outcome)
	}
	var afterPush app.TaskDeliveryView
	requestJSON(t, h, http.MethodGet, prefix+"/delivery", nil, 200, &afterPush)
	if afterPush.Repositories[0].Status != "pr_open" || afterPush.Repositories[0].Reason != mergeFailed.Reason {
		t.Fatalf("lower-stage success hid merge failure: %#v", afterPush)
	}
	if outcome := confirm(preview("merge", "no-mutation-merge-retry")); outcome.Status != "succeeded" {
		t.Fatalf("fresh merge: %#v", outcome)
	}
	var afterMerge app.TaskDeliveryView
	requestJSON(t, h, http.MethodGet, prefix+"/delivery", nil, 200, &afterMerge)
	if afterMerge.Repositories[0].Status != "merged" || afterMerge.Repositories[0].Reason != "" {
		t.Fatalf("successful merge did not clear warning: %#v", afterMerge)
	}
	for _, operation := range afterMerge.Repositories[0].Operations {
		if operation.ID == merge.ID && operation.Status == "failed" && operation.Reason == mergeFailed.Reason {
			return
		}
	}
	t.Fatal("merge retry erased failed operation history")
}

func TestTaskDeliveryGitHubLifecycleReconcileAndCleanup(t *testing.T) {
	var root string
	result := runResourceTerminalScenario(t, resourceTerminalScenario{futureDelivery: true, name: "full-delivery", repositoryCount: 2, checkMode: "none", completeAssistant: true, wantRunState: domain.RunStateAwaitingReview, change: func(t *testing.T, r string, rs []domain.TaskRepositoryResource) {
		root = r
		for _, repo := range rs {
			writeResourceTerminalFile(t, filepath.Join(r, repo.WorkspaceDirectory()), "same.txt", []byte("full delivery\n"))
		}
	}})
	host := &deliveryTestHosting{}
	installDeliveryTestService(t, result, root, host)
	repo := result.resources[0]
	original := result.originals[repo.RepoID]
	worktree := filepath.Join(root, repo.WorkspaceDirectory())
	runTaskWorktreeGitTest(t, original, "config", "user.name", "Test")
	runTaskWorktreeGitTest(t, original, "config", "user.email", "test@example.test")
	bare := filepath.Join(t.TempDir(), "remote.git")
	runTaskWorktreeGitTest(t, original, "init", "--bare", bare)
	runTaskWorktreeGitTest(t, original, "remote", "add", "origin", bare)
	runTaskWorktreeGitTest(t, original, "push", "origin", repo.BaseRef+":"+repo.BaseRef)
	writeResourceTerminalFile(t, original, "unrelated.txt", []byte("staged original\n"))
	runTaskWorktreeGitTest(t, original, "add", "unrelated.txt")
	writeResourceTerminalFile(t, original, "same.txt", []byte("dirty original\n"))
	index := runTaskWorktreeGitTest(t, original, "ls-files", "--stage")
	head := runTaskWorktreeGitTest(t, original, "rev-parse", "HEAD")
	h := result.server.Handler()
	prefix := "/api/v2/runs/" + result.run.ID
	var accepted runView
	requestJSONWithHeaders(t, h, "POST", prefix+"/review", map[string]any{"expectedVersion": result.run.Version, "kind": "accept", "resultDigest": result.run.ResourceResult.Digest}, map[string]string{"Idempotency-Key": "full-accept"}, 200, &accepted)
	args := func() map[string]any {
		return map[string]any{"expectedVersion": accepted.Version, "resultDigest": accepted.ResourceResult.Digest, "repoId": repo.RepoID}
	}
	preview := func(kind, key string) app.DeliveryOperationView {
		body := args()
		body["kind"] = kind
		body["message"] = "Reviewed commit"
		body["remote"] = "origin"
		body["title"] = "Reviewed PR"
		body["body"] = "reviewed changes"
		var p app.DeliveryOperationView
		requestJSONWithHeaders(t, h, "POST", prefix+"/delivery/preview", body, map[string]string{"Idempotency-Key": key}, 200, &p)
		return p
	}
	confirm := func(p app.DeliveryOperationView) app.DeliveryOperationView {
		body := args()
		body["operationId"] = p.ID
		var out app.DeliveryOperationView
		requestJSONWithHeaders(t, h, "POST", prefix+"/delivery/confirm", body, map[string]string{"Idempotency-Key": "confirm-" + p.ID}, 200, &out)
		return out
	}
	runID, _ := domain.ParseRunID(accepted.ID)
	assertSummary := func(want string) {
		t.Helper()
		// Summary must also work with no filesystem, materializer or hosting adapter.
		run, err := result.server.store.Reader().GetRun(context.Background(), runID)
		if err != nil {
			t.Fatal(err)
		}
		summary, err := app.NewService(app.Dependencies{Store: result.server.store}).LoadTaskDeliverySummary(context.Background(), run)
		if err != nil || summary == nil || len(summary.Repositories) != 2 || summary.Repositories[0].Status != want || summary.Repositories[1].Status != "uncommitted" {
			t.Fatalf("durable summary for %s: %#v %v", want, summary, err)
		}
		var workspace roomWorkspaceView
		requestJSON(t, result.server.Handler(), "GET", "/api/rooms/"+accepted.Room.ID+"/workspace", nil, 200, &workspace)
		if len(workspace.Tasks) != 1 || workspace.Tasks[0].Delivery == nil || workspace.Tasks[0].Delivery.Unavailable || workspace.Tasks[0].Delivery.Repositories[0].Status != want {
			t.Fatalf("task list omitted %s: %#v", want, workspace.Tasks)
		}
	}
	commitPreview := preview("commit", "commit")
	assertSummary("uncommitted")
	for _, meta := range []app.CommandMeta{{ActorID: "another-owner", SessionID: localSession, IdempotencyKey: "other-actor"}, {ActorID: localActor, SessionID: "another-session", IdempotencyKey: "other-session"}} {
		_, err := result.server.service.ConfirmTaskDelivery(context.Background(), app.DeliveryRequest{CommandMeta: meta, RunID: runID, ExpectedVersion: accepted.Version, ResultDigest: accepted.ResourceResult.Digest, OperationID: commitPreview.ID})
		if !errors.Is(err, app.ErrInvalidCommand) {
			t.Fatalf("cross-session confirmation allowed: %v", err)
		}
	}
	secondPreview := preview("commit", "second-preview")
	if out := confirm(commitPreview); out.Status != "succeeded" {
		t.Fatalf("commit %#v", out)
	}
	assertSummary("committed")
	_, err := result.server.service.ConfirmTaskDelivery(context.Background(), app.DeliveryRequest{CommandMeta: app.CommandMeta{ActorID: localActor, SessionID: localSession, IdempotencyKey: "confirm-" + commitPreview.ID}, RunID: runID, ExpectedVersion: accepted.Version, ResultDigest: accepted.ResourceResult.Digest, OperationID: secondPreview.ID})
	if !errors.Is(err, storecontract.ErrIdempotencyConflict) {
		t.Fatalf("reused confirmation key: %v", err)
	}
	if out := confirm(preview("push", "push")); out.Status != "succeeded" {
		t.Fatalf("push %#v", out)
	}
	assertSummary("pushed")

	p := preview("pr", "pr")
	host.uncertain = true
	out := confirm(p)
	if out.Status != "recovery_required" {
		t.Fatalf("PR uncertainty %#v", out)
	}
	assertSummary("recovery_required")
	confirm(p)
	if host.writes != 1 {
		t.Fatal("replayed PR")
	}
	// New service reads the durable intent; refresh observes, it never creates PR.
	installDeliveryTestService(t, result, root, host)
	var view app.TaskDeliveryView
	requestJSONWithHeaders(t, h, "POST", prefix+"/delivery/refresh", args(), map[string]string{"Idempotency-Key": "reconcile-pr"}, 200, &view)
	if view.Repositories[0].Status != "pr_open" || view.Repositories[1].Status != "uncommitted" || host.writes != 1 {
		t.Fatalf("partial/recovery %#v", view)
	}
	assertSummary("pr_open")
	host.pr.State = "closed"
	requestJSONWithHeaders(t, h, "POST", prefix+"/delivery/refresh", args(), map[string]string{"Idempotency-Key": "closed"}, 200, &view)
	if view.Repositories[0].Status != "pr_closed" {
		t.Fatal("closed not observed")
	}
	assertSummary("pr_closed")
	body := args()
	body["kind"] = "cleanup"
	requestJSONWithHeaders(t, h, "POST", prefix+"/delivery/preview", body, map[string]string{"Idempotency-Key": "closed-no-clean"}, 409, nil)
	host.pr.State = "open"
	mergePreview := preview("merge", "merge")
	if mergePreview.MergeMethod != "merge" || mergePreview.PRNumber != 1 || mergePreview.Head != host.pr.Head {
		t.Fatalf("incomplete merge preview: %#v", mergePreview)
	}
	host.uncertainUnobservable = true
	out = confirm(mergePreview)
	if out.Status != "recovery_required" || host.writes != 2 || host.pr.State != "open" {
		t.Fatalf("uncertain merge %#v writes=%d pr=%#v", out, host.writes, host.pr)
	}
	confirm(mergePreview)
	if host.writes != 2 {
		t.Fatal("replayed uncertain merge")
	}
	requestJSONWithHeaders(t, h, "POST", prefix+"/delivery/preview", map[string]any{"expectedVersion": accepted.Version, "resultDigest": accepted.ResourceResult.Digest, "repoId": repo.RepoID, "kind": "merge"}, map[string]string{"Idempotency-Key": "blocked-merge"}, 409, nil)
	requestJSONWithHeaders(t, h, "POST", prefix+"/delivery/refresh", args(), map[string]string{"Idempotency-Key": "open-merge"}, 200, &view)
	if view.Repositories[0].Status != "recovery_required" || host.writes != 2 {
		t.Fatalf("open merge uncertainty replayed or cleared %#v", view)
	}
	host.pr.State = "merged"
	host.pr.MergeCommit = host.pr.Head
	requestJSONWithHeaders(t, h, "POST", prefix+"/delivery/refresh", args(), map[string]string{"Idempotency-Key": "merged-after-uncertainty"}, 200, &view)
	if view.Repositories[0].Status != "merged" || host.writes != 2 {
		t.Fatalf("merged observation did not reconcile %#v", view)
	}
	requestJSON(t, h, http.MethodGet, prefix+"/delivery", nil, 200, &view)
	if view.Repositories[0].Status != "merged" {
		t.Fatalf("merge not projected: %#v", view)
	}
	assertSummary("merged")
	if _, err := os.Stat(worktree); err != nil {
		t.Fatalf("merge removed worktree: %v", err)
	}
	// Independent edits block cleanup without changing those files.
	writeResourceTerminalFile(t, worktree, "unsaved.txt", []byte("retain\n"))
	requestJSONWithHeaders(t, h, "POST", prefix+"/delivery/preview", body, map[string]string{"Idempotency-Key": "dirty-no-clean"}, 409, nil)
	if got, err := os.ReadFile(filepath.Join(worktree, "unsaved.txt")); err != nil || string(got) != "retain\n" {
		t.Fatalf("blocked cleanup lost extra file: %q %v", got, err)
	}
	os.Remove(filepath.Join(worktree, "unsaved.txt"))
	out = confirm(preview("cleanup", "cleanup"))
	if out.Status != "succeeded" {
		t.Fatalf("cleanup %#v", out)
	}
	installDeliveryTestService(t, result, root, host)
	requestJSON(t, h, http.MethodGet, prefix+"/delivery", nil, 200, &view)
	if view.Repositories[0].Status != "cleaned" || view.Repositories[1].Status != "uncommitted" {
		t.Fatalf("cleanup projection %#v", view)
	}
	assertSummary("cleaned")
	requestJSONWithHeaders(t, h, "POST", prefix+"/delivery/refresh", args(), map[string]string{"Idempotency-Key": "cleaned-refresh"}, 200, &view)
	if view.Repositories[0].Status != "cleaned" || view.Repositories[1].Status != "uncommitted" {
		t.Fatalf("refresh lost per-repo cleanup state: %#v", view)
	}
	if view.Repositories[0].Commit == "" || view.Repositories[0].PRURL != host.pr.URL {
		t.Fatal("cleanup lost commit/PR link")
	}
	completed := map[string]bool{}
	for _, operation := range view.Repositories[0].Operations {
		if operation.Status == "succeeded" {
			completed[operation.Kind] = true
		}
	}
	for _, kind := range []string{"commit", "push", "pr", "merge", "cleanup"} {
		if !completed[kind] {
			t.Fatalf("cleanup lost %s history", kind)
		}
	}
	var retainedResult map[string]any
	requestJSON(t, h, http.MethodGet, prefix+"/result", nil, 200, &retainedResult)
	if retainedResult["digest"] != accepted.ResourceResult.Digest {
		t.Fatal("cleanup lost immutable Review result")
	}
	if got := strings.TrimSpace(runTaskWorktreeGitTest(t, original, "rev-parse", "refs/heads/"+repo.TaskBranch)); got != host.pr.Head {
		t.Fatal("cleanup removed local Task branch")
	}
	if got := strings.TrimSpace(runTaskWorktreeGitTest(t, bare, "rev-parse", "refs/heads/"+repo.TaskBranch)); got != host.pr.Head {
		t.Fatal("cleanup removed remote Task branch")
	}
	other := result.resources[1]
	otherRoot := filepath.Join(root, other.WorkspaceDirectory())
	if got := strings.TrimSpace(runTaskWorktreeGitTest(t, otherRoot, "rev-parse", "HEAD")); got != other.BaseCommit {
		t.Fatal("delivery advanced another repository")
	}
	if got, err := os.ReadFile(filepath.Join(otherRoot, "same.txt")); err != nil || string(got) != "full delivery\n" {
		t.Fatalf("delivery changed another repository: %q %v", got, err)
	}
	events, err := result.server.store.Reader().ListRunEvents(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	confirmations := 0
	for _, event := range events {
		if event.Type() != "task_delivery.confirmed" {
			continue
		}
		confirmations++
		var audit map[string]string
		if err := json.Unmarshal(event.NormalizedJSON(), &audit); err != nil {
			t.Fatal(err)
		}
		if audit["actor_id"] != localActor || audit["session_id"] != localSession || audit["request_digest"] == "" || audit["operation_id"] == "" {
			t.Fatalf("confirmation audit %#v", audit)
		}
	}
	if confirmations != 5 {
		t.Fatalf("confirmation audit replay/missing count %d", confirmations)
	}
	if _, err := os.Stat(worktree); !os.IsNotExist(err) {
		t.Fatal("worktree remains")
	}
	if runTaskWorktreeGitTest(t, original, "rev-parse", "HEAD") != head || runTaskWorktreeGitTest(t, original, "ls-files", "--stage") != index {
		t.Fatal("original changed")
	}
	if raw, _ := os.ReadFile(filepath.Join(original, "same.txt")); string(raw) != "dirty original\n" {
		t.Fatal("dirty original lost")
	}
	taskID, _ := domain.ParseTaskID(result.run.Task.ID)
	resolver, err := newTaskResourceWorkspaceManager(result.server.store, filepath.Dir(filepath.Dir(root)))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := resolver.(*taskResourceWorkspaceManager).ResolveHandoffRoot(context.Background(), taskID); err != nil || got != root {
		t.Fatalf("partial cleanup blocked remaining handoff: %q %v", got, err)
	}
	if _, err := resolver.ResolveExecutionRoot(context.Background(), taskID); err == nil {
		t.Fatal("partial cleanup weakened execution proof")
	}
	// A cleanup record must not conceal a newly created path or symlink.
	for _, replacement := range []string{"directory", "symlink"} {
		t.Run(replacement, func(t *testing.T) {
			var err error
			if replacement == "directory" {
				err = os.Mkdir(worktree, 0700)
			} else {
				err = os.Symlink(otherRoot, worktree)
			}
			if err != nil {
				t.Fatal(err)
			}
			defer os.Remove(worktree)
			if got, err := resolver.(*taskResourceWorkspaceManager).ResolveHandoffRoot(context.Background(), taskID); err == nil || got != "" {
				t.Fatalf("replacement path exposed: %q %v", got, err)
			}
			if _, err := os.Lstat(worktree); err != nil {
				t.Fatalf("readiness check removed replacement: %v", err)
			}
		})
	}
	if err := os.Rename(otherRoot, otherRoot+"-moved"); err != nil {
		t.Fatal(err)
	}
	defer os.Rename(otherRoot+"-moved", otherRoot)
	if got, err := resolver.(*taskResourceWorkspaceManager).ResolveHandoffRoot(context.Background(), taskID); err == nil || got != "" || errors.Is(err, errTaskResourceWorkspacesCleaned) {
		t.Fatalf("unrecorded missing worktree accepted as cleaned: %q %v", got, err)
	}
}
