package localweb

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
	"github.com/Yangyang96/chora/internal/gitsource"
	"github.com/Yangyang96/chora/internal/pidiscovery"
	storecontract "github.com/Yangyang96/chora/internal/store"
	"github.com/Yangyang96/chora/internal/taskdelivery"
)

func TestResourceTerminalHandleExitFreezesOneMultiRepositoryResultGroup(t *testing.T) {
	result := runResourceTerminalScenario(t, resourceTerminalScenario{
		name: "two changed repositories", repositoryCount: 2, checkMode: "none", completeAssistant: true,
		change: func(t *testing.T, root string, resources []domain.TaskRepositoryResource) {
			for index, resource := range resources {
				writeResourceTerminalFile(t, filepath.Join(root, resource.WorkspaceDirectory()), "same.txt", []byte("changed in repository "+resource.RepoID+"\n"))
				if index == 1 {
					writeResourceTerminalFile(t, filepath.Join(root, resource.WorkspaceDirectory()), "new.txt", []byte("new text in second repository\n"))
				}
			}
		},
		wantRunState: domain.RunStateAwaitingReview,
	})
	if result.group.Outcome != "review_ready" || len(result.group.Repositories) != 2 {
		t.Fatalf("resource Result group=%#v", result.group)
	}
	for index, repository := range result.group.Repositories {
		if !slices.Contains(repository.ChangedPaths, "same.txt") || repository.PatchLocator == "" || repository.PatchDigest == strings.Repeat("0", 64) {
			t.Fatalf("repository Patch evidence=%#v", repository)
		}
		if index == 1 && !slices.Contains(repository.ChangedPaths, "new.txt") {
			t.Fatalf("second repository omitted new text file: %#v", repository)
		}
	}
	var persisted resourceResultView
	requestJSON(t, result.server.Handler(), http.MethodGet, "/api/v2/runs/"+result.run.ID+"/result", nil, http.StatusOK, &persisted)
	if persisted.Group.ID != result.group.ID || len(persisted.Patches) != 2 || persisted.Patches[0].RepoID == persisted.Patches[1].RepoID {
		t.Fatalf("persisted multi-repository Result=%#v", persisted)
	}
	if len(persisted.Repositories) != 2 {
		t.Fatal("missing frozen repository display metadata")
	}
	for index, identity := range persisted.Repositories {
		if identity.RepoID != persisted.Group.Repositories[index].RepoID || identity.Name == "" || identity.BaseRef == "" {
			t.Fatalf("invalid frozen display metadata: %#v", identity)
		}
	}
	for repoID, checkout := range result.originals {
		content, err := os.ReadFile(filepath.Join(checkout, "same.txt"))
		if err != nil || string(content) != "original "+repoID+"\n" {
			t.Fatalf("original checkout %s changed: %q err=%v", repoID, content, err)
		}
		if _, err := os.Stat(filepath.Join(checkout, "new.txt")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("new result file reached original checkout %s: %v", repoID, err)
		}
	}
	assertNoScalarLocalReviewResult(t, result)
}

func TestResourceTerminalHandleExitCompletesNoChangeWithoutInventingChecks(t *testing.T) {
	for _, mode := range []string{"none", "auto"} {
		t.Run(mode, func(t *testing.T) {
			result := runResourceTerminalScenario(t, resourceTerminalScenario{
				name: mode + " no change", repositoryCount: 2, checkMode: mode, completeAssistant: true,
				wantRunState: domain.RunStateCompleted,
			})
			if result.group.Outcome != string(execution.TerminalCompletedNoChange) || len(result.group.Repositories) != 2 {
				t.Fatalf("no-change Result group=%#v", result.group)
			}
			emptyDigest := sha256.Sum256(nil)
			for _, repository := range result.group.Repositories {
				if len(repository.ChangedPaths) != 0 || repository.PatchLocator != "" || repository.PatchDigest != strings.ToLower(strings.TrimSpace(hexDigest(emptyDigest))) {
					t.Fatalf("no-change repository evidence=%#v", repository)
				}
				var checks app.ResourceCheckDerivation
				if err := json.Unmarshal(repository.Checks, &checks); err != nil {
					t.Fatal(err)
				}
				if checks.RepositoryID != repository.RepoID || checks.Mode != mode || checks.Status != execution.CheckUnknown || checks.FinalContentVerified || len(checks.Checks) != 0 {
					t.Fatalf("no-change check truth=%#v", checks)
				}
			}
			if mode == "auto" {
				for _, resource := range result.resources {
					if resource.Checks.SelectionSource != "committed_configuration" || len(resource.Checks.Commands) != 0 {
						t.Fatalf("automatic check discovery authority=%#v", resource.Checks)
					}
				}
			}
			assertNoScalarLocalReviewResult(t, result)
		})
	}
}

