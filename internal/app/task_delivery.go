package app

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
	"github.com/Yangyang96/chora/internal/taskdelivery"
)

type TaskDeliveryWorkspaces interface {
	DeliveryRoot(context.Context, domain.TaskID, domain.TaskRepositoryResource) (string, error)
}
type TaskDeliveryGit interface {
	PreviewCommit(context.Context, taskdelivery.Binding, []byte, []string, string) (taskdelivery.CommitPreview, error)
	Commit(context.Context, taskdelivery.CommitPreview) (taskdelivery.CommitResult, error)
	ReconcileCommit(context.Context, taskdelivery.CommitPreview) (taskdelivery.CommitResult, error)
	PreviewPush(context.Context, taskdelivery.Binding, string) (taskdelivery.PushPreview, error)
	Push(context.Context, taskdelivery.PushPreview) error
	ReconcilePush(context.Context, taskdelivery.PushPreview) (bool, error)
}
type DeliveryRequest struct {
	CommandMeta
	RunID           domain.RunID
	ExpectedVersion uint64
	ResultDigest    string
	RepoID          string
	Kind            string
	Message         string
	Remote          string
	Title           string
	Body            string
	OperationID     string
}
type DeliveryOperationView struct {
	HostingRepository string   `json:"repository,omitempty"`
	WorktreePath      string   `json:"worktreePath,omitempty"`
	HeadBranch        string   `json:"headBranch,omitempty"`
	BaseBranch        string   `json:"baseBranch,omitempty"`
	Title             string   `json:"title,omitempty"`
	Body              string   `json:"body,omitempty"`
	MergeMethod       string   `json:"mergeMethod,omitempty"`
	PRNumber          int      `json:"number,omitempty"`
	PRState           string   `json:"prState,omitempty"`
	MergeCommit       string   `json:"mergeCommit,omitempty"`
	ID                string   `json:"id"`
	RepoID            string   `json:"repoId"`
	Kind              string   `json:"kind"`
	Status            string   `json:"status"`
	Version           uint64   `json:"version"`
	Message           string   `json:"message,omitempty"`
	Paths             []string `json:"paths,omitempty"`
	Head              string   `json:"head,omitempty"`
	Tree              string   `json:"tree,omitempty"`
	Remote            string   `json:"remote,omitempty"`
	URL               string   `json:"url,omitempty"`
	RemoteRef         string   `json:"remoteRef,omitempty"`
	RemoteHead        string   `json:"remoteHead,omitempty"`
	TargetHead        string   `json:"targetHead,omitempty"`
	Commit            string   `json:"commit,omitempty"`
	PRURL             string   `json:"prUrl,omitempty"`
	Reason            string   `json:"reason,omitempty"`
}
type RepositoryDeliveryView struct {
	RepoID       string                  `json:"repoId"`
	Name         string                  `json:"name"`
	TargetBranch string                  `json:"targetBranch"`
	TaskBranch   string                  `json:"taskBranch"`
	Status       string                  `json:"status"`
	Commit       string                  `json:"commit,omitempty"`
	Remote       string                  `json:"remote,omitempty"`
	URL          string                  `json:"url,omitempty"`
	PRURL        string                  `json:"prUrl,omitempty"`
	Reason       string                  `json:"reason,omitempty"`
	Operations   []DeliveryOperationView `json:"operations"`
}
type DeliveryCapabilities struct {
	Cleanup  bool   `json:"cleanup"`
	Provider string `json:"provider,omitempty"`
	Reason   string `json:"reason,omitempty"`
	Hosting  bool   `json:"hosting"`
	CreatePR bool   `json:"createPR"`
	Merge    bool   `json:"merge"`
}
type TaskDeliveryView struct {
	Capabilities DeliveryCapabilities     `json:"capabilities"`
	Repositories []RepositoryDeliveryView `json:"repositories"`
	Hosting      bool                     `json:"hosting"`
}
type deliveryIntent struct {
	ClosedResultID string                             `json:",omitempty"`
	ResultCleanup  *taskdelivery.ResultCleanupPreview `json:",omitempty"`
	Hosting        *taskdelivery.HostingPreview       `json:",omitempty"`
	Cleanup        *taskdelivery.CleanupPreview       `json:",omitempty"`
	ActorID        string
	SessionID      string
	ResultDigest   string
	ReviewID       string
	Binding        taskdelivery.Binding
	Commit         *taskdelivery.CommitPreview `json:",omitempty"`
	Push           *taskdelivery.PushPreview   `json:",omitempty"`
}
type deliveryOutcome struct {
	Commit, Tree, Reason string
	PR                   *taskdelivery.PullRequest `json:",omitempty"`
}

