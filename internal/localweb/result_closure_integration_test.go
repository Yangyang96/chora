package localweb

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

func closureRequest(run domain.AgentRun) app.ResultClosureRequest {
	return app.ResultClosureRequest{CommandMeta: app.CommandMeta{ActorID: "local-human", SessionID: "local-browser", IdempotencyKey: "close-result"}, RunID: run.ID(), ExpectedVersion: run.Version()}
}

func TestResultClosureUnreviewedRetainsEvidenceAndRefusesOldActions(t *testing.T) {
	ctx := context.Background()
	f := newResourceApplyIntegrationFixture(t, 2, 2)
	target := newRecordingResourceTarget()
	s := f.service(target)
	run := f.runs[0].run
	req := closureRequest(run)
	v, err := s.PreviewResultClosure(ctx, req)
	if err != nil || len(v.Entries) != 2 || v.ReviewID != "" {
		t.Fatalf("preview=%#v error=%v", v, err)
	}
	req.ResultDigest, req.PreviewDigest = v.ResultDigest, v.PreviewDigest
	bad := req
	bad.PreviewDigest = "stale"
	if _, err = s.CloseResult(ctx, bad); !errors.Is(err, app.ErrReviewEvidenceUnavailable) {
		t.Fatalf("stale=%v", err)
	}
	closed, err := s.CloseResult(ctx, req)
	if err != nil || closed.ClosedAt == nil || closed.Entries[0].Status != "closed" || closed.Entries[1].Status != "closed" {
		t.Fatalf("closed=%#v error=%v", closed, err)
	}
	duplicate, err := f.service(target).CloseResult(ctx, req)
	if err != nil || !duplicate.ClosedAt.Equal(*closed.ClosedAt) {
		t.Fatalf("duplicate=%#v error=%v", duplicate, err)
	}
	if _, err = f.server.store.Reader().GetLatestReviewForRun(ctx, run.ID()); !errors.Is(err, storecontract.ErrNotFound) {
		t.Fatalf("closure manufactured Review: %v", err)
	}
	if _, err = s.ReviewResourceResult(ctx, f.runs[0].reviewRequest("late-review", f.runs[0].digest)); !errors.Is(err, app.ErrResultClosed) {
		t.Fatalf("late review=%v", err)
	}
	if _, err = s.ApplyResourceResult(ctx, f.runs[0].applyRequest("late-apply", run.Version())); !errors.Is(err, app.ErrResultClosed) {
		t.Fatalf("late apply=%v", err)
	}
	if _, err = f.server.service.PrepareRetry(ctx, app.PrepareRetryRequest{CommandMeta: req.CommandMeta, RunID: run.ID(), ExpectedVersion: run.Version(), Reason: "closed result"}); !errors.Is(err, app.ErrResultClosed) {
		t.Fatalf("late retry=%v", err)
	}
	other, err := s.PreviewResultClosure(ctx, closureRequest(f.runs[1].run))
	if err != nil || other.ClosedAt != nil || other.Entries[0].Status != "eligible" {
		t.Fatalf("other Task changed=%#v error=%v", other, err)
	}
	// Closing one Task permits its archive but cannot hide the other Task's work.
	task, err := f.server.store.Reader().GetTask(ctx, run.TaskID())
	if err != nil {
		t.Fatal(err)
	}
	room, err := f.server.store.Reader().GetRoom(ctx, task.RoomID())
	if err != nil {
		t.Fatal(err)
	}
	archiveRoom := app.ChangeRoomLifecycleRequest{CommandMeta: commandMeta("closure-archive-room"), RoomID: room.ID(), ExpectedVersion: room.Version()}
	if _, err = s.ArchiveRoom(ctx, archiveRoom); !errors.Is(err, storecontract.ErrRoomStateForbidden) {
		t.Fatalf("another Task must block room archive: %v", err)
	}
	if _, err = s.ArchiveTask(ctx, app.ArchiveTaskRequest{CommandMeta: commandMeta("closure-archive-first"), TaskID: run.TaskID()}); err != nil {
		t.Fatalf("closed Task archive: %v", err)
	}
	if _, err = s.ArchiveTask(ctx, app.ArchiveTaskRequest{CommandMeta: commandMeta("closure-archive-other"), TaskID: f.runs[1].run.TaskID()}); !errors.Is(err, storecontract.ErrRoomStateForbidden) {
		t.Fatalf("unclosed Task archive: %v", err)
	}
	otherReq := closureRequest(f.runs[1].run)
	otherReq.ResultDigest, otherReq.PreviewDigest = other.ResultDigest, other.PreviewDigest
	if _, err = s.CloseResult(ctx, otherReq); err != nil {
		t.Fatal(err)
	}
	currentRoom, err := f.server.store.Reader().GetRoom(ctx, room.ID())
	if err != nil {
		t.Fatal(err)
	}
	archiveRoom.ExpectedVersion = currentRoom.Version()
	archiveRoom.CommandMeta = commandMeta("closure-archive-room-final")
	if _, err = s.ArchiveRoom(ctx, archiveRoom); err != nil {
		t.Fatalf("all closed Room archive: %v", err)
	}
	if target.totalApplies() != 0 {
		t.Fatal("closing wrote original repositories")
	}
	view, err := s.LoadResourceReview(ctx, run.ID())
	if err != nil || view.Digest != f.runs[0].digest || len(view.Patches) != 2 {
		t.Fatalf("history changed=%#v error=%v", view, err)
	}
}

