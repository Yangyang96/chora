package app

import (
	"encoding/json"
	"fmt"
	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
	"sort"
	"testing"
	"time"
)

func boardFixture(t *testing.T, state domain.RunState) (domain.Project, domain.Room, storecontract.TaskBoardFacts) {
	t.Helper()
	now := time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC)
	pid := domain.NewProjectID()
	rid := domain.NewRoomID()
	p, e := domain.NewProject(domain.ProjectParams{ID: pid, Name: "Project", DefaultRoomID: rid, CreatedAt: now, UpdatedAt: now})
	if e != nil {
		t.Fatal(e)
	}
	r, e := domain.NewRoom(domain.RoomParams{ID: rid, ProjectID: pid, OwnershipKind: domain.RoomOwnershipProject, Name: "Room", WorkspaceRoot: t.TempDir(), CreatedAt: now, UpdatedAt: now})
	if e != nil {
		t.Fatal(e)
	}
	task := currentActionTask(t, rid)
	f := storecontract.TaskBoardFacts{TaskID: task.ID().String(), RoomID: rid.String(), Title: task.Title(), State: "open", Activity: now, Item: storecontract.TaskWorkspaceItem{Task: task}}
	if state != "" {
		run := currentActionRun(t, task.ID(), state, now)
		f.Item.LatestRun = &run
		f.Item.RunCount = 1
		revision := domain.NewTechnicalPlanRevisionID()
		f.Item.AcceptedRevisionID = revision
		f.Item.LatestRunBindingRevisionID = revision
		f.Item.ActiveRunCount = 1
		if state == domain.RunStateAccepted || state == domain.RunStateCancelled || state == domain.RunStateCompleted {
			f.Item.ActiveRunCount = 0
		}
		f.AttemptID = domain.NewAttemptID().String()
	}
	return p, r, f
}
func TestTaskBoardLifecycle(t *testing.T) {
	cases := []struct {
		name                           string
		state                          domain.RunState
		change                         func(*storecontract.TaskBoardFacts)
		phase, sub, attention, outcome string
	}{
		{"editing", "", func(f *storecontract.TaskBoardFacts) {
			f.Item.OpenDraftID = domain.NewTechnicalPlanDraftID()
			f.Item.OpenDraftEditVersion = 1
		}, "preparing", "editing_plan", "none", ""},
		{"plan review", "", func(f *storecontract.TaskBoardFacts) { f.Item.LatestRevisionID = domain.NewTechnicalPlanRevisionID() }, "preparing", "plan_review", "required", ""},
		{"initial ready", domain.RunStateReady, nil, "preparing", "ready_to_start", "none", ""},
		{"running", domain.RunStateRunning, nil, "working", "executing", "none", ""},
		{"gate", domain.RunStateRunning, func(f *storecontract.TaskBoardFacts) { f.GateID = "gate-current" }, "working", "waiting_gate", "required", ""},
		{"stopping", domain.RunStateStopping, nil, "working", "stopping", "none", ""},
		{"waiting checks", domain.RunStateAwaitingVerification, nil, "working", "awaiting_verification", "none", ""},
		{"checking", domain.RunStateVerifying, nil, "working", "verifying", "none", ""},
		{"recovery", domain.RunStateRecoveryRequired, nil, "working", "recovery_required", "required", ""},
		{"verification recovery", domain.RunStateVerificationRecoveryRequired, nil, "working", "verification_recovery_required", "required", ""},
		{"review", domain.RunStateAwaitingReview, func(f *storecontract.TaskBoardFacts) { f.Item.LatestVerificationResultID = domain.NewResultID() }, "review", "result_review", "required", ""},
		{"resource rejection", domain.RunStateRevisionRequired, func(f *storecontract.TaskBoardFacts) { f.ResourceResultID = "result"; f.ResourceReviewKind = "reject" }, "working", "changes_requested", "required", ""},
		{"cancel open", domain.RunStateCancelled, nil, "working", "stopped", "required", ""},
		{"cancel closed", domain.RunStateCancelled, func(f *storecontract.TaskBoardFacts) { f.State = "closed" }, "finished", "cancelled", "none", "cancelled"},
		{"no change", domain.RunStateCompleted, func(f *storecontract.TaskBoardFacts) {
			f.ResourceOutcome = "completed_no_change"
			f.Repositories = []storecontract.TaskBoardRepositoryFacts{{ResultPresent: true, ChecksMode: "none"}}
		}, "finished", "no_change", "none", "no_change"},
		{"legacy apply", domain.RunStateAccepted, func(f *storecontract.TaskBoardFacts) {
			f.Item.LatestPatchApplicationState = domain.PatchApplicationApplied
		}, "finished", "applied_locally", "none", "applied_locally"},
		{"missing legacy", domain.RunStateAccepted, nil, "", "needs_reconciliation", "unknown", ""},
		{"missing no change", domain.RunStateCompleted, nil, "", "needs_reconciliation", "unknown", ""},
		{"contradictory active", domain.RunStateAccepted, func(f *storecontract.TaskBoardFacts) { f.Item.ActiveRunCount = 1 }, "", "needs_reconciliation", "unknown", ""},
		{"invalid record", domain.RunStateRunning, func(f *storecontract.TaskBoardFacts) { f.Invalid = "unknown_state" }, "", "needs_reconciliation", "unknown", ""},
		{"missing attempt", domain.RunStateRunning, func(f *storecontract.TaskBoardFacts) { f.AttemptID = "" }, "", "needs_reconciliation", "unknown", ""},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			p, r, f := boardFixture(t, tt.state)
			if tt.change != nil {
				tt.change(&f)
			}
			c := deriveTaskBoardCard(p, r, f, time.Now())
			phase, outcome := "", ""
			if c.Phase != nil {
				phase = *c.Phase
			}
			if c.Outcome != nil {
				outcome = *c.Outcome
			}
			if phase != tt.phase || c.Substate != tt.sub || c.Attention.State != tt.attention || outcome != tt.outcome {
				t.Fatalf("card=%+v phase=%s outcome=%s", c, phase, outcome)
			}
		})
	}
}
func TestTaskBoardDeliveryAggregate(t *testing.T) {
	op := func(kind, state, pr string) storecontract.TaskBoardOperation {
		return storecontract.TaskBoardOperation{Kind: kind, State: state, PRState: pr, MergeCommit: "merge", UpdatedAt: time.Now()}
	}
	cases := []struct {
		name           string
		repos          []storecontract.TaskBoardRepositoryFacts
		phase, outcome string
	}{
		{"uncommitted", []storecontract.TaskBoardRepositoryFacts{{}}, "delivery", ""},
		{"commit", []storecontract.TaskBoardRepositoryFacts{{Operations: []storecontract.TaskBoardOperation{op("commit", "succeeded", "")}}}, "delivery", ""},
		{"push", []storecontract.TaskBoardRepositoryFacts{{Operations: []storecontract.TaskBoardOperation{op("push", "succeeded", "")}}}, "delivery", ""},
		{"open PR", []storecontract.TaskBoardRepositoryFacts{{Operations: []storecontract.TaskBoardOperation{op("pr", "succeeded", "open")}}}, "delivery", ""},
		{"closed PR", []storecontract.TaskBoardRepositoryFacts{{Operations: []storecontract.TaskBoardOperation{op("pr", "succeeded", "closed")}}}, "delivery", ""},
		{"merged", []storecontract.TaskBoardRepositoryFacts{{Operations: []storecontract.TaskBoardOperation{op("merge", "succeeded", "merged")}}}, "finished", "delivered"},
		{"partial", []storecontract.TaskBoardRepositoryFacts{{Operations: []storecontract.TaskBoardOperation{op("merge", "succeeded", "merged")}}, {}}, "delivery", ""},
		{"closed and open", []storecontract.TaskBoardRepositoryFacts{{Closed: true}, {Operations: []storecontract.TaskBoardOperation{op("pr", "succeeded", "open")}}}, "delivery", ""},
		{"merged and closed", []storecontract.TaskBoardRepositoryFacts{{Closed: true}, {Operations: []storecontract.TaskBoardOperation{op("merge", "succeeded", "merged")}}}, "finished", "partially_delivered_closed"},
		{"mixed", []storecontract.TaskBoardRepositoryFacts{{ApplyState: "applied"}, {Operations: []storecontract.TaskBoardOperation{op("merge", "succeeded", "merged")}}}, "finished", "mixed_delivery"},
		{"cleanup failure", []storecontract.TaskBoardRepositoryFacts{{Operations: []storecontract.TaskBoardOperation{op("merge", "succeeded", "merged"), op("cleanup", "failed", "")}}}, "finished", "delivered"},
		{"cleanup after abandonment", []storecontract.TaskBoardRepositoryFacts{{Closed: true, Operations: []storecontract.TaskBoardOperation{op("cleanup", "succeeded", "")}}}, "finished", "closed"},
		{"cleanup alone", []storecontract.TaskBoardRepositoryFacts{{Operations: []storecontract.TaskBoardOperation{op("cleanup", "succeeded", "")}}}, "delivery", ""},
		{"uncertain closure", []storecontract.TaskBoardRepositoryFacts{{Closed: true, Operations: []storecontract.TaskBoardOperation{op("push", "recovery_required", "")}}}, "delivery", ""},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			p, r, f := boardFixture(t, domain.RunStateAccepted)
			f.ResourceSnapshot = true
			f.ResourceResultID = "result"
			f.ResourceReviewKind = "accept"
			for i := range tt.repos {
				tt.repos[i].RepoID = domain.NewRepositoryID().String()
				tt.repos[i].ResultPresent = true
				tt.repos[i].Changed = 1
				tt.repos[i].Mode = "task_branch"
				tt.repos[i].Role = "write"
			}
			f.Repositories = tt.repos
			c := deriveTaskBoardCard(p, r, f, time.Now())
			result := ""
			if c.Outcome != nil {
				result = *c.Outcome
			}
			if c.Phase == nil || *c.Phase != tt.phase || result != tt.outcome {
				t.Fatalf("card=%+v result=%s", c, result)
			}
			if tt.name == "cleanup failure" && c.Attention.State != "required" {
				t.Fatal("cleanup failure hidden")
			}
		})
	}
}
func TestTaskBoardRetryAndArchiveOverlays(t *testing.T) {
	for _, name := range []string{"pending", "exhausted", "blocked", "retrying"} {
		t.Run(name, func(t *testing.T) {
			p, r, f := boardFixture(t, domain.RunStateRecoveryRequired)
			f.RetryFinalized = true
			f.FailureReason = "runtime_exit_nonzero"
			f.RetryEvents = []storecontract.TaskBoardRetryEvent{{Type: automaticRetryEnabledEvent}}
			want := "retry_pending"
			attention := "none"
			switch name {
			case "exhausted":
				payload, _ := json.Marshal(automaticRetryEvent{AttemptID: "prior", PredecessorID: "older", RetriesUsed: 2, MaxRetries: 2})
				f.RetryEvents = append(f.RetryEvents, storecontract.TaskBoardRetryEvent{Type: automaticRetryPreparedEvent, Payload: payload})
				want = "retry_exhausted"
				attention = "required"
			case "blocked":
				f.RetryFinalized = false
				want = "retry_blocked"
				attention = "required"
			case "retrying":
				run := currentActionRun(t, f.Item.Task.ID(), domain.RunStateReady, f.Activity)
				f.Item.LatestRun = &run
				payload, _ := json.Marshal(automaticRetryEvent{AttemptID: f.AttemptID, PredecessorID: "older", RetriesUsed: 1, MaxRetries: 2})
				f.RetryEvents = append(f.RetryEvents, storecontract.TaskBoardRetryEvent{Type: automaticRetryPreparedEvent, Payload: payload})
				want = "retrying"
			}
			f.Archived = true
			c := deriveTaskBoardCard(p, r, f, time.Now())
			if c.Substate != want || c.Attention.State != attention || !c.Visibility.ReadOnly || c.NextAction.Kind != "open_task" {
				t.Fatalf("card=%+v", c)
			}
		})
	}
}