func deliveryOperationView(o storecontract.DeliveryOperation) (DeliveryOperationView, error) {
	var intent deliveryIntent
	var outcome deliveryOutcome
	if e := json.Unmarshal(o.PreviewJSON, &intent); e != nil {
		return DeliveryOperationView{}, e
	}
	if e := json.Unmarshal(o.OutcomeJSON, &outcome); e != nil {
		return DeliveryOperationView{}, e
	}
	v := DeliveryOperationView{ID: o.ID, RepoID: o.RepositoryID.String(), Kind: o.Kind, Status: o.State, Version: o.Version, Commit: outcome.Commit, Reason: outcome.Reason}
	if c := intent.ResultCleanup; c != nil {
		v.WorktreePath, v.Head, v.Paths = c.Binding.Root, c.Binding.BaseCommit, c.Paths
	}
	if c := intent.Cleanup; c != nil {
		v.WorktreePath = c.Binding.Root
		v.Head = c.Head
	}
	if c := intent.Commit; c != nil {
		v.Message = c.Message
		v.Paths = c.Paths
		v.Head = c.Head
		v.Tree = c.Tree
	}
	if p := intent.Push; p != nil {
		v.Head = p.Head
		v.Remote = p.Remote
		v.URL = p.URL
		v.RemoteRef = p.RemoteRef
		v.RemoteHead = p.RemoteHead
		v.TargetHead = p.TargetHead
	}
	if h := intent.Hosting; h != nil {
		v.HostingRepository, v.HeadBranch, v.BaseBranch = h.Repository, h.HeadBranch, h.BaseBranch
		v.Head, v.TargetHead = h.Head, h.BaseHead
		v.Title, v.Body, v.MergeMethod, v.PRNumber = h.Title, h.Body, h.MergeMethod, h.Number
		if h.Number > 0 {
			v.PRURL = fmt.Sprintf("https://github.com/%s/pull/%d", h.Repository, h.Number)
		}
	}
	if pr := outcome.PR; pr != nil {
		v.PRURL, v.PRNumber, v.PRState, v.MergeCommit = pr.URL, pr.Number, pr.State, pr.MergeCommit
	}
	return v, nil
}

// Compatibility name for the original commit-only API.
type CommitMessageContext = DeliveryDraftContext

func (s *Service) LoadCommitMessageContext(ctx context.Context, req DeliveryRequest) (CommitMessageContext, error) {
	req.Kind = "commit"
	return s.LoadDeliveryDraftContext(ctx, req)
}

func (s *Service) LoadTaskDelivery(ctx context.Context, runID domain.RunID) (TaskDeliveryView, error) {
	run, e := s.deps.Store.Reader().GetRun(ctx, runID)
	if e != nil {
		return TaskDeliveryView{}, e
	}
	review, e := s.LoadResourceReview(ctx, runID)
	if e != nil {
		return TaskDeliveryView{}, e
	}
	snapshot, e := s.deliverySnapshot(ctx, run.TaskID())
	if e != nil {
		return TaskDeliveryView{}, e
	}
	operations, e := s.deps.Store.Reader().ListDeliveryOperations(ctx, run.TaskID())
	if e != nil {
		return TaskDeliveryView{}, e
	}
	v := TaskDeliveryView{Repositories: []RepositoryDeliveryView{}, Capabilities: s.hostingCapabilities()}
	v.Hosting = v.Capabilities.Hosting
	if v.Repositories, e = projectDeliveryRepositories(run, snapshot, review.Group, operations); e != nil {
		return v, e
	}
	if e = s.projectHostingObservations(ctx, runID, &v, operations); e != nil {
		return v, e
	}
	if e = projectClosedDeliveries(ctx, s.deps.Store.Reader(), review.Group.ID, &v); e != nil {
		return v, e
	}
	return v, nil
}

