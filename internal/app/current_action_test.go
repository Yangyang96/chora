package app

import (
	"sort"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

func TestDeriveCurrentActionCoversClosedRunVocabulary(t *testing.T) {
	t.Parallel()
	roomID := domain.NewRoomID()
	task := currentActionTask(t, roomID)
	revisionID := domain.NewTechnicalPlanRevisionID()
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name  string
		state domain.RunState
		kind  CurrentActionKind
	}{
		{"draft", domain.RunStateDraft, CurrentActionStartAttempt},
		{"ready", domain.RunStateReady, CurrentActionStartAttempt},
		{"running", domain.RunStateRunning, CurrentActionMonitorRun},
		{"stopping", domain.RunStateStopping, CurrentActionMonitorRun},
		{"awaiting verification", domain.RunStateAwaitingVerification, CurrentActionMonitorRun},
		{"verifying", domain.RunStateVerifying, CurrentActionMonitorRun},
		{"run recovery", domain.RunStateRecoveryRequired, CurrentActionRecoverRun},
		{"verification recovery", domain.RunStateVerificationRecoveryRequired, CurrentActionRecoverVerification},
		{"review result", domain.RunStateAwaitingReview, CurrentActionReviewResult},
		{"accepted", domain.RunStateAccepted, CurrentActionApplyPatch},
		{"cancelled", domain.RunStateCancelled, CurrentActionViewTerminal},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			run := currentActionRun(t, task.ID(), test.state, now)
			item := storecontract.TaskWorkspaceItem{Task: task, ActiveRunCount: activeRunCount(test.state), AcceptedRevisionID: revisionID, LatestRun: &run, LatestRunBindingRevisionID: revisionID}
			action := deriveCurrentAction(domain.RoomStateActive, item)
			if action.Kind != test.kind || action.Target.RunID != run.ID() || action.Target.TaskID != task.ID() || action.Target.ExpectedVersion != run.Version() {
				t.Fatalf("action=%#v", action)
			}
			wantURL := "/rooms/" + roomID.String() + "/tasks/" + task.ID().String() + "/runs/" + run.ID().String()
			if action.URL != wantURL {
				t.Fatalf("URL=%q want=%q", action.URL, wantURL)
			}
		})
	}
}