func TestResourceTerminalHandleExitRequiresCompleteFinalAssistant(t *testing.T) {
	result := runResourceTerminalScenario(t, resourceTerminalScenario{
		name: "missing final assistant", repositoryCount: 1, checkMode: "none", completeAssistant: false,
		wantRunState: domain.RunStateRecoveryRequired,
	})
	if !errors.Is(result.groupErr, storecontract.ErrNotFound) {
		t.Fatalf("missing final assistant persisted a Result group: %#v err=%v", result.group, result.groupErr)
	}
	if result.run.AttemptDetail == nil || result.run.AttemptDetail.State != domain.AttemptStateFailed || resourceTerminalFailureReason(t, result) != "result_contract_invalid" {
		t.Fatalf("missing final assistant did not fail closed: %#v", result.run)
	}
	assertNoScalarLocalReviewResult(t, result)
}

func TestResourceTerminalHandleExitDistinguishesNamedCheckFailureAndMissing(t *testing.T) {
	tests := []struct {
		name           string
		emitCheck      bool
		exitCode       int64
		wantOutcome    string
		wantStatus     execution.CheckStatus
		wantObserved   execution.CheckStatus
		wantToolCallID string
	}{
		{name: "actual failure", emitCheck: true, exitCode: 7, wantOutcome: string(execution.TerminalChecksFailed), wantStatus: execution.CheckFail, wantObserved: execution.CheckFail, wantToolCallID: "named-check"},
		{name: "missing", wantOutcome: string(execution.TerminalChecksIncomplete), wantStatus: execution.CheckUnknown, wantObserved: execution.CheckUnknown},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := runResourceTerminalScenario(t, resourceTerminalScenario{
				name: test.name, repositoryCount: 1, checkMode: "named", completeAssistant: true,
				emitNamedCheck: test.emitCheck, namedExitCode: test.exitCode, wantRunState: domain.RunStateRecoveryRequired,
			})
			if result.groupErr != nil || result.group.Outcome != test.wantOutcome || len(result.group.Repositories) != 1 {
				t.Fatalf("named check Result group=%#v err=%v", result.group, result.groupErr)
			}
			var checks app.ResourceCheckDerivation
			if err := json.Unmarshal(result.group.Repositories[0].Checks, &checks); err != nil {
				t.Fatal(err)
			}
			if checks.Status != test.wantStatus || checks.FinalContentVerified || len(checks.Checks) != 1 || checks.Checks[0].ObservedStatus != test.wantObserved || checks.Checks[0].Status != test.wantStatus || checks.Checks[0].ToolCallID != test.wantToolCallID {
				t.Fatalf("named check derivation=%#v", checks)
			}
			if test.emitCheck {
				if checks.Checks[0].ExitCode == nil || *checks.Checks[0].ExitCode != test.exitCode {
					t.Fatalf("actual check exit code was not preserved: %#v", checks.Checks[0])
				}
			} else if checks.Checks[0].ExitCode != nil {
				t.Fatalf("missing check invented an exit code: %#v", checks.Checks[0])
			}
			assertNoScalarLocalReviewResult(t, result)
		})
	}
}

