package localweb

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/taskdelivery"
)

func TestTaskDeliveryReviewedCommitPushPreservesOriginalsAndPerRepositoryState(t *testing.T) {
	var taskRoot string
	result := runResourceTerminalScenario(t, resourceTerminalScenario{futureDelivery: true, name: "branch-delivery", repositoryCount: 2, checkMode: "none", completeAssistant: true, wantRunState: domain.RunStateAwaitingReview,
		change: func(t *testing.T, root string, resources []domain.TaskRepositoryResource) {
			taskRoot = root
			for _, r := range resources {
				writeResourceTerminalFile(t, filepath.Join(root, r.WorkspaceDirectory()), "same.txt", []byte("reviewed Task branch change\n"))
			}
		},
	})
	handler := result.server.Handler()
	run := result.run
	prefix := "/api/v2/runs/" + run.ID
	for _, r := range result.resources {
		if r.DeliveryMode != "task_branch" || r.TaskBranch == "" {
			t.Fatalf("new Task missing branch authority: %#v", r)
		}
		original := result.originals[r.RepoID]
		runTaskWorktreeGitTest(t, original, "config", "user.name", "Chora Delivery Test")
		runTaskWorktreeGitTest(t, original, "config", "user.email", "delivery@example.test")
		writeResourceTerminalFile(t, original, "unrelated.txt", []byte("preexisting staged file\n"))
		runTaskWorktreeGitTest(t, original, "add", "unrelated.txt")
		writeResourceTerminalFile(t, original, "same.txt", []byte("original checkout unsaved edit\n"))
	}
	var accepted runView
	requestJSONWithHeaders(t, handler, http.MethodPost, prefix+"/review", map[string]any{"expectedVersion": run.Version, "kind": "accept", "comment": "reviewed only", "resultDigest": run.ResourceResult.Digest}, map[string]string{"Idempotency-Key": "delivery-review"}, 200, &accepted)
	if accepted.Controls.CanAcceptAndApply {
		t.Fatal("new task branch still auto-Applies")
	}
	var before app.TaskDeliveryView
	requestJSON(t, handler, http.MethodGet, prefix+"/delivery", nil, 200, &before)
	if len(before.Repositories) != 2 || before.Repositories[0].Status != "uncommitted" {
		t.Fatalf("delivery before=%#v", before)
	}
	repo := result.resources[0]
	original := result.originals[repo.RepoID]
	originalHead := runTaskWorktreeGitTest(t, original, "rev-parse", "HEAD")
	originalIndex := runTaskWorktreeGitTest(t, original, "ls-files", "--stage")
	previewBody := map[string]any{"repoId": repo.RepoID, "expectedVersion": accepted.Version, "resultDigest": accepted.ResourceResult.Digest, "kind": "commit", "message": "Commit reviewed Task changes"}
	var preview app.DeliveryOperationView
	requestJSONWithHeaders(t, handler, http.MethodPost, prefix+"/delivery/preview", previewBody, map[string]string{"Idempotency-Key": "delivery-commit-preview"}, 200, &preview)
	if preview.Status != "preview" || len(preview.Paths) != 1 || preview.Message == "reviewed only" {
		t.Fatalf("preview=%#v", preview)
	}
	confirm := map[string]any{"operationId": preview.ID, "expectedVersion": accepted.Version, "resultDigest": accepted.ResourceResult.Digest}
	var committed app.DeliveryOperationView
	requestJSONWithHeaders(t, handler, http.MethodPost, prefix+"/delivery/confirm", confirm, map[string]string{"Idempotency-Key": "delivery-commit-confirm"}, 200, &committed)
	if committed.Status != "succeeded" || committed.Commit == "" {
		t.Fatalf("commit=%#v", committed)
	}
	var duplicate app.DeliveryOperationView
	requestJSONWithHeaders(t, handler, http.MethodPost, prefix+"/delivery/confirm", confirm, map[string]string{"Idempotency-Key": "different-key-same-intent"}, 200, &duplicate)
	if duplicate.Commit != committed.Commit || duplicate.Version != committed.Version {
		t.Fatalf("duplicate re-executed: %#v", duplicate)
	}
	if runTaskWorktreeGitTest(t, original, "rev-parse", "HEAD") != originalHead || runTaskWorktreeGitTest(t, original, "ls-files", "--stage") != originalIndex {
		t.Fatal("original HEAD/index changed")
	}
	if b, _ := os.ReadFile(filepath.Join(original, "same.txt")); string(b) != "original checkout unsaved edit\n" {
		t.Fatal("original unsaved file changed")
	}
	var after app.TaskDeliveryView
	requestJSON(t, handler, http.MethodGet, prefix+"/delivery", nil, 200, &after)
	if after.Repositories[0].Status != "committed" || after.Repositories[1].Status != "uncommitted" {
		t.Fatalf("cross repository state collapsed: %#v", after)
	}
	bare := filepath.Join(t.TempDir(), "remote.git")
	runTaskWorktreeGitTest(t, original, "init", "--bare", bare)
	runTaskWorktreeGitTest(t, original, "remote", "add", "delivery-test", bare)
	runTaskWorktreeGitTest(t, original, "push", "delivery-test", repo.BaseRef+":"+repo.BaseRef)
	pushBody := map[string]any{"repoId": repo.RepoID, "expectedVersion": accepted.Version, "resultDigest": accepted.ResourceResult.Digest, "kind": "push", "remote": "delivery-test"}
	var push app.DeliveryOperationView
	requestJSONWithHeaders(t, handler, http.MethodPost, prefix+"/delivery/preview", pushBody, map[string]string{"Idempotency-Key": "delivery-push-preview"}, 200, &push)
	if push.Head != committed.Commit || push.RemoteRef != "refs/heads/"+repo.TaskBranch || push.URL != bare {
		t.Fatalf("push destination=%#v", push)
	}
	confirm["operationId"] = push.ID
	var pushed app.DeliveryOperationView
	requestJSONWithHeaders(t, handler, http.MethodPost, prefix+"/delivery/confirm", confirm, map[string]string{"Idempotency-Key": "delivery-push-confirm"}, 200, &pushed)
	if pushed.Status != "succeeded" {
		t.Fatalf("push=%#v", pushed)
	}
	if strings.TrimSpace(runTaskWorktreeGitTest(t, bare, "rev-parse", "refs/heads/"+repo.TaskBranch)) != committed.Commit {
		t.Fatal("bare remote wrong commit")
	}
	if strings.TrimSpace(runTaskWorktreeGitTest(t, bare, "rev-parse", repo.BaseRef)) != repo.BaseCommit {
		t.Fatal("Push changed target branch")
	}
	requestJSON(t, handler, http.MethodGet, prefix+"/delivery", nil, 200, &after)
	if after.Repositories[0].Status != "pushed" || after.Repositories[1].Status != "uncommitted" {
		t.Fatalf("push falsely merged/group-completed: %#v", after)
	}
	// A new service instance reconstructs exact immutable result and delivery solely
	// from persisted operations; no process-local success cache or Git replay.
	workspaces, e := newTaskResourceWorkspaceManager(result.server.store, filepath.Dir(filepath.Dir(taskRoot)))
	if e != nil {
		t.Fatal(e)
	}
	patches, e := newResourcePatchStore(filepath.Join(filepath.Dir(filepath.Dir(taskRoot)), "resource-patches"), result.server.store.Reader(), workspaces)
	if e != nil {
		t.Fatal(e)
	}
	reopened := app.NewService(app.Dependencies{Store: result.server.store, ResourcePatchMaterializer: patches})
	id, _ := domain.ParseRunID(run.ID)
	reloaded, e := reopened.LoadTaskDelivery(context.Background(), id)
	if e != nil {
		t.Fatal(e)
	}
	a, _ := json.Marshal(after)
	b, _ := json.Marshal(reloaded)
	if string(a) != string(b) {
		t.Fatalf("reopened delivery changed: %s != %s", a, b)
	}
}

