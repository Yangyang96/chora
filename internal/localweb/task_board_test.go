package localweb

import (
	"context"
	"fmt"
	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
	"net/http"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

func TestTaskBoardAPIEmptyScopePaginationAndReadOnly(t *testing.T) {
	db := openTaskWorktreeResolverStore(t)
	defer db.Close()
	service := app.NewService(app.Dependencies{Store: db, DataRoot: t.TempDir(), Authorizer: localAuthorizer{}})
	server := &Server{store: db, service: service}
	h := server.Handler()
	var p, other resourceProjectTestView
	requestJSON(t, h, http.MethodPost, "/api/v2/projects", map[string]any{"name": "Board"}, 201, &p)
	requestJSON(t, h, http.MethodPost, "/api/v2/projects", map[string]any{"name": "Other"}, 201, &other)
	url := "/api/v2/projects/" + p.ID + "/task-board"
	var board app.TaskBoardView
	requestJSON(t, h, http.MethodGet, url, nil, 200, &board)
	if board.Total != 0 || board.Cards == nil || len(board.Rooms) != 1 || board.Snapshot == "" {
		t.Fatalf("empty=%+v", board)
	}
	requestJSON(t, h, http.MethodGet, url+"?roomId="+other.DefaultRoomID, nil, 400, nil)
	requestJSON(t, h, http.MethodGet, url+"?repoId="+domain.NewRepositoryID().String(), nil, 400, nil)
	for _, query := range []string{"limit=101", "limit=0", "limit=-1", "phase=bogus", "phase=working%7Creview", "attention=bogus", "archived=bogus", "cursor=bogus"} {
		requestJSON(t, h, http.MethodGet, url+"?"+query, nil, 400, nil)
	}
	room, _ := domain.ParseRoomID(p.DefaultRoomID)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		criterion, _ := domain.NewAcceptanceCriterion(domain.NewCriterionID(), "done", "")
		task, _ := domain.NewTask(domain.NewTaskID(), room, "Example", "Goal", []domain.AcceptanceCriterion{criterion})
		if err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error { return tx.InsertTask(ctx, task) }); err != nil {
			t.Fatal(err)
		}
	}
	requestJSON(t, h, http.MethodGet, url+"?limit=2", nil, 200, &board)
	if board.Total != 3 || len(board.Cards) != 2 || board.Counts.Reconciliation != 3 || board.NextCursor == "" {
		t.Fatalf("page=%+v", board)
	}
	first := board.Cards[0].TaskID
	cursor := board.NextCursor
	snapshot := board.Snapshot
	var next app.TaskBoardView
	requestJSON(t, h, http.MethodGet, url+"?limit=2&cursor="+cursor, nil, 200, &next)
	if next.Total != 3 || len(next.Cards) != 1 || next.Cards[0].TaskID == first || next.NextCursor != "" || next.Snapshot != snapshot {
		t.Fatalf("next=%+v", next)
	}
	requestJSON(t, h, http.MethodGet, url+"?phase=reconciliation", nil, 200, &next)
	if next.Total != 3 {
		t.Fatal(next.Total)
	}
	requestJSON(t, h, http.MethodGet, url+"?attention=none", nil, 200, &next)
	if next.Total != 0 {
		t.Fatal(next.Total)
	}
	// Reads do not prepare a draft, create Runs, or mutate the observed facts.
	requestJSON(t, h, http.MethodGet, url+"?limit=2", nil, 200, &next)
	if next.Snapshot != snapshot {
		t.Fatal("GET mutated board facts")
	}
	criterion, _ := domain.NewAcceptanceCriterion(domain.NewCriterionID(), "done", "")
	task, _ := domain.NewTask(domain.NewTaskID(), room, "New", "Goal", []domain.AcceptanceCriterion{criterion})
	if err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error { return tx.InsertTask(ctx, task) }); err != nil {
		t.Fatal(err)
	}
	requestJSON(t, h, http.MethodGet, url+"?cursor="+cursor, nil, 409, nil)
}

