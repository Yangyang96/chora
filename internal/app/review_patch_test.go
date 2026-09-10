package app_test

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/speccoding"
)

func TestBuildReviewablePatchDerivesFilesAndLinesFromVerifiedBytes(t *testing.T) {
	contract := loadReviewContract(t)
	patch := []byte("diff --git a/internal/domain/task.go b/internal/domain/task.go\n" +
		"--- a/internal/domain/task.go\n" +
		"+++ b/internal/domain/task.go\n" +
		"@@ -10,2 +10,2 @@ func validate() {\n" +
		"-\treturn false\n" +
		"+\treturn true\n" +
		" }\n")
	digest := sha256.Sum256(patch)

	reviewable, err := app.BuildReviewablePatch(contract, patch, digest)
	if err != nil {
		t.Fatal(err)
	}
	if string(reviewable.Raw) != string(patch) || reviewable.PatchDigest != digest || reviewable.DeclaredFilesDigest == ([32]byte{}) {
		t.Fatalf("reviewable identity = %#v", reviewable)
	}
	if len(reviewable.Files) != 1 || reviewable.Files[0].Path != "internal/domain/task.go" {
		t.Fatalf("files = %#v", reviewable.Files)
	}
	if reviewable.Files[0].Additions != 1 || reviewable.Files[0].Deletions != 1 || len(reviewable.Files[0].Hunks) != 1 ||
		reviewable.Files[0].Hunks[0].Header != "@@ -10,2 +10,2 @@ func validate() {" {
		t.Fatalf("file change projection = %#v", reviewable.Files[0])
	}
	lines := reviewable.Files[0].Lines
	if len(lines) != 3 || lines[0].Kind != app.PatchLineRemoved || lines[0].OldLine != 10 || lines[0].NewLine != 0 ||
		lines[1].Kind != app.PatchLineAdded || lines[1].OldLine != 0 || lines[1].NewLine != 10 ||
		lines[2].Kind != app.PatchLineContext || lines[2].OldLine != 11 || lines[2].NewLine != 11 {
		t.Fatalf("lines = %#v", lines)
	}
}

func TestBuildReviewablePatchPreservesMultipleHunkBoundaries(t *testing.T) {
	contract := loadReviewContract(t)
	patch := []byte("diff --git a/internal/domain/task.go b/internal/domain/task.go\n" +
		"--- a/internal/domain/task.go\n" +
		"+++ b/internal/domain/task.go\n" +
		"@@ -2,3 +2,4 @@\n" +
		" keep-2\n" +
		"+added-3\n" +
		" keep-3\n" +
		" keep-4\n" +
		"@@ -20,3 +21,2 @@\n" +
		" keep-20\n" +
		"-removed-21\n" +
		" keep-22\n")
	reviewable, err := app.BuildReviewablePatch(contract, patch, sha256.Sum256(patch))
	if err != nil {
		t.Fatal(err)
	}
	file := reviewable.Files[0]
	if len(file.Hunks) != 2 || file.Additions != 1 || file.Deletions != 1 ||
		file.Hunks[0].OldStart != 2 || file.Hunks[0].OldCount != 3 || file.Hunks[0].NewStart != 2 || file.Hunks[0].NewCount != 4 ||
		file.Hunks[1].OldStart != 20 || file.Hunks[1].NewStart != 21 {
		t.Fatalf("hunks = %#v", file)
	}
}

func TestBuildReviewablePatchMinimizesLegacyWholeFileHelloProjection(t *testing.T) {
	contract := loadReviewContract(t)
	before := []string{"package domain", "", "import \"strings\"", "", "type Task struct {", "\tTitle string", "}", "", "func (task Task) Valid() bool {", "\treturn strings.TrimSpace(task.Title) != \"\"", "}"}
	after := append(append([]string(nil), before...), "", "// Hello returns the canonical greeting.", "func Hello() string {", "\treturn \"hello\"", "}")
	var patch strings.Builder
	fmt.Fprintf(&patch, "diff --git a/internal/domain/task.go b/internal/domain/task.go\n--- a/internal/domain/task.go\n+++ b/internal/domain/task.go\n@@ -1,%d +1,%d @@\n", len(before), len(after))
	for _, line := range before {
		fmt.Fprintf(&patch, "-%s\n", line)
	}
	for _, line := range after {
		fmt.Fprintf(&patch, "+%s\n", line)
	}
	raw := []byte(patch.String())

	reviewable, err := app.BuildReviewablePatch(contract, raw, sha256.Sum256(raw))
	if err != nil {
		t.Fatal(err)
	}
	file := reviewable.Files[0]
	if string(reviewable.Raw) != string(raw) || file.Additions != 5 || file.Deletions != 0 || len(file.Hunks) != 1 || len(file.Lines) >= len(before)+len(after) {
		t.Fatalf("legacy projection = %#v", file)
	}
	for _, line := range file.Lines {
		if line.Kind == app.PatchLineRemoved || line.Kind == app.PatchLineAdded && line.Text == "package domain" {
			t.Fatalf("unchanged source appeared destructive: %#v", file.Lines)
		}
	}
}

