package app

import (
	"fmt"

	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

type CurrentActionKind string

const (
	CurrentActionEditPlan              CurrentActionKind = "edit_plan"
	CurrentActionReviewPlan            CurrentActionKind = "review_plan"
	CurrentActionStartRun              CurrentActionKind = "start_run"
	CurrentActionStartAttempt          CurrentActionKind = "start_attempt"
	CurrentActionMonitorRun            CurrentActionKind = "monitor_run"
	CurrentActionRecoverRun            CurrentActionKind = "recover_run"
	CurrentActionRecoverVerification   CurrentActionKind = "recover_verification"
	CurrentActionReviewResult          CurrentActionKind = "review_result"
	CurrentActionApplyPatch            CurrentActionKind = "apply_patch"
	CurrentActionRetryImplementation   CurrentActionKind = "retry_implementation"
	CurrentActionContinueSuccessorPlan CurrentActionKind = "continue_successor_plan"
	CurrentActionOpenRelatedTask       CurrentActionKind = "open_related_task"
	CurrentActionViewTerminal          CurrentActionKind = "view_terminal"
	CurrentActionNone                  CurrentActionKind = "none"
)

type CurrentActionTarget struct {
	RoomID          domain.RoomID
	TaskID          domain.TaskID
	RunID           domain.RunID
	DraftID         domain.TechnicalPlanDraftID
	RevisionID      domain.TechnicalPlanRevisionID
	PlanReviewID    domain.TechnicalPlanReviewID
	ResultID        domain.ResultID
	ResultReviewID  domain.ReviewDecisionID
	ExpectedVersion uint64
}

type CurrentAction struct {
	Kind   CurrentActionKind
	Target CurrentActionTarget
	URL    string
	Reason string
}

func deriveCurrentAction(roomState domain.RoomState, item storecontract.TaskWorkspaceItem) CurrentAction {
	task := item.Task
	if roomState == domain.RoomStateArchived {
		return noCurrentAction("Room is archived; Restore it before continuing.")
	}
	if roomState != domain.RoomStateActive || !task.ID().Valid() || !task.RoomID().Valid() {
		return noCurrentAction("Room or Task state is invalid.")
	}
	if task.State() != domain.TaskStateOpen && task.State() != domain.TaskStateClosed {
		return noCurrentAction("Task state is invalid.")
	}
	if task.State() == domain.TaskStateClosed && (item.LatestRun == nil || (item.LatestRun.State() != domain.RunStateAccepted && item.LatestRun.State() != domain.RunStateCancelled && item.LatestRun.State() != domain.RunStateCompleted)) {
		return noCurrentAction("A closed Task cannot continue nonterminal work.")
	}
	if item.LatestRun != nil {
		return deriveRunAction(item)
	}
	if item.ActiveRunCount != 0 || item.RunCount != 0 {
		return noCurrentAction("Run summary is inconsistent with persisted Run counts.")
	}
	return derivePlanningAction(item)
}

func derivePlanningAction(item storecontract.TaskWorkspaceItem) CurrentAction {
	task := item.Task
	target := CurrentActionTarget{RoomID: task.RoomID(), TaskID: task.ID()}
	url := taskURL(task.RoomID(), task.ID())
	if item.OpenDraftID.Valid() {
		if item.OpenDraftEditVersion == 0 {
			return noCurrentAction("The open Plan Draft has no valid edit version.")
		}
		if item.LatestRevisionID.Valid() {
			if item.LatestPlanReviewKind != domain.TechnicalPlanReviewRequestRevision ||
				item.LatestPlanReviewRevisionID != item.LatestRevisionID ||
				item.OpenDraftPredecessorRevisionID != item.LatestRevisionID {
				return noCurrentAction("The successor Plan Draft does not match the reviewed Revision.")
			}
		} else if item.OpenDraftPredecessorRevisionID.Valid() || item.LatestPlanReviewID.Valid() {
			return noCurrentAction("The initial Plan Draft has contradictory lineage.")
		}
		target.DraftID = item.OpenDraftID
		target.ExpectedVersion = item.OpenDraftEditVersion
		return CurrentAction{Kind: CurrentActionEditPlan, Target: target, URL: url, Reason: "An editable Plan Draft is open."}
	}
	if !item.LatestRevisionID.Valid() {
		return noCurrentAction("No open Plan Draft or submitted Plan Revision is available.")
	}
	target.RevisionID = item.LatestRevisionID
	if !item.LatestPlanReviewID.Valid() {
		if item.LatestPlanReviewRevisionID.Valid() || item.LatestPlanReviewKind != "" || item.AcceptedRevisionID.Valid() {
			return noCurrentAction("Plan Review or acceptance state is contradictory.")
		}
		return CurrentAction{Kind: CurrentActionReviewPlan, Target: target, URL: url, Reason: "The submitted Plan Revision awaits exact Review."}
	}
	if item.LatestPlanReviewRevisionID != item.LatestRevisionID {
		return noCurrentAction("The current Plan Review targets a different Revision.")
	}
	target.PlanReviewID = item.LatestPlanReviewID
	switch item.LatestPlanReviewKind {
	case domain.TechnicalPlanReviewAccept:
		if item.AcceptedRevisionID != item.LatestRevisionID {
			return noCurrentAction("The accepted Plan binding does not match the current Revision.")
		}
		return CurrentAction{Kind: CurrentActionStartRun, Target: target, URL: url, Reason: "The accepted Plan Revision has no Run."}
	case domain.TechnicalPlanReviewRequestRevision:
		return noCurrentAction("The requested successor Plan Draft is missing.")
	default:
		return noCurrentAction("The Plan Review kind is unknown.")
	}
}

func deriveRunAction(item storecontract.TaskWorkspaceItem) CurrentAction {
	task := item.Task
	run := *item.LatestRun
	if run.TaskID() != task.ID() {
		return noCurrentAction("The latest Run belongs to a different Task.")
	}
	terminal := run.State() == domain.RunStateAccepted || run.State() == domain.RunStateCancelled || run.State() == domain.RunStateCompleted
	if (!terminal && item.ActiveRunCount != 1) || (terminal && item.ActiveRunCount != 0) {
		return noCurrentAction("The Task has a contradictory number of active Runs.")
	}
	if !item.AcceptedRevisionID.Valid() || !item.LatestRunBindingRevisionID.Valid() || item.LatestRunBindingRevisionID != item.AcceptedRevisionID {
		return noCurrentAction("The latest Run binding drifts from the accepted Plan Revision.")
	}
	target := CurrentActionTarget{RoomID: task.RoomID(), TaskID: task.ID(), RunID: run.ID(), RevisionID: item.LatestRunBindingRevisionID, ExpectedVersion: run.Version()}
	url := runURL(task.RoomID(), task.ID(), run.ID())
	switch run.State() {
	case domain.RunStateDraft, domain.RunStateReady:
		return CurrentAction{Kind: CurrentActionStartAttempt, Target: target, URL: url, Reason: "The Run is ready for its next execution step."}
	case domain.RunStateRunning, domain.RunStateStopping, domain.RunStateAwaitingVerification, domain.RunStateVerifying:
		return CurrentAction{Kind: CurrentActionMonitorRun, Target: target, URL: url, Reason: "The Run or its Verification is active."}
	case domain.RunStateRecoveryRequired:
		return CurrentAction{Kind: CurrentActionRecoverRun, Target: target, URL: url, Reason: "The Agent Run requires explicit recovery."}
	case domain.RunStateVerificationRecoveryRequired:
		return CurrentAction{Kind: CurrentActionRecoverVerification, Target: target, URL: url, Reason: "Verification requires explicit recovery."}
	case domain.RunStateAwaitingReview:
		return CurrentAction{Kind: CurrentActionReviewResult, Target: target, URL: url, Reason: "Verified Result and Patch await human Review."}
	case domain.RunStateRevisionRequired:
		return deriveRejectionAction(item, target, url)
	case domain.RunStateAccepted, domain.RunStateCancelled, domain.RunStateCompleted:
		if run.State() == domain.RunStateAccepted {
			if item.HasTaskBranchDelivery {
				return CurrentAction{Kind: CurrentActionViewTerminal, Target: target, URL: url, Reason: "The accepted Task branch Result remains uncommitted and is available for audit."}
			}
			switch item.LatestPatchApplicationState {
			case "":
				if task.State() == domain.TaskStateClosed {
					return CurrentAction{Kind: CurrentActionViewTerminal, Target: target, URL: url, Reason: "The legacy accepted Run is terminal and remains available for audit."}
				}
				return CurrentAction{Kind: CurrentActionApplyPatch, Target: target, URL: url, Reason: "The verified Patch is accepted but has not been applied."}
			case domain.PatchApplicationApplying:
				return CurrentAction{Kind: CurrentActionMonitorRun, Target: target, URL: url, Reason: "The accepted Patch is being applied to the local target."}
			case domain.PatchApplicationConflict, domain.PatchApplicationRecoveryRequired:
				return CurrentAction{Kind: CurrentActionApplyPatch, Target: target, URL: url, Reason: "The accepted Patch application needs attention."}
			case domain.PatchApplicationApplied:
				if task.State() != domain.TaskStateClosed {
					return noCurrentAction("Applied Patch evidence exists but the Task is not closed.")
				}
			default:
				return noCurrentAction("The Patch application state is unknown.")
			}
		}
		return CurrentAction{Kind: CurrentActionViewTerminal, Target: target, URL: url, Reason: "The latest Run is terminal and remains available for audit."}
	default:
		return noCurrentAction(fmt.Sprintf("Run state %q is unknown.", run.State()))
	}
}

func deriveRejectionAction(item storecontract.TaskWorkspaceItem, target CurrentActionTarget, url string) CurrentAction {
	run := *item.LatestRun
	if !item.LatestVerifiedReviewID.Valid() {
		if item.LatestVerificationResultID.Valid() && item.LatestVerificationResultOutcome == domain.ResultNeedsRevision &&
			item.LatestVerifiedReviewKind == "" && item.LatestVerifiedReviewRunVersion == 0 && item.LatestRejectionClass == "" &&
			!item.RoutePlanningDraftID.Valid() && !item.RouteRelatedTaskID.Valid() && item.RouteRejectionClass == "" {
			target.ResultID = item.LatestVerificationResultID
			return CurrentAction{Kind: CurrentActionRetryImplementation, Target: target, URL: url, Reason: "Trusted Verification requires an implementation retry."}
		}
		return noCurrentAction("The revision-required Run has no exact verified rejection.")
	}
	if item.LatestVerifiedReviewKind != domain.ReviewDecisionReject || item.LatestVerifiedReviewRunVersion+1 != run.Version() {
		return noCurrentAction("The revision-required Run has no exact verified rejection.")
	}
	target.ResultReviewID = item.LatestVerifiedReviewID
	switch item.LatestRejectionClass {
	case domain.ReviewRejectionImplementationGap:
		if item.RoutePlanningDraftID.Valid() || item.RouteRelatedTaskID.Valid() || item.RouteRejectionClass != "" {
			return noCurrentAction("Implementation retry has a contradictory successor route.")
		}
		return CurrentAction{Kind: CurrentActionRetryImplementation, Target: target, URL: url, Reason: "The verified Review requires an implementation-only retry."}
	case domain.ReviewRejectionPlanningGap:
		if item.RouteRejectionClass != item.LatestRejectionClass || item.RouteSourceRunID != run.ID() || item.RouteSourceTaskID != item.Task.ID() ||
			!item.RoutePlanningDraftID.Valid() || item.RoutePlanningDraftPredecessorRevisionID != item.LatestRunBindingRevisionID || item.RouteRelatedTaskID.Valid() {
			return noCurrentAction("The planning-gap successor Draft route is missing or contradictory.")
		}
		target.DraftID = item.RoutePlanningDraftID
		return CurrentAction{Kind: CurrentActionContinueSuccessorPlan, Target: target, URL: taskURL(item.Task.RoomID(), item.Task.ID()), Reason: "The verified Review requires a successor Plan Draft."}
	case domain.ReviewRejectionContractChangeRequired:
		if item.RouteRejectionClass != item.LatestRejectionClass || item.RouteSourceRunID != run.ID() || item.RouteSourceTaskID != item.Task.ID() ||
			!item.RouteRelatedTaskID.Valid() || item.RouteRelatedTaskPredecessorTaskID != item.Task.ID() || item.RoutePlanningDraftID.Valid() {
			return noCurrentAction("The contract-change related Task route is missing or contradictory.")
		}
		target.TaskID = item.RouteRelatedTaskID
		return CurrentAction{Kind: CurrentActionOpenRelatedTask, Target: target, URL: taskURL(item.Task.RoomID(), item.RouteRelatedTaskID), Reason: "The verified Review created a related Task for the contract change."}
	default:
		return noCurrentAction("The verified rejection class is unknown.")
	}
}

func noCurrentAction(reason string) CurrentAction {
	return CurrentAction{Kind: CurrentActionNone, Reason: reason}
}

func taskURL(roomID domain.RoomID, taskID domain.TaskID) string {
	return "/rooms/" + roomID.String() + "/tasks/" + taskID.String()
}

func runURL(roomID domain.RoomID, taskID domain.TaskID, runID domain.RunID) string {
	return taskURL(roomID, taskID) + "/runs/" + runID.String()
}
