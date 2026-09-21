package app

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
	"sort"
	"strconv"
	"strings"
	"time"
)

var ErrTaskBoardSnapshotChanged = errors.New("task board changed; restart pagination")

type TaskBoardQuery struct {
	RoomID, RepoID, Phase, Attention, Archived, Cursor, OptionsCursor string
	Limit                                                             int
}
type TaskBoardReason struct {
	Summary         string `json:"summary,omitempty"`
	Code            string `json:"code"`
	TargetID        string `json:"targetId,omitempty"`
	ExpectedVersion uint64 `json:"expectedVersion,omitempty"`
}
type TaskBoardAttention struct {
	State   string            `json:"state"`
	Reasons []TaskBoardReason `json:"reasons"`
}
type TaskBoardSource struct {
	LatestRunID      string    `json:"latestRunId,omitempty"`
	LatestRunVersion uint64    `json:"latestRunVersion,omitempty"`
	LatestAttemptID  string    `json:"latestAttemptId,omitempty"`
	ObservedAt       time.Time `json:"observedAt"`
}
type TaskBoardAction struct {
	Kind               string `json:"kind"`
	URL                string `json:"url"`
	ExpectedVersion    uint64 `json:"expectedVersion,omitempty"`
	AvailabilityReason string `json:"availabilityReason,omitempty"`
}
type TaskBoardVisibility struct {
	TaskArchived    bool `json:"taskArchived"`
	RoomArchived    bool `json:"roomArchived"`
	ProjectArchived bool `json:"projectArchived"`
	ReadOnly        bool `json:"readOnly"`
}
type TaskBoardRepository struct {
	DeliveryObservedAt *time.Time `json:"deliveryObservedAt,omitempty"`
	RepoID             string     `json:"repoId"`
	Name               string     `json:"name"`
	Status             string     `json:"status"`
	Checks             string     `json:"checks"`
	Achievements       []string   `json:"achievements"`
}
type TaskBoardCard struct {
	ProjectID      string                `json:"projectId"`
	RoomID         string                `json:"roomId"`
	RoomName       string                `json:"roomName"`
	TaskID         string                `json:"taskId"`
	Title          string                `json:"title"`
	Repositories   []TaskBoardRepository `json:"repositories"`
	Phase          *string               `json:"phase"`
	Substate       string                `json:"substate"`
	Attention      TaskBoardAttention    `json:"attention"`
	Outcome        *string               `json:"outcome"`
	NextAction     TaskBoardAction       `json:"nextAction"`
	Visibility     TaskBoardVisibility   `json:"visibility"`
	Health         string                `json:"health"`
	LastActivityAt time.Time             `json:"lastActivityAt"`
	Source         TaskBoardSource       `json:"source"`
}
type TaskBoardRoom struct {
	RoomID string `json:"roomId"`
	Name   string `json:"name"`
}
type TaskBoardCounts struct {
	Phase          map[string]int `json:"phase"`
	Attention      int            `json:"attention"`
	Reconciliation int            `json:"reconciliation"`
}
type TaskBoardView struct {
	OptionsSnapshot   string                              `json:"optionsSnapshot"`
	NextOptionsCursor string                              `json:"nextOptionsCursor"`
	Cards             []TaskBoardCard                     `json:"cards"`
	Total             int                                 `json:"total"`
	Counts            TaskBoardCounts                     `json:"counts"`
	ObservedAt        time.Time                           `json:"observedAt"`
	NextCursor        string                              `json:"nextCursor"`
	Snapshot          string                              `json:"snapshot"`
	Rooms             []TaskBoardRoom                     `json:"rooms"`
	Repositories      []storecontract.TaskBoardRepository `json:"repositories"`
}