func TestTaskBoardPlanningSuccessorsAndReconciliation(t *testing.T) {
	for _, route := range []string{"planning", "related", "retry_ready", "bad_count", "unknown_mode", "reference_changed", "missing_repo", "missing_checks", "gate_resolved", "gate_reopened"} {
		t.Run(route, func(t *testing.T) {
			p, r, f := boardFixture(t, domain.RunStateRevisionRequired)
			rev := domain.NewTechnicalPlanRevisionID()
			f.Item.AcceptedRevisionID = rev
			f.Item.LatestRunBindingRevisionID = rev
			f.Item.LatestVerifiedReviewID = domain.NewReviewDecisionID()
			f.Item.LatestVerifiedReviewKind = domain.ReviewDecisionReject
			f.Item.LatestVerifiedReviewRunVersion = f.Item.LatestRun.Version() - 1
			f.Item.LatestRejectionClass = domain.ReviewRejectionImplementationGap
			want := "working"
			sub := "changes_requested"
			switch route {
			case "planning":
				f.Item.LatestRejectionClass = domain.ReviewRejectionPlanningGap
				f.Item.RouteRejectionClass = f.Item.LatestRejectionClass
				f.Item.RouteSourceRunID = f.Item.LatestRun.ID()
				f.Item.RouteSourceTaskID = f.Item.Task.ID()
				f.Item.RoutePlanningDraftID = domain.NewTechnicalPlanDraftID()
				f.Item.RoutePlanningDraftPredecessorRevisionID = rev
				want = "preparing"
				sub = "replanning"
			case "related":
				f.Item.LatestRejectionClass = domain.ReviewRejectionContractChangeRequired
				f.Item.RouteRejectionClass = f.Item.LatestRejectionClass
				f.Item.RouteSourceRunID = f.Item.LatestRun.ID()
				f.Item.RouteSourceTaskID = f.Item.Task.ID()
				f.Item.RouteRelatedTaskID = domain.NewTaskID()
				f.Item.RouteRelatedTaskPredecessorTaskID = f.Item.Task.ID()
				sub = "related_task"
			case "retry_ready":
				run, e := domain.RestoreAgentRun(domain.AgentRunRecord{ID: f.Item.LatestRun.ID(), TaskID: f.Item.Task.ID(), CharterID: domain.NewCharterID(), State: domain.RunStateReady, Version: 8, CurrentAttemptNumber: 2, CreatedAt: f.Activity, UpdatedAt: f.Activity})
				if e != nil {
					t.Fatal(e)
				}
				f.Item.LatestRun = &run
				sub = "waiting_retry"
			case "bad_count":
				f.Item.ActiveRunCount = 0
				want = ""
			case "unknown_mode":
				f.Repositories = []storecontract.TaskBoardRepositoryFacts{{Mode: "magic"}}
				want = ""
			case "reference_changed":
				f.Repositories = []storecontract.TaskBoardRepositoryFacts{{Role: "reference", Changed: 1}}
				want = ""
			case "missing_repo":
				run := currentActionRun(t, f.Item.Task.ID(), domain.RunStateAccepted, f.Activity)
				f.Item.LatestRun = &run
				f.Item.ActiveRunCount = 0
				f.ResourceSnapshot = true
				f.ResourceResultID = "result"
				f.ResourceReviewKind = "accept"
				f.Repositories = []storecontract.TaskBoardRepositoryFacts{{RepoID: "repo", Changed: 1}}
				want = ""
			case "missing_checks":
				run := currentActionRun(t, f.Item.Task.ID(), domain.RunStateCompleted, f.Activity)
				f.Item.LatestRun = &run
				f.Item.ActiveRunCount = 0
				f.ResourceOutcome = "completed_no_change"
				f.Repositories = []storecontract.TaskBoardRepositoryFacts{{ResultPresent: true, ChecksMode: "named", ChecksStatus: "UNKNOWN"}}
				want = ""
			case "gate_resolved", "gate_reopened":
				run := currentActionRun(t, f.Item.Task.ID(), domain.RunStateRunning, f.Activity)
				f.Item.LatestRun = &run
				sub = "executing"
				if route == "gate_reopened" {
					f.GateID = "new-current-gate"
					sub = "waiting_gate"
				}
			}
			c := deriveTaskBoardCard(p, r, f, time.Now())
			if want == "" {
				if c.Phase != nil || c.Attention.State != "unknown" {
					t.Fatalf("not reconciled: %+v", c)
				}
				return
			}
			if c.Phase == nil || *c.Phase != want || c.Substate != sub {
				t.Fatalf("card=%+v", c)
			}
			if route == "gate_reopened" && c.Attention.Reasons[0].TargetID != "new-current-gate" {
				t.Fatal("stale gate")
			}
			if route == "related" && c.Outcome != nil {
				t.Fatal("related link invents end disposition")
			}
		})
	}
}