func TestTaskBoardResourceResultAdapters(t *testing.T) {
	for _, kind := range []string{"none", "auto", "accept", "reject"} {
		t.Run(kind, func(t *testing.T) {
			scenario := resourceTerminalScenario{name: "board-" + kind, repositoryCount: 2, checkMode: "none", completeAssistant: true, wantRunState: domain.RunStateCompleted, futureDelivery: true, recordedSession: true}
			if kind == "auto" {
				scenario.checkMode = "auto"
			}
			if kind == "accept" || kind == "reject" {
				scenario.wantRunState = domain.RunStateAwaitingReview
				scenario.change = func(t *testing.T, root string, resources []domain.TaskRepositoryResource) {
					for _, r := range resources {
						writeResourceTerminalFile(t, filepath.Join(root, r.WorkspaceDirectory()), "same.txt", []byte("board changed\n"))
					}
				}
			}
			fixture := runResourceTerminalScenario(t, scenario)
			ctx := context.Background()
			taskID, _ := domain.ParseTaskID(fixture.group.TaskID)
			task, err := fixture.server.store.Reader().GetTask(ctx, taskID)
			if err != nil {
				t.Fatal(err)
			}
			room, err := fixture.server.store.Reader().GetRoom(ctx, task.RoomID())
			if err != nil {
				t.Fatal(err)
			}
			h := fixture.server.Handler()
			url := "/api/v2/projects/" + room.ProjectID().String() + "/task-board"
			var board app.TaskBoardView
			requestJSON(t, h, http.MethodGet, url, nil, 200, &board)
			if board.Total != 1 || len(board.Cards) != 1 || len(board.Cards[0].Repositories) != 2 || board.Cards[0].Phase == nil {
				t.Fatalf("board=%+v", board)
			}
			if kind == "none" || kind == "auto" {
				if *board.Cards[0].Phase != "finished" || board.Cards[0].Outcome == nil || *board.Cards[0].Outcome != "no_change" {
					t.Fatalf("card=%+v", board.Cards[0])
				}
				return
			}
			if *board.Cards[0].Phase != "review" {
				t.Fatal(board.Cards[0])
			}
			requestJSONWithHeaders(t, h, http.MethodPost, "/api/v2/runs/"+fixture.run.ID+"/review", map[string]any{"expectedVersion": fixture.run.Version, "kind": kind, "comment": "board result", "resultDigest": fixture.run.ResourceResult.Digest}, map[string]string{"Idempotency-Key": "board-review-" + kind}, 200, nil)
			requestJSON(t, h, http.MethodGet, url, nil, 200, &board)
			want := "delivery"
			if kind == "reject" {
				want = "working"
			}
			if board.Cards[0].Phase == nil || *board.Cards[0].Phase != want || board.Cards[0].Attention.State != "required" {
				t.Fatalf("card=%+v", board.Cards[0])
			}
		})
	}
}

