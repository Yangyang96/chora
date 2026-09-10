package localweb

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

func TestResourceApplyIntegrationReviewAndRecovery(t *testing.T) {
	t.Run("review requires the exact multi-repository result digest", func(t *testing.T) {
		fixture := newResourceApplyIntegrationFixture(t, 2, 1)
		target := newRecordingResourceTarget()
		service := fixture.service(target)

		wrong := fixture.runs[0].digest
		wrong = wrong[:len(wrong)-1] + "0"
		if wrong == fixture.runs[0].digest {
			wrong = wrong[:len(wrong)-1] + "1"
		}
		_, err := service.ReviewResourceResult(context.Background(), fixture.runs[0].reviewRequest("wrong-digest", wrong))
		if !errors.Is(err, app.ErrReviewEvidenceUnavailable) {
			t.Fatalf("wrong digest review error = %v", err)
		}
		stored, err := fixture.server.store.Reader().GetRun(context.Background(), fixture.runs[0].run.ID())
		if err != nil || stored.State() != domain.RunStateAwaitingReview || stored.Version() != fixture.runs[0].run.Version() {
			t.Fatalf("wrong digest changed Run: state=%s version=%d err=%v", stored.State(), stored.Version(), err)
		}

		accepted := fixture.accept(t, service, 0)
		if accepted.State() != domain.RunStateAccepted || accepted.Version() != fixture.runs[0].run.Version()+1 {
			t.Fatalf("accepted Run = state %s version %d", accepted.State(), accepted.Version())
		}
		view, err := service.LoadResourceReview(context.Background(), accepted.ID())
		if err != nil || view.Digest != fixture.runs[0].digest || len(view.Group.Repositories) != 2 || len(view.Patches) != 2 {
			t.Fatalf("grouped review = %#v err=%v", view, err)
		}
	})

	t.Run("all repositories preflight before the first write", func(t *testing.T) {
		fixture := newResourceApplyIntegrationFixture(t, 2, 1)
		target := newRecordingResourceTarget()
		target.conflict[fixture.resources[1].RepoID] = true
		service := fixture.service(target)
		accepted := fixture.accept(t, service, 0)

		view, err := service.ApplyResourceResult(context.Background(), fixture.runs[0].applyRequest("preflight", accepted.Version()))
		if err != nil {
			t.Fatal(err)
		}
		if view.Status != "conflict" || target.totalApplies() != 0 {
			t.Fatalf("preflight view=%#v apply calls=%d", view, target.totalApplies())
		}
		if target.inspectCount(fixture.resources[0].RepoID) != 1 || target.inspectCount(fixture.resources[1].RepoID) != 1 {
			t.Fatalf("preflight did not inspect the complete vector: %#v", target.inspectCalls)
		}
	})

	t.Run("write interruption is partial and retry reconciles without replay", func(t *testing.T) {
		fixture := newResourceApplyIntegrationFixture(t, 2, 1)
		target := newRecordingResourceTarget()
		second := fixture.resources[1].RepoID
		target.blockRepo = second
		target.applyEntered = make(chan struct{})
		target.releaseApply = make(chan struct{})
		target.failAfterWrite[second] = true
		service := fixture.service(target)
		accepted := fixture.accept(t, service, 0)

		type outcome struct {
			view app.ResourceApplyView
			err  error
		}
		result := make(chan outcome, 1)
		go func() {
			view, err := service.ApplyResourceResult(context.Background(), fixture.runs[0].applyRequest("interrupt", accepted.Version()))
			result <- outcome{view: view, err: err}
		}()
		select {
		case <-target.applyEntered:
		case <-time.After(5 * time.Second):
			t.Fatal("second repository Apply was not reached")
		}
		writing, err := service.LoadResourceApply(context.Background(), fixture.runs[0].resultID)
		if err != nil || repositoryApplyStatus(writing, second) != "writing" {
			t.Fatalf("durable pre-write marker = %#v err=%v", writing, err)
		}
		close(target.releaseApply)
		first := <-result
		if first.err != nil || first.view.Status != "partial" || repositoryApplyStatus(first.view, second) != "uncertain" {
			t.Fatalf("interrupted Apply = %#v err=%v", first.view, first.err)
		}
		firstRepo := fixture.resources[0].RepoID
		beforeFirstInspect, beforeFirstApply := target.inspectCount(firstRepo), target.applyCount(firstRepo)
		beforeSecondApply := target.applyCount(second)

		retried, err := service.ApplyResourceResult(context.Background(), fixture.runs[0].applyRequest("reconcile", accepted.Version()))
		if err != nil || retried.Status != "applied" {
			t.Fatalf("reconciled Apply = %#v err=%v", retried, err)
		}
		if target.inspectCount(firstRepo) != beforeFirstInspect || target.applyCount(firstRepo) != beforeFirstApply {
			t.Fatalf("retry replayed applied repository: inspect=%d/%d apply=%d/%d", target.inspectCount(firstRepo), beforeFirstInspect, target.applyCount(firstRepo), beforeFirstApply)
		}
		if target.applyCount(second) != beforeSecondApply {
			t.Fatalf("reconciliation replayed the interrupted write: apply=%d/%d", target.applyCount(second), beforeSecondApply)
		}
	})

	t.Run("unknown prior write remains blocked", func(t *testing.T) {
		fixture := newResourceApplyIntegrationFixture(t, 2, 1)
		target := newRecordingResourceTarget()
		second := fixture.resources[1].RepoID
		target.failBeforeWrite[second] = true
		service := fixture.service(target)
		accepted := fixture.accept(t, service, 0)

		first, err := service.ApplyResourceResult(context.Background(), fixture.runs[0].applyRequest("unknown-first", accepted.Version()))
		if err != nil || first.Status != "partial" || repositoryApplyStatus(first, second) != "uncertain" {
			t.Fatalf("first Apply = %#v err=%v", first, err)
		}
		firstRepo := fixture.resources[0].RepoID
		firstApplies, secondApplies := target.applyCount(firstRepo), target.applyCount(second)
		target.inspectFailure[second] = true
		retried, err := service.ApplyResourceResult(context.Background(), fixture.runs[0].applyRequest("unknown-retry", accepted.Version()))
		if err != nil || retried.Status != "partial" || repositoryApplyStatus(retried, second) != "uncertain" {
			t.Fatalf("unknown retry = %#v err=%v", retried, err)
		}
		if target.applyCount(firstRepo) != firstApplies || target.applyCount(second) != secondApplies {
			t.Fatalf("unknown retry performed a write: %#v", target.applyCalls)
		}
	})
}

