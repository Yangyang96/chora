package localweb

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
)

func TestCommitMessageModelPreservesNativeSelection(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "settings.json"), []byte(`{"defaultProvider":"chosen","defaultModel":"chosen-model"}`), 0600); err != nil {
		t.Fatal(err)
	}
	provider, model, err := commitMessageModel(home, []string{"another", "chosen"})
	if err != nil || provider != "chosen" || model != "chosen-model" {
		t.Fatalf("wrong model: %s %s %v", provider, model, err)
	}
	if _, _, err := commitMessageModel(home, []string{"another"}); err == nil {
		t.Fatal("silently switched from unavailable native provider")
	}
}

func TestCommitMessageParsingRequiresCompleteAssistant(t *testing.T) {
	frame := func(reason, message string) string {
		raw, _ := json.Marshal(map[string]any{"type": "message_end", "message": map[string]any{"role": "assistant", "stopReason": reason, "provider": "local", "model": "native", "content": []map[string]string{{"type": "text", "text": message}}}})
		return string(raw) + "\n"
	}
	valid := "feat(calculator): add multiplication\n\nCover positive, negative and zero operands."
	got, err := parseCommitMessage([]byte(frame("stop", valid)))
	if err != nil || got.Message != valid || got.Provider != "local" || got.Model != "native" {
		t.Fatalf("got=%#v err=%v", got, err)
	}
	for _, raw := range []string{
		frame("length", valid), frame("error", valid), frame("stop", ""), frame("stop", "```\n"+valid+"\n```"), frame("stop", strings.Repeat("x", 8193)), frame("stop", "bad\x00message"),
		frame("stop", valid) + frame("error", "failed"), "not JSON", `{"type":"message_update","text":"partial"}`,
	} {
		if _, err := parseCommitMessage([]byte(raw)); err == nil {
			t.Fatalf("accepted incomplete/invalid response %.100s", raw)
		}
	}
}

func TestCommitMessageUsesOnlyAcceptedRepositoryDiffWithoutGitMutations(t *testing.T) {
	var root string
	result := runResourceTerminalScenario(t, resourceTerminalScenario{futureDelivery: true, name: "commit-message", repositoryCount: 2, checkMode: "none", completeAssistant: true, wantRunState: domain.RunStateAwaitingReview,
		change: func(t *testing.T, taskRoot string, resources []domain.TaskRepositoryResource) {
			root = taskRoot
			for i, resource := range resources {
				writeResourceTerminalFile(t, filepath.Join(root, resource.WorkspaceDirectory()), "same.txt", []byte(fmt.Sprintf("changed repository %d\n", i)))
			}
		},
	})
	var prompts []string
	result.server.commitMessages.generate = func(_ context.Context, input string) (commitMessageSuggestion, error) {
		prompts = append(prompts, input)
		return commitMessageSuggestion{Message: "fix: correct repository content\n\nUpdate the reviewed repository content.", Provider: "test", Model: "test"}, nil
	}
	handler := result.server.Handler()
	run := result.run
	prefix := "/api/v2/runs/" + run.ID
	resource := result.resources[0]
	request := map[string]any{"repoId": resource.RepoID, "expectedVersion": run.Version, "resultDigest": run.ResourceResult.Digest, "language": "en"}
	requestJSON(t, handler, http.MethodPost, prefix+"/delivery/message", request, 409, nil)
	if len(prompts) != 0 {
		t.Fatal("generated before acceptance")
	}
	var accepted runView
	requestJSONWithHeaders(t, handler, http.MethodPost, prefix+"/review", map[string]any{"expectedVersion": run.Version, "kind": "accept", "comment": "reviewed", "resultDigest": run.ResourceResult.Digest}, map[string]string{"Idempotency-Key": "accept-for-message"}, 200, &accepted)
	request["expectedVersion"] = accepted.Version
	worktree := filepath.Join(root, resource.WorkspaceDirectory())
	beforeHead := runTaskWorktreeGitTest(t, worktree, "rev-parse", "HEAD")
	beforeIndex := runTaskWorktreeGitTest(t, worktree, "ls-files", "--stage")
	beforeStatus := runTaskWorktreeGitTest(t, worktree, "status", "--porcelain=v1")
	var suggestion commitMessageSuggestion
	requestJSON(t, handler, http.MethodPost, prefix+"/delivery/message", request, 200, &suggestion)
	requestJSON(t, handler, http.MethodPost, prefix+"/delivery/message", request, 200, &suggestion)
	if len(prompts) != 1 || suggestion.Message != "fix: correct repository content\n\nUpdate the reviewed repository content." {
		t.Fatalf("cache/suggestion mismatch: %d %#v", len(prompts), suggestion)
	}
	var input struct {
		app.CommitMessageContext
		Language string `json:"language"`
	}
	if err := json.Unmarshal([]byte(prompts[0]), &input); err != nil {
		t.Fatal(err)
	}
	if input.Repository != resource.Name || input.TaskTitle == "" || input.Language != "en" || !strings.Contains(input.Patch, "+changed repository 0") || strings.Contains(input.Patch, "changed repository 1") {
		t.Fatalf("incorrect generation context: %#v", input)
	}
	request["resultDigest"] = strings.Repeat("0", 64)
	requestJSON(t, handler, http.MethodPost, prefix+"/delivery/message", request, 409, nil)
	if len(prompts) != 1 {
		t.Fatal("wrong digest invoked provider or bypassed authority via cache")
	}
	if beforeHead != runTaskWorktreeGitTest(t, worktree, "rev-parse", "HEAD") || beforeIndex != runTaskWorktreeGitTest(t, worktree, "ls-files", "--stage") || beforeStatus != runTaskWorktreeGitTest(t, worktree, "status", "--porcelain=v1") {
		t.Fatal("generation changed Git state")
	}
	var delivery app.TaskDeliveryView
	requestJSON(t, handler, http.MethodGet, prefix+"/delivery", nil, 200, &delivery)
	for _, repository := range delivery.Repositories {
		if repository.Status != "uncommitted" || len(repository.Operations) != 0 {
			t.Fatalf("generation created delivery operation: %#v", repository)
		}
	}
}
