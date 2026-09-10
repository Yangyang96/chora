package localweb

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"unicode"

	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
)

func TestDeliveryDraftRequiresSubstantiveCommitAndCompletePR(t *testing.T) {
	for _, v := range []commitMessageSuggestion{{Message: "fix: title only"}, {Message: "fix: title\n\n "}, {Title: "PR title", Body: ""}, {Title: "first\nsecond", Body: "description"}} {
		kind := "commit"
		if v.Title != "" {
			kind = "pr"
		}
		if validateDeliverySuggestion(kind, v) == nil {
			t.Fatalf("accepted incomplete draft: %#v", v)
		}
	}
	for kind, v := range map[string]commitMessageSuggestion{"commit": {Message: "fix: handle empty input\n\nReturn an explicit error before parsing an empty request."}, "pr": {Title: "Handle empty input", Body: "## Changes\nReject empty requests.\n\n## Verification\nNot run."}} {
		raw, _ := json.Marshal(map[string]any{"message": v.Message, "title": v.Title, "body": v.Body})
		frame, _ := json.Marshal(map[string]any{"type": "message_end", "message": map[string]any{"role": "assistant", "stopReason": "stop", "provider": "native", "model": "selected", "content": []map[string]string{{"type": "text", "text": string(raw)}}}})
		got, err := parseDeliveryDraft(frame)
		if err != nil || validateDeliverySuggestion(kind, got) != nil {
			t.Fatalf("%s: %#v %v", kind, got, err)
		}
	}
}
func TestDeliveryDraftCoalescesAutomaticRequestsAndRegeneratesExplicitAttempts(t *testing.T) {
	server := &Server{}
	var calls atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	server.commitMessages.generate = func(ctx context.Context, input string) (commitMessageSuggestion, error) {
		if calls.Add(1) == 1 {
			close(started)
			<-release
		}
		return commitMessageSuggestion{Message: "fix: handle requests\n\nPreserve each request's reviewed content.", Provider: "test", Model: "test"}, nil
	}
	key := sha256.Sum256([]byte("same evidence"))
	var wg sync.WaitGroup
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := server.cachedDeliveryDraft(context.Background(), key, "auto", false, "input", "commit"); err != nil {
				t.Error(err)
			}
		}()
	}
	<-started
	close(release)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("automatic calls=%d", calls.Load())
	}
	newKey := sha256.Sum256([]byte("explicit attempt"))
	for range 2 {
		if _, err := server.cachedDeliveryDraft(context.Background(), newKey, "attempt-1", true, "input", "commit"); err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("explicit retry repeated provider call: %d", calls.Load())
	}
	if _, err := server.cachedDeliveryDraft(context.Background(), key, "attempt-1", true, "changed input", "commit"); err == nil {
		t.Fatal("reused attempt identity with different input")
	}
}
func TestDeliveryDraftPRUsesExactPushedRangeAndCheckEvidence(t *testing.T) {
	var root string
	result := runResourceTerminalScenario(t, resourceTerminalScenario{futureDelivery: true, name: "draft pr", repositoryCount: 1, checkMode: "none", completeAssistant: true, wantRunState: domain.RunStateAwaitingReview, change: func(t *testing.T, r string, rs []domain.TaskRepositoryResource) {
		root = r
		writeResourceTerminalFile(t, filepath.Join(r, rs[0].WorkspaceDirectory()), "same.txt", []byte("complete PR change\n"))
	}})
	host := &deliveryTestHosting{}
	installDeliveryTestService(t, result, root, host)
	repo := result.resources[0]
	original := result.originals[repo.RepoID]
	runTaskWorktreeGitTest(t, original, "config", "user.name", "Test")
	runTaskWorktreeGitTest(t, original, "config", "user.email", "test@example.test")
	bare := filepath.Join(t.TempDir(), "remote.git")
	runTaskWorktreeGitTest(t, original, "init", "--bare", bare)
	runTaskWorktreeGitTest(t, original, "remote", "add", "origin", bare)
	runTaskWorktreeGitTest(t, original, "push", "origin", repo.BaseRef+":"+repo.BaseRef)
	handler := result.server.Handler()
	prefix := "/api/v2/runs/" + result.run.ID
	var accepted runView
	requestJSONWithHeaders(t, handler, http.MethodPost, prefix+"/review", map[string]any{"expectedVersion": result.run.Version, "kind": "accept", "resultDigest": result.run.ResourceResult.Digest}, map[string]string{"Idempotency-Key": "draft-review"}, 200, &accepted)
	common := map[string]any{"repoId": repo.RepoID, "expectedVersion": accepted.Version, "resultDigest": result.run.ResourceResult.Digest}
	for _, kind := range []string{"commit", "push"} {
		common["kind"] = kind
		common["message"] = "fix: update reviewed content\n\nDescribe the complete change."
		common["remote"] = "origin"
		var p app.DeliveryOperationView
		requestJSONWithHeaders(t, handler, http.MethodPost, prefix+"/delivery/preview", common, map[string]string{"Idempotency-Key": "preview-" + kind}, 200, &p)
		requestJSONWithHeaders(t, handler, http.MethodPost, prefix+"/delivery/confirm", map[string]any{"operationId": p.ID, "expectedVersion": accepted.Version, "resultDigest": result.run.ResourceResult.Digest}, map[string]string{"Idempotency-Key": "confirm-" + kind}, 200, nil)
	}
	var captured struct {
		app.DeliveryDraftContext
		Language string `json:"language"`
	}
	result.server.commitMessages.generate = func(_ context.Context, input string) (commitMessageSuggestion, error) {
		if err := json.Unmarshal([]byte(input), &captured); err != nil {
			t.Fatal(err)
		}
		return commitMessageSuggestion{Title: "Update reviewed content", Body: "## Changes\nUpdate the repository content.\n\n## Verification\nNot run: no checks were selected.", Provider: "test", Model: "test"}, nil
	}
	input := map[string]any{"repoId": repo.RepoID, "expectedVersion": accepted.Version, "resultDigest": result.run.ResourceResult.Digest, "kind": "pr", "language": "zh-CN"}
	var ctx deliveryDraftContextView
	requestJSON(t, handler, http.MethodPost, prefix+"/delivery/draft-context", input, 200, &ctx)
	input["fingerprint"] = ctx.Fingerprint
	var suggestion commitMessageSuggestion
	requestJSON(t, handler, http.MethodPost, prefix+"/delivery/message", input, 200, &suggestion)
	if captured.Language != "en" || captured.Kind != "pr" || !strings.Contains(captured.Patch, "+complete PR change") || !strings.Contains(string(captured.Checks), "none") || captured.Source.Head == captured.Source.Base || captured.TaskGoal == "" {
		t.Fatalf("incorrect context: %#v", captured)
	}
	if suggestion.Fingerprint != ctx.Fingerprint || suggestion.Body == "" || host.writes != 0 {
		t.Fatalf("draft missing or wrote PR: %#v writes=%d", suggestion, host.writes)
	}
	input["fingerprint"] = strings.Repeat("0", 64)
	requestJSON(t, handler, http.MethodPost, prefix+"/delivery/message", input, 409, nil)
}