func TestTaskDeliveryStalePreviewCannotCommitChangedContent(t *testing.T) {
	var taskRoot string
	result := runResourceTerminalScenario(t, resourceTerminalScenario{futureDelivery: true, name: "delivery-drift", repositoryCount: 1, checkMode: "none", completeAssistant: true, wantRunState: domain.RunStateAwaitingReview, change: func(t *testing.T, root string, resources []domain.TaskRepositoryResource) {
		taskRoot = root
		writeResourceTerminalFile(t, filepath.Join(root, resources[0].WorkspaceDirectory()), "same.txt", []byte("reviewed\n"))
	}})
	h := result.server.Handler()
	prefix := "/api/v2/runs/" + result.run.ID
	r := result.resources[0]
	var accepted runView
	requestJSONWithHeaders(t, h, http.MethodPost, prefix+"/review", map[string]any{"expectedVersion": result.run.Version, "kind": "accept", "resultDigest": result.run.ResourceResult.Digest}, map[string]string{"Idempotency-Key": "drift-review"}, 200, &accepted)
	body := map[string]any{"repoId": r.RepoID, "expectedVersion": accepted.Version, "resultDigest": accepted.ResourceResult.Digest, "kind": "commit", "message": "reviewed"}
	worktree := filepath.Join(taskRoot, r.WorkspaceDirectory())
	writeResourceTerminalFile(t, worktree, "same.txt", []byte("unreviewed edit after Review\n"))
	requestJSONWithHeaders(t, h, http.MethodPost, prefix+"/delivery/preview", body, map[string]string{"Idempotency-Key": "drift-before-preview"}, 409, nil)
	if got, err := os.ReadFile(filepath.Join(worktree, "same.txt")); err != nil || string(got) != "unreviewed edit after Review\n" {
		t.Fatalf("rejected preview changed file: %q %v", got, err)
	}
	if got := strings.TrimSpace(runTaskWorktreeGitTest(t, worktree, "rev-parse", "HEAD")); got != r.BaseCommit {
		t.Fatal("rejected preview created a commit")
	}
	writeResourceTerminalFile(t, worktree, "same.txt", []byte("reviewed\n"))
	var preview app.DeliveryOperationView
	requestJSONWithHeaders(t, h, http.MethodPost, prefix+"/delivery/preview", body, map[string]string{"Idempotency-Key": "drift-preview"}, 200, &preview)
	writeResourceTerminalFile(t, filepath.Join(taskRoot, r.WorkspaceDirectory()), "same.txt", []byte("unreviewed edit after preview\n"))
	var rejected app.DeliveryOperationView
	requestJSONWithHeaders(t, h, http.MethodPost, prefix+"/delivery/confirm", map[string]any{"operationId": preview.ID, "expectedVersion": accepted.Version, "resultDigest": accepted.ResourceResult.Digest}, map[string]string{"Idempotency-Key": "drift-confirm"}, 200, &rejected)
	if rejected.Status != "failed" {
		t.Fatalf("stale preview not rejected: %#v", rejected)
	}
	if got, err := os.ReadFile(filepath.Join(worktree, "same.txt")); err != nil || string(got) != "unreviewed edit after preview\n" {
		t.Fatalf("rejected confirmation changed file: %q %v", got, err)
	}
	if strings.TrimSpace(runTaskWorktreeGitTest(t, filepath.Join(taskRoot, r.WorkspaceDirectory()), "rev-parse", "HEAD")) != r.BaseCommit {
		t.Fatal("stale preview created commit")
	}
}