func (s *Service) GetTaskBoard(ctx context.Context, id domain.ProjectID, q TaskBoardQuery) (TaskBoardView, error) {
	snapshot, err := s.deps.Store.Reader().GetTaskBoardSnapshot(ctx, id)
	if err != nil {
		return TaskBoardView{}, err
	}
	return projectTaskBoard(snapshot, q, s.deps.Clock.Now())
}
func projectTaskBoard(s storecontract.TaskBoardSnapshot, q TaskBoardQuery, now time.Time) (TaskBoardView, error) {
	if q.Limit == 0 {
		q.Limit = 50
	}
	if q.Limit < 1 || q.Limit > 100 {
		return TaskBoardView{}, domain.ErrInvalidArgument
	}
	if q.Archived == "" {
		q.Archived = "exclude"
	}
	if q.Archived != "exclude" && q.Archived != "include" && q.Archived != "only" {
		return TaskBoardView{}, domain.ErrInvalidArgument
	}
	switch q.Phase {
	case "", "preparing", "working", "review", "delivery", "finished", "reconciliation":
	default:
		return TaskBoardView{}, domain.ErrInvalidArgument
	}
	if q.Attention != "" && q.Attention != "none" && q.Attention != "required" && q.Attention != "unknown" {
		return TaskBoardView{}, domain.ErrInvalidArgument
	}
	v := TaskBoardView{Cards: []TaskBoardCard{}, Rooms: []TaskBoardRoom{}, Repositories: s.Repositories, ObservedAt: now, Counts: TaskBoardCounts{Phase: map[string]int{"preparing": 0, "working": 0, "review": 0, "delivery": 0, "finished": 0}}}
	rooms := map[string]domain.Room{}
	for _, r := range s.Rooms {
		rooms[r.ID().String()] = r
		v.Rooms = append(v.Rooms, TaskBoardRoom{r.ID().String(), r.Name()})
	}
	if q.RoomID != "" {
		if _, ok := rooms[q.RoomID]; !ok {
			return v, domain.ErrInvalidArgument
		}
	}
	if q.RepoID != "" {
		found := false
		for _, r := range s.Repositories {
			if r.RepoID == q.RepoID {
				found = true
			}
		}
		if !found {
			return v, domain.ErrInvalidArgument
		}
	}
	if err := pageTaskBoardOptions(&v, s.Project.ID().String(), q); err != nil {
		return v, err
	}
	cards := []TaskBoardCard{}
	for _, f := range s.Tasks {
		r, ok := rooms[f.RoomID]
		if !ok {
			f.Invalid = "invalid_scope"
		}
		c := deriveTaskBoardCard(s.Project, r, f, time.Time{})
		if q.RoomID != "" && c.RoomID != q.RoomID {
			continue
		}
		if q.Archived == "exclude" && c.Visibility.TaskArchived || q.Archived == "only" && !c.Visibility.TaskArchived {
			continue
		}
		if q.RepoID != "" {
			found := false
			for _, r := range c.Repositories {
				if r.RepoID == q.RepoID {
					found = true
				}
			}
			if !found {
				continue
			}
		}
		if q.Phase != "" {
			if c.Phase == nil && q.Phase != "reconciliation" || c.Phase != nil && *c.Phase != q.Phase {
				continue
			}
		}
		if q.Attention != "" && c.Attention.State != q.Attention {
			continue
		}
		cards = append(cards, c)
	}
	sort.Slice(cards, func(i, j int) bool {
		if cards[i].LastActivityAt.Equal(cards[j].LastActivityAt) {
			return cards[i].TaskID < cards[j].TaskID
		}
		return cards[i].LastActivityAt.After(cards[j].LastActivityAt)
	})
	for _, c := range cards {
		if c.Phase == nil {
			v.Counts.Reconciliation++
		} else {
			v.Counts.Phase[*c.Phase]++
		}
		if c.Attention.State == "required" {
			v.Counts.Attention++
		}
	}
	v.Total = len(cards)
	hashBytes, _ := json.Marshal(struct {
		Project string
		Query   TaskBoardQuery
		Cards   []TaskBoardCard
	}{s.Project.ID().String(), TaskBoardQuery{RoomID: q.RoomID, RepoID: q.RepoID, Phase: q.Phase, Attention: q.Attention, Archived: q.Archived}, cards})
	digest := sha256.Sum256(hashBytes)
	v.Snapshot = hex.EncodeToString(digest[:])
	offset := 0
	if q.Cursor != "" {
		raw, e := base64.RawURLEncoding.DecodeString(q.Cursor)
		parts := strings.Split(string(raw), ":")
		if e != nil || len(parts) != 2 {
			return v, domain.ErrInvalidArgument
		}
		if parts[0] != v.Snapshot {
			return v, ErrTaskBoardSnapshotChanged
		}
		offset, e = strconv.Atoi(parts[1])
		if e != nil || offset < 0 || offset > len(cards) {
			return v, domain.ErrInvalidArgument
		}
	}
	end := min(offset+q.Limit, len(cards))
	v.Cards = cards[offset:end]
	for i := range v.Cards {
		v.Cards[i].Source.ObservedAt = now
	}
	if end < len(cards) {
		v.NextCursor = base64.RawURLEncoding.EncodeToString([]byte(v.Snapshot + ":" + strconv.Itoa(end)))
	}
	return v, nil
}