func TestResourceApplyIntegrationSerializesSharedPhysicalRepository(t *testing.T) {
	originalRoot, _, _ := newTaskWorktreeTestRepository(t)
	firstFixture := newResourceApplyIntegrationFixtureForRoots(t, []string{originalRoot})
	secondFixture := newResourceApplyIntegrationFixtureForRoots(t, []string{originalRoot})
	target := newRecordingResourceTarget()
	target.blockRepo = firstFixture.resources[0].RepoID
	target.applyEntered = make(chan struct{})
	target.releaseApply = make(chan struct{})
	firstService := firstFixture.service(target)
	secondService := secondFixture.service(target)
	acceptedFirst := firstFixture.accept(t, firstService, 0)
	acceptedSecond := secondFixture.accept(t, secondService, 0)
	original := filepath.Join(originalRoot, "README.md")
	before, err := os.ReadFile(original)
	if err != nil {
		t.Fatal(err)
	}

	type outcome struct {
		view app.ResourceApplyView
		err  error
	}
	first := make(chan outcome, 1)
	go func() {
		view, err := firstService.ApplyResourceResult(context.Background(), firstFixture.runs[0].applyRequest("shared-first", acceptedFirst.Version()))
		first <- outcome{view: view, err: err}
	}()
	select {
	case <-target.applyEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("first shared-repository Apply was not reached")
	}
	second := make(chan outcome, 1)
	go func() {
		view, err := secondService.ApplyResourceResult(context.Background(), secondFixture.runs[0].applyRequest("shared-second", acceptedSecond.Version()))
		second <- outcome{view: view, err: err}
	}()
	select {
	case result := <-second:
		t.Fatalf("second Run bypassed the shared physical identity lock: %#v err=%v", result.view, result.err)
	case <-time.After(150 * time.Millisecond):
	}
	if got := target.inspectCount(secondFixture.resources[0].RepoID); got != 0 {
		t.Fatalf("second Run reached target while first held lock: inspect calls=%d", got)
	}
	close(target.releaseApply)
	if result := <-first; result.err != nil || result.view.Status != "applied" {
		t.Fatalf("first shared Apply = %#v err=%v", result.view, result.err)
	}
	if result := <-second; result.err != nil || result.view.Status != "applied" {
		t.Fatalf("second shared Apply = %#v err=%v", result.view, result.err)
	}
	after, err := os.ReadFile(original)
	if err != nil || string(after) != string(before) {
		t.Fatalf("original checkout changed: before=%q after=%q err=%v", before, after, err)
	}
}