func projectDeliveryRepositories(run domain.AgentRun, snapshot domain.TaskResourceSnapshot, group domain.ResourceResultGroup, operations []storecontract.DeliveryOperation) ([]RepositoryDeliveryView, error) {
	repositories := make([]RepositoryDeliveryView, 0, len(snapshot.Resources))
	for i, r := range snapshot.Resources {
		repo := RepositoryDeliveryView{RepoID: r.RepoID, Name: r.Name, TargetBranch: strings.TrimPrefix(r.BaseRef, "refs/heads/"), TaskBranch: r.TaskBranch, Status: "awaiting_review", Operations: []DeliveryOperationView{}}
		if run.State() == domain.RunStateAccepted {
			repo.Status = "uncommitted"
		}
		if i < len(group.Repositories) && len(group.Repositories[i].ChangedPaths) == 0 {
			repo.Status = "no_change"
		}
		if r.Role == "reference" {
			repo.Status = "no_change"
		} else if r.DeliveryMode != "task_branch" {
			repo.Status = "legacy"
		}
		repo, e := projectDeliveryOperations(repo, operations)
		if e != nil {
			return nil, e
		}
		repositories = append(repositories, repo)
	}
	return repositories, nil
}
func projectDeliveryOperations(repo RepositoryDeliveryView, operations []storecontract.DeliveryOperation) (RepositoryDeliveryView, error) {
	for _, o := range operations {
		if o.RepositoryID.String() != repo.RepoID {
			continue
		}
		op, e := deliveryOperationView(o)
		if e != nil {
			return repo, e
		}
		repo.Operations = append(repo.Operations, op)
		if o.State == "succeeded" {
			switch o.Kind {
			case "commit":
				if deliveryStageRank(repo.Status) < deliveryStageRank("committed") {
					repo.Status = "committed"
				}
				repo.Commit = op.Commit
			case "push":
				if deliveryStageRank(repo.Status) < deliveryStageRank("pushed") {
					repo.Status = "pushed"
				}
				repo.Remote, repo.URL = op.Remote, op.URL
			case "cleanup":
				if repo.Status != "recovery_required" {
					repo.Status = "cleaned"
				}
			case "pr", "merge":
				stage := hostingStage(op.PRState)
				if deliveryStageRank(repo.Status) <= deliveryStageRank(stage) {
					repo.Status = stage
					repo.PRURL = op.PRURL
				}
			}
		}
		if o.State == "writing" || o.State == "recovery_required" {
			repo.Status = "recovery_required"
			repo.Reason = "An operation requires read-only reconciliation before another delivery action."
		}
	}
	projectDeliveryReason(&repo)
	return repo, nil
}

// Project warnings from achieved facts, while retaining every operation's history.
func projectDeliveryReason(repo *RepositoryDeliveryView) {
	if repo.Status == "recovery_required" {
		return
	}
	stages := map[string]int{"commit": 1, "push": 2, "pr": 3, "merge": 4, "cleanup": 5}
	achievedRank, reasonRank := deliveryStageRank(repo.Status), 0
	repo.Reason = ""
	for _, operation := range repo.Operations {
		rank := stages[operation.Kind]
		if operation.Status == "failed" && rank > achievedRank && rank >= reasonRank {
			repo.Reason, reasonRank = operation.Reason, rank
		}
	}
}