// deriveTaskBoardCard is a total, pure read-model projection. Unknown evidence
// remains visible instead of becoming a guessed lifecycle state.
func deriveTaskBoardCard(p domain.Project, r domain.Room, f storecontract.TaskBoardFacts, now time.Time) TaskBoardCard {
	c := TaskBoardCard{ProjectID: p.ID().String(), RoomID: f.RoomID, RoomName: r.Name(), TaskID: f.TaskID, Title: f.Title, Repositories: []TaskBoardRepository{}, Attention: TaskBoardAttention{State: "none", Reasons: []TaskBoardReason{}}, Health: "current", LastActivityAt: f.Activity, Source: TaskBoardSource{LatestAttemptID: f.AttemptID, ObservedAt: now}, NextAction: TaskBoardAction{Kind: "open_task", URL: "/rooms/" + f.RoomID + "/tasks/" + f.TaskID}}
	c.Visibility = TaskBoardVisibility{TaskArchived: f.Archived, RoomArchived: r.State() == domain.RoomStateArchived, ProjectArchived: p.State() == domain.ProjectStateArchived}
	c.Visibility.ReadOnly = c.Visibility.TaskArchived || c.Visibility.RoomArchived || c.Visibility.ProjectArchived
	for _, repo := range f.Repositories {
		c.Repositories = append(c.Repositories, TaskBoardRepository{RepoID: repo.RepoID, Name: repo.Name, Status: "not_started", Checks: boardChecks(repo), Achievements: boardAchievements(repo), DeliveryObservedAt: boardDeliveryObservedAt(repo)})
	}
	reconcile := func(code string) TaskBoardCard {
		c.Phase = nil
		c.Outcome = nil
		c.Substate = "needs_reconciliation"
		c.Health = "inconsistent"
		c.Attention = TaskBoardAttention{State: "unknown", Reasons: []TaskBoardReason{{Code: code}}}
		c.NextAction.Kind = "open_task"
		c.NextAction.AvailabilityReason = "needs_reconciliation"
		return c
	}
	set := func(phase, sub, action string) { c.Phase = &phase; c.Substate = sub; c.NextAction.Kind = action }
	attention := func(code, target string) {
		c.Attention.State = "required"
		c.Attention.Reasons = append(c.Attention.Reasons, TaskBoardReason{Code: code, TargetID: target, ExpectedVersion: c.Source.LatestRunVersion})
	}
	outcome := func(value string) { c.Outcome = &value; set("finished", value, "view_result") }
	if f.Invalid != "" {
		return reconcile(f.Invalid)
	}
	if !r.ID().Valid() || r.ProjectID() != p.ID() || f.Item.Task.ID().String() != f.TaskID || f.Item.Task.RoomID() != r.ID() {
		return reconcile("invalid_scope")
	}
	if f.State != "open" && f.State != "closed" {
		return reconcile("invalid_task_state")
	}
	for _, repo := range f.Repositories {
		if repo.Mode != "" && repo.Mode != "task_branch" {
			return reconcile("unknown_delivery_mode")
		}
		if (f.ResourceSnapshot || repo.Role != "") && repo.Role != "write" && repo.Role != "reference" {
			return reconcile("unknown_repository_role")
		}
		if repo.Role == "reference" && repo.Changed > 0 {
			return reconcile("read_only_repository_changed")
		}
	}
	item := f.Item
	run := item.LatestRun
	if run == nil {
		if item.RunCount != 0 || item.ActiveRunCount != 0 {
			return reconcile("missing_run_summary")
		}
		if f.State == "closed" {
			return reconcile("missing_end_disposition")
		}
		a := derivePlanningAction(item)
		switch a.Kind {
		case CurrentActionEditPlan:
			set("preparing", "editing_plan", "edit_plan")
		case CurrentActionReviewPlan:
			set("preparing", "plan_review", "review_plan")
			attention("plan_review", a.Target.RevisionID.String())
		case CurrentActionStartRun:
			set("preparing", "ready_to_start", "start_run")
		default:
			return reconcile("invalid_plan_lineage")
		}
		c.NextAction.ExpectedVersion = a.Target.ExpectedVersion
	} else {
		c.Source.LatestRunID = run.ID().String()
		c.Source.LatestRunVersion = run.Version()
		c.NextAction.URL += "/runs/" + run.ID().String()
		c.NextAction.ExpectedVersion = run.Version()
		if run.TaskID() != item.Task.ID() || item.RunCount < 1 || item.ActiveRunCount > 1 {
			return reconcile("invalid_run_lineage")
		}
		terminal := run.State() == domain.RunStateAccepted || run.State() == domain.RunStateCancelled || run.State() == domain.RunStateCompleted
		if !terminal && item.ActiveRunCount != 1 {
			return reconcile("invalid_run_counts")
		}
		if item.ActiveRunCount != 0 && terminal {
			return reconcile("active_predecessor")
		}
		if run.CurrentAttemptNumber() > 0 && f.AttemptID == "" {
			return reconcile("missing_attempt")
		}
		if !item.LatestRunBindingRevisionID.Valid() || !item.AcceptedRevisionID.Valid() || item.LatestRunBindingRevisionID != item.AcceptedRevisionID {
			return reconcile("invalid_plan_lineage")
		}
		projectedState := run.State()
		if f.Closure && (projectedState == domain.RunStateRunning || projectedState == domain.RunStateStopping || projectedState == domain.RunStateReady || projectedState == domain.RunStateDraft || projectedState == domain.RunStateAwaitingVerification || projectedState == domain.RunStateVerifying) {
			return reconcile("closure_with_active_work")
		}

		// Closure is its own durable disposition. CloseResult deliberately
		// preserves Task.open and Run history; it does not manufacture review.
		// Reuse repository aggregation so a partial closure cannot finish work.
		if f.Closure && !item.OpenDraftID.Valid() && (projectedState == domain.RunStateAwaitingReview || projectedState == domain.RunStateRecoveryRequired || projectedState == domain.RunStateRevisionRequired) {
			projectedState = domain.RunStateAccepted
		}
		switch projectedState {
		case domain.RunStateDraft, domain.RunStateReady:
			if run.CurrentAttemptNumber() > 1 || !run.StartedAt().IsZero() {
				set("working", "waiting_retry", "retry")
			} else {
				set("preparing", "ready_to_start", "start_attempt")
			}
			for _, repo := range f.Repositories {
				if repo.Preparation == "failed" || repo.Preparation == "recovery_required" {
					c.Substate = "resource_preparation_failed"
					c.NextAction.Kind = "open_task"
					attention("resource_preparation_failed", repo.RepoID)
				} else if repo.Role == "write" && repo.Preparation != "ready" {
					c.Substate = "resource_preparation"
					c.NextAction.Kind = "open_task"
				}
			}
		case domain.RunStateRunning:
			set("working", "executing", "monitor")
		case domain.RunStateStopping:
			set("working", "stopping", "monitor")
		case domain.RunStateAwaitingVerification:
			set("working", "awaiting_verification", "open_checks")
		case domain.RunStateVerifying:
			set("working", "verifying", "open_checks")
		case domain.RunStateRecoveryRequired:
			set("working", "recovery_required", "recover")
			attention("recovery_required", run.ID().String())
		case domain.RunStateVerificationRecoveryRequired:
			set("working", "verification_recovery_required", "recover_verification")
			attention("verification_recovery_required", run.ID().String())
		case domain.RunStateRevisionRequired:
			if f.OutcomeKind == "document" && f.DocumentStatus == "pending" && f.ResourceResultID != "" {
				set("review", "document_review", "review_result")
				attention("document_review", run.ID().String())
				break
			}
			if f.OutcomeKind == "document" && f.DocumentStatus == "accept" && f.ResourceResultID != "" {
				outcome("document_accepted")
				break
			}
			if f.ResourceResultID != "" && (f.ResourceReviewKind == "reject" || f.ResourceOutcome == "checks_failed" || f.ResourceOutcome == "checks_incomplete") {
				set("working", "changes_requested", "retry")
				attention("changes_requested", f.ResourceResultID)
			} else {
				a := deriveRejectionAction(item, CurrentActionTarget{RoomID: r.ID(), TaskID: item.Task.ID(), RunID: run.ID(), ExpectedVersion: run.Version()}, c.NextAction.URL)
				switch a.Kind {
				case CurrentActionContinueSuccessorPlan:
					set("preparing", "replanning", string(a.Kind))
					c.NextAction.URL = a.URL
				case CurrentActionOpenRelatedTask:
					set("working", "related_task", string(a.Kind))
					c.NextAction.URL = a.URL
				case CurrentActionRetryImplementation:
					set("working", "changes_requested", "retry")
				default:
					return reconcile("invalid_review_lineage")
				}
				attention(c.Substate, a.Target.TaskID.String())
			}
		case domain.RunStateAwaitingReview:
			if f.ResourceResultID == "" && !item.LatestVerificationResultID.Valid() {
				return reconcile("missing_result_evidence")
			}
			set("review", "result_review", "review_result")
			attention("result_review", run.ID().String())
		case domain.RunStateCancelled:
			if f.State == "closed" && item.ActiveRunCount == 0 {
				outcome("cancelled")
			} else {
				set("working", "stopped", "retry")
				attention("stopped", run.ID().String())
			}
		case domain.RunStateCompleted:
			if f.ResourceOutcome != "completed_no_change" || len(f.Repositories) == 0 {
				return reconcile("missing_no_change_evidence")
			}
			for _, repo := range f.Repositories {
				if !repo.ResultPresent || repo.Changed != 0 || (repo.ChecksMode != "none" && !repo.ChecksNotApplicable && repo.ChecksStatus != "PASS") {
					return reconcile("invalid_no_change_evidence")
				}
			}
			outcome("no_change")
		case domain.RunStateAccepted:
			if f.OutcomeKind == "document" {
				if f.ResourceResultID == "" || len(f.Repositories) != 0 {
					return reconcile("missing_document_evidence")
				}
				if f.DocumentStatus == "accept" {
					outcome("document_accepted")
				} else {
					set("review", "document_review", "review_result")
					attention("document_review", run.ID().String())
				}
				break
			}
			set("delivery", "pending_apply", "open_delivery")
			if f.ResourceSnapshot {
				if f.ResourceResultID == "" || len(f.Repositories) == 0 {
					return reconcile("missing_result_evidence")
				}
				merged, applied, closed, changed, pending := 0, 0, 0, 0, 0
				closedNoChange := 0
				needsReview := false
				for i, repo := range f.Repositories {
					if !repo.ResultPresent {
						return reconcile("missing_repository_evidence")
					}
					status := "no_change"
					if repo.Changed == 0 {
						if repo.Closed {
							status = "closed"
							closedNoChange++
						}
						c.Repositories[i].Status = status
						continue
					}
					changed++
					status = "pending_apply"
					if repo.Mode == "task_branch" {
						status = "pending_commit"
					}
					if repo.ApplyState == "applied" {
						status = "applied_locally"
					}
					if repo.ApplyState == "uncertain" || repo.ApplyState == "conflict" || repo.ApplyState == "writing" {
						status = "apply_" + repo.ApplyState
					}
					achievedMerge, cleanupError, uncertain := false, false, false
					latestByKind := map[string]storecontract.TaskBoardOperation{}
					for _, op := range repo.Operations {
						old, ok := latestByKind[op.Kind]
						if !ok || !op.UpdatedAt.Before(old.UpdatedAt) {
							latestByKind[op.Kind] = op
						}
						if op.State == "succeeded" {
							switch op.Kind {
							case "commit":
								if status == "pending_commit" {
									status = "pending_push"
								}
							case "push":
								status = "pending_pr"
							case "pr", "merge":
								if op.PRState == "merged" && op.MergeCommit != "" {
									achievedMerge = true
								} else if op.PRState == "closed" {
									status = "pr_closed"
								} else if op.PRState == "open" {
									status = "pending_merge"
								}
							}
						}
					}
					for kind, op := range latestByKind {
						if op.State == "failed" || op.State == "recovery_required" || op.State == "writing" {
							if kind == "cleanup" {
								cleanupError = op.State != "writing"
							} else if !achievedMerge {
								status = "delivery_" + op.State
								uncertain = op.State == "recovery_required" || op.State == "writing"
							}
						}
					}
					if achievedMerge {
						status = "delivered"
						merged++
					} else if status == "applied_locally" {
						applied++
					} else if repo.Closed && !uncertain && repo.ApplyState != "uncertain" && repo.ApplyState != "writing" {
						status = "closed"
						closed++
					} else {
						pending++
					}
					if cleanupError {
						attention("cleanup_failed", repo.RepoID)
					}
					if f.ResourceReviewKind != "accept" && !repo.Closed && status != "delivered" && status != "applied_locally" {
						needsReview = true
						status = "result_review"
					}
					c.Repositories[i].Status = status
					if status != "delivered" && status != "applied_locally" && status != "closed" {
						c.Substate = status
					}
				}
				if needsReview {
					set("review", "result_review", "review_result")
					attention("result_review", f.ResourceResultID)
				} else if pending > 0 {
					attention(c.Substate, run.ID().String())
				} else if changed == 0 {
					if f.Closure && closedNoChange == len(f.Repositories) {
						outcome("closed")
					} else {
						return reconcile("accepted_without_changed_result")
					}
				} else if closed > 0 {
					if merged+applied > 0 {
						outcome("partially_delivered_closed")
					} else {
						outcome("closed")
					}
				} else if merged > 0 && applied > 0 {
					outcome("mixed_delivery")
				} else if merged > 0 {
					outcome("delivered")
				} else {
					outcome("applied_locally")
				}
			} else {
				switch item.LatestPatchApplicationState {
				case domain.PatchApplicationApplied:
					outcome("applied_locally")
				case domain.PatchApplicationApplying:
					set("delivery", "applying", "open_delivery")
				case domain.PatchApplicationConflict, domain.PatchApplicationRecoveryRequired:
					attention("apply_recovery", run.ID().String())
				case "":
					if f.Closure {
						outcome("closed")
					} else if item.LatestVerifiedReviewKind == domain.ReviewDecisionAccept && item.LatestVerificationResultID.Valid() {
						attention("pending_apply", run.ID().String())
					} else {
						return reconcile("missing_legacy_delivery_evidence")
					}
				default:
					return reconcile("unknown_apply_state")
				}
			}
		default:
			return reconcile("unknown_run_state")
		}
		if !f.Closure && (run.State() == domain.RunStateRecoveryRequired || run.State() == domain.RunStateReady || run.State() == domain.RunStateRunning || run.State() == domain.RunStateStopping) {
			retry, valid := boardAutomaticRetry(f)
			if !valid {
				return reconcile("invalid_retry_evidence")
			}
			if retry != "" {
				c.Substate = retry
				if retry == "retry_pending" || retry == "retrying" {
					c.Attention = TaskBoardAttention{State: "none", Reasons: []TaskBoardReason{}}
					c.NextAction.Kind = "monitor"
				} else {
					c.Attention = TaskBoardAttention{State: "none", Reasons: []TaskBoardReason{}}
					attention(retry, run.ID().String())
				}
			}
		}
		if f.GateID != "" {
			if run.State() != domain.RunStateRunning {
				return reconcile("gate_with_inactive_run")
			}
			c.Substate = "waiting_gate"
			c.NextAction.Kind = "answer_gate"
			attention("blocking_gate", f.GateID)
			c.Attention.Reasons[len(c.Attention.Reasons)-1].ExpectedVersion = 0
			c.Attention.Reasons[len(c.Attention.Reasons)-1].Summary = f.GateQuestion
		}
	}
	if c.Visibility.ReadOnly {
		c.NextAction.Kind = "open_task"
		c.NextAction.AvailabilityReason = "archived_read_only"
	}
	return c
}

