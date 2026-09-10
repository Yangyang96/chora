package sqlite_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/store/sqlite"
)

func TestRepositoryPendingWorkUsesExactClosureAndPreservesWritesAndDelivery(t *testing.T) {
	ctx := context.Background()
	db, seeded := openSeeded(t)
	now := timeTextForPendingTest(seeded.now)
	repo := domain.NewRepositoryID()
	otherRepo := domain.NewRepositoryID()
	result := domain.NewResultID()
	attempt := domain.NewAttemptID()
	digest := sha256.Sum256([]byte("immutable-result"))
	preview := sha256.Sum256([]byte("closure-preview"))
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", seeded.path)
	if err != nil {
		t.Fatal(err)
	}
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO repositories(repo_id,name,checkout,common_git_dir,physical_identity,identity_source,created_at) VALUES(?,?,?,?,?,'inspected',?)`, []any{repo.String(), "closed repo", filepath.Join(t.TempDir(), "repo"), filepath.Join(t.TempDir(), "git"), strings.Repeat("a", 64), now}},
		{`INSERT INTO repositories(repo_id,name,checkout,common_git_dir,physical_identity,identity_source,created_at) VALUES(?,?,?,?,?,'inspected',?)`, []any{otherRepo.String(), "open repo", filepath.Join(t.TempDir(), "repo"), filepath.Join(t.TempDir(), "git"), strings.Repeat("b", 64), now}},
		{`INSERT INTO task_resource_snapshots(task_id,canonical_json,digest,created_at) VALUES(?,?,?,?)`, []any{seeded.task.ID().String(), []byte(`{}`), digest[:], now}},
		{`INSERT INTO task_repository_worktrees(task_id,repo_id,path,root_fingerprint,base_commit,base_tree,state,version,created_at,updated_at) VALUES(?,?,?,?,?,?,'ready',1,?,?)`, []any{seeded.task.ID().String(), repo.String(), filepath.Join(t.TempDir(), "worktree"), strings.Repeat("c", 64), strings.Repeat("1", 40), strings.Repeat("2", 40), now, now}},
		{`INSERT INTO task_repository_worktrees(task_id,repo_id,path,root_fingerprint,base_commit,base_tree,state,version,created_at,updated_at) VALUES(?,?,?,?,?,?,'ready',1,?,?)`, []any{seeded.task.ID().String(), otherRepo.String(), filepath.Join(t.TempDir(), "worktree"), strings.Repeat("d", 64), strings.Repeat("3", 40), strings.Repeat("4", 40), now, now}},
		{`UPDATE runs SET state='accepted' WHERE id=?`, []any{seeded.run.ID().String()}},
		{`INSERT INTO resource_result_groups(result_id,run_id,task_id,attempt_id,canonical_json,digest,created_at) VALUES(?,?,?,?,?,?,?)`, []any{result.String(), seeded.run.ID().String(), seeded.task.ID().String(), attempt.String(), []byte(`{}`), digest[:], now}},
		{`INSERT INTO result_repository_changes(result_id,repo_id,canonical_json,digest,created_at) VALUES(?,?,?, ?,?)`, []any{result.String(), repo.String(), []byte(`{"changedPaths":["closed.txt"]}`), digest[:], now}},
		{`INSERT INTO result_repository_changes(result_id,repo_id,canonical_json,digest,created_at) VALUES(?,?,?, ?,?)`, []any{result.String(), otherRepo.String(), []byte(`{"changedPaths":["open.txt"]}`), digest[:], now}},
		{`INSERT INTO result_closures(result_id,run_id,task_id,attempt_id,result_digest,preview_digest,review_id,repository_ids,actor_id,session_id,created_at) VALUES(?,?,?,?,?,?,?,?,'actor','session',?)`, []any{result.String(), seeded.run.ID().String(), seeded.task.ID().String(), attempt.String(), digest[:], preview[:], "", []byte(`["` + repo.String() + `"]`), now}},
	}
	for _, statement := range statements {
		if _, err = raw.Exec(statement.query, statement.args...); err != nil {
			raw.Close()
			t.Fatalf("fixture statement failed: %v", err)
		}
	}
	if err = raw.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = sqlite.Open(ctx, seeded.path)
	if err != nil {
		t.Fatal(err)
	}
	closedPending, err := db.Reader().RepositoryHasPendingWork(ctx, repo)
	if err != nil || closedPending {
		t.Fatalf("exact closed repo pending=%v err=%v", closedPending, err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err = sql.Open("sqlite", seeded.path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = raw.Exec(`UPDATE runs SET state='running' WHERE id=?`, seeded.run.ID().String()); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if err = raw.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = sqlite.Open(ctx, seeded.path)
	if err != nil {
		t.Fatal(err)
	}
	active, err := db.Reader().RepositoryHasPendingWork(ctx, repo)
	if err != nil || !active {
		t.Fatalf("closed repo with active Run pending=%v err=%v", active, err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err = sql.Open("sqlite", seeded.path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = raw.Exec(`UPDATE runs SET state='accepted' WHERE id=?`, seeded.run.ID().String()); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if err = raw.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = sqlite.Open(ctx, seeded.path)
	if err != nil {
		t.Fatal(err)
	}
	openPending, err := db.Reader().RepositoryHasPendingWork(ctx, otherRepo)
	if err != nil || !openPending {
		t.Fatalf("other unclosed repo pending=%v err=%v", openPending, err)
	}

	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err = sql.Open("sqlite", seeded.path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = raw.Exec(`INSERT INTO apply_operations(operation_id,result_id,canonical_json,created_at) VALUES('apply',?,X'7b7d',?)`, result.String(), now); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if _, err = raw.Exec(`INSERT INTO apply_repository_steps(operation_id,repo_id,sequence,status,canonical_json,created_at) VALUES('apply',?,1,'uncertain',X'7b7d',?)`, repo.String(), now); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if err = raw.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = sqlite.Open(ctx, seeded.path)
	if err != nil {
		t.Fatal(err)
	}
	uncertain, err := db.Reader().RepositoryHasPendingWork(ctx, repo)
	if err != nil || !uncertain {
		t.Fatalf("closed repo with uncertain Apply pending=%v err=%v", uncertain, err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err = sql.Open("sqlite", seeded.path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = raw.Exec(`INSERT INTO apply_repository_steps(operation_id,repo_id,sequence,status,canonical_json,created_at) VALUES('apply',?,2,'applied',X'7b7d',?)`, repo.String(), now); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if err = raw.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = sqlite.Open(ctx, seeded.path)
	if err != nil {
		t.Fatal(err)
	}
	afterApply, err := db.Reader().RepositoryHasPendingWork(ctx, repo)
	if err != nil || afterApply {
		t.Fatalf("closed repo after resolved Apply pending=%v err=%v", afterApply, err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err = sql.Open("sqlite", seeded.path)
	if err != nil {
		t.Fatal(err)
	}
	request := sha256.Sum256([]byte("delivery"))
	if _, err = raw.Exec(`INSERT INTO task_delivery_operations(operation_id,task_id,run_id,repo_id,kind,state,version,request_digest,preview_json,outcome_json,created_at,updated_at) VALUES('delivery',?,?,?,'commit','succeeded',1,?,X'7b7d',X'7b7d',?,?)`, seeded.task.ID().String(), seeded.run.ID().String(), repo.String(), request[:], now, now); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if err = raw.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = sqlite.Open(ctx, seeded.path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	pendingDelivery, err := db.Reader().RepositoryHasPendingWork(ctx, repo)
	if err != nil || !pendingDelivery {
		t.Fatalf("committed unmerged delivery pending=%v err=%v", pendingDelivery, err)
	}
}

func timeTextForPendingTest(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}