type resourceApplyIntegrationRun struct {
	run      domain.AgentRun
	resultID domain.ResultID
	digest   string
}

func (r resourceApplyIntegrationRun) reviewRequest(key, digest string) app.ResourceReviewRequest {
	return app.ResourceReviewRequest{ReviewRequest: app.ReviewRequest{
		CommandMeta: app.CommandMeta{ActorID: "local-human", SessionID: "local-browser", IdempotencyKey: key},
		RunID:       r.run.ID(), ExpectedVersion: r.run.Version(), Kind: domain.ReviewDecisionAccept,
	}, ResultDigest: digest}
}

func (r resourceApplyIntegrationRun) applyRequest(key string, version uint64) app.ResourceApplyRequest {
	return app.ResourceApplyRequest{ApplyAcceptedPatchRequest: app.ApplyAcceptedPatchRequest{
		CommandMeta: app.CommandMeta{ActorID: "local-human", SessionID: "local-browser", IdempotencyKey: key},
		RunID:       r.run.ID(), ExpectedVersion: version,
	}, ResultDigest: r.digest}
}

type resourceApplyIntegrationFixture struct {
	server        *Server
	resources     []domain.TaskRepositoryResource
	originalRoots []string
	runs          []resourceApplyIntegrationRun
	patches       map[string][]byte
}

func newResourceApplyIntegrationFixture(t *testing.T, repositoryCount, taskCount int) *resourceApplyIntegrationFixture {
	return newResourceApplyIntegrationFixtureWithRoots(t, repositoryCount, taskCount, nil)
}

func newResourceApplyIntegrationFixtureForRoots(t *testing.T, roots []string) *resourceApplyIntegrationFixture {
	return newResourceApplyIntegrationFixtureWithRoots(t, len(roots), 1, roots)
}