func TestDeriveCurrentActionRoutesPlanningAndRejectionTargetsExactly(t *testing.T) {
	t.Parallel()
	roomID := domain.NewRoomID()
	task := currentActionTask(t, roomID)
	revisionID := domain.NewTechnicalPlanRevisionID()
	draftID := domain.NewTechnicalPlanDraftID()
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)

	item := storecontract.TaskWorkspaceItem{Task: task, OpenDraftID: draftID, OpenDraftEditVersion: 4}
	if action := deriveCurrentAction(domain.RoomStateActive, item); action.Kind != CurrentActionEditPlan || action.Target.DraftID != draftID || action.Target.ExpectedVersion != 4 {
		t.Fatalf("draft action=%#v", action)
	}
	item = storecontract.TaskWorkspaceItem{Task: task, LatestRevisionID: revisionID, LatestRevisionNumber: 1}
	if action := deriveCurrentAction(domain.RoomStateActive, item); action.Kind != CurrentActionReviewPlan || action.Target.RevisionID != revisionID {
		t.Fatalf("review action=%#v", action)
	}
	item.AcceptedRevisionID = revisionID
	item.LatestPlanReviewID = domain.NewTechnicalPlanReviewID()
	item.LatestPlanReviewRevisionID = revisionID
	item.LatestPlanReviewKind = domain.TechnicalPlanReviewAccept
	if action := deriveCurrentAction(domain.RoomStateActive, item); action.Kind != CurrentActionStartRun || action.Target.RevisionID != revisionID {
		t.Fatalf("start Run action=%#v", action)
	}

	run := currentActionRun(t, task.ID(), domain.RunStateRevisionRequired, now)
	base := storecontract.TaskWorkspaceItem{Task: task, ActiveRunCount: 1, AcceptedRevisionID: revisionID, LatestRun: &run, LatestRunBindingRevisionID: revisionID, LatestVerifiedReviewID: domain.NewReviewDecisionID(), LatestVerifiedReviewKind: domain.ReviewDecisionReject, LatestVerifiedReviewRunVersion: run.Version() - 1}
	base.LatestRejectionClass = domain.ReviewRejectionImplementationGap
	if action := deriveCurrentAction(domain.RoomStateActive, base); action.Kind != CurrentActionRetryImplementation || action.Target.RunID != run.ID() {
		t.Fatalf("implementation action=%#v", action)
	}
	base.LatestRejectionClass = domain.ReviewRejectionPlanningGap
	base.RouteRejectionClass = domain.ReviewRejectionPlanningGap
	base.RouteSourceRunID = run.ID()
	base.RouteSourceTaskID = task.ID()
	base.RoutePlanningDraftID = draftID
	base.RoutePlanningDraftPredecessorRevisionID = revisionID
	if action := deriveCurrentAction(domain.RoomStateActive, base); action.Kind != CurrentActionContinueSuccessorPlan || action.Target.DraftID != draftID {
		t.Fatalf("planning action=%#v", action)
	}
	base.LatestRejectionClass = domain.ReviewRejectionContractChangeRequired
	base.RouteRejectionClass = domain.ReviewRejectionContractChangeRequired
	base.RoutePlanningDraftID = domain.TechnicalPlanDraftID{}
	base.RoutePlanningDraftPredecessorRevisionID = domain.TechnicalPlanRevisionID{}
	base.RouteRelatedTaskID = domain.NewTaskID()
	base.RouteRelatedTaskPredecessorTaskID = task.ID()
	if action := deriveCurrentAction(domain.RoomStateActive, base); action.Kind != CurrentActionOpenRelatedTask || action.Target.TaskID != base.RouteRelatedTaskID {
		t.Fatalf("contract action=%#v", action)
	}
}

func TestDeriveCurrentActionRoutesTrustedNeedsRevisionWithoutHumanReview(t *testing.T) {
	t.Parallel()
	roomID := domain.NewRoomID()
	task := currentActionTask(t, roomID)
	revisionID := domain.NewTechnicalPlanRevisionID()
	resultID := domain.NewResultID()
	run := currentActionRun(t, task.ID(), domain.RunStateRevisionRequired, time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC))
	item := storecontract.TaskWorkspaceItem{
		Task: task, ActiveRunCount: 1, AcceptedRevisionID: revisionID, LatestRun: &run, LatestRunBindingRevisionID: revisionID,
		LatestVerificationResultID: resultID, LatestVerificationResultOutcome: domain.ResultNeedsRevision,
	}
	action := deriveCurrentAction(domain.RoomStateActive, item)
	if action.Kind != CurrentActionRetryImplementation || action.Target.ResultID != resultID || action.Target.RunID != run.ID() {
		t.Fatalf("action=%#v", action)
	}
}

func TestDeriveCurrentActionKeepsAcceptedWorkOpenUntilPatchIsApplied(t *testing.T) {
	t.Parallel()
	roomID := domain.NewRoomID()
	task := currentActionTask(t, roomID)
	revisionID := domain.NewTechnicalPlanRevisionID()
	run := currentActionRun(t, task.ID(), domain.RunStateAccepted, time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC))
	item := storecontract.TaskWorkspaceItem{
		Task: task, AcceptedRevisionID: revisionID, LatestRun: &run, LatestRunBindingRevisionID: revisionID,
	}
	if action := deriveCurrentAction(domain.RoomStateActive, item); action.Kind != CurrentActionApplyPatch {
		t.Fatalf("accepted-not-applied action=%#v", action)
	}
	legacy := item
	legacy.Task = task.Close()
	if action := deriveCurrentAction(domain.RoomStateActive, legacy); action.Kind != CurrentActionViewTerminal {
		t.Fatalf("legacy closed accepted action=%#v", action)
	}
	item.LatestPatchApplicationState = domain.PatchApplicationApplying
	if action := deriveCurrentAction(domain.RoomStateActive, item); action.Kind != CurrentActionMonitorRun {
		t.Fatalf("applying action=%#v", action)
	}
	item.LatestPatchApplicationState = domain.PatchApplicationApplied
	item.Task = task.Close()
	if action := deriveCurrentAction(domain.RoomStateActive, item); action.Kind != CurrentActionViewTerminal {
		t.Fatalf("applied action=%#v", action)
	}
}