func TestTaskBoardCountsScaleAndCursorScope(t *testing.T) {
	p, r, first := boardFixture(t, domain.RunStateRunning)
	snapshot := storecontract.TaskBoardSnapshot{Project: p, Rooms: []domain.Room{r}}
	for i := 0; i < 117; i++ {
		f := first
		task := currentActionTask(t, r.ID())
		f.TaskID = task.ID().String()
		f.Item.Task = task
		run := currentActionRun(t, task.ID(), domain.RunStateRunning, f.Activity)
		f.Item.LatestRun = &run
		if i%2 == 0 {
			f.GateID = "current-gate"
		}
		snapshot.Tasks = append(snapshot.Tasks, f)
	}
	v, e := projectTaskBoard(snapshot, TaskBoardQuery{}, time.Now())
	if e != nil || v.Total != 117 || len(v.Cards) != 50 || v.Counts.Phase["working"] != 117 || v.Counts.Attention != 59 {
		t.Fatalf("view=%+v err=%v", v, e)
	}
	n, e := projectTaskBoard(snapshot, TaskBoardQuery{Cursor: v.NextCursor}, time.Now())
	if e != nil || n.Snapshot != v.Snapshot || len(n.Cards) != 50 {
		t.Fatalf("next=%+v err=%v", n, e)
	}
	seen := map[string]bool{}
	for _, c := range v.Cards {
		seen[c.TaskID] = true
	}
	for _, c := range n.Cards {
		if seen[c.TaskID] {
			t.Fatal("duplicate card")
		}
	}
	filtered, e := projectTaskBoard(snapshot, TaskBoardQuery{Attention: "required", Limit: 10}, time.Now())
	if e != nil || filtered.Total != 59 || filtered.Counts.Phase["working"] != 59 || filtered.Counts.Attention != 59 {
		t.Fatalf("filtered=%+v err=%v", filtered, e)
	}
	if _, e = projectTaskBoard(snapshot, TaskBoardQuery{Attention: "required", Cursor: v.NextCursor}, time.Now()); e != ErrTaskBoardSnapshotChanged {
		t.Fatal("cursor broadened scope", e)
	}
}

