package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

type boardResourceReadMeasurement struct {
	queries int
	elapsed time.Duration
}
type boardResourceMeasuredStore struct {
	storecontract.Store
	db    *Store
	reads []boardResourceReadMeasurement
}

func (s *boardResourceMeasuredStore) Reader() storecontract.Reader {
	return &boardResourceMeasuredReader{Reader: s.Store.Reader(), owner: s}
}
func (s *boardResourceMeasuredStore) WithinWriteTx(context.Context, func(storecontract.WriteTx) error) error {
	panic("board read attempted an application write")
}

type boardResourceMeasuredReader struct {
	storecontract.Reader
	owner *boardResourceMeasuredStore
}
type boardResourceReadOnlyQuery struct{ *boardCountingQuery }

func (q *boardResourceReadOnlyQuery) ExecContext(context.Context, string, ...any) (sql.Result, error) {
	panic("board read attempted SQL mutation")
}
func (r *boardResourceMeasuredReader) GetTaskBoardSnapshot(ctx context.Context, id domain.ProjectID) (storecontract.TaskBoardSnapshot, error) {
	tx, err := r.owner.db.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return storecontract.TaskBoardSnapshot{}, err
	}
	defer tx.Rollback()
	count := &boardCountingQuery{queryer: tx}
	start := time.Now()
	result, err := (&reader{q: &boardResourceReadOnlyQuery{count}}).GetTaskBoardSnapshot(ctx, id)
	r.owner.reads = append(r.owner.reads, boardResourceReadMeasurement{count.queries, time.Since(start)})
	return result, err
}