func TestDeriveCurrentActionTreatsAcceptedTaskBranchResultAsTerminalHistory(t *testing.T) {
	t.Parallel()
	roomID := domain.NewRoomID()
	task := currentActionTask(t, roomID)
	revisionID := domain.NewTechnicalPlanRevisionID()
	run := currentActionRun(t, task.ID(), domain.RunStateAccepted, time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC))
	item := storecontract.TaskWorkspaceItem{
		Task: task, AcceptedRevisionID: revisionID, LatestRun: &run, LatestRunBindingRevisionID: revisionID, HasTaskBranchDelivery: true,
	}
	action := deriveCurrentAction(domain.RoomStateActive, item)
	if action.Kind != CurrentActionViewTerminal || action.URL != runURL(roomID, task.ID(), run.ID()) {
		t.Fatalf("accepted task-branch action=%#v", action)
	}
	legacy := item
	legacy.HasTaskBranchDelivery = false
	if action := deriveCurrentAction(domain.RoomStateActive, legacy); action.Kind != CurrentActionApplyPatch {
		t.Fatalf("accepted legacy action=%#v", action)
	}
}

func TestRoomHumanIndicatorUsesTheSameClosedCurrentAction(t *testing.T) {
	t.Parallel()
	roomID := domain.NewRoomID()
	room, err := domain.NewRoom(domain.RoomParams{ID: roomID, Name: "Room", WorkspaceRoot: "/workspace", CreatedAt: time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC), UpdatedAt: time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	task := currentActionTask(t, roomID)
	revisionID := domain.NewTechnicalPlanRevisionID()
	run := currentActionRun(t, task.ID(), domain.RunStateRevisionRequired, room.CreatedAt())
	item := storecontract.TaskWorkspaceItem{Task: task, ActiveRunCount: 1, AcceptedRevisionID: revisionID, LatestRun: &run, LatestRunBindingRevisionID: revisionID, LatestVerificationResultID: domain.NewResultID(), LatestVerificationResultOutcome: domain.ResultNeedsRevision}
	summaries := roomSummaries([]storecontract.RoomDirectoryItem{{Room: room, Tasks: []storecontract.TaskWorkspaceItem{item}}})
	if len(summaries) != 1 || !summaries[0].HumanActionRequired {
		t.Fatalf("needs-revision summaries=%#v", summaries)
	}
	item.LatestVerificationResultOutcome = domain.ResultReviewReady
	summaries = roomSummaries([]storecontract.RoomDirectoryItem{{Room: room, Tasks: []storecontract.TaskWorkspaceItem{item}}})
	if summaries[0].HumanActionRequired {
		t.Fatalf("contradictory summaries=%#v", summaries)
	}
}

func TestDeriveCurrentActionFailsClosedOnArchiveAndContradictions(t *testing.T) {
	t.Parallel()
	roomID := domain.NewRoomID()
	task := currentActionTask(t, roomID)
	revisionID := domain.NewTechnicalPlanRevisionID()
	run := currentActionRun(t, task.ID(), domain.RunStateReady, time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC))
	valid := storecontract.TaskWorkspaceItem{Task: task, ActiveRunCount: 1, AcceptedRevisionID: revisionID, LatestRun: &run, LatestRunBindingRevisionID: revisionID}
	tests := []struct {
		name  string
		state domain.RoomState
		item  storecontract.TaskWorkspaceItem
	}{
		{"archived", domain.RoomStateArchived, valid},
		{"multiple active Runs", domain.RoomStateActive, func() storecontract.TaskWorkspaceItem { value := valid; value.ActiveRunCount = 2; return value }()},
		{"binding drift", domain.RoomStateActive, func() storecontract.TaskWorkspaceItem {
			value := valid
			value.LatestRunBindingRevisionID = domain.NewTechnicalPlanRevisionID()
			return value
		}()},
		{"missing binding", domain.RoomStateActive, func() storecontract.TaskWorkspaceItem {
			value := valid
			value.LatestRunBindingRevisionID = domain.TechnicalPlanRevisionID{}
			return value
		}()},
		{"wrong Run Task", domain.RoomStateActive, func() storecontract.TaskWorkspaceItem {
			value := valid
			wrong := currentActionRun(t, domain.NewTaskID(), domain.RunStateReady, run.CreatedAt())
			value.LatestRun = &wrong
			return value
		}()},
		{"closed Task with nonterminal Run", domain.RoomStateActive, func() storecontract.TaskWorkspaceItem {
			value := valid
			value.Task = task.Close()
			return value
		}()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			action := deriveCurrentAction(test.state, test.item)
			if action.Kind != CurrentActionNone || action.URL != "" || action.Reason == "" {
				t.Fatalf("action=%#v", action)
			}
		})
	}
}