func boardAutomaticRetry(f storecontract.TaskBoardFacts) (string, bool) {
	enabled := false
	used := 0
	prepared, blocked := "", ""
	for _, e := range f.RetryEvents {
		var p automaticRetryEvent
		switch e.Type {
		case "run.started":
			blocked = ""
		case automaticRetryEnabledEvent:
			enabled = true
		case automaticRetryResetEvent:
			enabled = true
			used = 0
			prepared = ""
			blocked = ""
		case automaticRetryPreparedEvent:
			if json.Unmarshal(e.Payload, &p) != nil || p.AttemptID == "" || p.PredecessorID == "" || p.RetriesUsed < 1 || p.RetriesUsed > AutomaticRetryMaxRetries || p.MaxRetries != AutomaticRetryMaxRetries {
				return "", false
			}
			enabled = true
			used = p.RetriesUsed
			prepared = p.AttemptID
			blocked = ""
		case automaticRetryBlockedEvent:
			if json.Unmarshal(e.Payload, &p) != nil || p.AttemptID == "" || p.MaxRetries != AutomaticRetryMaxRetries {
				return "", false
			}
			blocked = p.AttemptID
		}
	}
	if !enabled {
		return "", true
	}
	if blocked == f.AttemptID {
		return "retry_blocked", true
	}
	state := f.Item.LatestRun.State()
	if prepared == f.AttemptID && (state == domain.RunStateReady || state == domain.RunStateRunning || state == domain.RunStateStopping) {
		return "retrying", true
	}
	if state != domain.RunStateRecoveryRequired {
		return "", true
	}
	if !f.RetryFinalized || !automaticRetryableFailure(f.FailureReason) || f.AttemptState == "interrupted" {
		return "retry_blocked", true
	}
	if used >= AutomaticRetryMaxRetries {
		return "retry_exhausted", true
	}
	return "retry_pending", true
}

