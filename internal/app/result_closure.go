package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

var ErrResultClosed = errors.New("result delivery eligibility is closed")
var ErrResultClosureBlocked = errors.New("result cannot be closed while work is active or uncertain")

type ResultClosureEntry struct {
	CanCleanup  bool                   `json:"canCleanup"`
	Cleanup     *DeliveryOperationView `json:"cleanup,omitempty"`
	RepoID      string                 `json:"repoId"`
	Name        string                 `json:"name"`
	Status      string                 `json:"status"` // eligible, retained, closed
	Achievement string                 `json:"achievement,omitempty"`
}
type ResultClosureView struct {
	ResultID      string               `json:"resultId"`
	ResultDigest  string               `json:"resultDigest"`
	RunID         string               `json:"runId"`
	AttemptID     string               `json:"attemptId"`
	RunVersion    uint64               `json:"runVersion"`
	ReviewID      string               `json:"reviewId,omitempty"`
	PreviewDigest string               `json:"previewDigest"`
	Entries       []ResultClosureEntry `json:"entries"`
	ClosedAt      *time.Time           `json:"closedAt,omitempty"`
}
type ResultClosureRequest struct {
	CommandMeta
	RunID           domain.RunID
	ExpectedVersion uint64
	ResultDigest    string
	PreviewDigest   string
}

func (s *Service) PreviewResultClosure(ctx context.Context, req ResultClosureRequest) (ResultClosureView, error) {
	if err := s.authorize(ctx, req.CommandMeta, "preview_result_closure", req.RunID.String(), req.ExpectedVersion); err != nil {
		return ResultClosureView{}, err
	}
	unlock := s.runLock(req.RunID)
	defer unlock()
	v, err := s.resultClosureView(ctx, s.deps.Store.Reader(), req.RunID)
	if err != nil {
		return v, err
	}
	if v.RunVersion != req.ExpectedVersion {
		return v, storecontract.ErrVersionConflict
	}
	if req.ResultDigest != "" && v.ResultDigest != req.ResultDigest {
		return v, ErrReviewEvidenceUnavailable
	}
	return v, nil
}

func (s *Service) LoadResultClosure(ctx context.Context, runID domain.RunID) (ResultClosureView, error) {
	return s.resultClosureView(ctx, s.deps.Store.Reader(), runID)
}

func (s *Service) CloseResult(ctx context.Context, req ResultClosureRequest) (ResultClosureView, error) {
	if err := s.authorize(ctx, req.CommandMeta, "close_result", req.RunID.String(), req.ExpectedVersion); err != nil {
		return ResultClosureView{}, err
	}
	unlock := s.runLock(req.RunID)
	defer unlock()
	var result ResultClosureView
	err := s.deps.Store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		v, err := s.resultClosureView(ctx, tx, req.RunID)
		if err != nil {
			return err
		}
		if v.RunVersion != req.ExpectedVersion {
			return storecontract.ErrVersionConflict
		}
		if v.ResultDigest != req.ResultDigest || v.PreviewDigest != req.PreviewDigest || req.PreviewDigest == "" {
			return ErrReviewEvidenceUnavailable
		}
		if v.ClosedAt != nil {
			result = v
			return nil
		}
		ids := []string{}
		for _, entry := range v.Entries {
			if entry.Status == "eligible" {
				ids = append(ids, entry.RepoID)
			}
		}
		if len(ids) == 0 {
			return fmt.Errorf("%w: no undelivered entries remain", ErrInvalidCommand)
		}
		sort.Strings(ids)
		run, err := tx.GetRun(ctx, req.RunID)
		if err != nil {
			return err
		}
		resultID, err := domain.ParseResultID(v.ResultID)
		if err != nil {
			return err
		}
		attemptID, err := domain.ParseAttemptID(v.AttemptID)
		if err != nil {
			return err
		}
		var digest, preview [32]byte
		d, _ := hex.DecodeString(v.ResultDigest)
		copy(digest[:], d)
		d, _ = hex.DecodeString(v.PreviewDigest)
		copy(preview[:], d)
		now := s.deps.Clock.Now()
		c := storecontract.ResultClosure{ResultID: resultID, RunID: req.RunID, TaskID: run.TaskID(), AttemptID: attemptID, ResultDigest: digest, PreviewDigest: preview, ReviewID: v.ReviewID, RepositoryIDs: ids, ActorID: req.ActorID, SessionID: req.SessionID, CreatedAt: now}
		if err = tx.InsertResultClosure(ctx, c); err != nil {
			return err
		}
		if _, err = tx.AppendRunEvent(ctx, run.ID(), storecontract.EventDraft{ID: s.deps.IDs.EventID(), Type: "result.closed", Source: "app", OccurredAt: now, RecordedAt: now, NormalizedJSON: responseBody(map[string]any{"result_id": v.ResultID, "result_digest": v.ResultDigest, "repository_ids": ids})}); err != nil {
			return err
		}
		result = applyResultClosure(v, c)
		return nil
	})
	return result, err
}

