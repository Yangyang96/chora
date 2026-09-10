package taskdelivery

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	testHead  = "1111111111111111111111111111111111111111"
	testBase  = "2222222222222222222222222222222222222222"
	newBase   = "3333333333333333333333333333333333333333"
	testMerge = "4444444444444444444444444444444444444444"
)

type ghStep struct {
	args []string
	out  string
	exit int
}

func mockGitHub(t *testing.T, steps ...ghStep) *githubCLI {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "gh")
	source := `#!/bin/sh
set -eu
d="$MOCK_GH_DIR"
n=$(cat "$d/count")
printf '%s\n' "$@" > "$d/$n.args"
next=$((n + 1))
printf '%s' "$next" > "$d/count"
if test -f "$d/$n.delay"; then sleep "$(cat "$d/$n.delay")" & echo $! > "$d/$n.pid"; wait; fi
if test -f "$d/$n.out"; then cat "$d/$n.out"; fi
if test -f "$d/remove-after" && test "$next" = "$(cat "$d/remove-after")"; then rm -f "$0"; fi
if test -f "$d/$n.exit"; then exit "$(cat "$d/$n.exit")"; fi
`
	if err := os.WriteFile(script, []byte(source), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "count"), []byte("0"), 0600); err != nil {
		t.Fatal(err)
	}
	for i, step := range steps {
		if err := os.WriteFile(filepath.Join(dir, strconv.Itoa(i)+".out"), []byte(step.out), 0600); err != nil {
			t.Fatal(err)
		}
		if step.exit != 0 {
			if err := os.WriteFile(filepath.Join(dir, strconv.Itoa(i)+".exit"), []byte(strconv.Itoa(step.exit)), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	g := &githubCLI{path: script, available: true, env: []string{"MOCK_GH_DIR=" + dir}}
	t.Cleanup(func() {
		raw, err := os.ReadFile(filepath.Join(dir, "count"))
		if err != nil {
			t.Error(err)
			return
		}
		count, _ := strconv.Atoi(string(raw))
		if count != len(steps) {
			t.Errorf("gh calls = %d, want %d", count, len(steps))
		}
		checked := count
		if checked > len(steps) {
			checked = len(steps)
		}
		for i, step := range steps[:checked] {
			raw, err := os.ReadFile(filepath.Join(dir, strconv.Itoa(i)+".args"))
			if err != nil {
				t.Error(err)
				continue
			}
			got := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
			if !reflect.DeepEqual(got, step.args) {
				t.Errorf("gh call %d args = %#v, want %#v", i, got, step.args)
			}
		}
	})
	return g
}

func mockGitHubDir(t *testing.T, g *githubCLI) string {
	t.Helper()
	const prefix = "MOCK_GH_DIR="
	if len(g.env) != 1 || !strings.HasPrefix(g.env[0], prefix) {
		t.Fatalf("unexpected mock environment %#v", g.env)
	}
	return strings.TrimPrefix(g.env[0], prefix)
}

func removeMockGitHubExecutableAfter(t *testing.T, g *githubCLI, calls int) {
	t.Helper()
	dir := mockGitHubDir(t, g)
	if err := os.WriteFile(filepath.Join(dir, "remove-after"), []byte(strconv.Itoa(calls)), 0600); err != nil {
		t.Fatal(err)
	}
}

func delayMockGitHubCall(t *testing.T, g *githubCLI, call int) {
	t.Helper()
	dir := mockGitHubDir(t, g)
	if err := os.WriteFile(filepath.Join(dir, strconv.Itoa(call)+".delay"), []byte("10"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		raw, err := os.ReadFile(filepath.Join(dir, strconv.Itoa(call)+".pid"))
		if err != nil {
			return
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
		if err != nil {
			return
		}
		if child, err := os.FindProcess(pid); err == nil {
			_ = child.Kill()
		}
	})
}

func waitForMockGitHubCalls(t *testing.T, g *githubCLI, want int) {
	t.Helper()
	countFile := filepath.Join(mockGitHubDir(t, g), "count")
	deadline := time.Now().Add(10 * time.Second)
	for {
		raw, err := os.ReadFile(countFile)
		if err == nil {
			count, parseErr := strconv.Atoi(string(raw))
			if parseErr == nil && count >= want {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("gh calls did not reach %d", want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func pushFixture() PushPreview {
	return PushPreview{
		Binding: Binding{Branch: "chora/task/repo", TargetRef: "refs/heads/main"},
		URL:     "https://github.com/acme/widget.git", RemoteRef: "refs/heads/chora/task/repo",
		Head: testHead, TargetHead: testBase,
	}
}

func markerFixture(t *testing.T) string {
	t.Helper()
	m, err := githubMarker("operation-1")
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func prJSON(state, base, marker string, number int) string {
	mergedAt, mergeCommit := "null", "null"
	if state == "MERGED" {
		mergedAt = `"2026-09-08T00:00:00Z"`
		mergeCommit = fmt.Sprintf(`{"oid":%q}`, testMerge)
	}
	return fmt.Sprintf(`{"number":%d,"url":%q,"state":%q,"body":%q,"headRefName":"chora/task/repo","headRefOid":%q,"baseRefName":"main","baseRefOid":%q,"isCrossRepository":false,"mergedAt":%s,"mergeCommit":%s}`,
		number, fmt.Sprintf("https://github.com/acme/widget/pull/%d", number), state, marker, testHead, base, mergedAt, mergeCommit)
}

func TestGitHubPreviewAndConfirmCreateExactIdentity(t *testing.T) {
	m := markerFixture(t)
	refHead := []string{"api", "repos/acme/widget/commits/chora%2Ftask%2Frepo", "--jq", ".sha"}
	refBase := []string{"api", "repos/acme/widget/commits/main", "--jq", ".sha"}
	list := []string{"pr", "list", "-R", "acme/widget", "--state", "all", "--head", "chora/task/repo", "--base", "main", "--limit", "100", "--json", "number,url,state,body,headRefName,headRefOid,baseRefName,baseRefOid,isCrossRepository,mergedAt,mergeCommit"}
	g := mockGitHub(t,
		ghStep{refHead, testHead + "\n", 0}, ghStep{refBase, testBase + "\n", 0}, ghStep{list, "[]", 0},
		ghStep{refHead, testHead + "\n", 0}, ghStep{refBase, testBase + "\n", 0}, ghStep{list, "[]", 0},
		ghStep{[]string{"pr", "create", "-R", "acme/widget", "--head", "chora/task/repo", "--base", "main", "--title", "Ship it", "--body-file", "-"}, "https://github.com/acme/widget/pull/7\n", 0},
		ghStep{list, "[" + prJSON("OPEN", testBase, m, 7) + "]", 0},
	)
	p, err := g.PreviewPR(context.Background(), pushFixture(), "Ship it", "Body", "operation-1")
	if err != nil {
		t.Fatal(err)
	}
	if p.Repository != "acme/widget" || p.Marker != m || p.Digest == "" {
		t.Fatalf("unexpected preview: %#v", p)
	}
	pr, err := g.Confirm(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if pr.Number != 7 || pr.State != "open" || pr.Head != testHead {
		t.Fatalf("unexpected PR: %#v", pr)
	}
}

func TestGitHubConfirmReconcilesUncertainCreateByMarker(t *testing.T) {
	m := markerFixture(t)
	refHead := []string{"api", "repos/acme/widget/commits/chora%2Ftask%2Frepo", "--jq", ".sha"}
	refBase := []string{"api", "repos/acme/widget/commits/main", "--jq", ".sha"}
	list := []string{"pr", "list", "-R", "acme/widget", "--state", "all", "--head", "chora/task/repo", "--base", "main", "--limit", "100", "--json", "number,url,state,body,headRefName,headRefOid,baseRefName,baseRefOid,isCrossRepository,mergedAt,mergeCommit"}
	g := mockGitHub(t, ghStep{refHead, testHead + "\n", 0}, ghStep{refBase, testBase + "\n", 0}, ghStep{list, "[" + prJSON("OPEN", testBase, m, 9) + "]", 0})
	p := HostingPreview{Kind: "pr", Repository: "acme/widget", HeadBranch: "chora/task/repo", BaseBranch: "main", Head: testHead, BaseHead: testBase, Title: "T", Marker: m, MergeMethod: "merge"}
	p.Digest = hostingDigest(p)
	pr, err := g.Confirm(context.Background(), p)
	if err != nil || pr.Number != 9 {
		t.Fatalf("reconcile = %#v, %v", pr, err)
	}
}

func TestGitHubConfirmPreflightRecoveryProvesNoMutation(t *testing.T) {
	m := markerFixture(t)
	t.Run("pull request", func(t *testing.T) {
		refHead := []string{"api", "repos/acme/widget/commits/chora%2Ftask%2Frepo", "--jq", ".sha"}
		g := mockGitHub(t, ghStep{refHead, "provider-secret", 2})
		p := HostingPreview{Kind: "pr", Repository: "acme/widget", HeadBranch: "chora/task/repo", BaseBranch: "main", Head: testHead, BaseHead: testBase, Title: "T", Marker: m, MergeMethod: "merge"}
		p.Digest = hostingDigest(p)
		_, err := g.Confirm(context.Background(), p)
		if !errors.Is(err, ErrHostingMutationNotStarted) || errors.Is(err, ErrRecovery) || strings.Contains(err.Error(), "provider-secret") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("merge", func(t *testing.T) {
		view := []string{"pr", "view", "12", "-R", "acme/widget", "--json", "number,url,state,body,headRefName,headRefOid,baseRefName,baseRefOid,isCrossRepository,mergedAt,mergeCommit"}
		g := mockGitHub(t, ghStep{view, "provider-secret", 2})
		p := HostingPreview{Kind: "merge", Repository: "acme/widget", HeadBranch: "chora/task/repo", BaseBranch: "main", Head: testHead, BaseHead: testBase, Title: "T", Marker: m, MergeMethod: "merge", Number: 12}
		p.Digest = hostingDigest(p)
		_, err := g.Confirm(context.Background(), p)
		if !errors.Is(err, ErrHostingMutationNotStarted) || errors.Is(err, ErrRecovery) || strings.Contains(err.Error(), "provider-secret") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("merge malformed read", func(t *testing.T) {
		view := []string{"pr", "view", "12", "-R", "acme/widget", "--json", "number,url,state,body,headRefName,headRefOid,baseRefName,baseRefOid,isCrossRepository,mergedAt,mergeCommit"}
		g := mockGitHub(t, ghStep{view, "not-json", 0})
		p := HostingPreview{Kind: "merge", Repository: "acme/widget", HeadBranch: "chora/task/repo", BaseBranch: "main", Head: testHead, BaseHead: testBase, Title: "T", Marker: m, MergeMethod: "merge", Number: 12}
		p.Digest = hostingDigest(p)
		_, err := g.Confirm(context.Background(), p)
		if !errors.Is(err, ErrHostingMutationNotStarted) || errors.Is(err, ErrRecovery) {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestGitHubAmbiguityClassificationDependsOnMutationStart(t *testing.T) {
	m := markerFixture(t)
	refHead := []string{"api", "repos/acme/widget/commits/chora%2Ftask%2Frepo", "--jq", ".sha"}
	refBase := []string{"api", "repos/acme/widget/commits/main", "--jq", ".sha"}
	list := []string{"pr", "list", "-R", "acme/widget", "--state", "all", "--head", "chora/task/repo", "--base", "main", "--limit", "100", "--json", "number,url,state,body,headRefName,headRefOid,baseRefName,baseRefOid,isCrossRepository,mergedAt,mergeCommit"}
	ambiguous := "[" + prJSON("OPEN", testBase, m, 1) + "," + prJSON("CLOSED", newBase, m, 2) + "]"
	p := HostingPreview{Kind: "pr", Repository: "acme/widget", HeadBranch: "chora/task/repo", BaseBranch: "main", Head: testHead, BaseHead: testBase, Title: "T", Marker: m, MergeMethod: "merge"}
	p.Digest = hostingDigest(p)
	for _, afterStart := range []bool{false, true} {
		t.Run(fmt.Sprintf("after mutation start=%t", afterStart), func(t *testing.T) {
			steps := []ghStep{{refHead, testHead + "\n", 0}, {refBase, testBase + "\n", 0}}
			if afterStart {
				steps = append(steps, ghStep{list, "[]", 0}, ghStep{[]string{"pr", "create", "-R", p.Repository, "--head", p.HeadBranch, "--base", p.BaseBranch, "--title", p.Title, "--body-file", "-"}, "", 0})
			}
			steps = append(steps, ghStep{list, ambiguous, 0})
			g := mockGitHub(t, steps...)
			_, err := g.Confirm(context.Background(), p)
			if errors.Is(err, ErrRecovery) != afterStart || errors.Is(err, ErrHostingMutationNotStarted) == afterStart {
				t.Fatalf("after mutation start=%t: %v", afterStart, err)
			}
		})
	}
}

func TestGitHubConfirmMutationStartFailureProvesNoMutation(t *testing.T) {
	m := markerFixture(t)
	refHead := []string{"api", "repos/acme/widget/commits/chora%2Ftask%2Frepo", "--jq", ".sha"}
	refBase := []string{"api", "repos/acme/widget/commits/main", "--jq", ".sha"}
	list := []string{"pr", "list", "-R", "acme/widget", "--state", "all", "--head", "chora/task/repo", "--base", "main", "--limit", "100", "--json", "number,url,state,body,headRefName,headRefOid,baseRefName,baseRefOid,isCrossRepository,mergedAt,mergeCommit"}
	t.Run("pull request", func(t *testing.T) {
		g := mockGitHub(t, ghStep{refHead, testHead + "\n", 0}, ghStep{refBase, testBase + "\n", 0}, ghStep{list, "[]", 0})
		removeMockGitHubExecutableAfter(t, g, 3)
		p := HostingPreview{Kind: "pr", Repository: "acme/widget", HeadBranch: "chora/task/repo", BaseBranch: "main", Head: testHead, BaseHead: testBase, Title: "T", Marker: m, MergeMethod: "merge"}
		p.Digest = hostingDigest(p)
		_, err := g.Confirm(context.Background(), p)
		if !errors.Is(err, ErrHostingMutationNotStarted) || errors.Is(err, ErrRecovery) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("merge", func(t *testing.T) {
		view := []string{"pr", "view", "12", "-R", "acme/widget", "--json", "number,url,state,body,headRefName,headRefOid,baseRefName,baseRefOid,isCrossRepository,mergedAt,mergeCommit"}
		g := mockGitHub(t, ghStep{view, prJSON("OPEN", testBase, m, 12), 0})
		removeMockGitHubExecutableAfter(t, g, 1)
		p := HostingPreview{Kind: "merge", Repository: "acme/widget", HeadBranch: "chora/task/repo", BaseBranch: "main", Head: testHead, BaseHead: testBase, Title: "T", Marker: m, MergeMethod: "merge", Number: 12}
		p.Digest = hostingDigest(p)
		_, err := g.Confirm(context.Background(), p)
		if !errors.Is(err, ErrHostingMutationNotStarted) || errors.Is(err, ErrRecovery) {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestGitHubObserveFailsClosedOnAmbiguityAndMismatch(t *testing.T) {
	m := markerFixture(t)
	list := []string{"pr", "list", "-R", "acme/widget", "--state", "all", "--head", "chora/task/repo", "--base", "main", "--limit", "100", "--json", "number,url,state,body,headRefName,headRefOid,baseRefName,baseRefOid,isCrossRepository,mergedAt,mergeCommit"}
	p := HostingPreview{Kind: "pr", Repository: "acme/widget", HeadBranch: "chora/task/repo", BaseBranch: "main", Head: testHead, BaseHead: testBase, Title: "T", Marker: m, MergeMethod: "merge"}
	p.Digest = hostingDigest(p)
	t.Run("bounded history", func(t *testing.T) {
		records := make([]string, 100)
		for i := range records {
			records[i] = prJSON("CLOSED", testBase, m, i+1)
		}
		g := mockGitHub(t, ghStep{list, "[" + strings.Join(records, ",") + "]", 0})
		if _, err := g.Observe(context.Background(), p); !errors.Is(err, ErrRecovery) {
			t.Fatalf("truncated history: %v", err)
		}
	})
	t.Run("ambiguous", func(t *testing.T) {
		g := mockGitHub(t, ghStep{list, "[" + prJSON("OPEN", testBase, m, 1) + "," + prJSON("CLOSED", newBase, m, 2) + "]", 0})
		_, err := g.Observe(context.Background(), p)
		if !errors.Is(err, ErrRecovery) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("head mismatch", func(t *testing.T) {
		bad := strings.Replace(prJSON("OPEN", testBase, m, 1), testHead, testBase, 1)
		g := mockGitHub(t, ghStep{list, "[" + bad + "]", 0})
		_, err := g.Observe(context.Background(), p)
		if !errors.Is(err, ErrConflict) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("closed with advanced base", func(t *testing.T) {
		g := mockGitHub(t, ghStep{list, "[" + prJSON("CLOSED", newBase, m, 4) + "]", 0})
		pr, err := g.Observe(context.Background(), p)
		if err != nil || pr.State != "closed" || pr.BaseHead != newBase {
			t.Fatalf("observe = %#v, %v", pr, err)
		}
	})
}

func TestGitHubMergeUsesConditionalMergeAndObservesMergedDeletedHead(t *testing.T) {
	m := markerFixture(t)
	view := []string{"pr", "view", "12", "-R", "acme/widget", "--json", "number,url,state,body,headRefName,headRefOid,baseRefName,baseRefOid,isCrossRepository,mergedAt,mergeCommit"}
	created := HostingPreview{Kind: "pr", Repository: "acme/widget", HeadBranch: "chora/task/repo", BaseBranch: "main", Head: testHead, BaseHead: testBase, Title: "T", Marker: m, MergeMethod: "merge"}
	created.Digest = hostingDigest(created)
	g := mockGitHub(t,
		ghStep{view, prJSON("OPEN", testBase, m, 12), 0},
		ghStep{view, prJSON("OPEN", testBase, m, 12), 0},
		ghStep{[]string{"api", "--method", "PUT", "repos/acme/widget/pulls/12/merge", "-f", "sha=" + testHead, "-f", "merge_method=merge"}, `{"merged":true,"sha":"` + testMerge + `"}`, 0},
		ghStep{view, prJSON("MERGED", newBase, m, 12), 0},
	)
	merge, err := g.PreviewMerge(context.Background(), created, 12)
	if err != nil {
		t.Fatal(err)
	}
	if merge.BaseHead != testBase || merge.MergeMethod != "merge" {
		t.Fatalf("unexpected merge preview: %#v", merge)
	}
	pr, err := g.Confirm(context.Background(), merge)
	if err != nil {
		t.Fatal(err)
	}
	if pr.State != "merged" || pr.MergeCommit != testMerge {
		t.Fatalf("unexpected merge result: %#v", pr)
	}

	// Original creation authority still observes a merged PR by marker even when
	// the source ref is gone and the target has advanced.
	list := []string{"pr", "list", "-R", "acme/widget", "--state", "all", "--head", "chora/task/repo", "--base", "main", "--limit", "100", "--json", "number,url,state,body,headRefName,headRefOid,baseRefName,baseRefOid,isCrossRepository,mergedAt,mergeCommit"}
	g2 := mockGitHub(t, ghStep{list, "[" + prJSON("MERGED", newBase, m, 12) + "]", 0})
	observed, err := g2.Observe(context.Background(), created)
	if err != nil || observed.State != "merged" {
		t.Fatalf("observe = %#v, %v", observed, err)
	}
}

func TestGitHubProviderMergeFailureRequiresRecoveryWithoutBypass(t *testing.T) {
	m := markerFixture(t)
	view := []string{"pr", "view", "12", "-R", "acme/widget", "--json", "number,url,state,body,headRefName,headRefOid,baseRefName,baseRefOid,isCrossRepository,mergedAt,mergeCommit"}
	g := mockGitHub(t, ghStep{view, prJSON("OPEN", testBase, m, 12), 0}, ghStep{[]string{"api", "--method", "PUT", "repos/acme/widget/pulls/12/merge", "-f", "sha=" + testHead, "-f", "merge_method=merge"}, "provider denied", 1})
	p := HostingPreview{Kind: "merge", Repository: "acme/widget", HeadBranch: "chora/task/repo", BaseBranch: "main", Head: testHead, BaseHead: testBase, Title: "T", Marker: m, MergeMethod: "merge", Number: 12}
	p.Digest = hostingDigest(p)
	_, err := g.Confirm(context.Background(), p)
	if !errors.Is(err, ErrRecovery) || strings.Contains(err.Error(), "provider denied") {
		t.Fatalf("error = %v", err)
	}
}

func TestGitHubPostStartFailuresRemainRecovery(t *testing.T) {
	m := markerFixture(t)
	refHead := []string{"api", "repos/acme/widget/commits/chora%2Ftask%2Frepo", "--jq", ".sha"}
	refBase := []string{"api", "repos/acme/widget/commits/main", "--jq", ".sha"}
	list := []string{"pr", "list", "-R", "acme/widget", "--state", "all", "--head", "chora/task/repo", "--base", "main", "--limit", "100", "--json", "number,url,state,body,headRefName,headRefOid,baseRefName,baseRefOid,isCrossRepository,mergedAt,mergeCommit"}
	t.Run("pull request process failure", func(t *testing.T) {
		create := []string{"pr", "create", "-R", "acme/widget", "--head", "chora/task/repo", "--base", "main", "--title", "T", "--body-file", "-"}
		g := mockGitHub(t,
			ghStep{refHead, testHead + "\n", 0}, ghStep{refBase, testBase + "\n", 0}, ghStep{list, "[]", 0},
			ghStep{create, "provider-secret", 2},
		)
		p := HostingPreview{Kind: "pr", Repository: "acme/widget", HeadBranch: "chora/task/repo", BaseBranch: "main", Head: testHead, BaseHead: testBase, Title: "T", Marker: m, MergeMethod: "merge"}
		p.Digest = hostingDigest(p)
		_, err := g.Confirm(context.Background(), p)
		if !errors.Is(err, ErrRecovery) || errors.Is(err, ErrHostingMutationNotStarted) || strings.Contains(err.Error(), "provider-secret") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("merge malformed response", func(t *testing.T) {
		view := []string{"pr", "view", "12", "-R", "acme/widget", "--json", "number,url,state,body,headRefName,headRefOid,baseRefName,baseRefOid,isCrossRepository,mergedAt,mergeCommit"}
		merge := []string{"api", "--method", "PUT", "repos/acme/widget/pulls/12/merge", "-f", "sha=" + testHead, "-f", "merge_method=merge"}
		g := mockGitHub(t, ghStep{view, prJSON("OPEN", testBase, m, 12), 0}, ghStep{merge, "not-json", 0})
		p := HostingPreview{Kind: "merge", Repository: "acme/widget", HeadBranch: "chora/task/repo", BaseBranch: "main", Head: testHead, BaseHead: testBase, Title: "T", Marker: m, MergeMethod: "merge", Number: 12}
		p.Digest = hostingDigest(p)
		_, err := g.Confirm(context.Background(), p)
		if !errors.Is(err, ErrRecovery) || errors.Is(err, ErrHostingMutationNotStarted) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("merge cancellation after mutation start", func(t *testing.T) {
		view := []string{"pr", "view", "12", "-R", "acme/widget", "--json", "number,url,state,body,headRefName,headRefOid,baseRefName,baseRefOid,isCrossRepository,mergedAt,mergeCommit"}
		merge := []string{"api", "--method", "PUT", "repos/acme/widget/pulls/12/merge", "-f", "sha=" + testHead, "-f", "merge_method=merge"}
		g := mockGitHub(t, ghStep{view, prJSON("OPEN", testBase, m, 12), 0}, ghStep{merge, "", 0})
		delayMockGitHubCall(t, g, 1)
		p := HostingPreview{Kind: "merge", Repository: "acme/widget", HeadBranch: "chora/task/repo", BaseBranch: "main", Head: testHead, BaseHead: testBase, Title: "T", Marker: m, MergeMethod: "merge", Number: 12}
		p.Digest = hostingDigest(p)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		result := make(chan error, 1)
		go func() {
			_, err := g.Confirm(ctx, p)
			result <- err
		}()
		waitForMockGitHubCalls(t, g, 2)
		start := time.Now()
		cancel()
		var err error
		select {
		case err = <-result:
		case <-time.After(2 * time.Second):
			t.Fatal("cancelled mutation did not stop promptly")
		}
		if !errors.Is(err, ErrRecovery) || errors.Is(err, ErrHostingMutationNotStarted) {
			t.Fatalf("error = %v", err)
		}
		if elapsed := time.Since(start); elapsed > 2*time.Second {
			t.Fatalf("cancelled mutation stayed pending for %s", elapsed)
		}
	})
}

func TestGitHubMergeRejectsChangedBaseBeforeMutation(t *testing.T) {
	m := markerFixture(t)
	view := []string{"pr", "view", "12", "-R", "acme/widget", "--json", "number,url,state,body,headRefName,headRefOid,baseRefName,baseRefOid,isCrossRepository,mergedAt,mergeCommit"}
	g := mockGitHub(t, ghStep{view, prJSON("OPEN", newBase, m, 12), 0})
	p := HostingPreview{Kind: "merge", Repository: "acme/widget", HeadBranch: "chora/task/repo", BaseBranch: "main", Head: testHead, BaseHead: testBase, Title: "T", Marker: m, MergeMethod: "merge", Number: 12}
	p.Digest = hostingDigest(p)
	_, err := g.Confirm(context.Background(), p)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("error = %v", err)
	}
}

func TestGitHubPostMergeIdentityMismatchStaysRecovery(t *testing.T) {
	m := markerFixture(t)
	view := []string{"pr", "view", "12", "-R", "acme/widget", "--json", "number,url,state,body,headRefName,headRefOid,baseRefName,baseRefOid,isCrossRepository,mergedAt,mergeCommit"}
	badAfter := strings.Replace(prJSON("MERGED", newBase, m, 12), testHead, testBase, 1)
	g := mockGitHub(t,
		ghStep{view, prJSON("OPEN", testBase, m, 12), 0},
		ghStep{[]string{"api", "--method", "PUT", "repos/acme/widget/pulls/12/merge", "-f", "sha=" + testHead, "-f", "merge_method=merge"}, `{"merged":true,"sha":"` + testMerge + `"}`, 0},
		ghStep{view, badAfter, 0},
	)
	p := HostingPreview{Kind: "merge", Repository: "acme/widget", HeadBranch: "chora/task/repo", BaseBranch: "main", Head: testHead, BaseHead: testBase, Title: "T", Marker: m, MergeMethod: "merge", Number: 12}
	p.Digest = hostingDigest(p)
	_, err := g.Confirm(context.Background(), p)
	if !errors.Is(err, ErrRecovery) {
		t.Fatalf("error = %v", err)
	}
}

func TestGitHubRepositoryRejectsOtherHostsAndCredentials(t *testing.T) {
	for _, raw := range []string{"https://gitlab.com/a/b.git", "https://token@github.com/a/b.git", "https://github.com:8443/a/b.git", "https://github.com:443/a/b.git", "https://github.com/a/b/c.git"} {
		if _, err := githubRepository(raw); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("githubRepository(%q) error = %v", raw, err)
		}
	}
}