func TestTaskBoardRequiresExactPlanBinding(t *testing.T) {
	for _, state := range []domain.RunState{domain.RunStateRunning, domain.RunStateAccepted} {
		for _, missing := range []string{"binding", "acceptance", "both"} {
			t.Run(string(state)+"/"+missing, func(t *testing.T) {
				p, r, f := boardFixture(t, state)
				if missing != "acceptance" {
					f.Item.LatestRunBindingRevisionID = domain.TechnicalPlanRevisionID{}
				}
				if missing != "binding" {
					f.Item.AcceptedRevisionID = domain.TechnicalPlanRevisionID{}
				}
				c := deriveTaskBoardCard(p, r, f, time.Now())
				if c.Phase != nil || c.Attention.State != "unknown" || c.Attention.Reasons[0].Code != "invalid_plan_lineage" {
					t.Fatalf("unbound Run trusted: %+v", c)
				}
			})
		}
	}
}

func TestTaskBoardGateQuestionAndResolvedIdentity(t *testing.T) {
	p, r, f := boardFixture(t, domain.RunStateRunning)
	f.GateID = "current-gate"
	f.GateQuestion = "Which target branch should receive this change?"
	c := deriveTaskBoardCard(p, r, f, time.Now())
	if len(c.Attention.Reasons) != 1 || c.Attention.Reasons[0].TargetID != f.GateID || c.Attention.Reasons[0].Summary != f.GateQuestion || c.Attention.Reasons[0].ExpectedVersion != 0 {
		t.Fatalf("gate summary=%+v", c.Attention)
	}
	f.GateID = ""
	f.GateQuestion = ""
	c = deriveTaskBoardCard(p, r, f, time.Now())
	if len(c.Attention.Reasons) != 0 || c.Attention.State != "none" {
		t.Fatalf("resolved gate still shown: %+v", c.Attention)
	}
}