func applyResultClosure(v ResultClosureView, c storecontract.ResultClosure) ResultClosureView {
	for i := range v.Entries {
		for _, id := range c.RepositoryIDs {
			if v.Entries[i].RepoID == id {
				v.Entries[i].Status = "closed"
			}
		}
	}
	v.ClosedAt = &c.CreatedAt
	v.PreviewDigest = hex.EncodeToString(c.PreviewDigest[:])
	return v
}

func (s *Service) resultClosureView(ctx context.Context, reader storecontract.Reader, runID domain.RunID) (ResultClosureView, error) {
	v := ResultClosureView{RunID: runID.String(), Entries: []ResultClosureEntry{}}
	run, err := reader.GetRun(ctx, runID)
	if err != nil {
		return v, err
	}
	switch run.State() {
	case domain.RunStateAwaitingReview, domain.RunStateRevisionRequired, domain.RunStateAccepted, domain.RunStateCompleted, domain.RunStateCancelled, domain.RunStateRecoveryRequired:
	default:
		return v, ErrResultClosureBlocked
	}
	attempt, err := reader.GetCurrentAttempt(ctx, runID)
	if err != nil {
		return v, err
	}
	v.RunVersion, v.AttemptID = run.Version(), attempt.ID().String()
	group, err := reader.GetResourceResultGroup(ctx, attempt.ID())
	if run.State() == domain.RunStateRecoveryRequired {
		// Failed checks are a terminal result, unlike an unknown runtime/write.
		session, sessionErr := reader.GetRuntimeSessionForAttempt(ctx, attempt.ID())
		if (err != nil && !errors.Is(err, storecontract.ErrNotFound)) || (err == nil && group.Outcome != "checks_failed" && group.Outcome != "checks_incomplete") || attempt.State() != domain.AttemptStateFailed || sessionErr != nil || !automaticRetryFinalized(attempt, session) {
			return v, ErrResultClosureBlocked
		}
	}
	if err == nil {
		if group.RunID != runID.String() || group.TaskID != run.TaskID().String() {
			return v, ErrReviewEvidenceUnavailable
		}
		_, digest, err := group.CanonicalJSON()
		if err != nil {
			return v, err
		}
		v.ResultID, v.ResultDigest = group.ID, hex.EncodeToString(digest[:])
		for _, repo := range group.Repositories {
			v.Entries = append(v.Entries, ResultClosureEntry{RepoID: repo.RepoID, Name: repo.RepoID, Status: "eligible"})
		}
		if snapshot, e := reader.GetTaskResourceSnapshot(ctx, run.TaskID()); e == nil {
			var resources domain.TaskResourceSnapshot
			if e = json.Unmarshal(snapshot.CanonicalJSON, &resources); e != nil {
				return v, e
			}
			for i := range v.Entries {
				for _, r := range resources.Resources {
					if r.RepoID == v.Entries[i].RepoID {
						v.Entries[i].Name = r.Name
						v.Entries[i].CanCleanup = r.Role == "write" && r.DeliveryMode == "task_branch"
					}
				}
			}
		} else {
			return v, e
		}
	} else if errors.Is(err, storecontract.ErrNotFound) {
		terminalID, terminalDigest, terminalErr := storecontract.TerminalResultForClosure(ctx, reader, run, attempt)
		if terminalErr == nil {
			v.ResultID, v.ResultDigest = terminalID.String(), hex.EncodeToString(terminalDigest[:])
			v.Entries = append(v.Entries, ResultClosureEntry{Name: "Task result", Status: "eligible"})
		} else if errors.Is(terminalErr, storecontract.ErrNotFound) {
			change, e := s.loadReviewableChange(ctx, reader, runID)
			if e != nil {
				return v, e
			}
			// Bind every scalar review authority field without inventing a repository ID.
			raw := responseBody(struct {
				Binding           verifiedReviewBindingWire
				Contract, Context [32]byte
			}{wireVerifiedReviewBinding(change.Binding), change.ContractDigest, change.contextSnapshotDigest})
			digest := sha256.Sum256(raw)
			v.ResultID, v.ResultDigest = change.Binding.ResultID.String(), hex.EncodeToString(digest[:])
			v.Entries = append(v.Entries, ResultClosureEntry{Name: "Task result", Status: "eligible"})
		} else {
			return v, terminalErr
		}
	} else {
		return v, err
	}
	id, err := domain.ParseResultID(v.ResultID)
	if err != nil {
		return v, err
	}
	if group.ID != "" {
		if review, digest, e := reader.GetResourceResultReview(ctx, id); e == nil {
			if review.RunID() != runID || hex.EncodeToString(digest[:]) != v.ResultDigest {
				return v, ErrReviewEvidenceUnavailable
			}
			v.ReviewID = review.ID().String()
		} else if !errors.Is(e, storecontract.ErrNotFound) {
			return v, e
		}
	} else if review, e := reader.GetVerifiedReviewForResult(ctx, id); e == nil {
		if review.RunID() != runID || review.Binding().AgentAttemptID != attempt.ID() {
			return v, ErrReviewEvidenceUnavailable
		}
		v.ReviewID = review.ID().String()
	} else if !errors.Is(e, storecontract.ErrNotFound) {
		return v, e
	}
	var existing *storecontract.ResultClosure
	if c, e := reader.GetResultClosure(ctx, id); e == nil {
		if hex.EncodeToString(c.ResultDigest[:]) != v.ResultDigest || c.AttemptID != attempt.ID() {
			return v, ErrReviewEvidenceUnavailable
		}
		existing = &c
	} else if !errors.Is(e, storecontract.ErrNotFound) {
		return v, e
	}
	if existing == nil {
		task, e := reader.GetTask(ctx, run.TaskID())
		if e != nil {
			return v, e
		}
		history, e := reader.ListTaskRunHistory(ctx, task.RoomID(), task.ID())
		if e != nil {
			return v, e
		}
		for _, sibling := range history {
			if sibling.Run.ID() == runID {
				continue
			}
			switch sibling.Run.State() {
			case domain.RunStateReady, domain.RunStateRunning, domain.RunStateStopping, domain.RunStateAwaitingVerification, domain.RunStateVerifying, domain.RunStateVerificationRecoveryRequired, domain.RunStateRecoveryRequired:
				return v, ErrResultClosureBlocked
			}
		}
	}
	type operationState struct {
		ID, State string
		Version   uint64
	}
	states := []operationState{}
	operations, err := reader.ListDeliveryOperations(ctx, run.TaskID())
	if err != nil {
		return v, err
	}
	for _, o := range operations {
		if existing == nil && (o.State == "writing" || o.State == "recovery_required") {
			return v, ErrResultClosureBlocked
		}
		states = append(states, operationState{o.ID, o.State, o.Version})
		if o.Kind == "cleanup" && o.RunID == runID {
			var intent deliveryIntent
			if e := json.Unmarshal(o.PreviewJSON, &intent); e != nil {
				return v, e
			}
			if intent.ClosedResultID == v.ResultID {
				op, e := deliveryOperationView(o)
				if e != nil {
					return v, e
				}
				for i := range v.Entries {
					if v.Entries[i].RepoID == o.RepositoryID.String() {
						v.Entries[i].Cleanup = &op
						if o.State == "succeeded" {
							v.Entries[i].CanCleanup = false
						}
					}
				}
			}
		}
		if o.State == "succeeded" && o.Kind != "cleanup" && o.RunID == runID {
			var intent deliveryIntent
			if err := json.Unmarshal(o.PreviewJSON, &intent); err != nil {
				return v, err
			}
			if intent.ResultDigest != v.ResultDigest {
				continue
			}
			for i := range v.Entries {
				if v.Entries[i].RepoID == o.RepositoryID.String() {
					v.Entries[i].Status, v.Entries[i].Achievement = "retained", "committed"
				}
			}
		}
	}
	if o, e := reader.GetResourceApplyOperation(ctx, id); e == nil {
		steps, e := reader.ListResourceApplySteps(ctx, o.ID)
		if e != nil {
			return v, e
		}
		for _, step := range steps {
			if step.Status == "writing" || step.Status == "uncertain" {
				return v, ErrResultClosureBlocked
			}
			states = append(states, operationState{o.ID + "/" + step.RepositoryID.String(), step.Status, step.Sequence})
			if step.Status == "applied" {
				for i := range v.Entries {
					if v.Entries[i].RepoID == step.RepositoryID.String() {
						v.Entries[i].Status, v.Entries[i].Achievement = "retained", "applied"
					}
				}
			}
		}
	} else if !errors.Is(e, storecontract.ErrNotFound) {
		return v, e
	}
	if p, e := reader.GetPatchApplication(ctx, runID); e == nil {
		if p.State() == domain.PatchApplicationApplying || p.State() == domain.PatchApplicationRecoveryRequired {
			return v, ErrResultClosureBlocked
		}
		states = append(states, operationState{runID.String() + "/apply", string(p.State()), 0})
		if p.State() == domain.PatchApplicationApplied {
			for i := range v.Entries {
				v.Entries[i].Status, v.Entries[i].Achievement = "retained", "applied"
			}
		}
	} else if !errors.Is(e, storecontract.ErrNotFound) {
		return v, e
	}
	if existing != nil {
		return applyResultClosure(v, *existing), nil
	}
	raw := responseBody(struct {
		View       ResultClosureView
		Operations []operationState
	}{v, states})
	digest := sha256.Sum256(raw)
	v.PreviewDigest = hex.EncodeToString(digest[:])
	return v, nil
}

