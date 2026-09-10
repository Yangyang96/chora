package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
	"sort"
	"sync"
)

var resourceApplyLocks keyedLocks
var resourceApplyProcessMu sync.Mutex

// File writes across repositories cannot be atomic. Durable writing markers and
// exact post-state evidence make an interrupted prefix explicit and recoverable.
type ResourceApplyRequest struct {
	ApplyAcceptedPatchRequest
	ResultDigest string
}
type ResourceApplyView struct {
	OperationID  string                        `json:"operationId"`
	Status       string                        `json:"status"`
	Repositories []ResourceApplyRepositoryView `json:"repositories"`
}
type ResourceApplyRepositoryView struct {
	RepoID   string `json:"repoId"`
	Status   string `json:"status"`
	Reason   string `json:"reason"`
	Sequence uint64 `json:"sequence"`
}
type resourceApplyPlan struct {
	ResultDigest   string   `json:"resultDigest"`
	ReviewID       string   `json:"reviewId"`
	SnapshotDigest string   `json:"snapshotDigest"`
	RepoIDs        []string `json:"repoIds"`
}
type resourceApplyEvidence struct {
	NoChanges       bool                  `json:"noChanges,omitempty"`
	Inspection      PatchTargetInspection `json:"inspection"`
	PostStateDigest [32]byte              `json:"postStateDigest"`
	Reason          string                `json:"reason"`
}