func boardChecks(r storecontract.TaskBoardRepositoryFacts) string {
	if r.ChecksMode == "none" {
		return "none"
	}
	if r.ChecksNotApplicable {
		return "not_applicable"
	}
	switch r.ChecksStatus {
	case "PASS":
		return "passed"
	case "FAIL":
		return "failed"
	default:
		return "unavailable"
	}
}

func boardAchievements(r storecontract.TaskBoardRepositoryFacts) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, o := range r.Operations {
		if o.State == "succeeded" {
			if o.Kind == "commit" || o.Kind == "push" || o.Kind == "pr" {
				seen[o.Kind] = true
			}
			if o.PRState == "merged" && o.MergeCommit != "" {
				seen["merged"] = true
			}
		}
	}
	if r.ApplyState == "applied" {
		seen["applied_locally"] = true
	}
	for _, k := range []string{"commit", "push", "pr", "merged", "applied_locally"} {
		if seen[k] {
			out = append(out, k)
		}
	}
	return out
}

// Keep the age of persisted delivery evidence distinct from the time this GET
// read it. Previews and cleanup are not observations of delivery progress.
func boardDeliveryObservedAt(r storecontract.TaskBoardRepositoryFacts) *time.Time {
	var latest time.Time
	var integrated time.Time
	for _, o := range r.Operations {
		if o.State == "succeeded" && o.PRState == "merged" && o.MergeCommit != "" && o.UpdatedAt.After(integrated) {
			integrated = o.UpdatedAt
		}
	}
	if !integrated.IsZero() {
		return &integrated
	}

	for _, o := range r.Operations {
		if o.State != "preview" && o.Kind != "cleanup" && o.UpdatedAt.After(latest) {
			latest = o.UpdatedAt
		}
	}
	if latest.IsZero() {
		return nil
	}
	return &latest
}

