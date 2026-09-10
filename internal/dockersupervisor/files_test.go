package dockersupervisor

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Yangyang96/chora/internal/linediff"
)

func TestDerivePatchProducesApplicableUnifiedDiff(t *testing.T) {
	baseline := t.TempDir()
	repository := t.TempDir()
	writePatchFixture(t, baseline, "internal/domain/task.go", "package domain\n\nfunc value() string { return \"before\" }\n")
	writePatchFixture(t, repository, "internal/domain/task.go", "package domain\n\nfunc value() string { return \"after\" }\n")

	patch, err := derivePatch(baseline, repository, []string{"internal/domain/task.go"}, 1<<20)
	if err != nil {
		t.Fatalf("derivePatch() = %v", err)
	}
	text := string(patch)
	if !strings.Contains(text, "diff --git a/internal/domain/task.go b/internal/domain/task.go") ||
		!strings.Contains(text, "-func value() string { return \"before\" }") ||
		!strings.Contains(text, "+func value() string { return \"after\" }") {
		t.Fatalf("patch is not a reviewable unified diff:\n%s", text)
	}
	if strings.Contains(text, "-package domain") || strings.Contains(text, "+package domain") ||
		!strings.Contains(text, " package domain") {
		t.Fatalf("unchanged lines were represented as replacements:\n%s", text)
	}

	checkRoot := t.TempDir()
	writePatchFixture(t, checkRoot, "internal/domain/task.go", "package domain\n\nfunc value() string { return \"before\" }\n")
	patchPath := filepath.Join(t.TempDir(), "change.diff")
	if err := os.WriteFile(patchPath, patch, 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("git", "apply", "--check", patchPath)
	command.Dir = checkRoot
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git apply --check = %v: %s", err, output)
	}
}

func TestDerivePatchShowsHelloAsAnInsertionInsteadOfAWholeFileReplacement(t *testing.T) {
	baseline := t.TempDir()
	repository := t.TempDir()
	before := "package domain\n\nimport \"strings\"\n\ntype Task struct {\n\tTitle string\n}\n\nfunc (task Task) Valid() bool {\n\treturn strings.TrimSpace(task.Title) != \"\"\n}\n"
	after := before + "\n// Hello returns the canonical greeting.\nfunc Hello() string {\n\treturn \"hello\"\n}\n"
	writePatchFixture(t, baseline, "internal/domain/task.go", before)
	writePatchFixture(t, repository, "internal/domain/task.go", after)

	patch, err := derivePatch(baseline, repository, []string{"internal/domain/task.go"}, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	text := string(patch)
	added, removed := 0, 0
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
			added++
		}
		if strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---") {
			removed++
		}
	}
	if added != 5 || removed != 0 || strings.Contains(text, "+package domain") {
		t.Fatalf("Hello insertion was rendered as destructive churn:\n%s", text)
	}
	assertPatchApplies(t, baseline, patch)
}