type resourceTerminalScenario struct {
	recordedSession   bool
	futureDelivery    bool
	name              string
	repositoryCount   int
	referenceLast     bool
	checkMode         string
	completeAssistant bool
	omitAutoSelection bool
	emitNamedCheck    bool
	namedExitCode     int64
	change            func(*testing.T, string, []domain.TaskRepositoryResource)
	wantRunState      domain.RunState
}

type resourceTerminalScenarioResult struct {
	server    *Server
	run       runView
	attemptID domain.AttemptID
	group     domain.ResourceResultGroup
	groupErr  error
	resources []domain.TaskRepositoryResource
	originals map[string]string
}

func runResourceTerminalScenario(t *testing.T, scenario resourceTerminalScenario) resourceTerminalScenarioResult {
	t.Helper()
	server, runtime, _ := newResourceTerminalIntegrationServer(t)
	server.deliveryCommandsEnabled = scenario.futureDelivery
	handler := server.Handler()
	requestJSONWithHeaders(t, handler, http.MethodPost, "/api/agent-execution/trusted-local-acknowledgements",
		map[string]any{"policyVersion": domain.TrustedLocalDisclosurePolicy}, map[string]string{"Idempotency-Key": "terminal-ack-" + scenario.name}, http.StatusOK, nil)

	var project resourceProjectTestView
	requestJSON(t, handler, http.MethodPost, "/api/v2/projects", map[string]any{"name": "Terminal " + scenario.name}, http.StatusCreated, &project)
	selections := make([]taskResourceSelection, 0, scenario.repositoryCount)
	originals := make(map[string]string, scenario.repositoryCount)
	for index := 0; index < scenario.repositoryCount; index++ {
		checkout, _, _ := newTaskWorktreeTestRepository(t)
		var added struct {
			Repository repositoryResourceView `json:"repository"`
		}
		requestJSON(t, handler, http.MethodPost, "/api/v2/projects/"+project.ID+"/repositories", map[string]any{"locator": checkout}, http.StatusOK, &added)
		originalContent := "original " + added.Repository.RepoID + "\n"
		writeResourceTerminalFile(t, checkout, "same.txt", []byte(originalContent))
		runTaskWorktreeGitTest(t, checkout, "add", "same.txt")
		runTaskWorktreeGitTest(t, checkout, "-c", "user.name=Chora Test", "-c", "user.email=chora@example.test", "commit", "-m", "repository identity content")
		originals[added.Repository.RepoID] = checkout
		policy := domain.TaskCheckPolicy{Mode: scenario.checkMode, SelectionSource: "user"}
		if scenario.checkMode == "named" {
			var definitions struct {
				Checks []domain.TaskCheckCommand `json:"checks"`
			}
			requestJSON(t, handler, http.MethodPut, "/api/v2/projects/"+project.ID+"/repositories/"+added.Repository.RepoID+"/checks", map[string]any{
				"checks": []map[string]any{{"id": "same-file", "name": "Same file exists", "version": 0, "command": "test -f same.txt", "workingDirectory": "."}},
			}, http.StatusOK, &definitions)
			if len(definitions.Checks) != 1 || definitions.Checks[0].Version != 1 {
				t.Fatalf("named check definition=%#v", definitions.Checks)
			}
			policy.Commands = definitions.Checks
		}
		role := "write"
		if scenario.referenceLast && index == scenario.repositoryCount-1 {
			role = "reference"
		}
		selections = append(selections, taskResourceSelection{
			RepoID: added.Repository.RepoID, AssociationVersion: added.Repository.Version, Role: role, TargetRef: runTaskWorktreeGitTest(t, checkout, "symbolic-ref", "HEAD"),
			Scope: domain.TaskRepositoryScope{Mode: "repository", MigrationChoice: "not_needed"}, Checks: policy,
		})
	}

	roomID, err := domain.ParseRoomID(project.DefaultRoomID)
	if err != nil {
		t.Fatal(err)
	}
	revisions, err := server.store.Reader().ListRoomRevisions(context.Background(), roomID)
	if err != nil {
		t.Fatal(err)
	}
	revisionIDs := make([]string, 0, len(revisions))
	for _, revision := range revisions {
		revisionIDs = append(revisionIDs, revision.ID().String())
	}
	var task taskRefView
	requestJSONWithHeaders(t, handler, http.MethodPost, "/api/v2/rooms/"+project.DefaultRoomID+"/tasks", map[string]any{
		"title": "Terminal " + scenario.name, "requirement": "Produce truthful repository-bound terminal evidence.",
		"agentExecutionProfile": string(domain.AgentExecutionProfileTrustedLocal), "resources": selections, "revisionIds": revisionIDs,
	}, map[string]string{"Idempotency-Key": "terminal-task-" + scenario.name}, http.StatusCreated, &task)
	if task.ResourceSnapshot == nil || len(task.ResourceSnapshot.Resources) != scenario.repositoryCount {
		t.Fatalf("Task resource snapshot=%#v", task.ResourceSnapshot)
	}
	acceptTaskPlan(t, handler, task.ID)

	var started runView
	requestJSONWithHeaders(t, handler, http.MethodPost, "/api/tasks/"+task.ID+"/runs", map[string]any{},
		map[string]string{"Idempotency-Key": "terminal-run-" + scenario.name}, http.StatusCreated, &started)
	if started.Status != domain.RunStateRunning || started.AttemptDetail == nil || started.AttemptDetail.Runtime == nil {
		t.Fatalf("resource terminal Run did not start: %#v", started)
	}
	_, invocation := runtime.snapshot()
	if scenario.change != nil {
		scenario.change(t, invocation.WorkingRoot(), task.ResourceSnapshot.Resources)
	}
	runtime.fakeSupervisor.mu.Lock()
	runtime.fakeSupervisor.streams[execution.StreamStdout] = resourceTerminalPiStream(task.ResourceSnapshot.Resources, scenario)
	runtime.fakeSupervisor.streams[execution.StreamStderr] = nil
	runtime.fakeSupervisor.mu.Unlock()
	if err := runtime.Notify(execution.StreamStdout); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Exit(); err != nil {
		t.Fatal(err)
	}
	finished := waitForRunState(t, handler, started.ID, string(scenario.wantRunState))

	runID, err := domain.ParseRunID(started.ID)
	if err != nil {
		t.Fatal(err)
	}
	attempts, err := server.store.Reader().ListAttemptsForRun(context.Background(), runID)
	if err != nil || len(attempts) != 1 {
		t.Fatalf("terminal attempts=%#v err=%v", attempts, err)
	}
	group, groupErr := server.store.Reader().GetResourceResultGroup(context.Background(), attempts[0].ID())
	return resourceTerminalScenarioResult{
		server: server, run: finished, attemptID: attempts[0].ID(), group: group, groupErr: groupErr,
		resources: append([]domain.TaskRepositoryResource(nil), task.ResourceSnapshot.Resources...), originals: originals,
	}
}