type uncertainDeliveryGit struct {
	taskdelivery.Git
	commitCalls, pushCalls int
}

func (g *uncertainDeliveryGit) Commit(ctx context.Context, p taskdelivery.CommitPreview) (taskdelivery.CommitResult, error) {
	g.commitCalls++
	result, e := g.Git.Commit(ctx, p)
	if e != nil {
		return result, e
	}
	return taskdelivery.CommitResult{}, taskdelivery.ErrRecovery
}
func (g *uncertainDeliveryGit) Push(ctx context.Context, p taskdelivery.PushPreview) error {
	g.pushCalls++
	if e := g.Git.Push(ctx, p); e != nil {
		return e
	}
	return taskdelivery.ErrRecovery
}

func TestTaskDeliveryReconcilesUnknownCommitAndPushWithoutReplay(t *testing.T) {
	var root string
	fixture := runResourceTerminalScenario(t, resourceTerminalScenario{futureDelivery: true, name: "delivery-uncertain", repositoryCount: 1, checkMode: "none", completeAssistant: true, wantRunState: domain.RunStateAwaitingReview, change: func(t *testing.T, taskRoot string, resources []domain.TaskRepositoryResource) {
		root = taskRoot
		writeResourceTerminalFile(t, filepath.Join(root, resources[0].WorkspaceDirectory()), "same.txt", []byte("reviewed uncertain outcome\n"))
	}})
	r := fixture.resources[0]
	original := fixture.originals[r.RepoID]
	runTaskWorktreeGitTest(t, original, "config", "user.name", "Chora Delivery Test")
	runTaskWorktreeGitTest(t, original, "config", "user.email", "delivery@example.test")
	bare := filepath.Join(t.TempDir(), "remote.git")
	runTaskWorktreeGitTest(t, original, "init", "--bare", bare)
	runTaskWorktreeGitTest(t, original, "remote", "add", "origin", bare)
	runTaskWorktreeGitTest(t, original, "push", "origin", r.BaseRef+":"+r.BaseRef)
	prefix := "/api/v2/runs/" + fixture.run.ID
	h := fixture.server.Handler()
	var accepted runView
	requestJSONWithHeaders(t, h, http.MethodPost, prefix+"/review", map[string]any{"expectedVersion": fixture.run.Version, "kind": "accept", "resultDigest": fixture.run.ResourceResult.Digest}, map[string]string{"Idempotency-Key": "uncertain-review"}, 200, &accepted)
	dataRoot := filepath.Dir(filepath.Dir(root))
	workspaces, e := newTaskResourceWorkspaceManager(fixture.server.store, dataRoot)
	if e != nil {
		t.Fatal(e)
	}
	patches, e := newResourcePatchStore(filepath.Join(dataRoot, "resource-patches"), fixture.server.store.Reader(), workspaces)
	if e != nil {
		t.Fatal(e)
	}
	faulty := &uncertainDeliveryGit{}
	newService := func(g app.TaskDeliveryGit) *app.Service {
		return app.NewService(app.Dependencies{Store: fixture.server.store, ResourcePatchMaterializer: patches, TaskDeliveryWorkspaces: newTaskDeliveryWorkspaceResolver(workspaces), TaskDeliveryGit: g, Authorizer: localAuthorizer{}})
	}
	fixture.server.service = newService(faulty)
	for _, kind := range []string{"commit", "push"} {
		body := map[string]any{"repoId": r.RepoID, "expectedVersion": accepted.Version, "resultDigest": accepted.ResourceResult.Digest, "kind": kind, "message": "reviewed exact result", "remote": "origin"}
		var preview app.DeliveryOperationView
		requestJSONWithHeaders(t, h, http.MethodPost, prefix+"/delivery/preview", body, map[string]string{"Idempotency-Key": "uncertain-preview-" + kind}, 200, &preview)
		confirm := map[string]any{"operationId": preview.ID, "expectedVersion": accepted.Version, "resultDigest": accepted.ResourceResult.Digest}
		var unknown app.DeliveryOperationView
		requestJSONWithHeaders(t, h, http.MethodPost, prefix+"/delivery/confirm", confirm, map[string]string{"Idempotency-Key": "uncertain-confirm-" + kind}, 200, &unknown)
		if unknown.Status != "recovery_required" {
			t.Fatalf("uncertain %s became %s", kind, unknown.Status)
		}
		// A replacement service has no process-local mutation memory. Confirming the
		// same durable intent must not call Git again, even with a different key.
		fixture.server.service = newService(faulty)
		requestJSONWithHeaders(t, h, http.MethodPost, prefix+"/delivery/confirm", confirm, map[string]string{"Idempotency-Key": "duplicate-" + kind}, 200, &unknown)
		if faulty.commitCalls != 1 || (kind == "push" && faulty.pushCalls != 1) {
			t.Fatalf("mutation replayed commit=%d push=%d", faulty.commitCalls, faulty.pushCalls)
		}
		var refreshed app.TaskDeliveryView
		requestJSONWithHeaders(t, h, http.MethodPost, prefix+"/delivery/refresh", map[string]any{"repoId": r.RepoID, "expectedVersion": accepted.Version, "resultDigest": accepted.ResourceResult.Digest}, map[string]string{"Idempotency-Key": "reconcile-" + kind}, 200, &refreshed)
		want := "committed"
		if kind == "push" {
			want = "pushed"
		}
		if refreshed.Repositories[0].Status != want {
			t.Fatalf("refresh %s=%#v", kind, refreshed)
		}
		if faulty.commitCalls != 1 || (kind == "push" && faulty.pushCalls != 1) {
			t.Fatal("refresh replayed side effect")
		}
	}
}