func (s *Service) deliverySnapshot(ctx context.Context, taskID domain.TaskID) (domain.TaskResourceSnapshot, error) {
	record, e := s.deps.Store.Reader().GetTaskResourceSnapshot(ctx, taskID)
	if e != nil {
		return domain.TaskResourceSnapshot{}, e
	}
	var snapshot domain.TaskResourceSnapshot
	if e = json.Unmarshal(record.CanonicalJSON, &snapshot); e != nil {
		return snapshot, e
	}
	raw, d, e := snapshot.CanonicalJSON()
	if e != nil || d != record.Digest || string(raw) != string(record.CanonicalJSON) {
		return snapshot, ErrReviewEvidenceUnavailable
	}
	return snapshot, nil
}
func (s *Service) deliveryAuthority(ctx context.Context, req DeliveryRequest) (domain.AgentRun, ResourceReviewView, domain.TaskRepositoryResource, string, error) {
	var resource domain.TaskRepositoryResource
	reader := s.deps.Store.Reader()
	run, e := reader.GetRun(ctx, req.RunID)
	if e != nil {
		return run, ResourceReviewView{}, resource, "", e
	}
	fail := func(e error) (domain.AgentRun, ResourceReviewView, domain.TaskRepositoryResource, string, error) {
		return run, ResourceReviewView{}, resource, "", e
	}
	if run.Version() != req.ExpectedVersion {
		return fail(storecontract.ErrVersionConflict)
	}
	if run.State() != domain.RunStateAccepted {
		return fail(ErrInvalidCommand)
	}
	task, e := reader.GetTask(ctx, run.TaskID())
	if e != nil {
		return fail(e)
	}
	room, e := reader.GetRoom(ctx, task.RoomID())
	if e != nil {
		return fail(e)
	}
	if room.State() != domain.RoomStateActive {
		return fail(storecontract.ErrRoomStateForbidden)
	}
	view, e := s.loadResourceReview(ctx, reader, run.ID())
	if e != nil {
		return fail(e)
	}
	if view.Digest != req.ResultDigest {
		return fail(ErrReviewEvidenceUnavailable)
	}
	resultID, _ := domain.ParseResultID(view.Group.ID)
	if e := requireOpenResultEntry(ctx, reader, resultID, req.RepoID); e != nil {
		return fail(e)
	}
	decision, digest, e := reader.GetResourceResultReview(ctx, resultID)
	if e != nil {
		return fail(e)
	}
	if decision.Kind() != domain.ReviewDecisionAccept || hex.EncodeToString(digest[:]) != view.Digest {
		return fail(ErrReviewEvidenceUnavailable)
	}
	snapshot, e := s.deliverySnapshot(ctx, run.TaskID())
	if e != nil {
		return fail(e)
	}
	for _, r := range snapshot.Resources {
		if r.RepoID == req.RepoID {
			resource = r
			break
		}
	}
	if resource.RepoID == "" || resource.Role != "write" || resource.DeliveryMode != "task_branch" || resource.TaskBranch == "" {
		return fail(fmt.Errorf("%w: delivery requires a new writable Task branch", taskdelivery.ErrUnsupported))
	}
	// An unresolved local write cannot race or be hidden by delivery.
	if operation, lookup := reader.GetResourceApplyOperation(ctx, resultID); lookup == nil {
		steps, e := reader.ListResourceApplySteps(ctx, operation.ID)
		if e != nil {
			return fail(e)
		}
		for _, step := range steps {
			if step.RepositoryID.String() == resource.RepoID && step.Status != "applied" {
				return fail(fmt.Errorf("%w: resolve the pending repository Apply first", taskdelivery.ErrConflict))
			}
		}
	} else if !errors.Is(lookup, storecontract.ErrNotFound) {
		return fail(lookup)
	}
	return run, view, resource, decision.ID().String(), nil
}
func (s *Service) deliveryBinding(ctx context.Context, taskID domain.TaskID, r domain.TaskRepositoryResource) (taskdelivery.Binding, error) {
	if s.deps.TaskDeliveryWorkspaces == nil || s.deps.TaskDeliveryGit == nil {
		return taskdelivery.Binding{}, taskdelivery.ErrUnsupported
	}
	root, e := s.deps.TaskDeliveryWorkspaces.DeliveryRoot(ctx, taskID, r)
	if e != nil {
		return taskdelivery.Binding{}, e
	}
	return taskdelivery.Binding{Root: root, CommonGitDir: r.CommonGitDir, BaseCommit: r.BaseCommit, BaseTree: r.BaseTree, Branch: r.TaskBranch, TargetRef: r.BaseRef}, nil
}
func (s *Service) PreviewTaskDelivery(ctx context.Context, req DeliveryRequest) (DeliveryOperationView, error) {
	if e := s.authorize(ctx, req.CommandMeta, "preview_task_delivery", req.RunID.String(), req.ExpectedVersion); e != nil {
		return DeliveryOperationView{}, e
	}
	unlock := s.runLock(req.RunID)
	defer unlock()
	run, view, resource, reviewID, e := s.deliveryAuthority(ctx, req)
	if e != nil {
		return DeliveryOperationView{}, e
	}
	release := resourceApplyLocks.lock(resource.PhysicalIdentity)
	defer release()
	key, e := s.commandKey(req.CommandMeta, "preview_task_delivery", req.RunID.String(), req.ExpectedVersion, req, s.deps.Clock.Now())
	if e != nil {
		return DeliveryOperationView{}, e
	}
	id := "delivery_" + hex.EncodeToString(key.KeyHash[:])
	if old, e := s.deps.Store.Reader().GetDeliveryOperation(ctx, id); e == nil {
		if old.RequestDigest != key.RequestDigest {
			return DeliveryOperationView{}, storecontract.ErrIdempotencyConflict
		}
		return deliveryOperationView(old)
	} else if !errors.Is(e, storecontract.ErrNotFound) {
		return DeliveryOperationView{}, e
	}
	operations, e := s.deps.Store.Reader().ListDeliveryOperations(ctx, run.TaskID())
	if e != nil {
		return DeliveryOperationView{}, e
	}
	var committed string
	for _, o := range operations {
		if o.RepositoryID.String() != resource.RepoID {
			continue
		}
		if o.State == "writing" || o.State == "recovery_required" {
			return DeliveryOperationView{}, taskdelivery.ErrRecovery
		}
		if o.State == "succeeded" && o.Kind == "commit" {
			var out deliveryOutcome
			if e = json.Unmarshal(o.OutcomeJSON, &out); e != nil {
				return DeliveryOperationView{}, e
			}
			committed = out.Commit
		}
	}
	binding, e := s.deliveryBinding(ctx, run.TaskID(), resource)
	if e != nil {
		return DeliveryOperationView{}, e
	}
	intent := deliveryIntent{ActorID: req.ActorID, SessionID: req.SessionID, ResultDigest: view.Digest, ReviewID: reviewID, Binding: binding}
	switch req.Kind {
	case "commit":
		if committed != "" {
			return DeliveryOperationView{}, fmt.Errorf("%w: this repository already has a recorded Task commit", taskdelivery.ErrConflict)
		}
		var patch ReviewablePatch
		for _, p := range view.Patches {
			if p.RepoID == resource.RepoID {
				patch = p.Patch
			}
		}
		paths := []string{}
		for _, p := range patch.Files {
			paths = append(paths, p.Path)
		}
		preview, e := s.deps.TaskDeliveryGit.PreviewCommit(ctx, binding, patch.Raw, paths, req.Message)
		if e != nil {
			return DeliveryOperationView{}, e
		}
		intent.Commit = &preview
	case "push":
		if committed == "" {
			return DeliveryOperationView{}, fmt.Errorf("%w: commit this Task repository first", taskdelivery.ErrConflict)
		}
		preview, e := s.deps.TaskDeliveryGit.PreviewPush(ctx, binding, req.Remote)
		if e != nil {
			return DeliveryOperationView{}, e
		}
		if preview.Head != committed {
			return DeliveryOperationView{}, taskdelivery.ErrConflict
		}
		intent.Push = &preview
	case "cleanup":
		preview, err := s.prepareCleanup(ctx, binding, operations, resource.RepoID)
		if err != nil {
			return DeliveryOperationView{}, err
		}
		intent.Cleanup = &preview
	case "pr", "merge":
		preview, pushed, err := s.prepareHosting(ctx, req, id, binding, operations)
		if err != nil {
			return DeliveryOperationView{}, err
		}
		intent.Hosting = &preview
		intent.Push = pushed
	default:
		return DeliveryOperationView{}, fmt.Errorf("%w: hosting adapter is not configured", taskdelivery.ErrUnsupported)
	}
	now := s.deps.Clock.Now()
	repoID, _ := domain.ParseRepositoryID(resource.RepoID)
	o := storecontract.DeliveryOperation{ID: id, TaskID: run.TaskID(), RunID: run.ID(), RepositoryID: repoID, Kind: req.Kind, State: "preview", Version: 1, RequestDigest: key.RequestDigest, PreviewJSON: responseBody(intent), OutcomeJSON: responseBody(deliveryOutcome{}), CreatedAt: now, UpdatedAt: now}
	e = s.deps.Store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error { return tx.SaveDeliveryOperation(ctx, 0, o) })
	if e != nil {
		return DeliveryOperationView{}, e
	}
	return deliveryOperationView(o)
}
func (s *Service) ConfirmTaskDelivery(ctx context.Context, req DeliveryRequest) (DeliveryOperationView, error) {
	if e := s.authorize(ctx, req.CommandMeta, "confirm_task_delivery", req.RunID.String(), req.ExpectedVersion); e != nil {
		return DeliveryOperationView{}, e
	}
	unlock := s.runLock(req.RunID)
	defer unlock()
	o, e := s.deps.Store.Reader().GetDeliveryOperation(ctx, req.OperationID)
	if e != nil {
		return DeliveryOperationView{}, e
	}
	if o.RunID != req.RunID {
		return DeliveryOperationView{}, ErrInvalidCommand
	}
	req.RepoID = o.RepositoryID.String()
	run, _, resource, reviewID, e := s.deliveryAuthority(ctx, req)
	if e != nil {
		return DeliveryOperationView{}, e
	}
	release := resourceApplyLocks.lock(resource.PhysicalIdentity)
	defer release()
	var intent deliveryIntent
	if e = json.Unmarshal(o.PreviewJSON, &intent); e != nil {
		return DeliveryOperationView{}, e
	}
	if intent.ResultDigest != req.ResultDigest || intent.ReviewID != reviewID {
		return DeliveryOperationView{}, ErrReviewEvidenceUnavailable
	}
	if intent.ActorID != req.ActorID || intent.SessionID != req.SessionID {
		return DeliveryOperationView{}, fmt.Errorf("%w: confirm delivery in the session that created its preview", ErrInvalidCommand)
	}
	if e = s.recordDeliveryConfirmation(ctx, req, o); e != nil {
		return DeliveryOperationView{}, e
	}
	if o.State != "preview" {
		return deliveryOperationView(o)
	} // Never replay a side effect, including an uncertain one.
	binding, e := s.deliveryBinding(ctx, run.TaskID(), resource)
	if e != nil {
		return DeliveryOperationView{}, e
	}
	if binding != intent.Binding {
		return DeliveryOperationView{}, taskdelivery.ErrConflict
	}
	// Revalidate other recorded operations since this preview was created.
	all, e := s.deps.Store.Reader().ListDeliveryOperations(ctx, run.TaskID())
	if e != nil {
		return DeliveryOperationView{}, e
	}
	for _, other := range all {
		if other.ID == o.ID || other.RepositoryID != o.RepositoryID {
			continue
		}
		if other.State == "writing" || other.State == "recovery_required" || (o.Kind == "commit" && other.Kind == "commit" || o.Kind == "pr" && (other.Kind == "pr" || other.Kind == "merge") || o.Kind == "merge" && other.Kind == "merge" || o.Kind == "cleanup" && other.Kind == "cleanup") && other.State == "succeeded" {
			return DeliveryOperationView{}, taskdelivery.ErrConflict
		}
	}
	if o.Kind == "pr" || o.Kind == "merge" {
		if intent.Hosting == nil || intent.Push == nil || !s.hostingCapabilities().Hosting {
			return DeliveryOperationView{}, taskdelivery.ErrUnsupported
		}
		done, err := s.deps.TaskDeliveryGit.ReconcilePush(ctx, *intent.Push)
		if err != nil {
			return DeliveryOperationView{}, err
		}
		if !done {
			return DeliveryOperationView{}, taskdelivery.ErrConflict
		}
	}
	if o.Kind == "cleanup" {
		if intent.Cleanup == nil {
			return DeliveryOperationView{}, ErrInvalidCommand
		}
		fresh, err := s.prepareCleanup(ctx, binding, all, resource.RepoID)
		if err != nil {
			return DeliveryOperationView{}, err
		}
		if fresh.Digest != intent.Cleanup.Digest {
			return DeliveryOperationView{}, taskdelivery.ErrConflict
		}
	}
	if e = s.saveDeliveryState(ctx, &o, "writing", deliveryOutcome{}); e != nil {
		return DeliveryOperationView{}, e
	}
	outcome := deliveryOutcome{}
	switch o.Kind {
	case "commit":
		if intent.Commit == nil {
			e = ErrInvalidCommand
		} else {
			var result taskdelivery.CommitResult
			result, e = s.deps.TaskDeliveryGit.Commit(ctx, *intent.Commit)
			outcome.Commit, outcome.Tree = result.Commit, result.Tree
		}
	case "push":
		if intent.Push == nil {
			e = ErrInvalidCommand
		} else {
			e = s.deps.TaskDeliveryGit.Push(ctx, *intent.Push)
			if e == nil {
				outcome.Commit = intent.Push.Head
			}
		}
	case "cleanup":
		e = (taskdelivery.Git{}).Cleanup(ctx, *intent.Cleanup)
		if e == nil {
			outcome.Commit = intent.Cleanup.Head
		}
	case "pr", "merge":
		var pr taskdelivery.PullRequest
		pr, e = s.deps.TaskDeliveryHosting.Confirm(ctx, *intent.Hosting)
		if e == nil {
			outcome.PR = &pr
			outcome.Commit = pr.Head
		}
	default:
		e = ErrInvalidCommand
	}
	state := "succeeded"
	if e != nil {
		state = "recovery_required"
		outcome.Reason = "The operation did not report a proven outcome. Refresh to reconcile before taking another action."
		if errors.Is(e, taskdelivery.ErrHostingMutationNotStarted) {
			state = "failed"
			outcome.Reason = "The provider mutation was not started. Restore connectivity, create a fresh preview, and confirm it explicitly."
		} else if errors.Is(e, taskdelivery.ErrConflict) || errors.Is(e, taskdelivery.ErrUnsupported) {
			state = "failed"
			outcome.Reason = e.Error()
		}
	}
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	if saveErr := s.saveDeliveryState(persistCtx, &o, state, outcome); saveErr != nil {
		return DeliveryOperationView{}, saveErr
	}
	return deliveryOperationView(o)
}
func (s *Service) saveDeliveryState(ctx context.Context, o *storecontract.DeliveryOperation, state string, outcome deliveryOutcome) error {
	next := *o
	next.State = state
	next.Version++
	next.OutcomeJSON = responseBody(outcome)
	next.UpdatedAt = s.deps.Clock.Now()
	e := s.deps.Store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		if err := tx.SaveDeliveryOperation(ctx, o.Version, next); err != nil {
			return err
		}
		_, err := tx.AppendRunEvent(ctx, o.RunID, storecontract.EventDraft{ID: s.deps.IDs.EventID(), Type: "task_delivery." + o.Kind + "." + state, Source: "app", OccurredAt: next.UpdatedAt, RecordedAt: next.UpdatedAt, NormalizedJSON: responseBody(map[string]any{"operation_id": o.ID, "repo_id": o.RepositoryID.String(), "state": state, "commit": outcome.Commit, "reason": outcome.Reason})})
		return err
	})
	if e == nil {
		*o = next
	}
	return e
}
func (s *Service) RefreshTaskDelivery(ctx context.Context, req DeliveryRequest) (TaskDeliveryView, error) {
	if e := s.authorize(ctx, req.CommandMeta, "refresh_task_delivery", req.RunID.String(), req.ExpectedVersion); e != nil {
		return TaskDeliveryView{}, e
	}
	unlock := s.runLock(req.RunID)
	defer unlock()
	run, _, resource, _, e := s.deliveryAuthority(ctx, req)
	if e != nil {
		return TaskDeliveryView{}, e
	}
	if refresh, ok := s.deps.TaskDeliveryHosting.(interface{ RefreshAvailability(context.Context) }); ok {
		refresh.RefreshAvailability(ctx)
	}
	release := resourceApplyLocks.lock(resource.PhysicalIdentity)
	defer release()
	operations, e := s.deps.Store.Reader().ListDeliveryOperations(ctx, run.TaskID())
	if e != nil {
		return TaskDeliveryView{}, e
	}
	for _, o := range operations {
		if o.RepositoryID.String() == req.RepoID && o.Kind == "cleanup" && o.State == "succeeded" {
			return s.LoadTaskDelivery(ctx, req.RunID)
		}
	}
	for _, o := range operations {
		if o.RepositoryID.String() != req.RepoID || (o.State != "writing" && o.State != "recovery_required" && !(o.Kind == "pr" && o.State == "succeeded")) {
			continue
		}
		var intent deliveryIntent
		if e = json.Unmarshal(o.PreviewJSON, &intent); e != nil {
			return TaskDeliveryView{}, e
		}
		binding, e := s.deliveryBindingForRecovery(ctx, run.TaskID(), resource, o.Kind)
		if e != nil {
			return TaskDeliveryView{}, e
		}
		if binding != intent.Binding {
			return TaskDeliveryView{}, taskdelivery.ErrConflict
		}
		if o.Kind == "pr" && o.State == "succeeded" {
			if intent.Hosting == nil || !s.hostingCapabilities().Hosting {
				return TaskDeliveryView{}, taskdelivery.ErrUnsupported
			}
			// Creation digest covers Number=0. Resolve the original immutable marker.
			observationPreview := *intent.Hosting
			pr, err := s.deps.TaskDeliveryHosting.Observe(ctx, observationPreview)
			if err != nil {
				return TaskDeliveryView{}, err
			}
			if e = s.observeHosting(ctx, run.ID(), o, pr); e != nil {
				return TaskDeliveryView{}, e
			}
			continue
		}
		state := "recovery_required"
		outcome := deliveryOutcome{Reason: "The operation could not be reconciled. Preserve the worktree and inspect the exact repository/ref."}
		switch o.Kind {
		case "commit":
			if intent.Commit != nil {
				result, err := s.deps.TaskDeliveryGit.ReconcileCommit(ctx, *intent.Commit)
				if err == nil {
					state = "succeeded"
					outcome = deliveryOutcome{Commit: result.Commit, Tree: result.Tree}
				} else if errors.Is(err, taskdelivery.ErrConflict) {
					state = "failed"
					outcome.Reason = "No matching commit was proven; create a fresh preview after inspecting the worktree."
				}
			}
		case "push":
			if intent.Push != nil {
				done, err := s.deps.TaskDeliveryGit.ReconcilePush(ctx, *intent.Push)
				if err == nil {
					if done {
						state = "succeeded"
						outcome = deliveryOutcome{Commit: intent.Push.Head}
					} else {
						state = "failed"
						outcome.Reason = "The exact commit is not present at the confirmed remote ref. A new preview is required."
					}
				}
			}
		case "cleanup":
			if intent.Cleanup != nil && (taskdelivery.Git{}).ReconcileCleanup(ctx, *intent.Cleanup) == nil {
				state = "succeeded"
				outcome = deliveryOutcome{Commit: intent.Cleanup.Head}
			}
		case "pr", "merge":
			if intent.Hosting != nil && s.hostingCapabilities().Hosting {
				pr, err := s.deps.TaskDeliveryHosting.Observe(ctx, *intent.Hosting)
				if err == nil && (o.Kind == "pr" || pr.State == "merged") {
					state = "succeeded"
					outcome = deliveryOutcome{Commit: pr.Head, PR: &pr}
				}
				// Absence/open after an uncertain write is not proof that a delayed
				// provider mutation cannot complete. Keep recovery_required; never replay.
			}
		}
		if e = s.saveDeliveryState(ctx, &o, state, outcome); e != nil {
			return TaskDeliveryView{}, e
		}
	}
	return s.LoadTaskDelivery(ctx, req.RunID)
}

