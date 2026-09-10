package domain

import (
	"crypto/sha256"
	"testing"
	"time"
)

func TestPatchInspectionFailureHasNoSyntheticApplyBinding(t *testing.T) {
	now := time.Date(2026, 8, 24, 20, 0, 0, 0, time.UTC)
	failure, err := NewPatchInspectionFailure(PatchInspectionFailureParams{
		RunID: NewRunID().String(), ReviewDecisionID: NewReviewDecisionID().String(),
		PatchDigest: sha256.Sum256([]byte("patch")), DeclaredFilesDigest: sha256.Sum256([]byte("files")),
		TargetIdentity: "sha256:target", AffectedPaths: []string{"internal/domain/task.go"},
		State: PatchApplicationConflict, Reason: "target file is unavailable", StartedAt: now, UpdatedAt: now,
	})
	if err != nil || failure.Version() != 1 || failure.Reason() == "" {
		t.Fatalf("failure=%#v err=%v", failure, err)
	}
	repeated, err := failure.Repeat(PatchApplicationRecoveryRequired, "inspection interrupted", now.Add(time.Second))
	if err != nil || repeated.Version() != 2 || repeated.TargetIdentity() != failure.TargetIdentity() {
		t.Fatalf("repeated=%#v err=%v", repeated, err)
	}
}