func requireOpenResultEntry(ctx context.Context, reader storecontract.Reader, resultID domain.ResultID, repoID string) error {
	c, err := reader.GetResultClosure(ctx, resultID)
	if errors.Is(err, storecontract.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, id := range c.RepositoryIDs {
		if id == repoID {
			return ErrResultClosed
		}
	}
	return nil
}

func requireRunWithoutClosure(ctx context.Context, reader storecontract.Reader, runID domain.RunID) error {
	closures, err := reader.ListResultClosuresForRun(ctx, runID)
	if err != nil {
		return err
	}
	if len(closures) != 0 {
		return ErrResultClosed
	}
	return nil
}

func projectClosedDeliveries(ctx context.Context, reader storecontract.Reader, result string, view *TaskDeliveryView) error {
	id, err := domain.ParseResultID(result)
	if err != nil {
		return err
	}
	c, err := reader.GetResultClosure(ctx, id)
	if errors.Is(err, storecontract.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	for i := range view.Repositories {
		for _, closed := range c.RepositoryIDs {
			if view.Repositories[i].RepoID == closed && view.Repositories[i].Commit == "" && view.Repositories[i].Status != "recovery_required" {
				view.Repositories[i].Status = "closed"
				view.Repositories[i].Reason = "Remaining delivery eligibility was explicitly closed. Files and history are retained."
			}
		}
	}
	return nil
}
