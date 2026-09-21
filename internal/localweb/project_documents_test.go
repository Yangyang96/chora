package localweb

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
)

func TestProjectDocumentJourneyFreezesAcceptedRevision(t *testing.T) {
	server, runtime, _ := newResourceTerminalIntegrationServer(t)
	h := server.Handler()
	requestJSONWithHeaders(t, h, http.MethodPost, "/api/agent-execution/trusted-local-acknowledgements", map[string]any{"policyVersion": domain.TrustedLocalDisclosurePolicy}, map[string]string{"Idempotency-Key": "document-ack"}, 200, nil)
	var project resourceProjectTestView
	requestJSON(t, h, http.MethodPost, "/api/v2/projects", map[string]any{"name": "Research project"}, 201, &project)
	input := map[string]any{"title": "Reviewed findings", "requirement": "Explain how reviewed findings become frozen coding inputs; cite supplied sources and name unknowns.", "agentExecutionProfile": "trusted_local", "resources": []any{}, "outcomeKind": "document", "materials": []map[string]string{{"title": "Workflow", "locator": "docs/project-workflows.md", "body": "Later tasks explicitly select an accepted revision. Unknown facts stay unknown."}}}
	initialRoom, _ := domain.ParseRoomID(project.DefaultRoomID)
	initialRevisions, err := server.store.Reader().ListRoomRevisions(context.Background(), initialRoom)
	if err != nil || len(initialRevisions) == 0 {
		t.Fatalf("initial Room context: %v", err)
	}
	input["revisionIds"] = []string{initialRevisions[0].ID().String()}
	var task taskRefView
	requestJSONWithHeaders(t, h, http.MethodPost, "/api/v2/rooms/"+project.DefaultRoomID+"/tasks", input, map[string]string{"Idempotency-Key": "document-task"}, 201, &task)
	if task.ResourceSnapshot == nil || task.ResourceSnapshot.OutcomeKind != "document" || len(task.ResourceSnapshot.Resources) != 0 {
		t.Fatalf("wrong resource snapshot: %#v", task.ResourceSnapshot)
	}
	if len(task.ResourceSnapshot.Materials) != 1 || task.ResourceSnapshot.Materials[0].Digest == "" {
		t.Fatal("missing frozen source identity")
	}
	acceptTaskPlan(t, h, task.ID)
	var run runView
	requestJSONWithHeaders(t, h, http.MethodPost, "/api/tasks/"+task.ID+"/runs", map[string]any{}, map[string]string{"Idempotency-Key": "document-run"}, 201, &run)
	if run.Status != domain.RunStateRunning || run.OutcomeKind != "document" {
		t.Fatalf("run: %#v", run)
	}
	_, invocation := runtime.snapshot()
	if strings.Contains(invocation.WorkingRoot(), ".git") || filepath.Base(invocation.WorkingRoot()) == "public" {
		t.Fatal("document execution must own its workspace")
	}
	const markdown = "# Proposed handoff\n\nFreeze the explicitly accepted revision (source: docs/project-workflows.md).\n\nUnknown: team authority is outside this evidence."
	raw, _ := json.Marshal(map[string]any{"type": "message_end", "message": map[string]any{"role": "assistant", "content": []map[string]any{{"type": "text", "text": markdown}}, "stopReason": "stop"}})
	runtime.fakeSupervisor.mu.Lock()
	runtime.fakeSupervisor.streams[execution.StreamStdout] = append(raw, []byte("\n{\"type\":\"agent_settled\"}\n")...)
	runtime.fakeSupervisor.streams[execution.StreamStderr] = nil
	runtime.fakeSupervisor.mu.Unlock()
	if err := runtime.Notify(execution.StreamStdout); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Exit(); err != nil {
		t.Fatal(err)
	}
	finished := waitForRunState(t, h, run.ID, "awaiting_review")
	if finished.ResourceResult == nil || finished.ResourceResult.Group.Markdown != markdown || finished.Controls.CanApplyPatch || finished.Controls.CanAcceptAndApply {
		t.Fatalf("document result: %#v", finished.ResourceResult)
	}
	endpoint := "/api/tasks/" + task.ID + "/document"
	var doc app.ProjectDocumentView
	requestJSON(t, h, http.MethodGet, endpoint, nil, 200, &doc)
	if doc.Version != 0 {
		t.Fatal("GET created a revision")
	}
	requestJSONWithHeaders(t, h, http.MethodPost, endpoint, map[string]any{"expectedVersion": 0, "sourceAttemptId": finished.ResourceResult.Group.AttemptID}, map[string]string{"Idempotency-Key": "document-import"}, 200, &doc)
	if doc.Version != 1 || doc.Status != "pending" || doc.Revisions[0].Body != markdown {
		t.Fatalf("draft: %#v", doc)
	}
	requestJSONWithHeaders(t, h, http.MethodPost, endpoint+"/reviews", map[string]any{"expectedVersion": 1, "kind": "accept", "note": "Accepted for this project"}, map[string]string{"Idempotency-Key": "document-accept"}, 200, &doc)
	acceptedID := doc.Revisions[0].AcceptedContextRevisionID
	if doc.Status != "accepted" || acceptedID == "" {
		t.Fatalf("accepted: %#v", doc)
	}
	var board app.TaskBoardView
	requestJSON(t, h, http.MethodGet, "/api/v2/projects/"+project.ID+"/task-board", nil, 200, &board)
	if len(board.Cards) != 1 || board.Cards[0].Phase == nil || *board.Cards[0].Phase != "finished" || len(board.Cards[0].Repositories) != 0 {
		t.Fatalf("document board: %#v", board)
	}
	// New edits never replace accepted bytes or inherit their acceptance.
	requestJSONWithHeaders(t, h, http.MethodPost, endpoint, map[string]any{"expectedVersion": 1, "body": markdown + "\n\nHuman refinement.", "note": "Clarify scope"}, map[string]string{"Idempotency-Key": "document-edit"}, 200, &doc)
	if doc.Version != 2 || doc.Status != "pending" || doc.Revisions[0].AcceptedContextRevisionID != acceptedID {
		t.Fatalf("revision history: %#v", doc)
	}
	requestJSONWithHeaders(t, h, http.MethodPost, endpoint+"/reviews", map[string]any{"expectedVersion": 1, "kind": "accept"}, map[string]string{"Idempotency-Key": "document-stale"}, 409, nil)
	requestJSON(t, h, http.MethodGet, "/api/v2/projects/"+project.ID+"/task-board", nil, 200, &board)
	if board.Cards[0].Phase == nil || *board.Cards[0].Phase != "review" {
		t.Fatalf("new draft board: %#v", board)
	}
	// Explicit later coding Task binds revision one even while revision two is pending.
	checkout, _, _ := newTaskWorktreeTestRepository(t)
	var added struct {
		Repository repositoryResourceView `json:"repository"`
	}
	requestJSON(t, h, http.MethodPost, "/api/v2/projects/"+project.ID+"/repositories", map[string]any{"locator": checkout}, 200, &added)
	selection := taskResourceSelection{RepoID: added.Repository.RepoID, AssociationVersion: added.Repository.Version, Role: "write", TargetRef: runTaskWorktreeGitTest(t, checkout, "symbolic-ref", "HEAD"), Scope: domain.TaskRepositoryScope{Mode: "repository", MigrationChoice: "not_needed"}, Checks: domain.TaskCheckPolicy{Mode: "none", SelectionSource: "user"}}
	var coding taskRefView
	requestJSONWithHeaders(t, h, http.MethodPost, "/api/v2/rooms/"+project.DefaultRoomID+"/tasks", map[string]any{"title": "Implement accepted handoff", "requirement": "Implement the explicitly accepted handoff", "agentExecutionProfile": "trusted_local", "resources": []taskResourceSelection{selection}, "revisionIds": []string{acceptedID}}, map[string]string{"Idempotency-Key": "document-coding"}, 201, &coding)
	codingID, _ := domain.ParseTaskID(coding.ID)
	frozen, err := server.store.Reader().GetTaskRevisionSelection(context.Background(), codingID)
	if err != nil || len(frozen.SelectedRevisionIDs()) != 1 || frozen.SelectedRevisionIDs()[0].String() != acceptedID {
		t.Fatalf("selection=%#v err=%v", frozen, err)
	}
	requestJSONWithHeaders(t, h, http.MethodPost, endpoint+"/reviews", map[string]any{"expectedVersion": 2, "kind": "accept"}, map[string]string{"Idempotency-Key": "document-accept-two"}, 200, &doc)
	roomID, _ := domain.ParseRoomID(project.DefaultRoomID)
	revisions, err := server.store.Reader().LookupRevisions(context.Background(), roomID, frozen.SelectedRevisionIDs())
	if err != nil || len(revisions) != 1 || revisions[0].Body() != markdown {
		t.Fatalf("frozen reference drift: %#v %v", revisions, err)
	}
	acceptTaskPlan(t, h, coding.ID)
}

