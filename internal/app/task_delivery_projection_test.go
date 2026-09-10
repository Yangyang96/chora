package app

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
	"github.com/Yangyang96/chora/internal/taskdelivery"
)

func TestProjectDeliveryReasonIgnoresFailedMergeAfterMergedFact(t *testing.T) {
	repoID := domain.NewRepositoryID()
	mergedPR := projectionDeliveryOperation(t, repoID, "pr-merged", "pr", "succeeded", "merged", "")
	failedMerge := projectionDeliveryOperation(t, repoID, "merge-failed", "merge", "failed", "", "merge request failed")

	for _, operations := range [][]storecontract.DeliveryOperation{
		{mergedPR, failedMerge},
		{failedMerge, mergedPR},
	} {
		got, err := projectDeliveryOperations(projectionRepository(repoID), operations)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != "merged" || got.Reason != "" {
			t.Fatalf("status=%q reason=%q", got.Status, got.Reason)
		}
		assertProjectionHistory(t, got.Operations, operations)
	}
}

func TestProjectDeliveryReasonRetainsUnachievedMergeFailure(t *testing.T) {
	repoID := domain.NewRepositoryID()
	operations := []storecontract.DeliveryOperation{
		projectionDeliveryOperation(t, repoID, "pr-open", "pr", "succeeded", "open", ""),
		projectionDeliveryOperation(t, repoID, "merge-failed", "merge", "failed", "", "merge needs attention"),
		projectionDeliveryOperation(t, repoID, "push-succeeded", "push", "succeeded", "", ""),
	}

	got, err := projectDeliveryOperations(projectionRepository(repoID), operations)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "pr_open" || got.Reason != "merge needs attention" {
		t.Fatalf("status=%q reason=%q", got.Status, got.Reason)
	}
	assertProjectionHistory(t, got.Operations, operations)
}

func TestProjectDeliveryReasonReprojectsAfterMergedObservation(t *testing.T) {
	repoID := domain.NewRepositoryID()
	openPR := projectionDeliveryOperation(t, repoID, "pr-open", "pr", "succeeded", "open", "")
	failedMerge := projectionDeliveryOperation(t, repoID, "merge-failed", "merge", "failed", "", "merge failed")
	failedCleanup := projectionDeliveryOperation(t, repoID, "cleanup-failed", "cleanup", "failed", "", "cleanup still blocked")

	t.Run("merged observation clears achieved merge warning", func(t *testing.T) {
		repo, err := projectDeliveryOperations(projectionRepository(repoID), []storecontract.DeliveryOperation{openPR, failedMerge})
		if err != nil {
			t.Fatal(err)
		}
		if repo.Status != "pr_open" || repo.Reason != "merge failed" {
			t.Fatalf("before observation status=%q reason=%q", repo.Status, repo.Reason)
		}
		history := append([]DeliveryOperationView(nil), repo.Operations...)

		// projectHostingObservations advances this achieved fact, then invokes
		// projectDeliveryReason against the retained append-only operation history.
		repo.Status = "merged"
		projectDeliveryReason(&repo)

		if repo.Reason != "" {
			t.Fatalf("reason after merged observation=%q", repo.Reason)
		}
		if !reflect.DeepEqual(repo.Operations, history) {
			t.Fatalf("operation history changed: before=%#v after=%#v", history, repo.Operations)
		}
	})

	t.Run("merged observation retains unachieved cleanup warning", func(t *testing.T) {
		operations := []storecontract.DeliveryOperation{failedMerge, openPR, failedCleanup}
		repo, err := projectDeliveryOperations(projectionRepository(repoID), operations)
		if err != nil {
			t.Fatal(err)
		}
		history := append([]DeliveryOperationView(nil), repo.Operations...)

		repo.Status = "merged"
		projectDeliveryReason(&repo)

		if repo.Reason != "cleanup still blocked" {
			t.Fatalf("reason after merged observation=%q", repo.Reason)
		}
		if !reflect.DeepEqual(repo.Operations, history) {
			t.Fatalf("operation history changed: before=%#v after=%#v", history, repo.Operations)
		}
	})
}