// Filter choices have a separate stable cursor: task progress must not restart
// discovery, and a large Project must not inflate every card-page response.
func pageTaskBoardOptions(v *TaskBoardView, project string, q TaskBoardQuery) error {
	rooms := append([]TaskBoardRoom{}, v.Rooms...)
	repos := append([]storecontract.TaskBoardRepository{}, v.Repositories...)
	sort.Slice(rooms, func(i, j int) bool { return rooms[i].RoomID < rooms[j].RoomID })
	sort.Slice(repos, func(i, j int) bool { return repos[i].RepoID < repos[j].RepoID })
	raw, _ := json.Marshal(struct {
		Project      string
		Rooms        []TaskBoardRoom
		Repositories []storecontract.TaskBoardRepository
	}{project, rooms, repos})
	digest := sha256.Sum256(raw)
	v.OptionsSnapshot = hex.EncodeToString(digest[:])
	offset := 0
	total := max(len(rooms), len(repos))
	if q.OptionsCursor != "" {
		b, e := base64.RawURLEncoding.DecodeString(q.OptionsCursor)
		parts := strings.Split(string(b), ":")
		if e != nil || len(parts) != 2 {
			return domain.ErrInvalidArgument
		}
		if parts[0] != v.OptionsSnapshot {
			return ErrTaskBoardSnapshotChanged
		}
		offset, e = strconv.Atoi(parts[1])
		if e != nil || offset < 0 || offset > total || offset%100 != 0 {
			return domain.ErrInvalidArgument
		}
	}
	end := min(offset+100, total)
	v.Rooms = append([]TaskBoardRoom{}, rooms[min(offset, len(rooms)):min(end, len(rooms))]...)
	v.Repositories = append([]storecontract.TaskBoardRepository{}, repos[min(offset, len(repos)):min(end, len(repos))]...)
	if q.RoomID != "" {
		for i, r := range rooms {
			if r.RoomID == q.RoomID && (i < offset || i >= end) {
				v.Rooms = append(v.Rooms, r)
				break
			}
		}
	}
	if q.RepoID != "" {
		for i, r := range repos {
			if r.RepoID == q.RepoID && (i < offset || i >= end) {
				v.Repositories = append(v.Repositories, r)
				break
			}
		}
	}
	if end < total {
		v.NextOptionsCursor = base64.RawURLEncoding.EncodeToString([]byte(v.OptionsSnapshot + ":" + strconv.Itoa(end)))
	}
	return nil
}