func TestTaskBoardDeliveryObservationRetainsEvidenceAge(t *testing.T) {
	old := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	newer := old.Add(time.Hour)
	repo := storecontract.TaskBoardRepositoryFacts{Operations: []storecontract.TaskBoardOperation{{Kind: "pr", State: "succeeded", UpdatedAt: old}, {Kind: "merge", State: "preview", UpdatedAt: newer}}}
	if got := boardDeliveryObservedAt(repo); got == nil || !got.Equal(old) {
		t.Fatalf("preview concealed stale evidence: %v", got)
	}
	repo.Operations[0].UpdatedAt = newer
	if got := boardDeliveryObservedAt(repo); got == nil || !got.Equal(newer) {
		t.Fatalf("observation not advanced: %v", got)
	}
	if got := boardDeliveryObservedAt(storecontract.TaskBoardRepositoryFacts{}); got != nil {
		t.Fatalf("invented observation: %v", got)
	}
}

func TestTaskBoardExplicitClosureDoesNotRequireTaskClosed(t *testing.T) {
	for _, state := range []domain.RunState{domain.RunStateAwaitingReview, domain.RunStateRecoveryRequired, domain.RunStateRevisionRequired} {
		for _, partial := range []bool{false, true} {
			t.Run(string(state)+"/partial="+fmt.Sprint(partial), func(t *testing.T) {
				p, r, f := boardFixture(t, state)
				f.Closure = true
				f.ResourceSnapshot = true
				f.ResourceResultID = "current-result"
				f.Repositories = []storecontract.TaskBoardRepositoryFacts{{RepoID: "first", Role: "write", Changed: 1, ResultPresent: true, Closed: true}}
				if partial {
					f.Repositories = append(f.Repositories, storecontract.TaskBoardRepositoryFacts{RepoID: "second", Role: "write", Changed: 1, ResultPresent: true})
				}
				c := deriveTaskBoardCard(p, r, f, time.Now())
				if partial {
					if c.Phase == nil || *c.Phase == "finished" || c.Outcome != nil {
						t.Fatalf("partial closure finished: %+v", c)
					}
				} else if c.Phase == nil || *c.Phase != "finished" || c.Outcome == nil || *c.Outcome != "closed" || c.Attention.State != "none" {
					t.Fatalf("open Task's explicit closure ignored: %+v", c)
				}
			})
		}
	}
}

