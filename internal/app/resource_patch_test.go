package app

import (
	"crypto/sha256"
	"errors"
	"testing"

	"github.com/Yangyang96/chora/internal/domain"
)

func TestBuildResourceReviewablePatchBindsRepositoryScopeAndActualPaths(t *testing.T) {
	resource := resourcePatchTestResource()
	raw := []byte("diff --git a/src/a.txt b/src/a.txt\n--- a/src/a.txt\n+++ b/src/a.txt\n@@ -1 +1 @@\n-old\n+new\n")
	digest := sha256.Sum256(raw)
	patch, err := BuildResourceReviewablePatch(resource, []string{"src/a.txt"}, raw, digest)
	if err != nil {
		t.Fatal(err)
	}
	if len(patch.Files) != 1 || patch.Files[0].Path != "src/a.txt" || patch.PatchDigest != digest || patch.DeclaredFilesDigest == ([32]byte{}) {
		t.Fatalf("patch=%+v", patch)
	}
	raw[0] = 'D'
	if patch.Raw[0] != 'd' {
		t.Fatal("review Patch retained mutable caller bytes")
	}

	other := resource
	other.RepoID = domain.NewRepositoryID().String()
	otherPatch, err := BuildResourceReviewablePatch(other, []string{"src/a.txt"}, patch.Raw, digest)
	if err != nil {
		t.Fatal(err)
	}
	if otherPatch.DeclaredFilesDigest == patch.DeclaredFilesDigest {
		t.Fatal("repository identity was not bound into declared scope digest")
	}
	other = resource
	other.BaseCommit = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	otherPatch, err = BuildResourceReviewablePatch(other, []string{"src/a.txt"}, patch.Raw, digest)
	if err != nil {
		t.Fatal(err)
	}
	if otherPatch.DeclaredFilesDigest == patch.DeclaredFilesDigest {
		t.Fatal("repository base was not bound into declared scope digest")
	}
}

func TestBuildResourceReviewablePatchRejectsScopeAndPathDrift(t *testing.T) {
	resource := resourcePatchTestResource()
	raw := []byte("diff --git a/src/a.txt b/src/a.txt\n--- a/src/a.txt\n+++ b/src/a.txt\n@@ -1 +1 @@\n-old\n+new\n")
	digest := sha256.Sum256(raw)
	for name, changed := range map[string][]string{
		"outside frozen scope":     {"outside.txt"},
		"missing collected path":   {"src/a.txt", "src/b.txt"},
		"duplicate collected path": {"src/a.txt", "src/a.txt"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := BuildResourceReviewablePatch(resource, changed, raw, digest); !errors.Is(err, ErrReviewEvidenceUnavailable) {
				t.Fatalf("err=%v", err)
			}
		})
	}
	reference := resource
	reference.Role = "reference"
	if _, err := BuildResourceReviewablePatch(reference, []string{"src/a.txt"}, raw, digest); !errors.Is(err, ErrReviewEvidenceUnavailable) {
		t.Fatalf("reference err=%v", err)
	}
}

func resourcePatchTestResource() domain.TaskRepositoryResource {
	return domain.TaskRepositoryResource{
		RepoID: domain.NewRepositoryID().String(), Role: "write",
		BaseCommit: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", BaseTree: "cccccccccccccccccccccccccccccccccccccccc",
		AssociationVersion: 1,
		Scope:              domain.TaskRepositoryScope{Mode: "restricted", WritableDirectories: []string{"src"}, ProtectedDirectories: []string{"src/generated"}, MigrationChoice: "not_needed"},
	}
}