func TestBuildReviewablePatchFailsClosedOnDriftOrUndeclaredFile(t *testing.T) {
	contract := loadReviewContract(t)
	allowed := []byte("diff --git a/internal/domain/task.go b/internal/domain/task.go\n--- a/internal/domain/task.go\n+++ b/internal/domain/task.go\n@@ -1 +1 @@\n-old\n+new\n")
	digest := sha256.Sum256(allowed)

	drifted := append([]byte(nil), allowed...)
	drifted[len(drifted)-2] = 'x'
	if _, err := app.BuildReviewablePatch(contract, drifted, digest); err == nil {
		t.Fatal("digest-drifted Patch was reviewable")
	}

	outside := []byte("diff --git a/README.md b/README.md\n--- a/README.md\n+++ b/README.md\n@@ -1 +1 @@\n-old\n+new\n")
	if _, err := app.BuildReviewablePatch(contract, outside, sha256.Sum256(outside)); err == nil {
		t.Fatal("out-of-bound Patch was reviewable")
	}
}

func TestBuildReviewablePatchClassifiesTextAddAndDelete(t *testing.T) {
	contract := loadReviewContract(t)
	tests := []struct {
		name string
		raw  string
		kind app.ReviewablePatchFileKind
	}{
		{
			name: "add",
			raw: "diff --git a/internal/domain/task.go b/internal/domain/task.go\n" +
				"new file mode 100644\n" +
				"index 0000000000000000000000000000000000000000..ce013625030ba8dba906f756967f9e9ca394464a\n" +
				"--- /dev/null\n+++ b/internal/domain/task.go\t\n@@ -0,0 +1 @@\n+hello\n",
			kind: app.ReviewablePatchFileAdded,
		},
		{
			name: "delete",
			raw: "diff --git a/internal/domain/task.go b/internal/domain/task.go\n" +
				"deleted file mode 100644\n" +
				"index ce013625030ba8dba906f756967f9e9ca394464a..0000000000000000000000000000000000000000\n" +
				"--- a/internal/domain/task.go\n+++ /dev/null\n@@ -1 +0,0 @@\n-hello\n",
			kind: app.ReviewablePatchFileDeleted,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw := []byte(test.raw)
			patch, err := app.BuildReviewablePatch(contract, raw, sha256.Sum256(raw))
			if err != nil {
				t.Fatal(err)
			}
			if len(patch.Files) != 1 || patch.Files[0].Kind != test.kind {
				t.Fatalf("files = %#v", patch.Files)
			}
		})
	}
}

func TestBuildReviewablePatchRejectsBinaryAndSpecialModes(t *testing.T) {
	contract := loadReviewContract(t)
	for name, raw := range map[string][]byte{
		"binary":      []byte("diff --git a/internal/domain/task.go b/internal/domain/task.go\nindex 1111111..2222222 100644\nBinary files a/internal/domain/task.go and b/internal/domain/task.go differ\n"),
		"submodule":   []byte("diff --git a/internal/domain/task.go b/internal/domain/task.go\nindex 1111111..2222222 160000\n--- a/internal/domain/task.go\n+++ b/internal/domain/task.go\n@@ -1 +1 @@\n-old\n+new\n"),
		"mode change": []byte("diff --git a/internal/domain/task.go b/internal/domain/task.go\nold mode 100644\nnew mode 100755\n"),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := app.BuildReviewablePatch(contract, raw, sha256.Sum256(raw)); !errors.Is(err, app.ErrReviewEvidenceUnavailable) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func loadReviewContract(t *testing.T) speccoding.CoreContract {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "contracts", "g2-m1a", "chora-m1-real-task.v8.json"))
	if err != nil {
		t.Fatal(err)
	}
	contract, err := speccoding.DecodeCoreContract(data)
	if err != nil {
		t.Fatal(err)
	}
	return contract
}