func resourceApplyProjection(o storecontract.ResourceApplyOperation, steps []storecontract.ResourceApplyStep) ResourceApplyView {
	v := ResourceApplyView{OperationID: o.ID, Status: "pending", Repositories: []ResourceApplyRepositoryView{}}
	applied, completed, uncertain, conflict := 0, 0, false, false
	for _, s := range steps {
		var evidence resourceApplyEvidence
		_ = json.Unmarshal(s.CanonicalJSON, &evidence)
		status := s.Status
		if s.Status == "applied" && evidence.NoChanges {
			status = "no_change"
		}
		v.Repositories = append(v.Repositories, ResourceApplyRepositoryView{RepoID: s.RepositoryID.String(), Status: status, Reason: evidence.Reason, Sequence: s.Sequence})
		if s.Status == "applied" {
			completed++
			if !evidence.NoChanges {
				applied++
			}
		}
		if s.Status == "writing" || s.Status == "uncertain" {
			uncertain = true
		}
		if s.Status == "conflict" {
			conflict = true
		}
	}
	if len(steps) > 0 && completed == len(steps) {
		v.Status = "applied"
	} else if applied > 0 {
		v.Status = "partial"
	} else if uncertain {
		v.Status = "recovery_required"
	} else if conflict {
		v.Status = "conflict"
	}
	return v
}
func (s *Service) LoadResourceApply(ctx context.Context, resultID domain.ResultID) (ResourceApplyView, error) {
	o, e := s.deps.Store.Reader().GetResourceApplyOperation(ctx, resultID)
	if e != nil {
		return ResourceApplyView{}, e
	}
	steps, e := s.deps.Store.Reader().ListResourceApplySteps(ctx, o.ID)
	if e != nil {
		return ResourceApplyView{}, e
	}
	v := resourceApplyProjection(o, steps)
	closure, e := s.deps.Store.Reader().GetResultClosure(ctx, resultID)
	if errors.Is(e, storecontract.ErrNotFound) {
		return v, nil
	}
	if e != nil {
		return v, e
	}
	closed, applied := 0, 0
	for i := range v.Repositories {
		entry := &v.Repositories[i]
		if entry.Status == "applied" || entry.Status == "no_change" {
			applied++
			continue
		}
		for _, id := range closure.RepositoryIDs {
			if entry.RepoID == id {
				entry.Status, entry.Reason = "closed", ""
				closed++
				break
			}
		}
	}
	if closed > 0 && closed+applied == len(v.Repositories) {
		v.Status = "remaining_closed"
		if applied > 0 {
			v.Status = "partial_applied"
		}
	}
	return v, nil
}
func (s *Service) ApplyResourceResult(ctx context.Context, req ResourceApplyRequest) (ResourceApplyView, error) {
	if e := s.authorize(ctx, req.CommandMeta, "apply_resource_result", req.RunID.String(), req.ExpectedVersion); e != nil {
		return ResourceApplyView{}, e
	}
	if s.deps.ResourcePatchTarget == nil {
		return ResourceApplyView{}, ErrPatchTargetConflict
	}
	unlock := s.runLock(req.RunID)
	defer unlock()
	reader := s.deps.Store.Reader()
	if e := requireRunWithoutClosure(ctx, reader, req.RunID); e != nil {
		return ResourceApplyView{}, e
	}
	run, e := reader.GetRun(ctx, req.RunID)
	if e != nil {
		return ResourceApplyView{}, e
	}
	if run.Version() != req.ExpectedVersion {
		return ResourceApplyView{}, storecontract.ErrVersionConflict
	}
	if run.State() != domain.RunStateAccepted {
		return ResourceApplyView{}, ErrInvalidCommand
	}
	review, e := s.loadResourceReview(ctx, reader, run.ID())
	if e != nil {
		return ResourceApplyView{}, e
	}
	if review.Digest != req.ResultDigest {
		return ResourceApplyView{}, ErrReviewEvidenceUnavailable
	}
	resultID, _ := domain.ParseResultID(review.Group.ID)
	decision, binding, e := reader.GetResourceResultReview(ctx, resultID)
	if e != nil {
		return ResourceApplyView{}, e
	}
	if decision.Kind() != domain.ReviewDecisionAccept || hex.EncodeToString(binding[:]) != review.Digest {
		return ResourceApplyView{}, ErrReviewEvidenceUnavailable
	}
	record, e := reader.GetTaskResourceSnapshot(ctx, run.TaskID())
	if e != nil {
		return ResourceApplyView{}, e
	}
	var snapshot domain.TaskResourceSnapshot
	if e = json.Unmarshal(record.CanonicalJSON, &snapshot); e != nil {
		return ResourceApplyView{}, e
	}
	// Sort physical identities, including shared resources across Projects. A
	// process-wide coordination mutex only protects lock acquisition ordering.
	keys := []string{}
	for _, r := range snapshot.Resources {
		keys = append(keys, r.PhysicalIdentity)
	}
	sort.Strings(keys)
	resourceApplyProcessMu.Lock()
	unlocks := []func(){}
	for i, k := range keys {
		if i == 0 || keys[i-1] != k {
			unlocks = append(unlocks, resourceApplyLocks.lock(k))
		}
	}
	resourceApplyProcessMu.Unlock()
	defer func() {
		for i := len(unlocks) - 1; i >= 0; i-- {
			unlocks[i]()
		}
	}()
	plan := resourceApplyPlan{ResultDigest: review.Digest, ReviewID: decision.ID().String(), SnapshotDigest: review.Group.ResourceSnapshotDigest, RepoIDs: []string{}}
	for _, r := range snapshot.Resources {
		plan.RepoIDs = append(plan.RepoIDs, r.RepoID)
	}
	planJSON, _ := json.Marshal(plan)
	o, e := reader.GetResourceApplyOperation(ctx, resultID)
	if errors.Is(e, storecontract.ErrNotFound) {
		digest := sha256.Sum256([]byte("chora.resource-apply.v1\x00" + resultID.String() + "\x00" + review.Digest))
		o = storecontract.ResourceApplyOperation{ID: "apply_" + hex.EncodeToString(digest[:]), ResultID: resultID, CanonicalJSON: planJSON, CreatedAt: s.deps.Clock.Now()}
		e = s.deps.Store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
			if e := tx.InsertResourceApplyOperation(ctx, o); e != nil {
				return e
			}
			for _, r := range snapshot.Resources {
				id, _ := domain.ParseRepositoryID(r.RepoID)
				if e := tx.AppendResourceApplyStep(ctx, 0, storecontract.ResourceApplyStep{OperationID: o.ID, RepositoryID: id, Sequence: 1, Status: "planned", CanonicalJSON: []byte(`{}`), CreatedAt: o.CreatedAt}); e != nil {
					return e
				}
			}
			return nil
		})
	}
	if e != nil {
		return ResourceApplyView{}, e
	}
	if string(o.CanonicalJSON) != string(planJSON) {
		return ResourceApplyView{}, ErrReviewEvidenceUnavailable
	}
	steps, e := reader.ListResourceApplySteps(ctx, o.ID)
	if e != nil {
		return ResourceApplyView{}, e
	}
	if len(steps) != len(snapshot.Resources) {
		return ResourceApplyView{}, ErrReviewEvidenceUnavailable
	}
	save := func(index int, status string, evidence resourceApplyEvidence) error {
		old := steps[index]
		body, _ := json.Marshal(evidence)
		next := old
		next.Sequence++
		next.Status = status
		next.CanonicalJSON = body
		next.CreatedAt = s.deps.Clock.Now()
		err := s.deps.Store.WithinWriteTx(context.WithoutCancel(ctx), func(tx storecontract.WriteTx) error {
			return tx.AppendResourceApplyStep(context.WithoutCancel(ctx), old.Sequence, next)
		})
		if err == nil {
			steps[index] = next
		}
		return err
	}
	inputs := make([]PatchTargetRequest, len(steps))
	inspections := make([]PatchTargetInspection, len(steps))
	blocked := false
	// All remaining repositories preflight before any new write. Already applied
	// entries are immutable and never replayed or reversed automatically.
	for i, r := range snapshot.Resources {
		if steps[i].RepositoryID.String() != r.RepoID {
			return ResourceApplyView{}, ErrReviewEvidenceUnavailable
		}
		if steps[i].Status == "applied" {
			continue
		}
		if len(review.Patches[i].Patch.Files) == 0 {
			if e = save(i, "applied", resourceApplyEvidence{NoChanges: true, Reason: "No changes in this repository."}); e != nil {
				return ResourceApplyView{}, e
			}
			continue
		}
		var prior resourceApplyEvidence
		if e = json.Unmarshal(steps[i].CanonicalJSON, &prior); e != nil {
			return ResourceApplyView{}, e
		}
		input := PatchTargetRequest{TaskID: run.TaskID(), Raw: review.Patches[i].Patch.Raw, PatchDigest: review.Patches[i].Patch.PatchDigest, AffectedPaths: review.Group.Repositories[i].ChangedPaths}
		if steps[i].Status == "writing" || steps[i].Status == "uncertain" {
			input.PriorPreStateDigest = prior.Inspection.StateDigest
		}
		inspection, inspectErr := s.deps.ResourcePatchTarget.Inspect(ctx, r, input)
		if inspectErr == nil && inspection.AlreadyApplied && input.PriorPreStateDigest != ([32]byte{}) {
			if e = save(i, "applied", resourceApplyEvidence{Inspection: prior.Inspection, PostStateDigest: inspection.StateDigest, Reason: "Exact prior write reconciled."}); e != nil {
				return ResourceApplyView{}, e
			}
			continue
		}
		if inspectErr != nil || !inspection.Applicable {
			status := "conflict"
			if steps[i].Status == "writing" || steps[i].Status == "uncertain" {
				status = "uncertain"
			}
			reason := "Target preflight failed; no new repository writes were started."
			if inspection.Reason != "" {
				reason += " " + inspection.Reason
			}
			if e = save(i, status, resourceApplyEvidence{Inspection: prior.Inspection, Reason: reason}); e != nil {
				return ResourceApplyView{}, e
			}
			blocked = true
			continue
		}
		if input.PriorPreStateDigest != ([32]byte{}) && inspection.StateDigest != input.PriorPreStateDigest {
			if e = save(i, "uncertain", resourceApplyEvidence{Inspection: prior.Inspection, Reason: "Prior write cannot be reconciled."}); e != nil {
				return ResourceApplyView{}, e
			}
			blocked = true
			continue
		}
		inputs[i], inspections[i] = input, inspection
		if e = save(i, "preflight", resourceApplyEvidence{Inspection: inspection}); e != nil {
			return ResourceApplyView{}, e
		}
	}
	if blocked {
		return resourceApplyProjection(o, steps), nil
	}
	for i, r := range snapshot.Resources {
		if steps[i].Status == "applied" {
			continue
		}
		if e = ctx.Err(); e != nil {
			return resourceApplyProjection(o, steps), e
		}
		if e = save(i, "writing", resourceApplyEvidence{Inspection: inspections[i]}); e != nil {
			return ResourceApplyView{}, e
		}
		proof, writeErr := s.deps.ResourcePatchTarget.Apply(ctx, r, inputs[i], inspections[i])
		if writeErr != nil {
			status := "uncertain"
			if errors.Is(writeErr, ErrPatchTargetConflict) {
				status = "conflict"
			}
			if e = save(i, status, resourceApplyEvidence{Inspection: inspections[i], Reason: "Apply stopped; inspect this repository before retrying remaining changes."}); e != nil {
				return ResourceApplyView{}, e
			}
			return resourceApplyProjection(o, steps), nil
		}
		if proof.PostStateDigest == ([32]byte{}) {
			if e = save(i, "uncertain", resourceApplyEvidence{Inspection: inspections[i], Reason: "Missing post-state proof."}); e != nil {
				return ResourceApplyView{}, e
			}
			return resourceApplyProjection(o, steps), nil
		}
		if e = save(i, "applied", resourceApplyEvidence{Inspection: inspections[i], PostStateDigest: proof.PostStateDigest}); e != nil {
			return ResourceApplyView{}, e
		}
	}
	view := resourceApplyProjection(o, steps)
	if view.Status == "applied" {
		e = s.deps.Store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
			task, err := tx.GetTask(ctx, run.TaskID())
			if err != nil {
				return err
			}
			if task.State() == domain.TaskStateClosed {
				return nil
			}
			return tx.CloseTaskForAcceptedRun(ctx, run.TaskID(), run.ID(), run.Version())
		})
		if e != nil {
			return view, fmt.Errorf("applied but task close failed: %w", e)
		}
	}
	return view, nil
}
func (s *Service) ResourceApplyAvailable() bool { return s != nil && s.deps.ResourcePatchTarget != nil }
