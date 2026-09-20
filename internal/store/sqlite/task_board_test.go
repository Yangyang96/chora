package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type boardCountingQuery struct {
	queryer
	queries int
}

func (q *boardCountingQuery) QueryContext(ctx context.Context, s string, args ...any) (*sql.Rows, error) {
	q.queries++
	return q.queryer.QueryContext(ctx, s, args...)
}
func (q *boardCountingQuery) QueryRowContext(ctx context.Context, s string, args ...any) *sql.Row {
	q.queries++
	return q.queryer.QueryRowContext(ctx, s, args...)
}
func TestTaskBoardBulkScale(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	db, e := openLatestInternalSQLiteTestStore(ctx, filepath.Join(root, "board.db"), nil)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	now := time.Now().UTC()
	pid := domain.NewProjectID()
	rooms := []domain.Room{}
	for i := 0; i < 10; i++ {
		room, e := domain.NewRoom(domain.RoomParams{ID: domain.NewRoomID(), ProjectID: pid, OwnershipKind: domain.RoomOwnershipProject, Name: fmt.Sprintf("Room %d", i), WorkspaceRoot: t.TempDir(), CreatedAt: now, UpdatedAt: now})
		if e != nil {
			t.Fatal(e)
		}
		rooms = append(rooms, room)
	}
	p, e := domain.NewProject(domain.ProjectParams{ID: pid, Name: "Scale", DefaultRoomID: rooms[0].ID(), CreatedAt: now, UpdatedAt: now})
	if e != nil {
		t.Fatal(e)
	}
	e = db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		if e := tx.InsertProject(ctx, p); e != nil {
			return e
		}
		for _, r := range rooms {
			if e := tx.InsertRoom(ctx, r); e != nil {
				return e
			}
		}
		for i := 0; i < 1000; i++ {
			criterion, _ := domain.NewAcceptanceCriterion(domain.NewCriterionID(), "done", "")
			task, _ := domain.NewTask(domain.NewTaskID(), rooms[i%len(rooms)].ID(), fmt.Sprintf("Task %d", i), "Goal", []domain.AcceptanceCriterion{criterion})
			if e := tx.InsertTask(ctx, task); e != nil {
				return e
			}
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	tx, e := db.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if e != nil {
		t.Fatal(e)
	}
	counter := &boardCountingQuery{queryer: tx}
	start := time.Now()
	snapshot, e := (&reader{q: counter}).GetTaskBoardSnapshot(ctx, pid)
	elapsed := time.Since(start)
	tx.Rollback()
	if e != nil {
		t.Fatal(e)
	}
	if len(snapshot.Tasks) != 1000 || len(snapshot.Rooms) != 10 || counter.queries > 12 {
		t.Fatalf("tasks=%d rooms=%d queries=%d", len(snapshot.Tasks), len(snapshot.Rooms), counter.queries)
	}
	service := app.NewService(app.Dependencies{Store: db})
	start = time.Now()
	view, e := service.GetTaskBoard(ctx, pid, app.TaskBoardQuery{})
	apiElapsed := time.Since(start)
	if e != nil {
		t.Fatal(e)
	}
	if len(view.Cards) != 50 || view.Total != 1000 || view.Counts.Reconciliation != 1000 || view.NextCursor == "" {
		t.Fatalf("incomplete counts: %+v", view)
	}
	payload, _ := json.Marshal(view)
	t.Logf("fixture: 1000 Tasks / 10 Rooms, no Run history; summary queries=%d read=%s first-page=%s payload=%d bytes cards=%d total=%d", counter.queries, elapsed, apiElapsed, len(payload), len(view.Cards), view.Total)
}

// Corruption fixtures bypass application writers while retaining the real
// schema, foreign keys and immutability triggers. One malformed summary must
// not make another task disappear or make the entire Project unavailable.
func TestTaskBoardMalformedEvidenceIsolatedToItsTask(t *testing.T) {
	cases := []string{"baseline", "trusted_local", "isolated_local", "closure_json", "delivery_preview_json", "delivery_outcome_json", "snapshot_taskId", "snapshot_roomId", "snapshot_projectId", "snapshot_repoId", "result_schemaVersion", "result_outcome", "result_id", "result_taskId", "result_runId", "result_attemptId", "result_resourceSnapshotDigest", "result_repositories"}
	for _, testCase := range cases {
		t.Run(testCase, func(t *testing.T) {
			isBaseline := testCase == "baseline" || testCase == "trusted_local" || testCase == "isolated_local"
			ctx := context.Background()
			db, err := openLatestInternalSQLiteTestStore(ctx, filepath.Join(t.TempDir(), "private", "board.db"), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			now := time.Now().UTC()
			pid := domain.NewProjectID()
			rid := domain.NewRoomID()
			room, _ := domain.NewRoom(domain.RoomParams{ID: rid, ProjectID: pid, OwnershipKind: domain.RoomOwnershipProject, Name: "Board", WorkspaceRoot: t.TempDir(), CreatedAt: now, UpdatedAt: now})
			project, _ := domain.NewProject(domain.ProjectParams{ID: pid, Name: "Board", DefaultRoomID: rid, CreatedAt: now, UpdatedAt: now})
			makeTask := func(name string) domain.Task {
				criterion, _ := domain.NewAcceptanceCriterion(domain.NewCriterionID(), "done", "")
				task, _ := domain.NewTask(domain.NewTaskID(), rid, name, "Goal", []domain.AcceptanceCriterion{criterion})
				return task
			}
			good, bad := makeTask("Independent task"), makeTask("Evidence under test")
			err = db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
				if e := tx.InsertProject(ctx, project); e != nil {
					return e
				}
				if e := tx.InsertRoom(ctx, room); e != nil {
					return e
				}
				if e := tx.InsertTask(ctx, good); e != nil {
					return e
				}
				return tx.InsertTask(ctx, bad)
			})
			if err != nil {
				t.Fatal(err)
			}
			exec := func(query string, args ...any) {
				t.Helper()
				if _, e := db.db.ExecContext(ctx, query, args...); e != nil {
					t.Fatalf("fixture SQL: %v", e)
				}
			}
			at := timeText(now)
			runID := domain.NewRunID().String()
			attemptID := domain.NewAttemptID().String()
			resultID := domain.NewResultID().String()
			repoID := domain.NewRepositoryID().String()
			charterID := domain.NewCharterID().String()
			contextID := domain.NewContextSnapshotID().String()
			digest := make([]byte, 32)
			exec(`INSERT INTO context_snapshots(id,digest,canonical_json,markdown,created_at) VALUES(?,?,?,'',?)`, contextID, digest, []byte(`{}`), at)
			profile := domain.AgentExecutionProfileContract{RuntimeAdapterID: "test"}
			if testCase == "trusted_local" || testCase == "isolated_local" {
				profile, err = domain.AgentExecutionProfileContractFor(domain.AgentExecutionProfile(testCase))
				if err != nil {
					t.Fatal(err)
				}
			}
			for _, task := range []domain.Task{good, bad} {
				cid := charterID
				run := runID
				state := "completed"
				attemptNumber := 1
				if task.ID() == good.ID() {
					cid = domain.NewCharterID().String()
					run = domain.NewRunID().String()
					state = "ready"
					attemptNumber = 0
				}
				exec(`INSERT INTO run_charters(id,task_id,task_goal,workspace_root,adapter_id,sandbox_mode,expected_output,responsible_human,initiator,created_at,agent_execution_profile,agent_runtime_source,agent_execution_provider,agent_capability_policy,agent_trust_disclosure_policy) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, cid, task.ID().String(), "goal", room.WorkspaceRoot(), profile.RuntimeAdapterID, "workspace", "patch", "owner", "test", at, string(profile.Profile), profile.RuntimeSource, profile.ExecutionProvider, profile.CapabilityPolicy, profile.TrustDisclosurePolicy)
				exec(`INSERT INTO runs(id,task_id,charter_id,state,version,current_attempt_number,created_at,updated_at) VALUES(?,?,?,?,1,?,?,?)`, run, task.ID().String(), cid, state, attemptNumber, at, at)
				draft, revision, review := domain.NewTechnicalPlanDraftID().String(), domain.NewTechnicalPlanRevisionID().String(), domain.NewTechnicalPlanReviewID().String()
				exec(`INSERT INTO technical_plan_drafts(id,task_id,edit_version,next_revision_number,selection_digest,content_json,created_at,updated_at) VALUES(?,?,1,1,?,'{}',?,?)`, draft, task.ID().String(), digest, at, at)
				exec(`INSERT INTO technical_plan_revisions(id,task_id,source_draft_id,revision_number,content_json,content_digest,selection_digest,unchanged,submitted_at) VALUES(?,?,?,1,'{}',?,?,0,?)`, revision, task.ID().String(), draft, digest, digest, at)
				exec(`UPDATE technical_plan_drafts SET closed_at=?,submitted_revision_id=? WHERE id=?`, at, revision, draft)
				exec(`INSERT INTO technical_plan_reviews(id,revision_id,task_id,kind,reviewer,note,decided_at) VALUES(?,?,?,'accept','owner','accepted',?)`, review, revision, task.ID().String(), at)
				exec(`INSERT INTO technical_plan_acceptance_bindings(review_id,task_id,revision_id,snapshot_id,snapshot_digest,bound_at) VALUES(?,?,?,?,?,?)`, review, task.ID().String(), revision, contextID, digest, at)
				exec(`INSERT INTO current_technical_plan_acceptances(task_id,revision_id) VALUES(?,?)`, task.ID().String(), revision)
				exec(`INSERT INTO technical_plan_run_bindings(run_id,task_id,revision_id,charter_id,snapshot_id,snapshot_digest,bound_at) VALUES(?,?,?,?,?,?,?)`, run, task.ID().String(), revision, cid, contextID, digest, at)

			}
			exec(`INSERT INTO attempts(id,run_id,sequence,context_snapshot_id,context_digest,adapter_id,state,created_at,agent_execution_profile,agent_runtime_source,agent_execution_provider,agent_capability_policy,agent_trust_disclosure_policy) VALUES(?,?,1,?,?,?,'output_submitted',?,?,?,?,?,?)`, attemptID, runID, contextID, digest, profile.RuntimeAdapterID, at, string(profile.Profile), profile.RuntimeSource, profile.ExecutionProvider, profile.CapabilityPolicy, profile.TrustDisclosurePolicy)
			exec(`INSERT INTO repositories(repo_id,name,checkout,identity_source,created_at) VALUES(?,?,?,'legacy_unverified',?)`, repoID, "Repository", t.TempDir(), at)
			exec(`INSERT INTO project_repositories(project_id,repo_id,state,version,added_at,updated_at) VALUES(?,?,'active',1,?,?)`, pid.String(), repoID, at, at)
			resource := map[string]any{"repoId": repoID, "name": "Repository", "role": "write", "baseCommit": "base", "baseTree": "tree", "checks": map[string]any{"mode": "none"}}
			snapshot := map[string]any{"schemaVersion": "chora.task-resources.v2", "taskId": bad.ID().String(), "roomId": rid.String(), "projectId": pid.String(), "resources": []any{resource}}
			group := map[string]any{"schemaVersion": "chora.result-group.v2", "id": resultID, "taskId": bad.ID().String(), "runId": runID, "attemptId": attemptID, "resourceSnapshotDigest": fmt.Sprintf("%x", digest), "outcome": "completed_no_change", "repositories": []any{map[string]any{"repoId": repoID}}}
			if strings.HasPrefix(testCase, "snapshot_") {
				key := strings.TrimPrefix(testCase, "snapshot_")
				if key == "repoId" {
					delete(resource, key)
				} else {
					delete(snapshot, key)
				}
			}
			if strings.HasPrefix(testCase, "result_") {
				delete(group, strings.TrimPrefix(testCase, "result_"))
			}
			encode := func(value any) []byte {
				b, e := json.Marshal(value)
				if e != nil {
					t.Fatal(e)
				}
				return b
			}
			exec(`INSERT INTO task_resource_snapshots(task_id,canonical_json,digest,created_at) VALUES(?,?,?,?)`, bad.ID().String(), encode(snapshot), digest, at)
			exec(`INSERT INTO resource_result_groups(result_id,run_id,task_id,attempt_id,canonical_json,digest,created_at) VALUES(?,?,?,?,?,?,?)`, resultID, runID, bad.ID().String(), attemptID, encode(group), digest, at)
			change := map[string]any{"repoId": repoID, "baseCommit": "base", "baseTree": "tree", "changedPaths": []string{}, "checks": map[string]any{"Mode": "none", "Status": "UNKNOWN"}}
			exec(`INSERT INTO result_repository_changes(result_id,repo_id,canonical_json,digest,created_at) VALUES(?,?,?,?,?)`, resultID, repoID, encode(change), digest, at)
			if testCase == "closure_json" {
				exec(`INSERT INTO result_closures(result_id,run_id,task_id,attempt_id,result_digest,preview_digest,review_id,repository_ids,actor_id,session_id,created_at) VALUES(?,?,?,?,?,?,'review',?,'owner','session',?)`, resultID, runID, bad.ID().String(), attemptID, digest, digest, []byte(`{"broken"`), at)
			}
			if strings.HasPrefix(testCase, "delivery_") {
				preview, outcome := []byte(`{}`), []byte(`{}`)
				if testCase == "delivery_preview_json" {
					preview = []byte(`{"broken"`)
				} else {
					outcome = []byte(`{"broken"`)
				}
				exec(`INSERT INTO task_delivery_operations(operation_id,task_id,run_id,repo_id,kind,state,version,request_digest,preview_json,outcome_json,created_at,updated_at) VALUES('broken-operation',?,?,?,'cleanup','failed',1,?,?,?,?,?)`, bad.ID().String(), runID, repoID, digest, preview, outcome, at, at)
			}
			view, e := app.NewService(app.Dependencies{Store: db}).GetTaskBoard(ctx, pid, app.TaskBoardQuery{})
			if e != nil {
				t.Fatalf("one corrupt task made Project unavailable: %v", e)
			}
			if view.Total != 2 || len(view.Cards) != 2 {
				t.Fatalf("lost a task: %+v", view)
			}
			for _, card := range view.Cards {
				if card.TaskID == good.ID().String() {
					if card.Phase == nil || *card.Phase != "preparing" || card.Health != "current" {
						t.Fatalf("unrelated task unusable: %+v", card)
					}
				} else if isBaseline {
					if card.Phase == nil || *card.Phase != "finished" || card.Outcome == nil || *card.Outcome != "no_change" {
						t.Fatalf("fixture has no valid baseline: %+v", card)
					}
				} else {
					if card.Phase != nil || card.Attention.State != "unknown" || card.Substate != "needs_reconciliation" {
						t.Fatalf("missing or invalid evidence was trusted: %+v", card)
					}
				}
			}
			want := 1
			if isBaseline {
				want = 0
			}
			if view.Counts.Reconciliation != want {
				t.Fatalf("reconciliation count=%d want=%d", view.Counts.Reconciliation, want)
			}
		})
	}
}