func TestTaskSummaryOrderUsesActionGroupThenActivityAndID(t *testing.T) {
	t.Parallel()
	roomID := domain.NewRoomID()
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	humanA := currentActionTask(t, roomID)
	humanB := currentActionTask(t, roomID)
	running := currentActionTask(t, roomID)
	otherOpen := currentActionTask(t, roomID)
	terminal := currentActionTask(t, roomID).Close()
	human := []TaskSummary{
		{Task: humanA, LastActivityAt: now, CurrentAction: CurrentAction{Kind: CurrentActionReviewPlan}},
		{Task: humanB, LastActivityAt: now, CurrentAction: CurrentAction{Kind: CurrentActionStartRun}},
	}
	sort.Slice(human, func(i, j int) bool { return human[i].Task.ID().String() < human[j].Task.ID().String() })
	tasks := []TaskSummary{
		{Task: terminal, LastActivityAt: now.Add(4 * time.Hour), CurrentAction: CurrentAction{Kind: CurrentActionViewTerminal}},
		{Task: otherOpen, LastActivityAt: now.Add(3 * time.Hour), CurrentAction: noCurrentAction("waiting")},
		human[1],
		{Task: running, LastActivityAt: now.Add(2 * time.Hour), CurrentAction: CurrentAction{Kind: CurrentActionMonitorRun}},
		human[0],
	}
	sortTaskSummaries(tasks)
	want := []domain.TaskID{human[0].Task.ID(), human[1].Task.ID(), running.ID(), otherOpen.ID(), terminal.ID()}
	for index, taskID := range want {
		if tasks[index].Task.ID() != taskID {
			t.Fatalf("tasks[%d]=%s want=%s order=%#v", index, tasks[index].Task.ID(), taskID, tasks)
		}
	}
}

func currentActionTask(t *testing.T, roomID domain.RoomID) domain.Task {
	t.Helper()
	criterion, err := domain.NewAcceptanceCriterion(domain.NewCriterionID(), "done", "")
	if err != nil {
		t.Fatal(err)
	}
	task, err := domain.NewTask(domain.NewTaskID(), roomID, "Task", "Goal", []domain.AcceptanceCriterion{criterion})
	if err != nil {
		t.Fatal(err)
	}
	return task
}

func currentActionRun(t *testing.T, taskID domain.TaskID, state domain.RunState, at time.Time) domain.AgentRun {
	t.Helper()
	run, err := domain.RestoreAgentRun(domain.AgentRunRecord{ID: domain.NewRunID(), TaskID: taskID, CharterID: domain.NewCharterID(), State: state, Version: 7, CurrentAttemptNumber: 1, CreatedAt: at, UpdatedAt: at})
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func activeRunCount(state domain.RunState) int {
	if state == domain.RunStateAccepted || state == domain.RunStateCancelled {
		return 0
	}
	return 1
}