// Opt-in smoke test: only a synthetic diff is sent to the user's configured
// native provider. No project tools, sessions, repository mutations or secrets.
func TestDeliveryDraftNativeModelEnglishSmoke(t *testing.T) {
	if os.Getenv("CHORA_DELIVERY_LIVE_MODEL") != "1" {
		t.Skip("opt-in native text-only model smoke test")
	}
	server := &Server{pathPiEnabled: true}
	results := map[string]commitMessageSuggestion{}
	for _, kind := range []string{"commit", "pr"} {
		input := map[string]any{
			"kind": kind, "language": "en", "selectedTemplate": "", "taskTitle": "拒绝空白 token", "taskGoal": "修复只含空格的 token 仍被接受的问题", "repository": "synthetic-auth",
			"reviewedDiff": "diff --git a/auth.ts b/auth.ts\n--- a/auth.ts\n+++ b/auth.ts\n@@ -1,3 +1,3 @@\n export function validToken(token: string) {\n-  return token.length > 0\n+  return token.trim().length > 0\n }\n",
			"checks":       map[string]any{"Mode": "none", "Status": "UNKNOWN", "Checks": []any{}, "Explanation": "Not run: no checks were selected."},
			"source":       map[string]any{"conventions": map[string]string{}, "templates": map[string]string{}, "recentCommits": "fix(auth): reject invalid tokens", "warnings": []string{}},
		}
		data, _ := json.Marshal(input)
		result, err := server.generatePiCommitMessage(context.Background(), string(data))
		if err != nil {
			t.Fatalf("native %s generation: %v", kind, err)
		}
		if err := validateDeliverySuggestion(kind, result); err != nil {
			t.Fatal(err)
		}
		text := result.Message + result.Title + result.Body
		if strings.ContainsFunc(text, func(r rune) bool { return unicode.Is(unicode.Han, r) }) {
			t.Fatalf("native %s did not default to English", kind)
		}
		if !strings.Contains(strings.ToLower(text), "whitespace") && !strings.Contains(strings.ToLower(text), "trim") {
			t.Fatalf("native %s omitted concrete change", kind)
		}
		if kind == "commit" {
			_, body, found := strings.Cut(result.Message, "\n\n")
			if !found || len(strings.Fields(body)) > 45 {
				t.Fatalf("native commit did not produce a brief body: %q", result.Message)
			}
			bullets := strings.Split(strings.TrimSpace(body), "\n")
			if len(bullets) > 3 {
				t.Fatalf("native commit produced too many bullets: %q", body)
			}
			for _, bullet := range bullets {
				if !strings.HasPrefix(bullet, "- ") {
					t.Fatalf("native commit used prose instead of short bullets: %q", body)
				}
			}
		}
		if kind == "pr" && (!strings.HasPrefix(result.Title, "fix") || strings.Contains(result.Body, "## ") || len(strings.Fields(result.Body)) > 60) {
			t.Fatalf("native PR ignored concise default/title style: %#v", result)
		}
		results[kind] = result
		input["feedback"] = "简化成一句话"
		input["current"] = result
		// Explicit revision must win even over a selected multi-section template.
		input["selectedTemplate"] = "change.md"
		input["source"] = map[string]any{"conventions": map[string]string{}, "templates": map[string]string{"change.md": "## 背景\n\n## 具体修改\n\n## 验证\n"}, "recentCommits": "fix(auth): reject invalid tokens"}
		data, _ = json.Marshal(input)
		revision, err := server.generatePiCommitMessage(context.Background(), string(data))
		if err != nil || validateDeliverySuggestion(kind, revision) != nil {
			t.Fatalf("native %s revision: %#v %v", kind, revision, err)
		}
		body := revision.Body
		if kind == "commit" {
			before, _, _ := strings.Cut(result.Message, "\n\n")
			after, revisedBody, _ := strings.Cut(revision.Message, "\n\n")
			if before != after {
				t.Fatalf("revision unexpectedly changed commit title: %q", revision.Message)
			}
			body = revisedBody
		} else if revision.Title != result.Title {
			t.Fatalf("revision unexpectedly changed PR title: %q", revision.Title)
		}
		if strings.ContainsAny(body, "\n#") || strings.HasPrefix(body, "- ") || len(strings.Fields(body)) > 45 || len(regexp.MustCompile(`[.!?](?:\s|$)`).FindAllString(body, -1)) != 1 {
			t.Fatalf("native %s did not obey one-sentence revision: %q", kind, body)
		}
		if strings.ContainsFunc(body, func(r rune) bool { return unicode.Is(unicode.Han, r) }) || (!strings.Contains(strings.ToLower(body), "whitespace") && !strings.Contains(strings.ToLower(body), "trim")) {
			t.Fatalf("native %s revision lost English/concrete change: %q", kind, body)
		}
		results[kind+"_one_sentence"] = revision
	}
	if output := os.Getenv("CHORA_DELIVERY_MODEL_EVIDENCE"); output != "" {
		raw, _ := json.MarshalIndent(results, "", "  ")
		if err := os.WriteFile(output, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
