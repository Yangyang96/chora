package localweb

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/gitsource"
	storecontract "github.com/Yangyang96/chora/internal/store"
	"github.com/Yangyang96/chora/internal/store/sqlite"
)

func TestResourcePatchStoreKeepsRepositoryIdentityAndEmptyParticipants(t *testing.T) {
	fixture := newResourcePatchFixture(t,
		resourcePatchConfig{role: "write"},
		resourcePatchConfig{role: "write"},
		resourcePatchConfig{role: "write"},
	)
	first, second, unchanged := fixture.snapshot.Resources[0], fixture.snapshot.Resources[1], fixture.snapshot.Resources[2]
	if err := os.WriteFile(filepath.Join(fixture.root, first.RepoID, "README.md"), []byte("first repository\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.root, first.RepoID, "new.txt"), []byte("new file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.root, second.RepoID, "README.md"), []byte("second repository\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	attemptID := domain.NewAttemptID()
	patches, err := fixture.materializer.Materialize(context.Background(), fixture.task.ID(), attemptID, fixture.root)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	if len(patches) != 3 {
		t.Fatalf("patches=%+v", patches)
	}
	byRepository := make(map[string]app.ResourceReviewPatch, len(patches))
	for _, patch := range patches {
		byRepository[patch.RepoID] = patch
	}
	if len(byRepository[first.RepoID].Patch.Files) != 2 || len(byRepository[second.RepoID].Patch.Files) != 1 {
		t.Fatalf("first=%+v second=%+v", byRepository[first.RepoID], byRepository[second.RepoID])
	}
	if byRepository[first.RepoID].Patch.Files[0].Path == byRepository[second.RepoID].Patch.Files[0].Path &&
		byRepository[first.RepoID].Patch.PatchDigest == byRepository[second.RepoID].Patch.PatchDigest {
		t.Fatal("same relative path in two repositories lost repository-specific content")
	}
	if empty := byRepository[unchanged.RepoID]; len(empty.Patch.Raw) != 0 || empty.Artifact.Locator != "" {
		t.Fatalf("unchanged participant=%+v", empty)
	}
	for _, resource := range []domain.TaskRepositoryResource{first, second} {
		patch := byRepository[resource.RepoID]
		wantLocator := attemptID.String() + "/" + resource.RepoID + ".diff"
		if patch.Artifact.Locator != wantLocator || patch.Artifact.SHA256 != hashHex(patch.Patch.Raw) {
			t.Fatalf("artifact=%+v", patch.Artifact)
		}
		raw, err := fixture.materializer.Read(context.Background(), attemptID, resource.RepoID, patch.Patch.PatchDigest)
		if err != nil || !bytesEqual(raw, patch.Patch.Raw) {
			t.Fatalf("Read(%s)=%q err=%v", resource.RepoID, raw, err)
		}
	}
	for repoID, original := range fixture.originals {
		contents, err := os.ReadFile(filepath.Join(original, "README.md"))
		if err != nil || string(contents) != "initial\n" {
			t.Fatalf("original %s changed before Apply: %q err=%v", repoID, contents, err)
		}
	}

	// A retry with different bytes cannot replace an artifact under the same
	// Attempt/Repo identity.
	if err := os.WriteFile(filepath.Join(fixture.root, first.RepoID, "README.md"), []byte("retry drift\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.materializer.Materialize(context.Background(), fixture.task.ID(), attemptID, fixture.root); err == nil || !strings.Contains(err.Error(), "identity conflict") {
		t.Fatalf("immutable retry err=%v", err)
	}
	old := byRepository[first.RepoID].Patch
	if raw, err := fixture.materializer.Read(context.Background(), attemptID, first.RepoID, old.PatchDigest); err != nil || !bytesEqual(raw, old.Raw) {
		t.Fatalf("immutable bytes=%q err=%v", raw, err)
	}

	secondPatch := byRepository[second.RepoID]
	artifactPath := filepath.Join(fixture.artifactRoot, attemptID.String(), second.RepoID+".diff")
	if err := os.WriteFile(artifactPath, []byte("drifted\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.materializer.Read(context.Background(), attemptID, second.RepoID, secondPatch.Patch.PatchDigest); err == nil || !strings.Contains(err.Error(), "drifted") {
		t.Fatalf("digest drift err=%v", err)
	}
}

func TestResourcePatchStoreRejectsUnsupportedChangesAcrossWholeResult(t *testing.T) {
	tests := []struct {
		name   string
		config resourcePatchConfig
		change func(*testing.T, string)
		want   string
	}{
		{
			name: "reference repository", config: resourcePatchConfig{role: "reference"}, want: "reference repository",
			change: func(t *testing.T, root string) { writeResourcePatchFile(t, root, "README.md", []byte("changed\n")) },
		},
		{
			name:   "outside restricted scope",
			config: resourcePatchConfig{role: "write", scope: domain.TaskRepositoryScope{Mode: "restricted", WritableFiles: []string{"README.md"}, MigrationChoice: "not_needed"}},
			want:   "outside its frozen writable scope",
			change: func(t *testing.T, root string) { writeResourcePatchFile(t, root, "outside.txt", []byte("changed\n")) },
		},
		{
			name:   "protected directory",
			config: resourcePatchConfig{role: "write", scope: domain.TaskRepositoryScope{Mode: "repository", ProtectedDirectories: []string{"protected"}, MigrationChoice: "not_needed"}},
			want:   "outside its frozen writable scope",
			change: func(t *testing.T, root string) {
				writeResourcePatchFile(t, root, "protected/output.txt", []byte("changed\n"))
			},
		},
		{
			name: "symlink", config: resourcePatchConfig{role: "write"}, want: "symlink or special file",
			change: func(t *testing.T, root string) {
				if err := os.Remove(filepath.Join(root, "README.md")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("missing", filepath.Join(root, "README.md")); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "binary", config: resourcePatchConfig{role: "write"}, want: "binary or non-UTF-8",
			change: func(t *testing.T, root string) { writeResourcePatchFile(t, root, "README.md", []byte{'a', 0, 'b'}) },
		},
		{
			name: "Git LFS pointer", config: resourcePatchConfig{role: "write"}, want: "Git LFS pointer",
			change: func(t *testing.T, root string) {
				writeResourcePatchFile(t, root, "README.md", []byte("version https://git-lfs.github.com/spec/v1\noid sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\nsize 1\n"))
			},
		},
		{
			name: "oversize file", config: resourcePatchConfig{role: "write"}, want: "exceeds the supported review size",
			change: func(t *testing.T, root string) {
				if err := os.Truncate(filepath.Join(root, "README.md"), int64(domain.RepositoryTextBytes+1)); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newResourcePatchFixture(t, test.config)
			resourceRoot := filepath.Join(fixture.root, fixture.snapshot.Resources[0].RepoID)
			test.change(t, resourceRoot)
			if _, err := fixture.materializer.Materialize(context.Background(), fixture.task.ID(), domain.NewAttemptID(), fixture.root); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestResourcePatchStoreIgnoresUnchangedUnsupportedFiles(t *testing.T) {
	fixture := newResourcePatchFixture(t, resourcePatchConfig{role: "write", unchangedObstacles: true})
	resource := fixture.snapshot.Resources[0]
	writeResourcePatchFile(t, filepath.Join(fixture.root, resource.RepoID), "README.md", []byte("ordinary change\n"))
	patches, err := fixture.materializer.Materialize(context.Background(), fixture.task.ID(), domain.NewAttemptID(), fixture.root)
	if err != nil {
		t.Fatalf("unchanged unsupported files blocked ordinary change: %v", err)
	}
	if len(patches) != 1 || len(patches[0].Patch.Files) != 1 || patches[0].Patch.Files[0].Path != "README.md" {
		t.Fatalf("patches=%+v", patches)
	}
}

func TestResourcePatchStoreEnforcesSharedPathAndPatchBudgets(t *testing.T) {
	fixture := newResourcePatchFixture(t, resourcePatchConfig{role: "write"}, resourcePatchConfig{role: "write"})
	for index, resource := range fixture.snapshot.Resources {
		writeResourcePatchFile(t, filepath.Join(fixture.root, resource.RepoID), "README.md", []byte{byte('a' + index), '\n'})
	}
	store := fixture.materializer.(*resourcePatchStore)
	store.changedPathLimit = 1
	if _, err := store.Materialize(context.Background(), fixture.task.ID(), domain.NewAttemptID(), fixture.root); err == nil || !strings.Contains(err.Error(), "path limit") {
		t.Fatalf("aggregate path limit err=%v", err)
	}

	store.changedPathLimit = domain.TaskChangedEntryLimit
	baseline, err := store.Materialize(context.Background(), fixture.task.ID(), domain.NewAttemptID(), fixture.root)
	if err != nil {
		t.Fatal(err)
	}
	total, largest := 0, 0
	for _, patch := range baseline {
		total += len(patch.Patch.Raw)
		if len(patch.Patch.Raw) > largest {
			largest = len(patch.Patch.Raw)
		}
	}
	if total <= largest {
		t.Fatalf("expected patches from two repositories: total=%d largest=%d", total, largest)
	}
	limitedRoot := canonicalTempDir(t)
	limitedMaterializer, err := newResourcePatchStore(filepath.Join(limitedRoot, "patches"), fixture.db.Reader(), fixture.resolver)
	if err != nil {
		t.Fatal(err)
	}
	limited := limitedMaterializer.(*resourcePatchStore)
	limited.patchByteLimit = total - 1
	if limited.patchByteLimit < largest {
		t.Fatalf("test limit %d does not prove aggregation over per-repo limit %d", limited.patchByteLimit, largest)
	}
	if _, err := limited.Materialize(context.Background(), fixture.task.ID(), domain.NewAttemptID(), fixture.root); err == nil || !strings.Contains(err.Error(), "Patch could not be materialized") {
		t.Fatalf("aggregate Patch limit err=%v", err)
	}
}

func TestResourcePatchStoreRejectsHeadDrift(t *testing.T) {
	fixture := newResourcePatchFixture(t, resourcePatchConfig{role: "write"})
	resource := fixture.snapshot.Resources[0]
	root := filepath.Join(fixture.root, resource.RepoID)
	writeResourcePatchFile(t, root, "README.md", []byte("committed drift\n"))
	runTaskWorktreeGitTest(t, root, "add", "README.md")
	runTaskWorktreeGitTest(t, root, "-c", "user.name=Chora Test", "-c", "user.email=chora@example.test", "commit", "-m", "drift")
	if _, err := fixture.materializer.Materialize(context.Background(), fixture.task.ID(), domain.NewAttemptID(), fixture.root); err == nil {
		t.Fatal("materialized a worktree whose HEAD drifted from the frozen base")
	}
}

type resourcePatchConfig struct {
	role               string
	scope              domain.TaskRepositoryScope
	unchangedObstacles bool
}

type resourcePatchFixture struct {
	db           *sqlite.Store
	task         domain.Task
	snapshot     domain.TaskResourceSnapshot
	root         string
	originals    map[string]string
	artifactRoot string
	resolver     app.TaskResourceWorkspaceResolver
	materializer app.ResourceReviewPatchMaterializer
}

func newResourcePatchFixture(t *testing.T, configs ...resourcePatchConfig) *resourcePatchFixture {
	t.Helper()
	db := openTaskWorktreeResolverStore(t)
	t.Cleanup(func() { _ = db.Close() })
	now := time.Date(2026, 9, 8, 16, 0, 0, 0, time.UTC)
	room, task := insertResolverRoomWithOwnership(t, db, now, domain.RoomOwnershipProject)
	resources := make([]domain.TaskRepositoryResource, 0, len(configs))
	records := make([]domain.RepositoryRecord, 0, len(configs))
	originals := make(map[string]string, len(configs))
	for index, config := range configs {
		checkout, _, _ := newTaskWorktreeTestRepository(t)
		if config.unchangedObstacles {
			large, err := os.OpenFile(filepath.Join(checkout, "large.bin"), os.O_CREATE|os.O_WRONLY, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			if err := large.Truncate(int64(domain.RepositoryTextBytes + 1)); err != nil {
				_ = large.Close()
				t.Fatal(err)
			}
			if err := large.Close(); err != nil {
				t.Fatal(err)
			}
			writeResourcePatchFile(t, checkout, "vendor/blob.bin", []byte{'a', 0, 'b'})
			writeResourcePatchFile(t, checkout, "pointer.lfs", []byte("version https://git-lfs.github.com/spec/v1\noid sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\nsize 1\n"))
			if err := os.Symlink("README.md", filepath.Join(checkout, "unchanged-link")); err != nil {
				t.Fatal(err)
			}
			runTaskWorktreeGitTest(t, checkout, "add", ".")
			runTaskWorktreeGitTest(t, checkout, "-c", "user.name=Chora Test", "-c", "user.email=chora@example.test", "commit", "-m", "unsupported unchanged fixtures")
		}
		inspection, err := (gitsource.Default{}).Inspect(context.Background(), checkout)
		if err != nil {
			t.Fatal(err)
		}
		record := domain.RepositoryRecord{
			ID: domain.NewRepositoryID(), Name: "patch-repository-" + string(rune('a'+index)), Checkout: inspection.CanonicalPath,
			CommonGitDir: inspection.CommonGitDir, PhysicalIdentity: inspection.PhysicalIdentity, IdentitySource: "inspected", CreatedAt: now,
		}
		scope := config.scope
		if scope.Mode == "" {
			scope = domain.TaskRepositoryScope{Mode: "repository", MigrationChoice: "not_needed"}
		}
		resources = append(resources, domain.TaskRepositoryResource{
			RepoID: record.ID.String(), Name: record.Name, Checkout: record.Checkout, CommonGitDir: record.CommonGitDir,
			PhysicalIdentity: record.PhysicalIdentity, Role: config.role, BaseCommit: inspection.HeadCommit, BaseTree: inspection.RootTree,
			AssociationVersion: 1, Scope: scope, Checks: domain.TaskCheckPolicy{Mode: "none"},
		})
		records = append(records, record)
		originals[record.ID.String()] = record.Checkout
	}
	sort.Slice(resources, func(i, j int) bool { return resources[i].RepoID < resources[j].RepoID })
	snapshot := domain.TaskResourceSnapshot{
		SchemaVersion: domain.TaskResourceSchemaV2, TaskID: task.ID().String(), ProjectID: room.ProjectID().String(), RoomID: room.ID().String(),
		SelectionSource: "room_explicit", Resources: resources,
	}
	canonical, digest, err := snapshot.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if err := db.WithinWriteTx(context.Background(), func(tx storecontract.WriteTx) error {
		for _, record := range records {
			if err := tx.InsertRepository(context.Background(), record); err != nil {
				return err
			}
			if err := tx.SaveProjectRepository(context.Background(), 0, domain.ProjectRepository{
				ProjectID: room.ProjectID(), Repository: record, State: "active", Version: 1, AddedAt: now, UpdatedAt: now,
			}); err != nil {
				return err
			}
		}
		return tx.InsertTaskResourceSnapshot(context.Background(), storecontract.TaskResourceRecord{TaskID: task.ID(), CanonicalJSON: canonical, Digest: digest, CreatedAt: now})
	}); err != nil {
		t.Fatal(err)
	}
	dataRoot := canonicalTempDir(t)
	resolver, err := newTaskResourceWorkspaceManager(db, dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := resolver.Ensure(context.Background(), task.ID()); err != nil {
		t.Fatal(err)
	}
	root, err := resolver.ResolveExecutionRoot(context.Background(), task.ID())
	if err != nil {
		t.Fatal(err)
	}
	artifactRoot := filepath.Join(canonicalTempDir(t), "resource-patches")
	materializer, err := newResourcePatchStore(artifactRoot, db.Reader(), resolver)
	if err != nil {
		t.Fatal(err)
	}
	return &resourcePatchFixture{
		db: db, task: task, snapshot: snapshot, root: root, originals: originals,
		artifactRoot: artifactRoot, resolver: resolver, materializer: materializer,
	}
}

func canonicalTempDir(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func writeResourcePatchFile(t *testing.T, root, relative string, data []byte) {
	t.Helper()
	path := filepath.Join(root, relative)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func bytesEqual(first, second []byte) bool {
	return sha256.Sum256(first) == sha256.Sum256(second) && len(first) == len(second)
}

func TestCollectResourceReviewPathsStreams4096LongPathsAndRejectsOverflow(t *testing.T) {
	root, _, base := newTaskWorktreeTestRepository(t)
	for index := 0; index < domain.TaskChangedEntryLimit; index++ {
		name := fmt.Sprintf("%s-%04d.txt", strings.Repeat("p", 150), index)
		if err := os.WriteFile(filepath.Join(root, name), []byte("new\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	paths, untracked, err := collectResourceReviewPaths(context.Background(), root, base, domain.TaskChangedEntryLimit)
	if err != nil || len(paths) != domain.TaskChangedEntryLimit || len(untracked) != domain.TaskChangedEntryLimit {
		t.Fatalf("legal long-path collection=%d/%d err=%v", len(paths), len(untracked), err)
	}
	if err := os.WriteFile(filepath.Join(root, "overflow.txt"), []byte("new\n"), 0600); err != nil {
		t.Fatal(err)
	}
	paths, _, err = collectResourceReviewPaths(context.Background(), root, base, domain.TaskChangedEntryLimit)
	if err == nil || len(paths) != 0 || !strings.Contains(err.Error(), "path limit") {
		t.Fatalf("overflow returned partial success: %d %v", len(paths), err)
	}
}