func TestResultClosureRefusesUncertainPartialApply(t *testing.T) {
	ctx := context.Background()
	f := newResourceApplyIntegrationFixture(t, 2, 1)
	target := newRecordingResourceTarget()
	target.failBeforeWrite[f.resources[1].RepoID] = true
	s := f.service(target)
	run := f.accept(t, s, 0)
	v, err := s.ApplyResourceResult(ctx, f.runs[0].applyRequest("partial", run.Version()))
	if err != nil || v.Status != "partial" {
		t.Fatalf("apply=%#v error=%v", v, err)
	}
	calls := target.totalApplies()
	if _, err = s.PreviewResultClosure(ctx, closureRequest(run)); !errors.Is(err, app.ErrResultClosureBlocked) {
		t.Fatalf("uncertain closure=%v", err)
	}
	if target.totalApplies() != calls {
		t.Fatal("closure replayed uncertain write")
	}
	if _, err = f.server.store.Reader().GetResultClosure(ctx, f.runs[0].resultID); !errors.Is(err, storecontract.ErrNotFound) {
		t.Fatalf("uncertain result closed: %v", err)
	}
}

func TestResultClosureMixedCommitPreservesFilesAndRejectsStaleCommit(t *testing.T) {
	var taskRoot string
	f := runResourceTerminalScenario(t, resourceTerminalScenario{futureDelivery: true, name: "closure-mixed", repositoryCount: 2, checkMode: "none", completeAssistant: true, wantRunState: domain.RunStateAwaitingReview, change: func(t *testing.T, root string, resources []domain.TaskRepositoryResource) {
		taskRoot = root
		for _, r := range resources {
			writeResourceTerminalFile(t, filepath.Join(root, r.WorkspaceDirectory()), "same.txt", []byte("retained closure content\n"))
		}
	}})
	h := f.server.Handler()
	prefix := "/api/v2/runs/" + f.run.ID
	for _, r := range f.resources {
		runTaskWorktreeGitTest(t, f.originals[r.RepoID], "config", "user.name", "Chora Closure Test")
		runTaskWorktreeGitTest(t, f.originals[r.RepoID], "config", "user.email", "closure@example.test")
	}
	var accepted runView
	requestJSONWithHeaders(t, h, http.MethodPost, prefix+"/review", map[string]any{"expectedVersion": f.run.Version, "kind": "accept", "resultDigest": f.run.ResourceResult.Digest}, map[string]string{"Idempotency-Key": "closure-review"}, 200, &accepted)
	previews := []app.DeliveryOperationView{}
	for _, r := range f.resources {
		var p app.DeliveryOperationView
		requestJSONWithHeaders(t, h, http.MethodPost, prefix+"/delivery/preview", map[string]any{"expectedVersion": accepted.Version, "resultDigest": accepted.ResourceResult.Digest, "repoId": r.RepoID, "kind": "commit", "message": "feat: retain reviewed changes"}, map[string]string{"Idempotency-Key": "preview-" + r.RepoID}, 200, &p)
		previews = append(previews, p)
	}
	var old app.ResultClosureView
	requestJSONWithHeaders(t, h, http.MethodPost, prefix+"/closure/preview", map[string]any{"expectedVersion": accepted.Version}, map[string]string{"Idempotency-Key": "close-preview-old"}, 200, &old)
	var committed app.DeliveryOperationView
	requestJSONWithHeaders(t, h, http.MethodPost, prefix+"/delivery/confirm", map[string]any{"expectedVersion": accepted.Version, "resultDigest": accepted.ResourceResult.Digest, "operationId": previews[0].ID}, map[string]string{"Idempotency-Key": "commit-first"}, 200, &committed)
	requestJSONWithHeaders(t, h, http.MethodPost, prefix+"/closure/confirm", map[string]any{"expectedVersion": accepted.Version, "resultDigest": old.ResultDigest, "previewDigest": old.PreviewDigest}, map[string]string{"Idempotency-Key": "stale-close"}, 409, nil)
	var fresh app.ResultClosureView
	requestJSONWithHeaders(t, h, http.MethodPost, prefix+"/closure/preview", map[string]any{"expectedVersion": accepted.Version}, map[string]string{"Idempotency-Key": "close-preview-fresh"}, 200, &fresh)
	if fresh.Entries[0].Status != "retained" || fresh.Entries[1].Status != "eligible" {
		t.Fatalf("mixed=%#v", fresh)
	}
	var closed app.ResultClosureView
	requestJSONWithHeaders(t, h, http.MethodPost, prefix+"/closure/confirm", map[string]any{"expectedVersion": accepted.Version, "resultDigest": fresh.ResultDigest, "previewDigest": fresh.PreviewDigest}, map[string]string{"Idempotency-Key": "close-confirm"}, 200, &closed)
	requestJSONWithHeaders(t, h, http.MethodPost, prefix+"/delivery/confirm", map[string]any{"expectedVersion": accepted.Version, "resultDigest": accepted.ResourceResult.Digest, "operationId": previews[1].ID}, map[string]string{"Idempotency-Key": "stale-commit"}, 409, nil)
	for _, r := range f.resources {
		b, err := os.ReadFile(filepath.Join(taskRoot, r.WorkspaceDirectory(), "same.txt"))
		if err != nil || string(b) != "retained closure content\n" {
			t.Fatal("closing removed or changed Task files", err)
		}
		if got := runTaskWorktreeGitTest(t, f.originals[r.RepoID], "rev-parse", "HEAD"); got != r.BaseCommit+"\n" {
			t.Fatalf("original HEAD changed: %q", got)
		}
	}
	if closed.Entries[0].Status != "retained" || closed.Entries[1].Status != "closed" || committed.Commit == "" {
		t.Fatalf("facts lost: %#v commit=%#v", closed, committed)
	}
	resource := f.resources[1]
	cleanupPrefix := prefix + "/closure/cleanup/"
	body := map[string]any{"expectedVersion": accepted.Version, "resultDigest": closed.ResultDigest, "repoId": resource.RepoID}
	var cleanup app.DeliveryOperationView
	requestJSONWithHeaders(t, h, http.MethodPost, cleanupPrefix+"preview", body, map[string]string{"Idempotency-Key": "closed-cleanup-preview"}, 200, &cleanup)
	extraPath := filepath.Join(taskRoot, resource.WorkspaceDirectory(), "keep-extra.txt")
	if err := os.WriteFile(extraPath, []byte("preserve extra\n"), 0600); err != nil {
		t.Fatal(err)
	}
	body["operationId"] = cleanup.ID
	var refused app.DeliveryOperationView
	requestJSONWithHeaders(t, h, http.MethodPost, cleanupPrefix+"confirm", body, map[string]string{"Idempotency-Key": "closed-cleanup-stale"}, 200, &refused)
	if refused.Status != "failed" || string(mustReadResourceClosure(t, extraPath)) != "preserve extra\n" {
		t.Fatalf("extra file cleanup=%#v", refused)
	}
	if err := os.Remove(extraPath); err != nil {
		t.Fatal(err)
	}
	delete(body, "operationId")
	requestJSONWithHeaders(t, h, http.MethodPost, cleanupPrefix+"preview", body, map[string]string{"Idempotency-Key": "closed-cleanup-fresh"}, 200, &cleanup)
	body["operationId"] = cleanup.ID
	var removed app.DeliveryOperationView
	requestJSONWithHeaders(t, h, http.MethodPost, cleanupPrefix+"confirm", body, map[string]string{"Idempotency-Key": "closed-cleanup-confirm"}, 200, &removed)
	if removed.Status != "succeeded" || removed.Commit != "" {
		t.Fatalf("cleanup invented commit: %#v", removed)
	}
	if _, err := os.Stat(filepath.Join(taskRoot, resource.WorkspaceDirectory())); !os.IsNotExist(err) {
		t.Fatal("closed Task repository not removed", err)
	}
	if _, err := os.Stat(filepath.Join(taskRoot, f.resources[0].WorkspaceDirectory(), "same.txt")); err != nil {
		t.Fatal("committed repository removed", err)
	}
	if got := runTaskWorktreeGitTest(t, f.originals[resource.RepoID], "rev-parse", "refs/heads/"+resource.TaskBranch); got != resource.BaseCommit+"\n" {
		t.Fatalf("closed Task branch removed: %q", got)
	}
	var refreshed app.ResultClosureView
	requestJSON(t, h, http.MethodGet, prefix+"/closure", nil, 200, &refreshed)
	if refreshed.Entries[1].Cleanup == nil || refreshed.Entries[1].Cleanup.Status != "succeeded" || refreshed.Entries[1].CanCleanup {
		t.Fatalf("cleanup refresh=%#v", refreshed)
	}
	manager, err := newTaskResourceWorkspaceManager(f.server.store, filepath.Dir(filepath.Dir(taskRoot)))
	if err != nil {
		t.Fatal(err)
	}
	taskID, err := domain.ParseTaskID(f.group.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := manager.(*taskResourceWorkspaceManager).ResolveHandoffRoot(context.Background(), taskID); err != nil || got != taskRoot {
		t.Fatalf("partial closed cleanup handoff=%q err=%v", got, err)
	}
}

// A typed pre-write conflict is known not to have written, unlike uncertainty.
type closureConflictTarget struct {
	*recordingResourceTarget
	conflictRepo string
}

func (target closureConflictTarget) Apply(ctx context.Context, resource domain.TaskRepositoryResource, request app.PatchTargetRequest, inspection app.PatchTargetInspection) (app.PatchTargetEvidence, error) {
	if resource.RepoID == target.conflictRepo {
		return app.PatchTargetEvidence{}, app.ErrPatchTargetConflict
	}
	return target.recordingResourceTarget.Apply(ctx, resource, request, inspection)
}

func TestResultClosurePartialLegacyApplyRetainsExactHistory(t *testing.T) {
	ctx := context.Background()
	f := newResourceApplyIntegrationFixture(t, 2, 1)
	target := closureConflictTarget{newRecordingResourceTarget(), f.resources[1].RepoID}
	s := f.service(target)
	run := f.accept(t, s, 0)
	partial, err := s.ApplyResourceResult(ctx, f.runs[0].applyRequest("partial-known-conflict", run.Version()))
	if err != nil || partial.Status != "partial" || target.totalApplies() != 1 {
		t.Fatalf("partial=%#v err=%v writes=%d", partial, err, target.totalApplies())
	}
	operation, err := f.server.store.Reader().GetResourceApplyOperation(ctx, f.runs[0].resultID)
	if err != nil {
		t.Fatal(err)
	}
	before, err := f.server.store.Reader().ListResourceApplySteps(ctx, operation.ID)
	if err != nil {
		t.Fatal(err)
	}
	req := closureRequest(run)
	preview, err := s.PreviewResultClosure(ctx, req)
	if err != nil || preview.Entries[0].Status != "retained" || preview.Entries[1].Status != "eligible" {
		t.Fatalf("preview=%#v err=%v", preview, err)
	}
	req.ResultDigest, req.PreviewDigest = preview.ResultDigest, preview.PreviewDigest
	if _, err = s.CloseResult(ctx, req); err != nil {
		t.Fatal(err)
	}
	view, err := f.service(target).LoadResourceApply(ctx, f.runs[0].resultID)
	if err != nil || view.Status != "partial_applied" || repositoryApplyStatus(view, f.resources[0].RepoID) != "applied" || repositoryApplyStatus(view, f.resources[1].RepoID) != "closed" {
		t.Fatalf("closed projection=%#v err=%v", view, err)
	}
	after, err := f.server.store.Reader().ListResourceApplySteps(ctx, operation.ID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("Apply evidence mutated: %v", err)
	}
	if _, err = s.ApplyResourceResult(ctx, f.runs[0].applyRequest("late-partial-apply", run.Version())); !errors.Is(err, app.ErrResultClosed) {
		t.Fatalf("late Apply=%v", err)
	}
	if target.totalApplies() != 1 {
		t.Fatal("closing replayed an original repository write")
	}
}

func TestResultClosureFailedChecksRemainUnverified(t *testing.T) {
	for _, tc := range []struct {
		observed, changed bool
		repos             int
	}{{true, false, 1}, {false, false, 2}, {false, true, 2}} {
		observed := tc.observed
		t.Run(fmt.Sprintf("observed=%v/changed=%v", observed, tc.changed), func(t *testing.T) {
			wantState := domain.RunStateRecoveryRequired
			if tc.changed {
				wantState = domain.RunStateAwaitingReview
			}
			f := runResourceTerminalScenario(t, resourceTerminalScenario{
				name: "close failed checks", repositoryCount: tc.repos, checkMode: "named", completeAssistant: true,
				emitNamedCheck: observed, namedExitCode: 7, wantRunState: wantState,
				change: func(t *testing.T, root string, resources []domain.TaskRepositoryResource) {
					if !tc.changed {
						return
					}
					for _, repo := range resources {
						writeResourceTerminalFile(t, filepath.Join(root, repo.WorkspaceDirectory()), "same.txt", []byte("failed-check result retained\n"))
					}
				},
			})
			ctx := context.Background()
			runID, err := domain.ParseRunID(f.run.ID)
			if err != nil {
				t.Fatal(err)
			}
			run, err := f.server.store.Reader().GetRun(ctx, runID)
			if err != nil {
				t.Fatal(err)
			}
			req := closureRequest(run)
			v, err := f.server.service.PreviewResultClosure(ctx, req)
			if err != nil {
				t.Fatalf("failed-check preview: %v", err)
			}
			req.ResultDigest, req.PreviewDigest = v.ResultDigest, v.PreviewDigest
			if _, err = f.server.service.CloseResult(ctx, req); err != nil {
				t.Fatal(err)
			}
			after, err := f.server.store.Reader().GetResourceResultGroup(ctx, f.attemptID)
			if err != nil || !reflect.DeepEqual(f.group, after) {
				t.Fatalf("failed check evidence mutated: %v", err)
			}
			if _, err = f.server.store.Reader().GetLatestReviewForRun(ctx, runID); !errors.Is(err, storecontract.ErrNotFound) {
				t.Fatalf("closure invented Review: %v", err)
			}
		})
	}
}

func mustReadResourceClosure(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