func TestTaskBoardAPIFilterOptionsPagination(t *testing.T) {
	db := openTaskWorktreeResolverStore(t)
	defer db.Close()
	service := app.NewService(app.Dependencies{Store: db, DataRoot: t.TempDir(), Authorizer: localAuthorizer{}})
	h := (&Server{store: db, service: service}).Handler()
	var p resourceProjectTestView
	requestJSON(t, h, http.MethodPost, "/api/v2/projects", map[string]any{"name": "Many filter choices"}, 201, &p)
	pid, _ := domain.ParseProjectID(p.ID)
	ctx := context.Background()
	now := time.Now().UTC()
	root := t.TempDir()
	roomIDs := []string{p.DefaultRoomID}
	repoIDs := []string{}
	err := db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		for i := 0; i < 124; i++ {
			room, e := domain.NewRoom(domain.RoomParams{ID: domain.NewRoomID(), ProjectID: pid, OwnershipKind: domain.RoomOwnershipProject, Name: fmt.Sprintf("Room %d", i), WorkspaceRoot: filepath.Join(root, fmt.Sprintf("room-%d", i)), CreatedAt: now, UpdatedAt: now})
			if e != nil {
				return e
			}
			if e = tx.InsertRoom(ctx, room); e != nil {
				return e
			}
			roomIDs = append(roomIDs, room.ID().String())
		}
		for i := 0; i < 507; i++ {
			repo := domain.RepositoryRecord{ID: domain.NewRepositoryID(), Name: fmt.Sprintf("Repository %d", i), Checkout: filepath.Join(root, fmt.Sprintf("repo-%d", i)), IdentitySource: "legacy_unverified", CreatedAt: now}
			if e := tx.InsertRepository(ctx, repo); e != nil {
				return e
			}
			if e := tx.SaveProjectRepository(ctx, 0, domain.ProjectRepository{ProjectID: pid, Repository: repo, State: "active", Version: 1, AddedAt: now, UpdatedAt: now}); e != nil {
				return e
			}
			repoIDs = append(repoIDs, repo.ID.String())
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	base := "/api/v2/projects/" + p.ID + "/task-board"
	var first app.TaskBoardView
	requestJSON(t, h, http.MethodGet, base, nil, 200, &first)
	if len(first.Rooms) != 100 || len(first.Repositories) != 100 || first.NextOptionsCursor == "" {
		t.Fatalf("unbounded options: rooms=%d repos=%d", len(first.Rooms), len(first.Repositories))
	}
	rooms, repos := map[string]bool{}, map[string]bool{}
	page := first
	for {
		for _, r := range page.Rooms {
			rooms[r.RoomID] = true
		}
		for _, r := range page.Repositories {
			repos[r.RepoID] = true
		}
		if page.NextOptionsCursor == "" {
			break
		}
		var next app.TaskBoardView
		requestJSON(t, h, http.MethodGet, base+"?optionsCursor="+page.NextOptionsCursor, nil, 200, &next)
		if len(next.Rooms) > 100 || len(next.Repositories) > 100 || next.OptionsSnapshot != first.OptionsSnapshot || next.Snapshot != first.Snapshot {
			t.Fatal("filter cursor invalidated tasks or bounds")
		}
		page = next
	}
	if len(rooms) != 125 || len(repos) != 507 {
		t.Fatalf("inaccessible filters %d/%d", len(rooms), len(repos))
	}
	sort.Strings(roomIDs)
	sort.Strings(repoIDs)
	var selected app.TaskBoardView
	requestJSON(t, h, http.MethodGet, base+"?roomId="+roomIDs[124]+"&repoId="+repoIDs[506], nil, 200, &selected)
	if len(selected.Rooms) != 101 || len(selected.Repositories) != 101 || selected.Rooms[100].RoomID != roomIDs[124] || selected.Repositories[100].RepoID != repoIDs[506] {
		t.Fatal("URL-selected off-page choices missing")
	}
	requestJSON(t, h, http.MethodGet, base+"?optionsCursor=bad", nil, 400, nil)
	// Task progress changes the card snapshot, never the filter-directory cursor.
	roomID, _ := domain.ParseRoomID(p.DefaultRoomID)
	criterion, _ := domain.NewAcceptanceCriterion(domain.NewCriterionID(), "done", "")
	task, _ := domain.NewTask(domain.NewTaskID(), roomID, "New task", "Goal", []domain.AcceptanceCriterion{criterion})
	if err = db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error { return tx.InsertTask(ctx, task) }); err != nil {
		t.Fatal(err)
	}
	var progress app.TaskBoardView
	requestJSON(t, h, http.MethodGet, base+"?optionsCursor="+first.NextOptionsCursor, nil, 200, &progress)
	if progress.OptionsSnapshot != first.OptionsSnapshot || progress.Snapshot == first.Snapshot {
		t.Fatal("filter and task snapshots coupled")
	}
	room, _ := domain.NewRoom(domain.RoomParams{ID: domain.NewRoomID(), ProjectID: pid, OwnershipKind: domain.RoomOwnershipProject, Name: "Added room", WorkspaceRoot: filepath.Join(root, "new-room"), CreatedAt: now, UpdatedAt: now})
	if err = db.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error { return tx.InsertRoom(ctx, room) }); err != nil {
		t.Fatal(err)
	}
	requestJSON(t, h, http.MethodGet, base+"?optionsCursor="+first.NextOptionsCursor, nil, 409, nil)
}
