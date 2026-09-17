package desktop

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	storesqlite "github.com/Yangyang96/chora/internal/store/sqlite"
	_ "modernc.org/sqlite"
)

func TestActivityReadsCurrentMigratedDatabase(t *testing.T) {
	root := filepath.Join(canonicalTemp(t), "data")
	store, err := storesqlite.Open(context.Background(), filepath.Join(root, "chora.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	report, err := Activity(context.Background(), root)
	if err != nil || !report.Idle {
		t.Fatalf("report=%+v err=%v", report, err)
	}
}

func TestActivityFailsClosedAndFindsDurableWork(t *testing.T) {
	root := filepath.Join(canonicalTemp(t), "data")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(root, "chora.db"))
	if err != nil {
		t.Fatal(err)
	}
	schema := `CREATE TABLE runs(state TEXT); CREATE TABLE attempts(id TEXT,state TEXT); CREATE TABLE verification_runs(state TEXT); CREATE TABLE verification_attempts(state TEXT); CREATE TABLE task_delivery_operations(state TEXT); CREATE TABLE apply_repository_steps(operation_id TEXT,repo_id TEXT,sequence INTEGER,status TEXT); CREATE TABLE checks(attempt_id TEXT,status TEXT); CREATE TABLE check_invocations(attempt_id TEXT);`
	if _, err = db.Exec(schema); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	report, err := Activity(context.Background(), root)
	if err != nil || !report.Idle {
		t.Fatalf("idle=%+v err=%v", report, err)
	}
	db, err = sql.Open("sqlite", filepath.Join(root, "chora.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO attempts(state) VALUES('running')`); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	report, err = Activity(context.Background(), root)
	if err != nil || report.Idle || len(report.Reasons) != 1 || report.Reasons[0].Kind != "agent_attempt" {
		t.Fatalf("active=%+v err=%v", report, err)
	}
}

func TestActivityRejectsUnknownPreviewManifest(t *testing.T) {
	root := filepath.Join(canonicalTemp(t), "data")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(root, "chora.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`CREATE TABLE runs(state TEXT); CREATE TABLE attempts(id TEXT,state TEXT); CREATE TABLE verification_runs(state TEXT); CREATE TABLE verification_attempts(state TEXT); CREATE TABLE task_delivery_operations(state TEXT); CREATE TABLE apply_repository_steps(operation_id TEXT,repo_id TEXT,sequence INTEGER,status TEXT); CREATE TABLE checks(attempt_id TEXT,status TEXT); CREATE TABLE check_invocations(attempt_id TEXT);`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	dir := filepath.Join(root, "runtime", "app-previews", "x")
	if err = os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(`{"schema":"unknown","owner":"unknown","view":{"state":"stopped"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = Activity(context.Background(), root); err == nil {
		t.Fatal("accepted unknown preview manifest")
	}
}
