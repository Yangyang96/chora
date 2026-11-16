package app_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/speccoding"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

func TestMaterializeAndRegisterSpecCodingContractUsesCanonicalSnapshot(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := openLatestSQLiteTestStore(ctx, filepath.Join(root, "chora.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	service := app.NewService(app.Dependencies{
		Store: db, Context: app.ContextAssembler{}, Authorizer: allowAuthorizer{},
		Clock: &fixedClock{now: time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC)}, IDs: app.RandomIDs{},
	})
	v4 := readSpecCodingContract(t, "chora-m1-real-task.v4.json")
	entryID := mustContextEntryID(t, "context_entry_018f0c4a-1a30-7c3d-8e4f-1234567890ab")
	revisionID := mustContextRevisionID(t, "context_revision_018f0c4a-1a31-7c3d-8e4f-1234567890ab")
	charterID := mustCharterID(t, "charter_018f0c4a-1a32-7c3d-8e4f-1234567890ab")
	frozenAt := time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC)
	workspaceRoot := filepath.Join(t.TempDir(), "source-baseline-v2", "repo")

	materialized, err := service.MaterializeSpecCodingContract(ctx, app.MaterializeSpecCodingContractRequest{
		CommandMeta: meta("materialize-spec-coding", "materialize-spec-coding"), Contract: v4,
		WorkspaceRoot: workspaceRoot, ContextEntryID: entryID, ContextRevisionID: revisionID,
		CharterID: charterID, FrozenAt: frozenAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(materialized.Snapshot.CanonicalJSON())
	if digest != materialized.Snapshot.Digest() || hex.EncodeToString(digest[:]) == speccoding.SupersededPlaceholderContextDigest {
		t.Fatalf("materialized digest = %x", digest)
	}
	if materialized.Task.ID().String() != v4.Document().Task.ID || materialized.Snapshot.ID().String() != v4.Document().Execution.Input.ContextSnapshotID {
		t.Fatalf("materialized identity task=%s snapshot=%s", materialized.Task.ID(), materialized.Snapshot.ID())
	}
	binding, err := db.Reader().GetSpecCodingBinding(ctx, materialized.Task.ID())
	if err != nil || binding.Status != "materialized" || binding.SnapshotDigest != digest {
		t.Fatalf("materialized binding = %#v, err=%v", binding, err)
	}
	submitted, err := service.SubmitTechnicalPlanDraft(ctx, app.SubmitTechnicalPlanDraftRequest{CommandMeta: meta("submit-spec-coding-plan", "submit-spec-coding-plan"), DraftID: materialized.Draft.ID(), ExpectedEditVersion: materialized.Draft.EditVersion()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ReviewTechnicalPlanRevision(ctx, app.ReviewTechnicalPlanRevisionRequest{CommandMeta: meta("reject-unregistered-plan-acceptance", "reject-unregistered-plan-acceptance"), RevisionID: submitted.Revision.ID(), Kind: domain.TechnicalPlanReviewAccept, Note: "Must remain unaccepted until registration."}); !errors.Is(err, storecontract.ErrPlanningConflict) {
		t.Fatalf("materialized-only contract accepted for execution: %v", err)
	}

	v5Document := v4.Document()
	v5Document.SchemaVersion = speccoding.CoreContractSchemaVersionV5
	v5Document.Revision = 5
	v5Document.Execution.Input.ContextSnapshotDigest = hex.EncodeToString(digest[:])
	v5JSON, err := json.Marshal(v5Document)
	if err != nil {
		t.Fatal(err)
	}
	v5, err := speccoding.DecodeCoreContract(v5JSON)
	if err != nil {
		t.Fatal(err)
	}
	registered, err := service.RegisterSpecCodingContract(ctx, app.RegisterSpecCodingContractRequest{
		CommandMeta: meta("register-spec-coding", "register-spec-coding"), Contract: v5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if registered.Binding.Status != "registered" || registered.Binding.ActiveContractDigest != sha256.Sum256(v5.CanonicalJSON()) {
		t.Fatalf("registered binding = %#v", registered.Binding)
	}
	accepted, err := service.ReviewTechnicalPlanRevision(ctx, app.ReviewTechnicalPlanRevisionRequest{CommandMeta: meta("accept-spec-coding-plan", "accept-spec-coding-plan"), RevisionID: submitted.Revision.ID(), Kind: domain.TechnicalPlanReviewAccept, Note: "Registered contract plan accepted."})
	if err != nil || accepted.Binding == nil {
		t.Fatalf("accepted=%#v err=%v", accepted, err)
	}
	createdRun, err := service.CreateRun(ctx, app.CreateRunRequest{CommandMeta: meta("create-spec-coding-run", "create-spec-coding-run"), TaskID: materialized.Task.ID(), RevisionID: submitted.Revision.ID()})
	if err != nil {
		t.Fatal(err)
	}
	executionService := app.NewService(app.Dependencies{Store: db, Context: app.ContextAssembler{}, Agents: fakeRegistry{&fakeAdapter{}}, Supervisor: &fakeSupervisor{}, Authorizer: allowAuthorizer{}, Clock: &fixedClock{now: createdRun.Run.UpdatedAt()}, IDs: app.RandomIDs{}})
	prepared, err := executionService.PrepareRun(ctx, app.PrepareRunRequest{CommandMeta: meta("prepare-accepted-spec-coding", "prepare-accepted-spec-coding"), RunID: createdRun.Run.ID(), ExpectedVersion: createdRun.Run.Version()})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Snapshot.ID() != materialized.Snapshot.ID() || prepared.Attempt.ContextDigest() != digest {
		t.Fatalf("prepared unregistered Snapshot: snapshot=%s digest=%x", prepared.Snapshot.ID(), prepared.Attempt.ContextDigest())
	}
	persisted, err := db.Reader().GetSnapshot(ctx, materialized.Snapshot.ID())
	if err != nil || sha256.Sum256(persisted.CanonicalJSON()) != digest {
		t.Fatalf("persisted snapshot digest drift, err=%v", err)
	}
}

func TestRegisterSpecCodingContractRejectsDigestAndBoundaryDrift(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*speccoding.CoreContractDocument)
	}{
		{"digest", func(document *speccoding.CoreContractDocument) {
			document.Execution.Input.ContextSnapshotDigest = strings.Repeat("a", 64)
		}},
		{"Task", func(document *speccoding.CoreContractDocument) { document.Task.Goal += " drift" }},
		{"Acceptance", func(document *speccoding.CoreContractDocument) {
			document.Acceptance.Criteria[0].Description += " drift"
		}},
		{"Context", func(document *speccoding.CoreContractDocument) { document.Requirement.Statement += " drift" }},
		{"target file", func(document *speccoding.CoreContractDocument) {
			oldPath := document.Execution.Boundary.WritableFiles[0]
			newPath := "internal/domain/acceptance.go"
			document.Execution.Boundary.WritableFiles[0] = newPath
			for stepIndex := range document.TechnicalPlan.Steps {
				for fileIndex, file := range document.TechnicalPlan.Steps[stepIndex].Files {
					if file == oldPath {
						document.TechnicalPlan.Steps[stepIndex].Files[fileIndex] = newPath
					}
				}
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, v4, digest := materializedSpecCodingFixture(t)
			document := v5Document(v4, digest)
			test.mutate(&document)
			candidate, err := speccoding.NewCoreContract(document)
			if err != nil {
				t.Fatalf("drift fixture invalid before registration: %v", err)
			}
			_, err = service.RegisterSpecCodingContract(context.Background(), app.RegisterSpecCodingContractRequest{CommandMeta: meta("register-drift", "register-drift"), Contract: candidate})
			if !errors.Is(err, app.ErrInvalidCommand) {
				t.Fatalf("registration accepted %s drift: %v", test.name, err)
			}
		})
	}

	t.Run("candidate", func(t *testing.T) {
		_, v4, digest := materializedSpecCodingFixture(t)
		document := v5Document(v4, digest)
		document.Candidate.Model.ModelID = "unbound-model"
		if _, err := speccoding.NewCoreContract(document); err == nil {
			t.Fatal("candidate drift passed contract validation")
		}
	})
}

func TestRegisterSpecCodingContractAcceptsV6IdentityOnlyRevision(t *testing.T) {
	service, v4, digest := materializedSpecCodingFixture(t)
	document := v5Document(v4, digest)
	document.SchemaVersion = speccoding.CoreContractSchemaVersionV6
	document.Revision = 6
	candidate, err := speccoding.NewCoreContract(document)
	if err != nil {
		t.Fatal(err)
	}
	registered, err := service.RegisterSpecCodingContract(context.Background(), app.RegisterSpecCodingContractRequest{
		CommandMeta: meta("register-v6", "register-v6"), Contract: candidate,
	})
	if err != nil {
		t.Fatal(err)
	}
	if registered.Binding.Status != storecontract.SpecCodingRegistered || !bytes.Equal(registered.Binding.ActiveContractJSON, candidate.CanonicalJSON()) {
		t.Fatalf("registered v6 binding = %#v", registered.Binding)
	}
}

func TestRegisterSpecCodingContractAcceptsV7IdentityOnlyRevision(t *testing.T) {
	service, v4, digest := materializedSpecCodingFixture(t)
	document := v5Document(v4, digest)
	document.SchemaVersion = speccoding.CoreContractSchemaVersionV7
	document.Revision = 7
	candidate, err := speccoding.NewCoreContract(document)
	if err != nil {
		t.Fatal(err)
	}
	registered, err := service.RegisterSpecCodingContract(context.Background(), app.RegisterSpecCodingContractRequest{
		CommandMeta: meta("register-v7", "register-v7"), Contract: candidate,
	})
	if err != nil {
		t.Fatal(err)
	}
	if registered.Binding.Status != storecontract.SpecCodingRegistered || !bytes.Equal(registered.Binding.ActiveContractJSON, candidate.CanonicalJSON()) {
		t.Fatalf("registered v7 binding = %#v", registered.Binding)
	}
}

func TestRegisterSpecCodingContractAcceptsV8StructuredVerificationAmendment(t *testing.T) {
	service, v4, digest := materializedSpecCodingFixture(t)
	document := v5Document(v4, digest)
	document.SchemaVersion = speccoding.CoreContractSchemaVersionV8
	document.Revision = 8
	document.Acceptance.VerificationCommands = []speccoding.BoundedCommand{{ID: "domain-tests", Argv: []string{"go", "test", "./internal/domain"}}}
	for index := range document.Acceptance.Criteria {
		document.Acceptance.Criteria[index].VerificationCommandIDs = []string{"domain-tests"}
	}
	candidate, err := speccoding.NewCoreContract(document)
	if err != nil {
		t.Fatal(err)
	}
	registered, err := service.RegisterSpecCodingContract(context.Background(), app.RegisterSpecCodingContractRequest{
		CommandMeta: meta("register-v8", "register-v8"), Contract: candidate,
	})
	if err != nil {
		t.Fatal(err)
	}
	if registered.Binding.Status != storecontract.SpecCodingRegistered || !bytes.Equal(registered.Binding.ActiveContractJSON, candidate.CanonicalJSON()) {
		t.Fatalf("registered v8 binding = %#v", registered.Binding)
	}
}

func materializedSpecCodingFixture(t *testing.T) (*app.Service, speccoding.CoreContract, [32]byte) {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := openLatestSQLiteTestStore(context.Background(), filepath.Join(root, "chora.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	frozenAt := time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC)
	service := app.NewService(app.Dependencies{Store: db, Context: app.ContextAssembler{}, Authorizer: allowAuthorizer{}, Clock: &fixedClock{now: frozenAt}, IDs: app.RandomIDs{}})
	v4 := readSpecCodingContract(t, "chora-m1-real-task.v4.json")
	materialized, err := service.MaterializeSpecCodingContract(context.Background(), app.MaterializeSpecCodingContractRequest{
		CommandMeta: meta("materialize-drift", "materialize-drift"), Contract: v4, WorkspaceRoot: filepath.Join(root, "source-baseline-v2", "repo"),
		ContextEntryID: mustContextEntryID(t, "context_entry_018f0c4a-1a30-7c3d-8e4f-1234567890ab"), ContextRevisionID: mustContextRevisionID(t, "context_revision_018f0c4a-1a31-7c3d-8e4f-1234567890ab"),
		CharterID: mustCharterID(t, "charter_018f0c4a-1a32-7c3d-8e4f-1234567890ab"), FrozenAt: frozenAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	return service, v4, materialized.Snapshot.Digest()
}

func v5Document(v4 speccoding.CoreContract, digest [32]byte) speccoding.CoreContractDocument {
	document := v4.Document()
	document.SchemaVersion = speccoding.CoreContractSchemaVersionV5
	document.Revision = 5
	document.Execution.Input.ContextSnapshotDigest = hex.EncodeToString(digest[:])
	return document
}

func readSpecCodingContract(t *testing.T, name string) speccoding.CoreContract {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "contracts", "g2-m1a", name))
	if err != nil {
		t.Fatal(err)
	}
	contract, err := speccoding.DecodeCoreContract(data)
	if err != nil {
		t.Fatal(err)
	}
	return contract
}

func mustContextEntryID(t *testing.T, value string) domain.ContextEntryID {
	t.Helper()
	id, err := domain.ParseContextEntryID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func mustContextRevisionID(t *testing.T, value string) domain.ContextRevisionID {
	t.Helper()
	id, err := domain.ParseContextRevisionID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func mustCharterID(t *testing.T, value string) domain.CharterID {
	t.Helper()
	id, err := domain.ParseCharterID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}