func newResourceApplyIntegrationFixtureWithRoots(t *testing.T, repositoryCount, taskCount int, suppliedRoots []string, emptyIndexes ...int) *resourceApplyIntegrationFixture {
	t.Helper()
	server, _, _ := newTaskResourceLifecycleServer(t)
	handler := server.Handler()
	requestJSONWithHeaders(t, handler, http.MethodPost, "/api/agent-execution/trusted-local-acknowledgements",
		map[string]any{"policyVersion": domain.TrustedLocalDisclosurePolicy},
		map[string]string{"Idempotency-Key": "resource-apply-ack"}, http.StatusOK, nil)
	var project resourceProjectTestView
	requestJSON(t, handler, http.MethodPost, "/api/v2/projects", map[string]any{"name": "Resource Apply integration"}, http.StatusCreated, &project)

	selections := make([]taskResourceSelection, 0, repositoryCount)
	originalRoots := make([]string, 0, repositoryCount)
	for i := 0; i < repositoryCount; i++ {
		var root string
		if i < len(suppliedRoots) {
			root = suppliedRoots[i]
		} else {
			root, _, _ = newTaskWorktreeTestRepository(t)
		}
		var response struct {
			Repository repositoryResourceView `json:"repository"`
		}
		requestJSON(t, handler, http.MethodPost, "/api/v2/projects/"+project.ID+"/repositories", map[string]any{"locator": root}, http.StatusOK, &response)
		originalRoots = append(originalRoots, root)
		selections = append(selections, taskResourceSelection{
			RepoID: response.Repository.RepoID, AssociationVersion: response.Repository.Version, Role: "write", TargetRef: runTaskWorktreeGitTest(t, root, "symbolic-ref", "HEAD"),
			Scope:  domain.TaskRepositoryScope{Mode: "repository", MigrationChoice: "not_needed"},
			Checks: domain.TaskCheckPolicy{Mode: "auto", SelectionSource: "user"},
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

	fixture := &resourceApplyIntegrationFixture{server: server, originalRoots: originalRoots, patches: map[string][]byte{}}
	for i := 0; i < taskCount; i++ {
		var task taskRefView
		requestJSONWithHeaders(t, handler, http.MethodPost, "/api/v2/rooms/"+project.DefaultRoomID+"/tasks", map[string]any{
			"title": "Resource Apply integration", "requirement": "Produce an exact grouped change.",
			"agentExecutionProfile": string(domain.AgentExecutionProfileTrustedLocal), "resources": selections, "revisionIds": revisionIDs,
		}, map[string]string{"Idempotency-Key": "resource-apply-create-" + string(rune('a'+i))}, http.StatusCreated, &task)
		acceptTaskPlan(t, handler, task.ID)
		var started runView
		requestJSONWithHeaders(t, handler, http.MethodPost, "/api/tasks/"+task.ID+"/runs", map[string]any{},
			map[string]string{"Idempotency-Key": "resource-apply-start-" + string(rune('a'+i))}, http.StatusCreated, &started)
		runID, err := domain.ParseRunID(started.ID)
		if err != nil {
			t.Fatal(err)
		}
		run, err := server.store.Reader().GetRun(context.Background(), runID)
		if err != nil {
			t.Fatal(err)
		}
		seeded, resources := seedResourceApplyResult(t, server, run, fixture.patches, emptyIndexes...)
		fixture.runs = append(fixture.runs, seeded)
		if fixture.resources == nil {
			fixture.resources = resources
		}
	}
	return fixture
}

func seedResourceApplyResult(t *testing.T, server *Server, run domain.AgentRun, patches map[string][]byte, emptyIndexes ...int) (resourceApplyIntegrationRun, []domain.TaskRepositoryResource) {
	t.Helper()
	ctx := context.Background()
	attempt, err := server.store.Reader().GetCurrentAttempt(ctx, run.ID())
	if err != nil {
		t.Fatal(err)
	}
	record, err := server.store.Reader().GetTaskResourceSnapshot(ctx, run.TaskID())
	if err != nil {
		t.Fatal(err)
	}
	var snapshot domain.TaskResourceSnapshot
	if err := json.Unmarshal(record.CanonicalJSON, &snapshot); err != nil {
		t.Fatal(err)
	}
	binding, err := server.store.Reader().GetSpecCodingBinding(ctx, run.TaskID())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if !now.After(run.UpdatedAt()) {
		now = run.UpdatedAt().Add(time.Millisecond)
	}
	nextRun, err := run.Transition(domain.CommandSubmitAgentReportDirectReview, now)
	if err != nil {
		t.Fatal(err)
	}
	nextAttempt, err := attempt.Transition(domain.AttemptEventOutputSubmitted)
	if err != nil {
		t.Fatal(err)
	}
	report, err := domain.NewAgentReport(domain.AgentReportParams{
		ID: domain.NewAgentReportID(), RunID: run.ID(), AttemptID: attempt.ID(), Summary: "Grouped result ready.", FinalText: "Grouped result ready.", CompletedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	var final domain.FinalAssistantEvidence
	err = server.store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		payload, _ := json.Marshal(map[string]any{"text": report.FinalText(), "terminal_complete": true})
		event, err := tx.AppendRunEvent(ctx, run.ID(), storecontract.EventDraft{ID: domain.NewEventID(), Type: "assistant_message", Source: "adapter", OccurredAt: now, RecordedAt: now, NormalizedJSON: payload})
		if err != nil {
			return err
		}
		digest := sha256.Sum256([]byte(report.FinalText()))
		final = domain.FinalAssistantEvidence{EventID: event.ID().String(), Sequence: event.Sequence(), TextDigest: hex.EncodeToString(digest[:]), Complete: true}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	group := domain.ResourceResultGroup{
		SchemaVersion: "chora.result-group.v2", ID: domain.NewResultID().String(), RunID: run.ID().String(), TaskID: run.TaskID().String(),
		AttemptID: attempt.ID().String(), AgentReportID: report.ID().String(), ResourceSnapshotDigest: hex.EncodeToString(record.Digest[:]),
		ContractDigest: hex.EncodeToString(binding.ActiveContractDigest[:]), ContextDigest: hex.EncodeToString(binding.SnapshotDigest[:]),
		Outcome: "review_ready", CreatedAt: now, FinalAssistant: final,
	}
	for index, resource := range snapshot.Resources {
		empty := false
		for _, emptyIndex := range emptyIndexes {
			if index == emptyIndex {
				empty = true
			}
		}
		if empty {
			digest := sha256.Sum256(nil)
			group.Repositories = append(group.Repositories, domain.RepositoryResultChange{RepoID: resource.RepoID, BaseCommit: resource.BaseCommit, BaseTree: resource.BaseTree, PatchDigest: hex.EncodeToString(digest[:]), ChangedPaths: []string{}, Checks: json.RawMessage(`{"mode":"none"}`)})
			continue
		}
		raw := []byte("diff --git a/README.md b/README.md\n--- a/README.md\n+++ b/README.md\n@@ -1 +1 @@\n-initial\n+changed " + resource.RepoID + "\n")
		digest := sha256.Sum256(raw)
		patches[run.ID().String()+"/"+resource.RepoID] = raw
		group.Repositories = append(group.Repositories, domain.RepositoryResultChange{
			RepoID: resource.RepoID, BaseCommit: resource.BaseCommit, BaseTree: resource.BaseTree,
			PatchDigest: hex.EncodeToString(digest[:]), PatchLocator: "patches/" + resource.RepoID + ".diff",
			ChangedPaths: []string{"README.md"}, Checks: json.RawMessage(`{"mode":"none"}`),
		})
	}
	_, digest, err := group.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	err = server.store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		if err := tx.SaveRunCAS(ctx, run.Version(), nextRun); err != nil {
			return err
		}
		if err := tx.SaveAttemptCAS(ctx, attempt.State(), nextAttempt); err != nil {
			return err
		}
		if err := tx.InsertAgentReport(ctx, report); err != nil {
			return err
		}
		return tx.InsertResourceResultGroup(ctx, group)
	})
	if err != nil {
		t.Fatal(err)
	}
	resultID, _ := domain.ParseResultID(group.ID)
	return resourceApplyIntegrationRun{run: nextRun, resultID: resultID, digest: hex.EncodeToString(digest[:])}, snapshot.Resources
}

func (f *resourceApplyIntegrationFixture) service(target app.ResourcePatchApplicationTarget) *app.Service {
	return app.NewService(app.Dependencies{
		Store: f.server.store, Presence: localPresence{}, Authorizer: localAuthorizer{}, IDs: app.RandomIDs{},
		ResourcePatchMaterializer: resourceApplyMaterializer{patches: f.patches}, ResourcePatchTarget: target, ResourceReviewVerifier: syntheticApplyReviewVerifier{},
	})
}

func (f *resourceApplyIntegrationFixture) accept(t *testing.T, service *app.Service, index int) domain.AgentRun {
	t.Helper()
	result, err := service.ReviewResourceResult(context.Background(), f.runs[index].reviewRequest("accept-"+f.runs[index].run.ID().String(), f.runs[index].digest))
	if err != nil {
		t.Fatal(err)
	}
	return result.Run
}

type resourceApplyMaterializer struct{ patches map[string][]byte }

func (m resourceApplyMaterializer) Materialize(context.Context, domain.TaskID, domain.AttemptID, string) ([]app.ResourceReviewPatch, error) {
	return nil, errors.New("unexpected materialization")
}

func (m resourceApplyMaterializer) Read(_ context.Context, attemptID domain.AttemptID, repoID string, digest [32]byte) ([]byte, error) {
	for key, raw := range m.patches {
		if len(key) >= len(repoID) && key[len(key)-len(repoID):] == repoID && sha256.Sum256(raw) == digest {
			return append([]byte(nil), raw...), nil
		}
	}
	return nil, errors.New("patch not found")
}

type recordingResourceTarget struct {
	mu              sync.Mutex
	inspectCalls    map[string]int
	applyCalls      map[string]int
	state           map[string][32]byte
	applied         map[string]bool
	conflict        map[string]bool
	inspectFailure  map[string]bool
	failBeforeWrite map[string]bool
	failAfterWrite  map[string]bool
	blockRepo       string
	applyEntered    chan struct{}
	releaseApply    chan struct{}
	blockOnce       sync.Once
}

func newRecordingResourceTarget() *recordingResourceTarget {
	return &recordingResourceTarget{
		inspectCalls: map[string]int{}, applyCalls: map[string]int{}, state: map[string][32]byte{}, applied: map[string]bool{},
		conflict: map[string]bool{}, inspectFailure: map[string]bool{}, failBeforeWrite: map[string]bool{}, failAfterWrite: map[string]bool{},
	}
}

func (t *recordingResourceTarget) Inspect(_ context.Context, resource domain.TaskRepositoryResource, request app.PatchTargetRequest) (app.PatchTargetInspection, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.inspectCalls[resource.RepoID]++
	if t.inspectFailure[resource.RepoID] {
		return app.PatchTargetInspection{}, errors.New("inspection unavailable")
	}
	state := t.state[resource.RepoID]
	if state == ([32]byte{}) {
		state = sha256.Sum256([]byte(resource.RepoID + ":pre"))
		t.state[resource.RepoID] = state
	}
	if t.applied[resource.RepoID] && request.PriorPreStateDigest != ([32]byte{}) {
		return app.PatchTargetInspection{TargetIdentity: resource.PhysicalIdentity, BaseRevision: resource.BaseCommit, StateDigest: state, AlreadyApplied: true}, nil
	}
	return app.PatchTargetInspection{
		TargetIdentity: resource.PhysicalIdentity, BaseRevision: resource.BaseCommit, StateDigest: state,
		Applicable: !t.conflict[resource.RepoID], Reason: "recording target",
	}, nil
}

func (t *recordingResourceTarget) Apply(_ context.Context, resource domain.TaskRepositoryResource, _ app.PatchTargetRequest, _ app.PatchTargetInspection) (app.PatchTargetEvidence, error) {
	t.mu.Lock()
	t.applyCalls[resource.RepoID]++
	block := resource.RepoID == t.blockRepo && t.applyEntered != nil && t.releaseApply != nil
	t.mu.Unlock()
	if block {
		t.blockOnce.Do(func() {
			close(t.applyEntered)
			<-t.releaseApply
		})
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.failBeforeWrite[resource.RepoID] {
		return app.PatchTargetEvidence{}, errors.New("simulated interruption before write evidence")
	}
	post := sha256.Sum256([]byte(resource.RepoID + ":post"))
	t.state[resource.RepoID] = post
	t.applied[resource.RepoID] = true
	if t.failAfterWrite[resource.RepoID] {
		return app.PatchTargetEvidence{}, errors.New("simulated interruption after write")
	}
	return app.PatchTargetEvidence{PostStateDigest: post}, nil
}

func (t *recordingResourceTarget) inspectCount(repoID string) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.inspectCalls[repoID]
}

func (t *recordingResourceTarget) applyCount(repoID string) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.applyCalls[repoID]
}

func (t *recordingResourceTarget) totalApplies() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	total := 0
	for _, count := range t.applyCalls {
		total += count
	}
	return total
}

func repositoryApplyStatus(view app.ResourceApplyView, repoID string) string {
	for _, repository := range view.Repositories {
		if repository.RepoID == repoID {
			return repository.Status
		}
	}
	return ""
}

func TestResourceApplyIntegrationRealTargetUsesFrozenSnapshot(t *testing.T) {
	fixture := newResourceApplyIntegrationFixture(t, 2, 1)
	service := fixture.service(newResourcePatchTarget())
	accepted := fixture.accept(t, service, 0)
	view, err := service.ApplyResourceResult(context.Background(), fixture.runs[0].applyRequest("real-apply", accepted.Version()))
	if err != nil || view.Status != "applied" {
		t.Fatalf("real target Apply = %#v, %v", view, err)
	}
	for _, resource := range fixture.resources {
		contents, err := os.ReadFile(filepath.Join(resource.Checkout, "README.md"))
		if err != nil || string(contents) != "changed "+resource.RepoID+"\n" {
			t.Fatalf("real Apply contents %q, %v", contents, err)
		}
	}
}

func TestResourceApplyIntegrationEmptyParticipantDoesNotCountAsWrite(t *testing.T) {
	for _, conflict := range []bool{true, false} {
		t.Run(fmt.Sprintf("conflict=%v", conflict), func(t *testing.T) {
			fixture := newResourceApplyIntegrationFixtureWithRoots(t, 2, 1, nil, 0)
			target := newRecordingResourceTarget()
			target.conflict[fixture.resources[1].RepoID] = conflict
			service := fixture.service(target)
			accepted := fixture.accept(t, service, 0)
			view, err := service.ApplyResourceResult(context.Background(), fixture.runs[0].applyRequest("mixed-empty", accepted.Version()))
			if err != nil {
				t.Fatal(err)
			}
			want := "applied"
			if conflict {
				want = "conflict"
			}
			if view.Status != want || len(view.Repositories) != 2 || view.Repositories[0].Status != "no_change" {
				t.Fatalf("mixed result=%+v want=%s", view, want)
			}
			if target.inspectCount(fixture.resources[0].RepoID) != 0 {
				t.Fatal("empty repository inspected as write target")
			}
			restored, err := service.LoadResourceApply(context.Background(), fixture.runs[0].resultID)
			if err != nil || restored.Status != want || restored.Repositories[0].Status != "no_change" {
				t.Fatalf("restored=%+v err=%v", restored, err)
			}
		})
	}
}

// These tests seed synthetic immutable patches to isolate Apply CAS/recovery;
// live Git acceptance is exercised by the terminal/review-stage integration tests.
type syntheticApplyReviewVerifier struct{}

func (syntheticApplyReviewVerifier) Verify(context.Context, storecontract.Reader, domain.TaskID, domain.TaskResourceSnapshot, app.ResourceReviewView) error {
	return nil
}