// Reserve each explicit confirmation key before any side effect. Replays look
// up the latest operation state rather than returning a stale writing receipt.
func (s *Service) recordDeliveryConfirmation(ctx context.Context, req DeliveryRequest, o storecontract.DeliveryOperation) error {
	now := s.deps.Clock.Now()
	semantic := struct{ OperationID, ResultDigest string }{o.ID, req.ResultDigest}
	key, err := s.commandKey(req.CommandMeta, "confirm_task_delivery", req.RunID.String(), req.ExpectedVersion, semantic, now)
	if err != nil {
		return err
	}
	return s.deps.Store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		if _, found, err := tx.LookupCommand(ctx, key); err != nil {
			return err
		} else if found {
			return nil
		}
		receipt, err := jsonResponse(map[string]string{"operationId": o.ID})
		if err != nil {
			return err
		}
		if err = tx.SaveCommand(ctx, key, receipt); err != nil {
			return err
		}
		_, err = tx.AppendRunEvent(ctx, o.RunID, storecontract.EventDraft{ID: s.deps.IDs.EventID(), Type: "task_delivery.confirmed", Source: "app", OccurredAt: now, RecordedAt: now, NormalizedJSON: responseBody(map[string]string{"operation_id": o.ID, "actor_id": req.ActorID, "session_id": req.SessionID, "request_digest": hex.EncodeToString(key.RequestDigest[:]), "key_hash": hex.EncodeToString(key.KeyHash[:])})})
		return err
	})
}