func TestTaskBoardClosedZeroChangeFailedChecksRemainUnverified(t *testing.T) {
	for _, status := range []string{"FAIL", "UNKNOWN"} {
		p, r, f := boardFixture(t, domain.RunStateRecoveryRequired)
		f.Closure = true
		f.ResourceSnapshot = true
		f.ResourceResultID = "result"
		f.ResourceOutcome = "checks_failed"
		f.Repositories = []storecontract.TaskBoardRepositoryFacts{{RepoID: "repo", Role: "write", ResultPresent: true, Closed: true, ChecksMode: "named", ChecksStatus: status}}
		c := deriveTaskBoardCard(p, r, f, time.Now())
		wantChecks := "failed"
		if status == "UNKNOWN" {
			wantChecks = "unavailable"
		}
		if c.Phase == nil || *c.Phase != "finished" || c.Outcome == nil || *c.Outcome != "closed" || c.Repositories[0].Status != "closed" || c.Repositories[0].Checks != wantChecks {
			t.Fatalf("closed unverified no-change result=%+v", c)
		}
	}
}

func TestTaskBoardCleanupDoesNotFreshenMergedObservation(t *testing.T) {
	merged := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	repo := storecontract.TaskBoardRepositoryFacts{Operations: []storecontract.TaskBoardOperation{{Kind: "merge", State: "succeeded", PRState: "merged", MergeCommit: "commit", UpdatedAt: merged}, {Kind: "cleanup", State: "succeeded", UpdatedAt: merged.Add(24 * time.Hour)}, {Kind: "merge", State: "failed", UpdatedAt: merged.Add(25 * time.Hour)}}}
	if got := boardDeliveryObservedAt(repo); got == nil || !got.Equal(merged) {
		t.Fatalf("maintenance refreshed merge evidence: %v", got)
	}
}