func TestTaskDeliveryReferenceRepositoryIsNotLegacy(t *testing.T) {
	result := runResourceTerminalScenario(t, resourceTerminalScenario{futureDelivery: true, name: "delivery-reference", repositoryCount: 2, referenceLast: true, checkMode: "none", completeAssistant: true, wantRunState: domain.RunStateAwaitingReview, change: func(t *testing.T, root string, resources []domain.TaskRepositoryResource) {
		for _, r := range resources {
			if r.Role == "write" {
				writeResourceTerminalFile(t, filepath.Join(root, r.WorkspaceDirectory()), "same.txt", []byte("reviewed change\n"))
			}
		}
	}})
	var view app.TaskDeliveryView
	requestJSON(t, result.server.Handler(), http.MethodGet, "/api/v2/runs/"+result.run.ID+"/delivery", nil, 200, &view)
	for i, r := range result.resources {
		if r.Role == "reference" && (view.Repositories[i].Status != "no_change" || view.Repositories[i].TaskBranch != "") {
			t.Fatalf("reference mislabeled: %#v", view.Repositories[i])
		}
	}
}

// Review itself stays non-mutating while delivery requires separate confirmation.
func TestTaskBranchReviewStageAllowsDeliveryPreviewAndRetainsEvidence(t *testing.T) {
	result := runResourceTerminalScenario(t, resourceTerminalScenario{name: "review-stage", repositoryCount: 1, checkMode: "none", completeAssistant: true, wantRunState: domain.RunStateAwaitingReview, change: func(t *testing.T, root string, resources []domain.TaskRepositoryResource) {
		writeResourceTerminalFile(t, filepath.Join(root, resources[0].WorkspaceDirectory()), "same.txt", []byte("retained reviewed edit\n"))
	}})
	h := result.server.Handler()
	prefix := "/api/v2/runs/" + result.run.ID
	var accepted runView
	requestJSONWithHeaders(t, h, http.MethodPost, prefix+"/review", map[string]any{"expectedVersion": result.run.Version, "kind": "accept", "resultDigest": result.run.ResourceResult.Digest}, map[string]string{"Idempotency-Key": "review-stage-accept"}, 200, &accepted)
	if accepted.Controls.CanApplyPatch || accepted.Controls.CanAcceptAndApply {
		t.Fatal("new Task still offers original-checkout Apply")
	}
	var roomView roomWorkspaceView
	requestJSON(t, h, http.MethodGet, "/api/rooms/"+accepted.Room.ID+"/workspace", nil, 200, &roomView)
	if len(roomView.Tasks) != 1 || roomView.Tasks[0].CurrentAction.Kind != app.CurrentActionViewTerminal || roomView.Room.HumanActionRequired {
		t.Fatalf("accepted Task still demands delivery: %#v", roomView)
	}
	roomID, _ := domain.ParseRoomID(accepted.Room.ID)
	reopened, err := app.NewService(app.Dependencies{Store: result.server.store}).GetRoomWorkspace(context.Background(), roomID)
	if err != nil || len(reopened.Tasks) != 1 || reopened.Tasks[0].CurrentAction.Kind != app.CurrentActionViewTerminal {
		t.Fatalf("reopened room lost review-stage terminal state: %v %#v", err, reopened)
	}
	if accepted.ResourceResult.Digest != result.run.ResourceResult.Digest {
		t.Fatal("Review changed immutable Result")
	}
	var preview app.DeliveryOperationView
	requestJSONWithHeaders(t, h, http.MethodPost, prefix+"/delivery/preview", map[string]any{"expectedVersion": accepted.Version, "repoId": result.resources[0].RepoID, "resultDigest": accepted.ResourceResult.Digest, "kind": "commit", "message": "explicit future commit"}, map[string]string{"Idempotency-Key": "active-preview"}, 200, &preview)
	if preview.Status != "preview" {
		t.Fatalf("preview = %#v", preview)
	}

	requestJSONWithHeaders(t, h, http.MethodPost, prefix+"/apply", map[string]any{"expectedVersion": accepted.Version, "resultDigest": accepted.ResourceResult.Digest}, map[string]string{"Idempotency-Key": "no-original-apply"}, 403, nil)
	root := accepted.ResourceResult.Repositories[0].WorktreePath
	if root == "" {
		t.Fatal("missing proven worktree path")
	}
	r := result.resources[0]
	if strings.TrimSpace(runTaskWorktreeGitTest(t, root, "rev-parse", "HEAD")) != r.BaseCommit {
		t.Fatal("Review/delivery gate wrote commit")
	}
	if strings.TrimSpace(runTaskWorktreeGitTest(t, root, "symbolic-ref", "--short", "HEAD")) != r.TaskBranch {
		t.Fatal("lost Task branch")
	}
	if got := runTaskWorktreeGitTest(t, root, "diff", "--name-only"); strings.TrimSpace(got) != "same.txt" {
		t.Fatalf("lost pending edit: %s", got)
	}
	taskID, _ := domain.ParseTaskID(accepted.Task.ID)
	operations, err := result.server.store.Reader().ListDeliveryOperations(context.Background(), taskID)
	if err != nil || len(operations) != 1 || operations[0].State != "preview" {
		t.Fatalf("preview did not remain non-mutating: %v %#v", err, operations)
	}
}

