package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

var (
	ErrPatchTargetConflict = errors.New("patch target conflict")
	ErrPatchTargetRecovery = errors.New("patch target recovery required")
)

type PatchTargetRequest struct {
	TaskID              domain.TaskID
	Raw                 []byte
	PatchDigest         [32]byte
	AffectedPaths       []string
	PriorPreStateDigest [32]byte
}

type PatchTargetInspection struct {
	TargetIdentity string
	BaseRevision   string
	StateDigest    [32]byte
	Applicable     bool
	AlreadyApplied bool
	Reason         string
}

type PatchTargetEvidence struct{ PostStateDigest [32]byte }

type ApplyAcceptedPatchRequest struct {
	CommandMeta
	RunID           domain.RunID
	ExpectedVersion uint64
}

type PatchApplicationResult struct {
	Application       domain.PatchApplication
	InspectionFailure *domain.PatchInspectionFailure
	Replayed          bool
}

func (s *Service) ApplyAcceptedPatch(ctx context.Context, request ApplyAcceptedPatchRequest) (PatchApplicationResult, error) {
	if s == nil || s.deps.Store == nil || s.deps.PatchSource == nil || s.deps.PatchTarget == nil {
		return PatchApplicationResult{}, fmt.Errorf("%w: Patch Apply dependencies unavailable", ErrInvalidCommand)
	}
	if err := s.authorize(ctx, request.CommandMeta, "apply_accepted_patch", request.RunID.String(), request.ExpectedVersion); err != nil {
		return PatchApplicationResult{}, err
	}
	unlock := s.runLock(request.RunID)
	defer unlock()

	reader := s.deps.Store.Reader()
	if e := requireRunWithoutClosure(ctx, reader, request.RunID); e != nil {
		return PatchApplicationResult{}, e
	}
	run, err := reader.GetRun(ctx, request.RunID)
	if err != nil {
		return PatchApplicationResult{}, err
	}
	if run.Version() != request.ExpectedVersion {
		return PatchApplicationResult{}, storecontract.ErrVersionConflict
	}
	if run.State() != domain.RunStateAccepted {
		return PatchApplicationResult{}, fmt.Errorf("%w: Run is not accepted", ErrInvalidCommand)
	}
	change, err := s.loadReviewableChange(ctx, reader, run.ID())
	if err != nil {
		return PatchApplicationResult{}, err
	}
	review, err := reader.GetVerifiedReviewForResult(ctx, change.Binding.ResultID)
	if err != nil || review.RunID() != run.ID() || review.Kind() != domain.ReviewDecisionAccept || review.Binding() != change.Binding {
		return PatchApplicationResult{}, fmt.Errorf("%w: accepted Patch binding is unavailable or drifted", ErrInvalidCommand)
	}

	existing, existingErr := reader.GetPatchApplication(ctx, run.ID())
	if existingErr == nil && existing.State() == domain.PatchApplicationApplied {
		return PatchApplicationResult{Application: existing, Replayed: true}, nil
	}
	if existingErr != nil && !errors.Is(existingErr, storecontract.ErrNotFound) {
		return PatchApplicationResult{}, existingErr
	}
	inspectionFailure, inspectionFailureErr := reader.GetPatchInspectionFailure(ctx, run.ID())
	if inspectionFailureErr != nil && !errors.Is(inspectionFailureErr, storecontract.ErrNotFound) {
		return PatchApplicationResult{}, inspectionFailureErr
	}
	paths := make([]string, len(change.Patch.Files))
	for index, file := range change.Patch.Files {
		paths[index] = file.Path
	}
	priorPre := [32]byte{}
	if existingErr == nil {
		priorPre = existing.PreStateDigest()
	}
	if err := s.ensureExistingTaskWorktree(ctx, run.TaskID()); err != nil {
		return PatchApplicationResult{}, err
	}
	input := PatchTargetRequest{TaskID: run.TaskID(), Raw: change.Patch.Raw, PatchDigest: change.Patch.PatchDigest, AffectedPaths: paths, PriorPreStateDigest: priorPre}
	inspection, err := s.deps.PatchTarget.Inspect(ctx, input)
	if err != nil {
		if inspection.TargetIdentity == "" {
			return PatchApplicationResult{}, err
		}
		state := domain.PatchApplicationConflict
		if errors.Is(err, ErrPatchTargetRecovery) {
			state = domain.PatchApplicationRecoveryRequired
		}
		now := s.deps.Clock.Now()
		if existingErr == nil {
			if inspection.TargetIdentity != existing.TargetIdentity() {
				return PatchApplicationResult{}, fmt.Errorf("%w: Patch target binding changed after the first application attempt", ErrInvalidCommand)
			}
			applying, transitionErr := existing.Retry(now)
			if transitionErr != nil {
				return PatchApplicationResult{}, transitionErr
			}
			if saveErr := s.insertOrSaveApplying(ctx, true, existing, applying, now); saveErr != nil {
				return PatchApplicationResult{}, saveErr
			}
			failed, transitionErr := applying.Failed(state, publicApplyReason(inspection.Reason), s.deps.Clock.Now())
			if transitionErr != nil {
				return PatchApplicationResult{}, transitionErr
			}
			if saveErr := s.savePatchApplication(ctx, applying, failed, "patch_application."+string(state), failed.UpdatedAt(), false, run.TaskID()); saveErr != nil {
				return PatchApplicationResult{}, saveErr
			}
			return PatchApplicationResult{Application: failed}, nil
		}
		failure, saveErr := s.recordPatchInspectionFailure(ctx, inspectionFailureErr == nil, inspectionFailure, domain.PatchInspectionFailureParams{
			RunID: run.ID().String(), ReviewDecisionID: review.ID().String(), PatchDigest: change.Patch.PatchDigest,
			DeclaredFilesDigest: change.Patch.DeclaredFilesDigest, TargetIdentity: inspection.TargetIdentity,
			AffectedPaths: paths, State: state, Reason: publicApplyReason(inspection.Reason), StartedAt: now, UpdatedAt: now,
		})
		if saveErr != nil {
			return PatchApplicationResult{}, saveErr
		}
		return PatchApplicationResult{InspectionFailure: &failure}, nil
	}
	if inspectionFailureErr == nil && inspection.TargetIdentity != inspectionFailure.TargetIdentity() {
		return PatchApplicationResult{}, fmt.Errorf("%w: Patch target binding changed after the first inspection failure", ErrInvalidCommand)
	}
	now := s.deps.Clock.Now()
	var applying domain.PatchApplication
	if existingErr == nil {
		if existing.State() == domain.PatchApplicationApplying {
			recovery, transitionErr := existing.Failed(domain.PatchApplicationRecoveryRequired, "Application was interrupted before completion could be proven.", now)
			if transitionErr != nil {
				return PatchApplicationResult{}, transitionErr
			}
			if err := s.savePatchApplication(ctx, existing, recovery, "patch_application.recovery_required", now, false, run.TaskID()); err != nil {
				return PatchApplicationResult{}, err
			}
			existing = recovery
		}
		if inspection.TargetIdentity != existing.TargetIdentity() || inspection.BaseRevision != existing.BaseRevision() {
			return PatchApplicationResult{}, fmt.Errorf("%w: Patch target binding changed after the first application attempt", ErrInvalidCommand)
		}
		applying, err = existing.Retry(now)
	} else {
		applying, err = domain.NewPatchApplication(domain.PatchApplicationParams{
			RunID: run.ID().String(), ReviewDecisionID: review.ID().String(), PatchDigest: change.Patch.PatchDigest,
			DeclaredFilesDigest: change.Patch.DeclaredFilesDigest, TargetIdentity: inspection.TargetIdentity,
			BaseRevision: inspection.BaseRevision, AffectedPaths: paths, PreStateDigest: inspection.StateDigest,
			StartedAt: now, UpdatedAt: now,
		})
	}
	if err != nil {
		return PatchApplicationResult{}, err
	}
	if err := s.insertOrSaveApplying(ctx, existingErr == nil, existing, applying, now); err != nil {
		return PatchApplicationResult{}, err
	}
	if !inspection.Applicable && !inspection.AlreadyApplied {
		failed, transitionErr := applying.Failed(domain.PatchApplicationConflict, publicApplyReason(inspection.Reason), s.deps.Clock.Now())
		if transitionErr != nil {
			return PatchApplicationResult{}, transitionErr
		}
		if err := s.savePatchApplication(ctx, applying, failed, "patch_application.conflict", failed.UpdatedAt(), false, run.TaskID()); err != nil {
			return PatchApplicationResult{}, err
		}
		return PatchApplicationResult{Application: failed}, nil
	}
	var evidence PatchTargetEvidence
	if inspection.AlreadyApplied {
		evidence.PostStateDigest = inspection.StateDigest
	} else {
		evidence, err = s.deps.PatchTarget.Apply(ctx, input, inspection)
		if err != nil {
			state := domain.PatchApplicationRecoveryRequired
			if errors.Is(err, ErrPatchTargetConflict) {
				state = domain.PatchApplicationConflict
			}
			failed, transitionErr := applying.Failed(state, publicApplyReason(err.Error()), s.deps.Clock.Now())
			if transitionErr != nil {
				return PatchApplicationResult{}, transitionErr
			}
			if saveErr := s.savePatchApplication(ctx, applying, failed, "patch_application."+string(state), failed.UpdatedAt(), false, run.TaskID()); saveErr != nil {
				return PatchApplicationResult{}, saveErr
			}
			return PatchApplicationResult{Application: failed}, nil
		}
	}
	applied, err := applying.Applied(evidence.PostStateDigest, s.deps.Clock.Now())
	if err != nil {
		return PatchApplicationResult{}, err
	}
	if err := s.savePatchApplication(ctx, applying, applied, "patch_application.applied", applied.UpdatedAt(), true, run.TaskID()); err != nil {
		return PatchApplicationResult{}, err
	}
	return PatchApplicationResult{Application: applied}, nil
}