func TestTaskBoardFilterOptionsHaveIndependentBoundedCursor(t *testing.T) {
	p, r, f := boardFixture(t, domain.RunStateRunning)
	s := storecontract.TaskBoardSnapshot{Project: p, Rooms: []domain.Room{r}, Tasks: []storecontract.TaskBoardFacts{f}}
	for i := 0; i < 124; i++ {
		room, e := domain.NewRoom(domain.RoomParams{ID: domain.NewRoomID(), ProjectID: p.ID(), OwnershipKind: domain.RoomOwnershipProject, Name: fmt.Sprintf("Room %d", i), WorkspaceRoot: fmt.Sprintf("%s/room-%d", r.WorkspaceRoot(), i), CreatedAt: f.Activity, UpdatedAt: f.Activity})
		if e != nil {
			t.Fatal(e)
		}
		s.Rooms = append(s.Rooms, room)
	}
	for i := 0; i < 507; i++ {
		s.Repositories = append(s.Repositories, storecontract.TaskBoardRepository{RepoID: domain.NewRepositoryID().String(), Name: fmt.Sprintf("Repo %d", i)})
	}
	first, e := projectTaskBoard(s, TaskBoardQuery{}, time.Now())
	if e != nil {
		t.Fatal(e)
	}
	if len(first.Rooms) != 100 || len(first.Repositories) != 100 || first.NextOptionsCursor == "" || first.OptionsSnapshot == "" {
		t.Fatal("unbounded/missing options")
	}
	seenRooms, seenRepos := map[string]bool{}, map[string]bool{}
	page := first
	for {
		for _, r := range page.Rooms {
			if seenRooms[r.RoomID] {
				t.Fatal("duplicate room page")
			}
			seenRooms[r.RoomID] = true
		}
		for _, r := range page.Repositories {
			if seenRepos[r.RepoID] {
				t.Fatal("duplicate repo page")
			}
			seenRepos[r.RepoID] = true
		}
		if page.NextOptionsCursor == "" {
			break
		}
		page, e = projectTaskBoard(s, TaskBoardQuery{OptionsCursor: page.NextOptionsCursor}, time.Now())
		if e != nil {
			t.Fatal(e)
		}
		if page.Snapshot != first.Snapshot || page.OptionsSnapshot != first.OptionsSnapshot || len(page.Rooms) > 100 || len(page.Repositories) > 100 {
			t.Fatal("option pagination changes cards/bounds")
		}
	}
	if len(seenRooms) != 125 || len(seenRepos) != 507 {
		t.Fatalf("options inaccessible: rooms=%d repos=%d", len(seenRooms), len(seenRepos))
	}
	roomIDs, repoIDs := []string{}, []string{}
	for id := range seenRooms {
		roomIDs = append(roomIDs, id)
	}
	for id := range seenRepos {
		repoIDs = append(repoIDs, id)
	}
	sort.Strings(roomIDs)
	sort.Strings(repoIDs)
	selected, e := projectTaskBoard(s, TaskBoardQuery{RoomID: roomIDs[len(roomIDs)-1], RepoID: repoIDs[len(repoIDs)-1]}, time.Now())
	if e != nil {
		t.Fatal(e)
	}
	if len(selected.Rooms) != 101 || len(selected.Repositories) != 101 || selected.Rooms[100].RoomID != roomIDs[124] || selected.Repositories[100].RepoID != repoIDs[506] {
		t.Fatal("selected off-page option missing")
	}
	s.Tasks[0].GateID = "current-gate"
	progress, e := projectTaskBoard(s, TaskBoardQuery{OptionsCursor: first.NextOptionsCursor}, time.Now())
	if e != nil || progress.OptionsSnapshot != first.OptionsSnapshot || progress.Snapshot == first.Snapshot {
		t.Fatal("task progress invalidated filter cursor", e)
	}
	s.Repositories[0].Name = "Renamed"
	if _, e = projectTaskBoard(s, TaskBoardQuery{OptionsCursor: first.NextOptionsCursor}, time.Now()); e != ErrTaskBoardSnapshotChanged {
		t.Fatalf("stale options accepted: %v", e)
	}
	if _, e = projectTaskBoard(s, TaskBoardQuery{OptionsCursor: "bad"}, time.Now()); e != domain.ErrInvalidArgument {
		t.Fatal("invalid cursor accepted", e)
	}
}