func TestDerivePatchSeparatesDistantChangesIntoContextualHunks(t *testing.T) {
	baseline := t.TempDir()
	repository := t.TempDir()
	before := make([]string, 24)
	after := make([]string, 24)
	for index := range before {
		before[index] = fmt.Sprintf("line %02d\n", index+1)
		after[index] = before[index]
	}
	after[2] = "changed near start\n"
	after[21] = "changed near end\n"
	writePatchFixture(t, baseline, "internal/domain/task.go", strings.Join(before, ""))
	writePatchFixture(t, repository, "internal/domain/task.go", strings.Join(after, ""))

	patch, err := derivePatch(baseline, repository, []string{"internal/domain/task.go"}, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	text := string(patch)
	if strings.Count(text, "@@ ") != 2 || strings.Contains(text, "-line 10") || strings.Contains(text, "+line 10") {
		t.Fatalf("distant edits were not separated into minimal hunks:\n%s", text)
	}
	assertPatchApplies(t, baseline, patch)
}

func TestDerivePatchSeparatesDistantChangesInLargeRepeatedFile(t *testing.T) {
	baseline := t.TempDir()
	repository := t.TempDir()
	before := make([]string, 5000)
	after := make([]string, 5000)
	for index := range before {
		before[index] = "same repeated line\n"
		after[index] = before[index]
	}
	after[100] = "changed near start\n"
	after[4900] = "changed near end\n"
	writePatchFixture(t, baseline, "internal/domain/task.go", strings.Join(before, ""))
	writePatchFixture(t, repository, "internal/domain/task.go", strings.Join(after, ""))

	patch, err := derivePatch(baseline, repository, []string{"internal/domain/task.go"}, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	text := string(patch)
	if strings.Count(text, "@@ ") != 2 || strings.Count(text, "\n-same repeated line") != 2 || strings.Count(text, "\n+changed near ") != 2 {
		t.Fatalf("large repeated change was not minimal:\n%s", text)
	}
	assertPatchApplies(t, baseline, patch)
}

func TestDerivePatchHandlesPureInsertionAndMissingFinalNewline(t *testing.T) {
	baseline := t.TempDir()
	repository := t.TempDir()
	writePatchFixture(t, baseline, "internal/domain/task.go", "package domain")
	writePatchFixture(t, repository, "internal/domain/task.go", "package domain\n\nfunc Hello() string { return \"hello\" }\n")

	patch, err := derivePatch(baseline, repository, []string{"internal/domain/task.go"}, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	text := string(patch)
	if !strings.Contains(text, `\ No newline at end of file`) || !strings.Contains(text, "+func Hello()") {
		t.Fatalf("newline-aware insertion Patch =\n%s", text)
	}
	assertPatchApplies(t, baseline, patch)
}

func TestShortestLineEditsReconstructsBothSides(t *testing.T) {
	tests := []struct {
		name   string
		before []string
		after  []string
	}{
		{name: "empty", before: nil, after: nil},
		{name: "insert at edges", before: []string{"b\n", "c\n"}, after: []string{"a\n", "b\n", "c\n", "d\n"}},
		{name: "delete at edges", before: []string{"a\n", "b\n", "c\n", "d\n"}, after: []string{"b\n", "c\n"}},
		{name: "repeated lines", before: []string{"same\n", "old\n", "same\n", "tail\n"}, after: []string{"same\n", "new\n", "same\n", "tail\n"}},
		{name: "replace all", before: []string{"old one\n", "old two\n"}, after: []string{"new one\n", "new two\n"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			edits := linediff.ShortestEdits(test.before, test.after)
			var reconstructedBefore, reconstructedAfter []string
			for _, edit := range edits {
				if edit.Kind != linediff.Insert {
					reconstructedBefore = append(reconstructedBefore, edit.Line)
				}
				if edit.Kind != linediff.Delete {
					reconstructedAfter = append(reconstructedAfter, edit.Line)
				}
			}
			if strings.Join(reconstructedBefore, "") != strings.Join(test.before, "") || strings.Join(reconstructedAfter, "") != strings.Join(test.after, "") {
				t.Fatalf("edits = %#v; reconstructed before = %#v, after = %#v", edits, reconstructedBefore, reconstructedAfter)
			}
		})
	}
}

func assertPatchApplies(t *testing.T, baseline string, patch []byte) {
	t.Helper()
	patchPath := filepath.Join(t.TempDir(), "change.diff")
	if err := os.WriteFile(patchPath, patch, 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("git", "apply", "--check", patchPath)
	command.Dir = baseline
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git apply --check = %v: %s\n%s", err, output, patch)
	}
}

func TestCopyTreeRemovesPrivateHomePathsFromModelWorkspace(t *testing.T) {
	source := t.TempDir()
	destination := filepath.Join(t.TempDir(), "model-workspace")
	content := "private=/Users/alice/company/code.go\nlinux=/home/bob/private.txt\n"
	writePatchFixture(t, source, "internal/privacy.txt", content)

	if err := copyTree(source, destination); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(destination, "internal", "privacy.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if privateHomePathPattern.Match(got) || strings.Contains(string(got), "alice") || strings.Contains(string(got), "bob") ||
		strings.Count(string(got), string(redactedHomePath)) != 2 {
		t.Fatalf("model workspace retained private home path: %q", got)
	}
	original, err := os.ReadFile(filepath.Join(source, "internal", "privacy.txt"))
	if err != nil || string(original) != content {
		t.Fatalf("immutable source was changed: %q err=%v", original, err)
	}
}

func TestDerivePatchRejectsChangesOutsideWritableBoundary(t *testing.T) {
	baseline := t.TempDir()
	repository := t.TempDir()
	for _, root := range []string{baseline, repository} {
		writePatchFixture(t, root, "internal/domain/task.go", "package domain\n")
		writePatchFixture(t, root, "README.md", "before\n")
	}
	writePatchFixture(t, repository, "README.md", "after\n")

	if _, err := derivePatch(baseline, repository, []string{"internal/domain/task.go"}, 1<<20); err == nil || !strings.Contains(err.Error(), "outside writable boundary") {
		t.Fatalf("derivePatch() error = %v", err)
	}
}

func TestDerivePatchRejectsEmptyChange(t *testing.T) {
	baseline := t.TempDir()
	repository := t.TempDir()
	for _, root := range []string{baseline, repository} {
		writePatchFixture(t, root, "internal/domain/task.go", "package domain\n")
	}
	if _, err := derivePatch(baseline, repository, []string{"internal/domain/task.go"}, 1<<20); err == nil || !strings.Contains(err.Error(), "no workspace changes") {
		t.Fatalf("derivePatch() error = %v", err)
	}
}

func writePatchFixture(t *testing.T, root, relative, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
