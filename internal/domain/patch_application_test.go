package domain

import (
	"crypto/sha256"
	"testing"
	"time"
)

func TestPatchApplicationLifecycle(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	application, err := NewPatchApplication(PatchApplicationParams{
		RunID: NewRunID().String(), ReviewDecisionID: NewReviewDecisionID().String(),
		PatchDigest: sha256.Sum256([]byte("patch")), DeclaredFilesDigest: sha256.Sum256([]byte("files")),
		TargetIdentity: "sha256:target", BaseRevision: "abc", AffectedPaths: []string{"internal/domain/task.go"},
		PreStateDigest: sha256.Sum256([]byte("before")), StartedAt: now, UpdatedAt: now,
	})
	if err != nil || application.State() != PatchApplicationApplying || application.Version() != 1 {
		t.Fatalf("application=%#v err=%v", application, err)
	}
	conflict, err := application.Failed(PatchApplicationConflict, "target content conflicts with the Patch", now.Add(time.Second))
	if err != nil || conflict.State() != PatchApplicationConflict || conflict.Reason() == "" {
		t.Fatalf("conflict=%#v err=%v", conflict, err)
	}
	retrying, err := conflict.Retry(now.Add(2 * time.Second))
	if err != nil || retrying.State() != PatchApplicationApplying || retrying.Version() != 3 {
		t.Fatalf("retrying=%#v err=%v", retrying, err)
	}
	if retrying.TargetIdentity() != application.TargetIdentity() || retrying.BaseRevision() != application.BaseRevision() || retrying.PreStateDigest() != application.PreStateDigest() {
		t.Fatal("retry changed immutable target binding")
	}
	applied, err := retrying.Applied(sha256.Sum256([]byte("after")), now.Add(3*time.Second))
	if err != nil || applied.State() != PatchApplicationApplied || applied.Version() != 4 || applied.AppliedAt().IsZero() {
		t.Fatalf("applied=%#v err=%v", applied, err)
	}
	if _, err := applied.Failed(PatchApplicationConflict, "late", now.Add(4*time.Second)); err == nil {
		t.Fatal("terminal application accepted another transition")
	}
}

func TestPatchApplicationRejectsUnsafePaths(t *testing.T) {
	now := time.Now().UTC()
	base := PatchApplicationParams{
		RunID: NewRunID().String(), ReviewDecisionID: NewReviewDecisionID().String(),
		PatchDigest: sha256.Sum256([]byte("patch")), DeclaredFilesDigest: sha256.Sum256([]byte("files")),
		TargetIdentity: "sha256:target", BaseRevision: "abc", PreStateDigest: sha256.Sum256([]byte("before")),
		StartedAt: now, UpdatedAt: now,
	}
	for _, paths := range [][]string{{"../escape.go"}, {"/absolute.go"}, {"a/./b.go"}, {"same.go", "same.go"}} {
		base.AffectedPaths = paths
		if _, err := NewPatchApplication(base); err == nil {
			t.Fatalf("paths %v accepted", paths)
		}
	}
}