// This fixture populates the real SQLite schema, foreign keys and immutable
// lineage without invoking Git, a model, a worktree manager or hosting. Its
// generated file/check evidence is deliberately large enough to catch accidental
// result/log payload expansion; the board must return summary metadata only.
func TestTaskBoardRepositoryScaleRepeatedReads(t *testing.T) {
	const tasks = 120
	const roomCount = 6
	const repoCount = 8
	const repeats = 3
	const directoryRooms = 125
	const directoryRepos = 512
	ctx := context.Background()
	db, err := openLatestInternalSQLiteTestStore(ctx, filepath.Join(t.TempDir(), "private", "board.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	pid := seedBoardResourceScale(t, db, tasks, roomCount, repoCount, directoryRooms, directoryRepos)
	measured := &boardResourceMeasuredStore{Store: db, db: db}
	// No executor, model, Git/hosting adapter, materializer or filesystem manager
	// is installed. Writes through either application or query ports panic.
	service := app.NewService(app.Dependencies{Store: measured})
	var stable string
	for repeat := 0; repeat < repeats; repeat++ {
		start := time.Now()
		first, e := service.GetTaskBoard(ctx, pid, app.TaskBoardQuery{})
		if e != nil {
			t.Fatal(e)
		}
		firstElapsed := time.Since(start)
		if len(first.Cards) != 50 || first.NextCursor == "" {
			t.Fatalf("default page len=%d", len(first.Cards))
		}
		second, e := service.GetTaskBoard(ctx, pid, app.TaskBoardQuery{Cursor: first.NextCursor, Limit: 100})
		if e != nil {
			t.Fatal(e)
		}
		maxPage, e := service.GetTaskBoard(ctx, pid, app.TaskBoardQuery{Limit: 100})
		if e != nil {
			t.Fatal(e)
		}
		if len(second.Cards) != 70 || second.NextCursor != "" || len(maxPage.Cards) != 100 || maxPage.NextCursor == "" {
			t.Fatal("50/100 pagination bounds lost")
		}
		if stable != "" && stable != first.Snapshot {
			t.Fatal("repeated GET changed persisted snapshot")
		}
		stable = first.Snapshot
		for _, v := range []app.TaskBoardView{first, second, maxPage} {
			if v.Total != tasks || v.Counts.Phase["delivery"] != tasks || v.Counts.Attention != tasks || v.Counts.Reconciliation != 0 || len(v.Rooms) != 100 || len(v.Repositories) != 100 || v.NextOptionsCursor == "" || v.Snapshot != stable {
				t.Fatalf("page totals/facts drifted: total=%d counts=%+v", v.Total, v.Counts)
			}
		}
		seen := map[string]bool{}
		for _, card := range append(append([]app.TaskBoardCard{}, first.Cards...), second.Cards...) {
			if seen[card.TaskID] {
				t.Fatal("duplicate Task card")
			}
			seen[card.TaskID] = true
			if len(card.Repositories) != repoCount || card.Phase == nil || *card.Phase != "delivery" || card.Outcome != nil {
				t.Fatalf("repository aggregation lost: %+v", card)
			}
			want := map[string]string{"Repository 0": "delivered", "Repository 1": "pending_merge", "Repository 2": "applied_locally", "Repository 3": "closed", "Repository 4": "no_change", "Repository 5": "pending_push", "Repository 6": "pending_commit", "Repository 7": "pending_commit"}
			for _, repo := range card.Repositories {
				if repo.Status != want[repo.Name] {
					t.Fatalf("%s=%s want=%s", repo.Name, repo.Status, want[repo.Name])
				}
				if repo.Name == "Repository 0" && (repo.DeliveryObservedAt == nil || len(repo.Achievements) != 4) {
					t.Fatalf("integrated achievement evidence lost: %+v", repo)
				}
				if repo.Name == "Repository 1" && (repo.DeliveryObservedAt == nil || len(repo.Achievements) != 3) {
					t.Fatalf("persisted PR observation lost: %+v", repo)
				}
			}
		}
		if len(seen) != tasks {
			t.Fatalf("unique tasks=%d", len(seen))
		}
		firstPayload, _ := json.Marshal(first)
		maxPayload, _ := json.Marshal(maxPage)
		// This is a fixture-specific regression ceiling, not a capacity or latency
		// SLA. It excludes the deliberately large check bodies and changed-path lists.
		if len(firstPayload) > 160000 || len(maxPayload) > 320000 || strings.Contains(string(maxPayload), "fixture-heavy-check-body") {
			t.Fatalf("summary payload expanded: 50=%d 100=%d", len(firstPayload), len(maxPayload))
		}
		for _, m := range measured.reads[repeat*3:] {
			if m.queries != 11 {
				t.Fatalf("repository data caused query fan-out: %d", m.queries)
			}
		}
		t.Logf("repeat=%d tasks=%d active-rooms=%d bound-repos/task=%d directory-rooms=125 directory-repos=512 bound-results=%d delivery-operations=%d hosting-observations=%d apply-steps=%d closures=%d queries/read=%d read50=%s request50=%s payload50=%d payload100=%d", repeat+1, tasks, roomCount, repoCount, tasks*repoCount, tasks*8, tasks*3, tasks, tasks, measured.reads[repeat*3].queries, measured.reads[repeat*3].elapsed, firstElapsed, len(firstPayload), len(maxPayload))
	}
	if len(measured.reads) != repeats*3 {
		t.Fatal("unexpected request count")
	}
}

func seedBoardResourceScale(t *testing.T, db *Store, taskCount, roomCount, repoCount, directoryRooms, directoryRepos int) domain.ProjectID {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC)
	at := timeText(now)
	pid := domain.NewProjectID()
	root := t.TempDir()
	rooms := make([]domain.Room, roomCount)
	repoIDs := make([]string, repoCount)
	for i := range rooms {
		var e error
		rooms[i], e = domain.NewRoom(domain.RoomParams{ID: domain.NewRoomID(), ProjectID: pid, OwnershipKind: domain.RoomOwnershipProject, Name: fmt.Sprintf("Room %d", i), WorkspaceRoot: filepath.Join(root, fmt.Sprintf("room-%d", i)), CreatedAt: now, UpdatedAt: now})
		if e != nil {
			t.Fatal(e)
		}
	}
	project, e := domain.NewProject(domain.ProjectParams{ID: pid, Name: "Resource board scale", DefaultRoomID: rooms[0].ID(), CreatedAt: now, UpdatedAt: now})
	if e != nil {
		t.Fatal(e)
	}
	taskValues := make([]domain.Task, taskCount)
	e = db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		if e := tx.InsertProject(ctx, project); e != nil {
			return e
		}
		for _, r := range rooms {
			if e := tx.InsertRoom(ctx, r); e != nil {
				return e
			}
		}
		for i := range taskValues {
			criterion, _ := domain.NewAcceptanceCriterion(domain.NewCriterionID(), "done", "")
			taskValues[i], _ = domain.NewTask(domain.NewTaskID(), rooms[i%roomCount].ID(), fmt.Sprintf("Task %03d", i), "Goal", []domain.AcceptanceCriterion{criterion})
			if e := tx.InsertTask(ctx, taskValues[i]); e != nil {
				return e
			}
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	tx, e := db.db.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, e := tx.ExecContext(ctx, query, args...); e != nil {
			t.Fatalf("scale fixture SQL: %v", e)
		}
	}
	encode := func(value any) []byte {
		b, e := json.Marshal(value)
		if e != nil {
			t.Fatal(e)
		}
		return b
	}
	for i := range repoIDs {
		repoIDs[i] = domain.NewRepositoryID().String()
		exec(`INSERT INTO repositories(repo_id,name,checkout,identity_source,created_at) VALUES(?,?,?,'legacy_unverified',?)`, repoIDs[i], fmt.Sprintf("Repository %d", i), filepath.Join(root, fmt.Sprintf("repo-%d", i)), at)
		exec(`INSERT INTO project_repositories(project_id,repo_id,state,version,added_at,updated_at) VALUES(?,?,'active',1,?,?)`, pid.String(), repoIDs[i], at, at)
	}
	contextID := domain.NewContextSnapshotID().String()
	contextBytes := []byte(`{}`)
	contextDigest := sha256.Sum256(contextBytes)
	exec(`INSERT INTO context_snapshots(id,digest,canonical_json,markdown,created_at) VALUES(?,?,?,'',?)`, contextID, contextDigest[:], contextBytes, at)
	for _, task := range taskValues {
		taskID := task.ID().String()
		runID := domain.NewRunID().String()
		attemptID := domain.NewAttemptID().String()
		charterID := domain.NewCharterID().String()
		resultID := domain.NewResultID().String()
		reviewID := domain.NewReviewDecisionID().String()
		exec(`INSERT INTO run_charters(id,task_id,task_goal,workspace_root,adapter_id,sandbox_mode,expected_output,responsible_human,initiator,created_at) VALUES(?,?,'goal',?,'test','workspace','patch','owner','test',?)`, charterID, taskID, root, at)
		exec(`INSERT INTO runs(id,task_id,charter_id,state,version,current_attempt_number,created_at,updated_at) VALUES(?,?,?,'accepted',2,1,?,?)`, runID, taskID, charterID, at, at)
		draft, revision, planReview := domain.NewTechnicalPlanDraftID().String(), domain.NewTechnicalPlanRevisionID().String(), domain.NewTechnicalPlanReviewID().String()
		exec(`INSERT INTO technical_plan_drafts(id,task_id,edit_version,next_revision_number,selection_digest,content_json,created_at,updated_at) VALUES(?,?,1,1,?,'{}',?,?)`, draft, taskID, contextDigest[:], at, at)
		exec(`INSERT INTO technical_plan_revisions(id,task_id,source_draft_id,revision_number,content_json,content_digest,selection_digest,unchanged,submitted_at) VALUES(?,?,?,1,'{}',?,?,0,?)`, revision, taskID, draft, contextDigest[:], contextDigest[:], at)
		exec(`UPDATE technical_plan_drafts SET closed_at=?,submitted_revision_id=? WHERE id=?`, at, revision, draft)
		exec(`INSERT INTO technical_plan_reviews(id,revision_id,task_id,kind,reviewer,note,decided_at) VALUES(?,?,?,'accept','owner','accepted',?)`, planReview, revision, taskID, at)
		exec(`INSERT INTO technical_plan_acceptance_bindings(review_id,task_id,revision_id,snapshot_id,snapshot_digest,bound_at) VALUES(?,?,?,?,?,?)`, planReview, taskID, revision, contextID, contextDigest[:], at)
		exec(`INSERT INTO current_technical_plan_acceptances(task_id,revision_id) VALUES(?,?)`, taskID, revision)
		exec(`INSERT INTO technical_plan_run_bindings(run_id,task_id,revision_id,charter_id,snapshot_id,snapshot_digest,bound_at) VALUES(?,?,?,?,?,?,?)`, runID, taskID, revision, charterID, contextID, contextDigest[:], at)
		exec(`INSERT INTO attempts(id,run_id,sequence,context_snapshot_id,context_digest,adapter_id,state,created_at) VALUES(?,?,1,?,?,'test','output_submitted',?)`, attemptID, runID, contextID, contextDigest[:], at)
		resources := []map[string]any{}
		changes := []map[string]any{}
		for j, repo := range repoIDs {
			role, mode := "write", "task_branch"
			if j == 2 {
				mode = ""
			}
			if j == 4 {
				role = "reference"
				mode = ""
			}
			resources = append(resources, map[string]any{"repoId": repo, "name": fmt.Sprintf("Repository %d", j), "role": role, "deliveryMode": mode, "baseCommit": "base", "baseTree": "tree", "baseRef": "refs/heads/main", "taskBranch": "chora/" + taskID + "/" + repo, "checks": map[string]any{"mode": "none"}})
			paths := []string{}
			if j != 4 {
				for k := 0; k < 64; k++ {
					paths = append(paths, fmt.Sprintf("src/generated-fixture-%03d.go", k))
				}
			}
			changes = append(changes, map[string]any{"repoId": repo, "baseCommit": "base", "baseTree": "tree", "changedPaths": paths, "checks": map[string]any{"Mode": "none", "Status": "UNKNOWN", "Explanation": strings.Repeat("fixture-heavy-check-body ", 256)}})
		}
		sort.Slice(resources, func(i, j int) bool { return resources[i]["repoId"].(string) < resources[j]["repoId"].(string) })
		sort.Slice(changes, func(i, j int) bool { return changes[i]["repoId"].(string) < changes[j]["repoId"].(string) })
		snapshotBytes := encode(map[string]any{"schemaVersion": "chora.task-resources.v2", "taskId": taskID, "roomId": task.RoomID().String(), "projectId": pid.String(), "resources": resources})
		snapshotDigest := sha256.Sum256(snapshotBytes)
		exec(`INSERT INTO task_resource_snapshots(task_id,canonical_json,digest,created_at) VALUES(?,?,?,?)`, taskID, snapshotBytes, snapshotDigest[:], at)
		groupBytes := encode(map[string]any{"schemaVersion": "chora.result-group.v2", "id": resultID, "taskId": taskID, "runId": runID, "attemptId": attemptID, "resourceSnapshotDigest": fmt.Sprintf("%x", snapshotDigest), "outcome": "review_ready", "repositories": changes})
		groupDigest := sha256.Sum256(groupBytes)
		exec(`INSERT INTO resource_result_groups(result_id,run_id,task_id,attempt_id,canonical_json,digest,created_at) VALUES(?,?,?,?,?,?,?)`, resultID, runID, taskID, attemptID, groupBytes, groupDigest[:], at)
		for _, change := range changes {
			b := encode(change)
			d := sha256.Sum256(b)
			exec(`INSERT INTO result_repository_changes(result_id,repo_id,canonical_json,digest,created_at) VALUES(?,?,?,?,?)`, resultID, change["repoId"], b, d[:], at)
		}
		exec(`INSERT INTO review_decisions(id,run_id,kind,expected_run_version,comment,decided_at) VALUES(?,?,'accept',1,'reviewed',?)`, reviewID, runID, at)
		exec(`INSERT INTO resource_result_reviews(result_id,result_digest,review_id) VALUES(?,?,?)`, resultID, groupDigest[:], reviewID)
		exec(`INSERT INTO result_closures(result_id,run_id,task_id,attempt_id,result_digest,preview_digest,review_id,repository_ids,actor_id,session_id,created_at) VALUES(?,?,?,?,?,?,?,?,'owner','session',?)`, resultID, runID, taskID, attemptID, groupDigest[:], contextDigest[:], reviewID, encode([]string{repoIDs[3]}), at)
		applyID := "apply-" + taskID
		exec(`INSERT INTO apply_operations(operation_id,result_id,canonical_json,created_at) VALUES(?,?,?,?)`, applyID, resultID, []byte(`{}`), at)
		exec(`INSERT INTO apply_repository_steps(operation_id,repo_id,sequence,status,canonical_json,created_at) VALUES(?,?,1,'applied',?,?)`, applyID, repoIDs[2], []byte(`{}`), at)
		for j := range repoIDs {
			var kinds []string
			switch j {
			case 0:
				kinds = []string{"commit", "push", "pr", "merge"}
			case 1:
				kinds = []string{"commit", "push", "pr"}
			case 5:
				kinds = []string{"commit"}
			}
			for k, kind := range kinds {
				opID := fmt.Sprintf("%s-%d-%s", taskID, j, kind)
				pr := map[string]any{"State": "open", "Head": "head", "HeadBranch": "branch", "BaseBranch": "main", "Repository": "fixture/repo", "Number": 1}
				if kind == "merge" {
					pr["State"] = "merged"
					pr["MergeCommit"] = "integrated"
				}
				intent := map[string]any{"ResultDigest": fmt.Sprintf("%x", groupDigest), "ReviewID": reviewID, "Binding": map[string]any{"BaseCommit": "base", "BaseTree": "tree", "TargetRef": "refs/heads/main", "Branch": "chora/" + taskID + "/" + repoIDs[j]}, "Hosting": map[string]any{"Head": "head", "HeadBranch": "branch", "BaseBranch": "main", "Repository": "fixture/repo"}}
				outcome := map[string]any{"Commit": "head"}
				if kind == "pr" || kind == "merge" {
					outcome["PR"] = pr
				}
				when := timeText(now.Add(time.Duration(k+1) * time.Second))
				exec(`INSERT INTO task_delivery_operations(operation_id,task_id,run_id,repo_id,kind,state,version,request_digest,preview_json,outcome_json,created_at,updated_at) VALUES(?,?,?,?,?,'succeeded',3,?,?,?,?,?)`, opID, taskID, runID, repoIDs[j], kind, contextDigest[:], encode(intent), encode(outcome), when, when)
				if j == 1 && kind == "pr" {
					for n := 1; n <= 3; n++ {
						observation := encode(map[string]any{"OperationID": opID, "RepoID": repoIDs[j], "PR": pr})
						observedAt := timeText(now.Add(time.Duration(10+n) * time.Second))
						exec(`INSERT INTO run_events(id,run_id,sequence,type,source,occurred_at,recorded_at,normalized_json) VALUES(?,?,?,'task_delivery.github.observed','app',?,?,?)`, domain.NewEventID().String(), runID, n, observedAt, observedAt, observation)
					}
				}
			}
		}
	}

	// Large Project directories are independently paged even when only a
	// bounded subset participates in any one Task's result.
	for i := roomCount; i < directoryRooms; i++ {
		exec(`INSERT INTO rooms(id,name,description,workspace_root,state,version,created_at,updated_at,project_id,ownership_kind) VALUES(?,?,'',?,'active',1,?,?,?,'project')`, domain.NewRoomID().String(), fmt.Sprintf("Directory room %d", i), filepath.Join(root, fmt.Sprintf("directory-room-%d", i)), at, at, pid.String())
	}
	for i := repoCount; i < directoryRepos; i++ {
		id := domain.NewRepositoryID().String()
		exec(`INSERT INTO repositories(repo_id,name,checkout,identity_source,created_at) VALUES(?,?,?,'legacy_unverified',?)`, id, fmt.Sprintf("Directory repository %d", i), filepath.Join(root, fmt.Sprintf("directory-repo-%d", i)), at)
		exec(`INSERT INTO project_repositories(project_id,repo_id,state,version,added_at,updated_at) VALUES(?,?,'active',1,?,?)`, pid.String(), id, at, at)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	return pid
}
