package sqlite_test

import (
	"context"
	"database/sql"
	"github.com/Yangyang96/chora/migrations"
	"strings"
	"testing"

	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
	"github.com/Yangyang96/chora/internal/store/sqlite"
)

func TestV35ReviewCommentMigrationPreservesReview(t *testing.T) {
	ctx := context.Background()
	db, seeded := openSeeded(t)
	var review domain.ReviewDecision
	if err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		_, decision, err := advanceRunToAccepted(ctx, tx, seeded.run, seeded.now)
		if err != nil {
			return err
		}
		review = decision
		return tx.InsertReview(ctx, decision)
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", seeded.path)
	if err != nil {
		t.Fatal(err)
	}
	// Reconstruct v35 only in the disposable fixture, with its valid review graph.
	base, err := migrations.Files.ReadFile("0034_task_first_resources.sql")
	if err != nil {
		t.Fatal(err)
	}
	trigger := string(base)[strings.Index(string(base), "CREATE TRIGGER task_repository_worktrees_transition"):]
	trigger = trigger[:strings.Index(trigger, "END;")+4]
	rollback := `DROP TABLE result_closures; DROP TABLE task_delivery_operations; DROP TABLE repository_delivery_defaults;
 DROP TRIGGER task_repository_worktrees_branch_authority_insert; DROP TRIGGER task_repository_worktrees_transition;
 ALTER TABLE task_repository_worktrees DROP COLUMN delivery_mode; ALTER TABLE task_repository_worktrees DROP COLUMN task_branch; ALTER TABLE task_repository_worktrees DROP COLUMN target_ref;`
	if _, err = raw.Exec(rollback + trigger + `ALTER TABLE review_decisions RENAME COLUMN comment TO reviewer_note; DELETE FROM schema_migrations WHERE version>=36;`); err != nil {
		t.Fatal(err)
	}
	raw.Close()
	upgraded, err := sqlite.Open(ctx, seeded.path)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	got, err := upgraded.Reader().GetReview(ctx, review.ID())
	if err != nil {
		t.Fatal(err)
	}
	if got.Comment() != review.Comment() || got.RunID() != review.RunID() || got.Kind() != review.Kind() || got.ExpectedRunVersion() != review.ExpectedRunVersion() || !got.DecidedAt().Equal(review.DecidedAt()) {
		t.Fatalf("review changed: %#v", got)
	}
	raw, err = sql.Open("sqlite", seeded.path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var count int
	if err = raw.QueryRow(`SELECT count(*) FROM pragma_table_info('review_decisions') WHERE name='reviewer_note'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("legacy column remains: %d %v", count, err)
	}
	var integrity string
	if err = raw.QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatalf("integrity: %s %v", integrity, err)
	}
}