func (s *Service) recordPatchInspectionFailure(ctx context.Context, exists bool, previous domain.PatchInspectionFailure, params domain.PatchInspectionFailureParams) (domain.PatchInspectionFailure, error) {
	var next domain.PatchInspectionFailure
	var err error
	if exists {
		if previous.PatchDigest() != params.PatchDigest || previous.DeclaredFilesDigest() != params.DeclaredFilesDigest || previous.TargetIdentity() != params.TargetIdentity {
			return domain.PatchInspectionFailure{}, fmt.Errorf("%w: Patch inspection binding changed", ErrInvalidCommand)
		}
		next, err = previous.Repeat(params.State, params.Reason, params.UpdatedAt)
	} else {
		next, err = domain.NewPatchInspectionFailure(params)
	}
	if err != nil {
		return domain.PatchInspectionFailure{}, err
	}
	err = s.deps.Store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		if exists {
			if err := tx.SavePatchInspectionFailureCAS(ctx, previous.Version(), next); err != nil {
				return err
			}
		} else if err := tx.InsertPatchInspectionFailure(ctx, next); err != nil {
			return err
		}
		_, err := tx.AppendRunEvent(ctx, next.RunID(), storecontract.EventDraft{ID: s.deps.IDs.EventID(), Type: "patch_inspection." + string(next.State()), Source: "app", OccurredAt: next.UpdatedAt(), RecordedAt: next.UpdatedAt(), NormalizedJSON: responseBody(map[string]any{"type": "patch_inspection." + string(next.State()), "patch_digest": fmt.Sprintf("%x", next.PatchDigest()), "inspection_version": next.Version()})})
		return err
	})
	return next, err
}