func TestTaskBranchAcceptRejectsLiveContentOrBranchDrift(t *testing.T) {
	for _, kind := range []string{"edited", "extra-file", "advanced-head", "staged-reviewed"} {
		t.Run(kind, func(t *testing.T) {
			var root string
			fixture := runResourceTerminalScenario(t, resourceTerminalScenario{name: "review-drift-" + kind, repositoryCount: 1, checkMode: "none", completeAssistant: true, wantRunState: domain.RunStateAwaitingReview, change: func(t *testing.T, taskRoot string, resources []domain.TaskRepositoryResource) {
				root = filepath.Join(taskRoot, resources[0].WorkspaceDirectory())
				writeResourceTerminalFile(t, root, "same.txt", []byte("reviewed before drift\n"))
			}})
			switch kind {
			case "staged-reviewed":
				runTaskWorktreeGitTest(t, root, "add", "same.txt")
			case "edited":
				writeResourceTerminalFile(t, root, "same.txt", []byte("unreviewed replacement\n"))
			case "extra-file":
				writeResourceTerminalFile(t, root, "extra.txt", []byte("unreviewed addition\n"))
			case "advanced-head":
				runTaskWorktreeGitTest(t, root, "add", "same.txt")
				runTaskWorktreeGitTest(t, root, "-c", "user.name=Test", "-c", "user.email=test@example.test", "commit", "-m", "external commit")
			}
			prefix := "/api/v2/runs/" + fixture.run.ID
			if kind == "staged-reviewed" {
				before := runTaskWorktreeGitTest(t, root, "ls-files", "--stage")
				requestJSONWithHeaders(t, fixture.server.Handler(), http.MethodPost, prefix+"/review", map[string]any{"expectedVersion": fixture.run.Version, "kind": "accept", "resultDigest": fixture.run.ResourceResult.Digest}, map[string]string{"Idempotency-Key": "staged-accept"}, 200, nil)
				if after := runTaskWorktreeGitTest(t, root, "ls-files", "--stage"); after != before {
					t.Fatal("Review changed staged index")
				}
				return
			}
			requestJSONWithHeaders(t, fixture.server.Handler(), http.MethodPost, prefix+"/review", map[string]any{"expectedVersion": fixture.run.Version, "kind": "accept", "resultDigest": fixture.run.ResourceResult.Digest}, map[string]string{"Idempotency-Key": "drift-accept"}, 409, nil)
			id, _ := domain.ParseRunID(fixture.run.ID)
			stored, err := fixture.server.store.Reader().GetRun(context.Background(), id)
			if err != nil || stored.State() != domain.RunStateAwaitingReview || stored.Version() != fixture.run.Version {
				t.Fatalf("drift acceptance mutated Run: %v %#v", err, stored)
			}
			requestJSONWithHeaders(t, fixture.server.Handler(), http.MethodPost, prefix+"/review", map[string]any{"expectedVersion": fixture.run.Version, "kind": "reject", "resultDigest": fixture.run.ResourceResult.Digest, "comment": "produce a new result"}, map[string]string{"Idempotency-Key": "drift-reject"}, 200, nil)
		})
	}
}