func newResourceTerminalIntegrationServer(t *testing.T) (*Server, *taskResourceLifecycleSupervisor, string) {
	t.Helper()
	server, runtime, dataRoot := newTaskResourceLifecycleServer(t)
	workspaces, err := newTaskResourceWorkspaceManager(server.store, dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	patches, err := newResourcePatchStore(filepath.Join(dataRoot, "resource-patches"), server.store.Reader(), workspaces)
	if err != nil {
		t.Fatal(err)
	}
	source := gitsource.Default{}
	server.service = app.NewService(app.Dependencies{
		RoomCreationMode: app.RoomCreationProjectOnly, Lifecycle: server.ctx, Store: server.store, Context: app.ContextAssembler{},
		Agents: server.registry, Supervisor: server.supervisor, Presence: localPresence{}, Authorizer: localAuthorizer{}, IDs: app.RandomIDs{},
		TaskResourceWorkspaces: workspaces, ResourcePatchMaterializer: patches,
		ResourceReviewVerifier: newResourceReviewVerifier(workspaces),
		TaskDeliveryWorkspaces: newTaskDeliveryWorkspaceResolver(workspaces), TaskDeliveryGit: taskdelivery.Git{},
		SpecCodingEnvelopeResolver: newBoundSpecCodingEnvelopeResolver(server.store.Reader(), nil, pidiscovery.Result{State: pidiscovery.StateReady, Version: "0.84.2"}),
		RepositorySource:           source, DataRoot: dataRoot,
	})
	return server, runtime, dataRoot
}

func resourceTerminalPiStream(resources []domain.TaskRepositoryResource, scenario resourceTerminalScenario) []byte {
	lines := make([]string, 0, 4)
	if scenario.recordedSession {
		lines = append(lines, `{"id":"chora-get-state","type":"response","command":"get_state","success":true,"data":{"sessionId":"123e4567-e89b-12d3-a456-426614174000"}}`)
	}
	if scenario.emitNamedCheck && len(resources) == 1 {
		lines = append(lines,
			`{"type":"tool_execution_start","toolName":"bash","toolCallId":"named-check","args":{"command":"cd `+resources[0].WorkspaceDirectory()+` && test -f same.txt"}}`,
			`{"type":"tool_execution_end","toolName":"bash","toolCallId":"named-check","exitCode":`+jsonNumber(scenario.namedExitCode)+`,"isError":false}`,
		)
	}
	if scenario.completeAssistant {
		text := "Resource result is ready."
		if scenario.checkMode == "auto" && !scenario.omitAutoSelection {
			selected := []map[string]any{}
			for _, resource := range resources {
				selected = append(selected, map[string]any{"repoId": resource.RepoID, "checks": []any{}, "noApplicableChecks": true, "explanation": "This documentation-only fixture has no applicable build or test entry."})
			}
			raw, _ := json.Marshal(map[string]any{"repositories": selected})
			text += "\n```chora-check-selection\n" + string(raw) + "\n```"
		}
		raw, _ := json.Marshal(map[string]any{"type": "message_end", "message": map[string]any{"role": "assistant", "content": []map[string]any{{"type": "text", "text": text}}, "stopReason": "stop"}})
		lines = append(lines, string(raw))
	}
	lines = append(lines, `{"type":"agent_settled"}`)
	return []byte(strings.Join(lines, "\n") + "\n")
}

func resourceTerminalFailureReason(t *testing.T, result resourceTerminalScenarioResult) string {
	t.Helper()
	runID, err := domain.ParseRunID(result.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	events, err := result.server.store.Reader().ListRunEvents(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	for index := len(events) - 1; index >= 0; index-- {
		if events[index].Type() != "attempt.failed" {
			continue
		}
		var payload struct {
			FailureReason string `json:"failure_reason"`
		}
		if json.Unmarshal(events[index].NormalizedJSON(), &payload) == nil {
			return payload.FailureReason
		}
	}
	return ""
}

func assertNoScalarLocalReviewResult(t *testing.T, result resourceTerminalScenarioResult) {
	t.Helper()
	if _, err := result.server.store.Reader().GetLocalReviewResultForAgentAttempt(context.Background(), result.attemptID); !errors.Is(err, storecontract.ErrNotFound) {
		t.Fatalf("v2 terminal wrote legacy scalar LocalReviewResult: %v", err)
	}
}

func writeResourceTerminalFile(t *testing.T, root, relative string, content []byte) {
	t.Helper()
	path := filepath.Join(root, relative)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
}

func jsonNumber(value int64) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}

func hexDigest(value [32]byte) string {
	const alphabet = "0123456789abcdef"
	encoded := make([]byte, len(value)*2)
	for index, b := range value {
		encoded[index*2] = alphabet[b>>4]
		encoded[index*2+1] = alphabet[b&15]
	}
	return string(encoded)
}

func TestResourceTerminalAutoWithoutSelectionExplanationIsIncomplete(t *testing.T) {
	result := runResourceTerminalScenario(t, resourceTerminalScenario{name: "auto missing explanation", repositoryCount: 1, checkMode: "auto", completeAssistant: true, omitAutoSelection: true, wantRunState: domain.RunStateRecoveryRequired})
	if result.group.Outcome != string(execution.TerminalChecksIncomplete) {
		t.Fatalf("missing auto explanation completed: %#v", result.group)
	}
}

func TestMultiRepositoryReviewRetryPreservesWorkAndRejectsUnprovenWorkspace(t *testing.T) {
	for _, tamper := range []bool{false, true} {
		t.Run(fmt.Sprint("tamper=", tamper), func(t *testing.T) {
			var taskRoot string
			result := runResourceTerminalScenario(t, resourceTerminalScenario{futureDelivery: true, recordedSession: true, name: "multi-repo-retry", repositoryCount: 2, checkMode: "none", completeAssistant: true, wantRunState: domain.RunStateAwaitingReview,
				change: func(t *testing.T, root string, resources []domain.TaskRepositoryResource) {
					taskRoot = root
					for _, r := range resources {
						writeResourceTerminalFile(t, filepath.Join(root, r.WorkspaceDirectory()), "same.txt", []byte(r.RepoID+" retained change\n"))
					}
				},
			})
			h := result.server.Handler()
			var rejected runView
			requestJSONWithHeaders(t, h, http.MethodPost, "/api/v2/runs/"+result.run.ID+"/review", map[string]any{"expectedVersion": result.run.Version, "kind": "reject", "comment": "Rerun final checks without losing either repository.", "resultDigest": result.run.ResourceResult.Digest}, map[string]string{"Idempotency-Key": "multi-retry-review"}, 200, &rejected)
			if tamper {
				runTaskWorktreeGitTest(t, filepath.Join(taskRoot, result.resources[1].WorkspaceDirectory()), "symbolic-ref", "HEAD", "refs/heads/unrelated")
			}
			status := http.StatusOK
			if tamper {
				status = http.StatusConflict
			}
			var retried runView
			requestJSONWithHeaders(t, h, http.MethodPost, "/api/runs/"+result.run.ID+"/retry", map[string]any{"expectedVersion": rejected.Version, "instructions": "Keep both repository edits and rerun checks."}, map[string]string{"Idempotency-Key": "multi-retry"}, status, &retried)
			runID, _ := domain.ParseRunID(result.run.ID)
			attempts, err := result.server.store.Reader().ListAttemptsForRun(context.Background(), runID)
			wantAttempts := 2
			if tamper {
				wantAttempts = 1
			}
			if err != nil || len(attempts) != wantAttempts {
				t.Fatalf("attempts=%d want=%d err=%v", len(attempts), wantAttempts, err)
			}
			if !tamper && (retried.Status != domain.RunStateRunning || retried.Attempt != 2) {
				t.Fatalf("retry did not start successor: %+v", retried)
			}
			for _, r := range result.resources {
				got, err := os.ReadFile(filepath.Join(taskRoot, r.WorkspaceDirectory(), "same.txt"))
				if err != nil || string(got) != r.RepoID+" retained change\n" {
					t.Fatalf("repository %s lost edits: %q %v", r.RepoID, got, err)
				}
				if strings.TrimSpace(runTaskWorktreeGitTest(t, result.originals[r.RepoID], "rev-parse", "HEAD")) != r.BaseCommit {
					t.Fatal("retry changed original main")
				}
			}
			old, err := result.server.store.Reader().GetResourceResultGroup(context.Background(), result.attemptID)
			oldJSON, _ := json.Marshal(old)
			wantJSON, _ := json.Marshal(result.group)
			if err != nil || string(oldJSON) != string(wantJSON) {
				t.Fatalf("predecessor result changed: %v", err)
			}
		})
	}
}