func (s *Service) insertOrSaveApplying(ctx context.Context, exists bool, previous, applying domain.PatchApplication, at time.Time) error {
	return s.deps.Store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		if exists {
			if err := tx.SavePatchApplicationCAS(ctx, previous.Version(), applying); err != nil {
				return err
			}
		} else if err := tx.InsertPatchApplication(ctx, applying); err != nil {
			return err
		}
		_, err := tx.AppendRunEvent(ctx, applying.RunID(), storecontract.EventDraft{ID: s.deps.IDs.EventID(), Type: "patch_application.started", Source: "app", OccurredAt: at, RecordedAt: at, NormalizedJSON: responseBody(map[string]any{"type": "patch_application.started", "patch_digest": fmt.Sprintf("%x", applying.PatchDigest()), "application_version": applying.Version()})})
		return err
	})
}

func (s *Service) savePatchApplication(ctx context.Context, previous, next domain.PatchApplication, eventType string, at time.Time, closeTask bool, taskID domain.TaskID) error {
	return s.deps.Store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		if err := tx.SavePatchApplicationCAS(ctx, previous.Version(), next); err != nil {
			return err
		}
		if closeTask {
			if err := tx.CloseTaskForAppliedRun(ctx, taskID, next.RunID(), next.Version()); err != nil {
				return err
			}
		}
		_, err := tx.AppendRunEvent(ctx, next.RunID(), storecontract.EventDraft{ID: s.deps.IDs.EventID(), Type: eventType, Source: "app", OccurredAt: at, RecordedAt: at, NormalizedJSON: responseBody(map[string]any{"type": eventType, "patch_digest": fmt.Sprintf("%x", next.PatchDigest()), "application_version": next.Version(), "state": next.State()})})
		return err
	})
}

func publicApplyReason(reason string) string {
	reason = strings.TrimSpace(reason)
	if reason == "" || len([]rune(reason)) > 240 {
		return "The target working tree cannot accept the exact Patch."
	}
	return reason
}

func (s *Service) recoverPatchApplications(ctx context.Context) error {
	applications, err := s.deps.Store.Reader().ListApplyingPatchApplications(ctx)
	if err != nil {
		return err
	}
	for _, application := range applications {
		now := s.deps.Clock.Now()
		recovery, err := application.Failed(domain.PatchApplicationRecoveryRequired, "Backend restart interrupted Patch application; retry to reconcile the target.", now)
		if err != nil {
			return err
		}
		run, err := s.deps.Store.Reader().GetRun(ctx, application.RunID())
		if err != nil {
			return err
		}
		if err := s.savePatchApplication(ctx, application, recovery, "patch_application.recovery_required", now, false, run.TaskID()); err != nil {
			return err
		}
	}
	return nil
}