func TestProjectDeliveryRecoveryDominatesOperationOrder(t *testing.T) {
	repoID := domain.NewRepositoryID()
	mergedPR := projectionDeliveryOperation(t, repoID, "pr-merged", "pr", "succeeded", "merged", "")
	cleaned := projectionDeliveryOperation(t, repoID, "cleanup-succeeded", "cleanup", "succeeded", "", "")

	for _, state := range []string{"writing", "recovery_required"} {
		recovery := projectionDeliveryOperation(t, repoID, "operation-"+state, "merge", state, "", "provider detail")
		for _, operations := range [][]storecontract.DeliveryOperation{
			{recovery, mergedPR, cleaned},
			{mergedPR, recovery, cleaned},
			{mergedPR, cleaned, recovery},
		} {
			got, err := projectDeliveryOperations(projectionRepository(repoID), operations)
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != "recovery_required" || got.Reason != "An operation requires read-only reconciliation before another delivery action." {
				t.Fatalf("state=%q status=%q reason=%q", state, got.Status, got.Reason)
			}
			assertProjectionHistory(t, got.Operations, operations)
		}
	}
}

func TestProjectDeliveryReasonUsesLastFailureAtSameStage(t *testing.T) {
	repoID := domain.NewRepositoryID()
	push := projectionDeliveryOperation(t, repoID, "push-succeeded", "push", "succeeded", "", "")
	first := projectionDeliveryOperation(t, repoID, "merge-failed-first", "merge", "failed", "", "first merge failure")
	second := projectionDeliveryOperation(t, repoID, "merge-failed-second", "merge", "failed", "", "second merge failure")

	for _, test := range []struct {
		name       string
		operations []storecontract.DeliveryOperation
		want       string
	}{
		{name: "second is latest", operations: []storecontract.DeliveryOperation{first, push, second}, want: "second merge failure"},
		{name: "first is latest", operations: []storecontract.DeliveryOperation{second, push, first}, want: "first merge failure"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := projectDeliveryOperations(projectionRepository(repoID), test.operations)
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != "pushed" || got.Reason != test.want {
				t.Fatalf("status=%q reason=%q, want reason %q", got.Status, got.Reason, test.want)
			}
			assertProjectionHistory(t, got.Operations, test.operations)
		})
	}
}

func projectionRepository(repoID domain.RepositoryID) RepositoryDeliveryView {
	return RepositoryDeliveryView{RepoID: repoID.String(), Status: "uncommitted", Operations: []DeliveryOperationView{}}
}

func projectionDeliveryOperation(t *testing.T, repoID domain.RepositoryID, id, kind, state, prState, reason string) storecontract.DeliveryOperation {
	t.Helper()
	intent := deliveryIntent{}
	if kind == "push" {
		intent.Push = &taskdelivery.PushPreview{Remote: "origin", URL: "https://github.com/acme/widget.git"}
	}
	outcome := deliveryOutcome{Reason: reason}
	if prState != "" {
		outcome.PR = &taskdelivery.PullRequest{URL: "https://github.com/acme/widget/pull/7", State: prState, Number: 7}
	}
	previewJSON, err := json.Marshal(intent)
	if err != nil {
		t.Fatal(err)
	}
	outcomeJSON, err := json.Marshal(outcome)
	if err != nil {
		t.Fatal(err)
	}
	return storecontract.DeliveryOperation{
		ID: id, RepositoryID: repoID, Kind: kind, State: state, Version: 1,
		PreviewJSON: previewJSON, OutcomeJSON: outcomeJSON,
	}
}

func assertProjectionHistory(t *testing.T, got []DeliveryOperationView, want []storecontract.DeliveryOperation) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("operation history length=%d, want %d", len(got), len(want))
	}
	for i := range want {
		var outcome deliveryOutcome
		if err := json.Unmarshal(want[i].OutcomeJSON, &outcome); err != nil {
			t.Fatal(err)
		}
		if got[i].ID != want[i].ID || got[i].Kind != want[i].Kind || got[i].Status != want[i].State || got[i].Reason != outcome.Reason {
			t.Fatalf("operation %d changed: got=%#v want id=%q kind=%q status=%q reason=%q", i, got[i], want[i].ID, want[i].Kind, want[i].State, outcome.Reason)
		}
	}
}