func TestProjectDocumentHumanAcceptanceAfterRunRejectionClosesTask(t *testing.T) {
	runRejectedProjectDocumentScenario(t, false)
}

func TestProjectDocumentRejectsStaleSourceAfterRetryStarts(t *testing.T) {
	runRejectedProjectDocumentScenario(t, true)
}

func runRejectedProjectDocumentScenario(t *testing.T, startRetry bool) {
	server, runtime, _ := newResourceTerminalIntegrationServer(t)
	h := server.Handler()
	requestJSONWithHeaders(t, h, http.MethodPost, "/api/agent-execution/trusted-local-acknowledgements", map[string]any{"policyVersion": domain.TrustedLocalDisclosurePolicy}, map[string]string{"Idempotency-Key": "document-reject-ack"}, 200, nil)
	var project resourceProjectTestView
	requestJSON(t, h, http.MethodPost, "/api/v2/projects", map[string]any{"name": "Revision project"}, 201, &project)
	roomID, _ := domain.ParseRoomID(project.DefaultRoomID)
	roomRevisions, err := server.store.Reader().ListRoomRevisions(context.Background(), roomID)
	if err != nil || len(roomRevisions) == 0 {
		t.Fatalf("initial Room context: %v", err)
	}
	input := map[string]any{
		"title": "Revised findings", "requirement": "Draft and revise a cited project document.",
		"agentExecutionProfile": "trusted_local", "resources": []any{}, "outcomeKind": "document",
		"materials":   []map[string]string{{"title": "Source", "locator": "docs/source.md", "body": "Supplied source."}},
		"revisionIds": []string{roomRevisions[0].ID().String()},
	}
	var task taskRefView
	requestJSONWithHeaders(t, h, http.MethodPost, "/api/v2/rooms/"+project.DefaultRoomID+"/tasks", input, map[string]string{"Idempotency-Key": "document-reject-task"}, 201, &task)
	acceptTaskPlan(t, h, task.ID)
	var run runView
	requestJSONWithHeaders(t, h, http.MethodPost, "/api/tasks/"+task.ID+"/runs", map[string]any{}, map[string]string{"Idempotency-Key": "document-reject-run"}, 201, &run)
	const markdown = "# Draft\n\nCites docs/source.md."
	raw, _ := json.Marshal(map[string]any{"type": "message_end", "message": map[string]any{"role": "assistant", "content": []map[string]any{{"type": "text", "text": markdown}}, "stopReason": "stop"}})
	runtime.fakeSupervisor.mu.Lock()
	runtime.fakeSupervisor.streams[execution.StreamStdout] = append(raw, []byte("\n{\"type\":\"agent_settled\"}\n")...)
	runtime.fakeSupervisor.streams[execution.StreamStderr] = nil
	runtime.fakeSupervisor.mu.Unlock()
	if err := runtime.Notify(execution.StreamStdout); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Exit(); err != nil {
		t.Fatal(err)
	}
	finished := waitForRunState(t, h, run.ID, "awaiting_review")
	endpoint := "/api/tasks/" + task.ID + "/document"
	var doc app.ProjectDocumentView
	requestJSONWithHeaders(t, h, http.MethodPost, endpoint, map[string]any{"expectedVersion": 0, "sourceAttemptId": finished.ResourceResult.Group.AttemptID}, map[string]string{"Idempotency-Key": "document-reject-import"}, 200, &doc)
	requestJSONWithHeaders(t, h, http.MethodPost, endpoint+"/reviews", map[string]any{"expectedVersion": 1, "kind": "reject", "note": "Clarify scope"}, map[string]string{"Idempotency-Key": "document-reject-review"}, 200, &doc)
	requestJSONWithHeaders(t, h, http.MethodPost, endpoint, map[string]any{"expectedVersion": 1, "body": markdown + "\n\nHuman clarification.", "note": "Clarify scope"}, map[string]string{"Idempotency-Key": "document-human-edit"}, 200, &doc)
	var pendingBoard app.TaskBoardView
	requestJSON(t, h, http.MethodGet, "/api/v2/projects/"+project.ID+"/task-board", nil, 200, &pendingBoard)
	if len(pendingBoard.Cards) != 1 || pendingBoard.Cards[0].Phase == nil || *pendingBoard.Cards[0].Phase != "review" {
		t.Fatalf("pending human revision should need review: %#v", pendingBoard)
	}
	if startRetry {
		runID, _ := domain.ParseRunID(run.ID)
		rejectedRun, readErr := server.store.Reader().GetRun(context.Background(), runID)
		if readErr != nil {
			t.Fatal(readErr)
		}
		var retrying runView

		requestJSONWithHeaders(t, h, http.MethodPost, "/api/runs/"+run.ID+"/retry", map[string]any{"expectedVersion": rejectedRun.Version(), "instructions": "Produce a corrected document."}, map[string]string{"Idempotency-Key": "document-stale-retry"}, http.StatusOK, &retrying)
		requestJSONWithHeaders(t, h, http.MethodPost, endpoint+"/reviews", map[string]any{"expectedVersion": 2, "kind": "accept", "note": "Must not accept stale source"}, map[string]string{"Idempotency-Key": "document-stale-accept"}, http.StatusConflict, nil)
		return
	}
	requestJSONWithHeaders(t, h, http.MethodPost, endpoint+"/reviews", map[string]any{"expectedVersion": 2, "kind": "accept", "note": "Accept edited document"}, map[string]string{"Idempotency-Key": "document-human-accept"}, 200, &doc)
	if doc.Status != "accepted" || doc.Revisions[1].AcceptedContextRevisionID == "" {
		t.Fatalf("accepted human revision: %#v", doc)
	}
	runID, _ := domain.ParseRunID(run.ID)
	rejectedRun, err := server.store.Reader().GetRun(context.Background(), runID)
	if err != nil || rejectedRun.State() != domain.RunStateRevisionRequired {
		t.Fatalf("document acceptance rewrote rejected Run: %#v %v", rejectedRun, err)
	}
	taskID, _ := domain.ParseTaskID(task.ID)
	closedTask, err := server.store.Reader().GetTask(context.Background(), taskID)
	if err != nil || closedTask.State() != domain.TaskStateClosed {
		t.Fatalf("accepted document did not close Task: %#v %v", closedTask, err)
	}
	var workspace roomWorkspaceView
	requestJSON(t, h, http.MethodGet, "/api/rooms/"+project.DefaultRoomID+"/workspace", nil, 200, &workspace)
	if len(workspace.Tasks) != 1 || workspace.Tasks[0].CurrentAction.Kind != app.CurrentActionViewTerminal || workspace.Room.HumanActionRequired {
		t.Fatalf("accepted human document still requests execution: %#v", workspace)
	}
	requestJSONWithHeaders(t, h, http.MethodPost, "/api/runs/"+run.ID+"/retry", map[string]any{"expectedVersion": rejectedRun.Version(), "instructions": "Retry despite accepted document."}, map[string]string{"Idempotency-Key": "document-accepted-retry"}, http.StatusConflict, nil)
}
