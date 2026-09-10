package localweb

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/contextcore"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/speccoding"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

func TestLocalConnectedPatchFlowsFromTaskWorktreeToOriginalCheckout(t *testing.T) {
	ctx := context.Background()
	db := openTaskWorktreeResolverStore(t)
	defer db.Close()
	anchor, _, revision := newTaskWorktreeTestRepository(t)
	now := time.Date(2026, 9, 4, 15, 0, 0, 0, time.UTC)
	_, task := insertResolverRoomAndTask(t, db, anchor, revision, "Example", now)
	registerPatchScope(t, db, task, anchor, revision, []string{"README.md"}, nil, now)
	resolver := newTaskWorktreeResolver(db.Reader(), nil)
	worktree := persistReadyResolverWorktree(t, db, resolver, task, now)

	if err := os.WriteFile(filepath.Join(worktree, "README.md"), []byte("implemented in Task worktree\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	patchStore, err := newLocalConnectedPatchStore(filepath.Join(t.TempDir(), "review-patches"), db.Reader(), resolver)
	if err != nil {
		t.Fatal(err)
	}
	attemptID := domain.NewAttemptID()
	artifact, err := patchStore.MaterializeReviewPatch(ctx, app.ReviewPatchMaterializationRequest{
		RunID: domain.NewRunID(), TaskID: task.ID(), AttemptID: attemptID, WorkingRoot: worktree,
	})
	if err != nil {
		t.Fatalf("MaterializeReviewPatch: %v", err)
	}
	raw, err := patchStore.ReadReviewPatch(ctx, artifact.Locator)
	if err != nil {
		t.Fatalf("ReadReviewPatch: %v", err)
	}
	if artifact.MediaType != "text/x-diff" || artifact.SHA256 != hashHex(raw) || !strings.Contains(string(raw), "+implemented in Task worktree") {
		t.Fatalf("artifact=%+v patch=%s", artifact, raw)
	}
	if got, _ := os.ReadFile(filepath.Join(anchor, "README.md")); string(got) != "initial\n" {
		t.Fatalf("original checkout mutated before Apply: %q", got)
	}

	target := &repositoryBoundGitPatchTarget{reader: db.Reader()}
	request := app.PatchTargetRequest{TaskID: task.ID(), Raw: raw, PatchDigest: sha256.Sum256(raw), AffectedPaths: []string{"README.md"}}
	inspection, err := target.Inspect(ctx, request)
	if err != nil || !inspection.Applicable || inspection.BaseRevision != revision {
		t.Fatalf("Inspect=%+v err=%v", inspection, err)
	}
	if _, err := target.Apply(ctx, request, inspection); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(anchor, "README.md")); string(got) != "implemented in Task worktree\n" {
		t.Fatalf("accepted Patch not applied to original checkout: %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(worktree, "README.md")); string(got) != "implemented in Task worktree\n" {
		t.Fatalf("Task worktree changed during Apply: %q", got)
	}
}

func TestLocalConnectedPatchRejectsUntrackedOutputAndOriginalHeadDrift(t *testing.T) {
	ctx := context.Background()
	db := openTaskWorktreeResolverStore(t)
	defer db.Close()
	anchor, _, revision := newTaskWorktreeTestRepository(t)
	now := time.Date(2026, 9, 4, 15, 0, 0, 0, time.UTC)
	_, task := insertResolverRoomAndTask(t, db, anchor, revision, "Example", now)
	registerPatchScope(t, db, task, anchor, revision, []string{"README.md"}, nil, now)
	resolver := newTaskWorktreeResolver(db.Reader(), nil)
	worktree := persistReadyResolverWorktree(t, db, resolver, task, now)
	if err := os.WriteFile(filepath.Join(worktree, "README.md"), []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree, "untracked.txt"), []byte("not declared\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	patchStore, err := newLocalConnectedPatchStore(filepath.Join(t.TempDir(), "review-patches"), db.Reader(), resolver)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := patchStore.MaterializeReviewPatch(ctx, app.ReviewPatchMaterializationRequest{
		RunID: domain.NewRunID(), TaskID: task.ID(), AttemptID: domain.NewAttemptID(), WorkingRoot: worktree,
	}); err == nil || !strings.Contains(err.Error(), "outside its frozen writable scope") {
		t.Fatalf("untracked materialization err=%v", err)
	}
	if err := os.Remove(filepath.Join(worktree, "untracked.txt")); err != nil {
		t.Fatal(err)
	}
	artifact, err := patchStore.MaterializeReviewPatch(ctx, app.ReviewPatchMaterializationRequest{
		RunID: domain.NewRunID(), TaskID: task.ID(), AttemptID: domain.NewAttemptID(), WorkingRoot: worktree,
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := patchStore.ReadReviewPatch(ctx, artifact.Locator)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(anchor, "README.md"), []byte("new commit\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runTaskWorktreeGitTest(t, anchor, "add", "README.md")
	runTaskWorktreeGitTest(t, anchor, "-c", "user.name=Chora Test", "-c", "user.email=chora@example.test", "commit", "-m", "drift")
	target := &repositoryBoundGitPatchTarget{reader: db.Reader()}
	inspection, err := target.Inspect(ctx, app.PatchTargetRequest{TaskID: task.ID(), Raw: raw, PatchDigest: sha256.Sum256(raw), AffectedPaths: []string{"README.md"}})
	if err == nil || !strings.Contains(inspection.Reason, "drifted") || inspection.Applicable {
		t.Fatalf("drift inspection=%+v err=%v", inspection, err)
	}
}

func registerPatchScope(t *testing.T, db interface {
	WithinWriteTx(context.Context, func(storecontract.WriteTx) error) error
}, task domain.Task, repositoryRoot, revision string, files, directories []string, now time.Time) speccoding.CoreContract {
	t.Helper()
	envelope, err := speccoding.NewBoundEnvelope(repositoryRoot, speccoding.RepositoryIdentity{Name: "patch-test", SourceRevision: revision}, "0.50.0")
	if err != nil {
		t.Fatal(err)
	}
	criteria := make([]speccoding.UserAcceptanceCriterion, 0, len(task.Criteria()))
	for _, criterion := range task.Criteria() {
		description := criterion.Description()
		if strings.TrimSpace(description) == "" {
			description = "No-check acceptance criterion for the Patch test fixture."
		}
		criteria = append(criteria, speccoding.UserAcceptanceCriterion{ID: criterion.ID(), Title: criterion.Title(), Description: description})
	}
	intent, err := envelope.Declare(speccoding.UserTaskDeclaration{
		ContractID: "patch-test", TaskID: task.ID(), RoomID: task.RoomID(), WorkspaceRoot: repositoryRoot,
		Title: task.Title(), Requirement: task.Goal(), Constraints: []string{"bounded test"}, OutOfScope: []string{"unrelated files"},
		Criteria: criteria, WritableFiles: files, WritableDirectories: directories, NoChecks: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	contextRevision, err := domain.NewRoomContextRevision(domain.RoomContextRevisionParams{
		EntryID: domain.NewContextEntryID(), RevisionID: domain.NewContextRevisionID(), RoomID: task.RoomID(), Kind: domain.ContextKindBrief,
		RevisionNumber: 1, Title: "Patch fixture context", Body: "Exercise the frozen Patch scope.", CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	charter, err := domain.NewRunCharter(domain.RunCharterParams{
		ID: domain.NewCharterID(), TaskID: task.ID(), TaskGoal: task.Goal(), Criteria: task.Criteria(), ContextRevisionIDs: []domain.ContextRevisionID{contextRevision.RevisionID()}, WorkspaceRoot: repositoryRoot,
		AdapterID: "fake", SandboxMode: "workspace", ExpectedOutput: "reviewable Patch", ResponsibleHuman: "patch-test",
		CapabilityEnvelope: domain.CapabilityEnvelope{"read": true}, Initiator: "patch-test", CreatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	var snapshot contextcore.Snapshot
	if err := db.WithinWriteTx(context.Background(), func(tx storecontract.WriteTx) error {
		if err := tx.InsertRevision(context.Background(), contextRevision); err != nil {
			return err
		}
		var err error
		snapshot, err = contextcore.NewAssembler(tx).Assemble(context.Background(), contextcore.AssembleRequest{SnapshotID: domain.NewContextSnapshotID(), Task: task, Charter: charter})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	snapshotDigest := snapshot.Digest()
	contract, err := envelope.Accept(intent, intent.InitialPlan(), snapshot.ID(), hex.EncodeToString(snapshotDigest[:]))
	if err != nil {
		t.Fatal(err)
	}
	canonical := contract.CanonicalJSON()
	contractDigest := sha256.Sum256(canonical)
	binding := storecontract.SpecCodingBinding{
		TaskID: task.ID(), SnapshotID: snapshot.ID(), SnapshotDigest: snapshotDigest,
		MaterializedContractDigest: contractDigest, MaterializedContractJSON: canonical,
		Status: storecontract.SpecCodingMaterialized, MaterializedAt: now,
	}
	if err := db.WithinWriteTx(context.Background(), func(tx storecontract.WriteTx) error {
		if err := tx.InsertSpecCodingBinding(context.Background(), binding); err != nil {
			return fmt.Errorf("insert materialized Patch contract: %w", err)
		}
		binding.ActiveContractDigest = contractDigest
		binding.ActiveContractJSON = append([]byte(nil), canonical...)
		binding.Status = storecontract.SpecCodingRegistered
		binding.RegisteredAt = now
		if err := tx.RegisterSpecCodingBinding(context.Background(), binding); err != nil {
			return fmt.Errorf("register active Patch contract: %w", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return contract
}

func persistReadyResolverWorktree(t *testing.T, db interface {
	WithinWriteTx(context.Context, func(storecontract.WriteTx) error) error
}, resolver *taskWorktreeResolver, task domain.Task, now time.Time) string {
	t.Helper()
	planned, err := resolver.Plan(context.Background(), task, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.WithinWriteTx(context.Background(), func(tx storecontract.WriteTx) error {
		return tx.InsertTaskWorktreeBinding(context.Background(), planned)
	}); err != nil {
		t.Fatal(err)
	}
	if err := resolver.Ensure(context.Background(), planned); err != nil {
		t.Fatal(err)
	}
	ready, err := planned.Ready(now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.WithinWriteTx(context.Background(), func(tx storecontract.WriteTx) error {
		return tx.SaveTaskWorktreeBindingCAS(context.Background(), planned.Version(), ready)
	}); err != nil {
		t.Fatal(err)
	}
	resolved, err := resolver.ResolveExecutionRoot(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func hashHex(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}
