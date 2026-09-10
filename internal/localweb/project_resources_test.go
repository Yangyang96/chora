package localweb

import (
	"context"
	"errors"
	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/gitsource"
	storecontract "github.com/Yangyang96/chora/internal/store"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

type resourceProjectTestView struct {
	ID                   string                   `json:"id"`
	Description          string                   `json:"description"`
	Version              uint64                   `json:"version"`
	DefaultRoomID        string                   `json:"defaultRoomId"`
	Rooms                []roomView               `json:"rooms"`
	Repositories         []repositoryResourceView `json:"repositories"`
	NextRepositoryCursor string                   `json:"nextRepositoryCursor"`
	RepositoryBinding    any                      `json:"repositoryBinding"`
}

func TestTaskFirstEmptyProjectMultiRepositoryAndSharedIdentity(t *testing.T) {
	db := openTaskWorktreeResolverStore(t)
	defer db.Close()
	source := gitsource.Default{}
	service := app.NewService(app.Dependencies{Store: db, DataRoot: t.TempDir(), RepositorySource: source, Authorizer: localAuthorizer{}})
	server := &Server{store: db, service: service, repositorySource: source, pathPiEnabled: true}
	handler := server.Handler()
	var first, second resourceProjectTestView
	requestJSON(t, handler, http.MethodPost, "/api/v2/projects", map[string]any{"name": "Frontend and backend"}, 201, &first)
	if first.ID == "" || first.DefaultRoomID == "" || len(first.Repositories) != 0 || first.RepositoryBinding != nil {
		t.Fatalf("empty project: %+v", first)
	}
	requestJSON(t, handler, http.MethodPost, "/api/v2/projects", map[string]any{"name": "Shared workspace"}, 201, &second)
	var topic roomView
	requestJSON(t, handler, http.MethodPost, "/api/projects/"+first.ID+"/rooms", map[string]any{"name": "Login"}, 201, &topic)
	requestJSON(t, handler, http.MethodGet, "/api/projects/"+first.ID, nil, 409, nil)
	rootA, _, _ := newTaskWorktreeTestRepository(t)
	rootB, _, _ := newTaskWorktreeTestRepository(t)
	var a, b, shared struct {
		Repository repositoryResourceView `json:"repository"`
	}
	requestJSON(t, handler, http.MethodPost, "/api/v2/projects/"+first.ID+"/repositories", map[string]any{"locator": rootA}, 200, &a)
	requestJSON(t, handler, http.MethodPost, "/api/v2/projects/"+first.ID+"/repositories", map[string]any{"locator": rootB}, 200, &b)
	requestJSON(t, handler, http.MethodPost, "/api/v2/projects/"+second.ID+"/repositories", map[string]any{"locator": rootA}, 200, &shared)
	if a.Repository.RepoID == b.Repository.RepoID || a.Repository.RepoID != shared.Repository.RepoID || a.Repository.Availability != "ready" {
		t.Fatalf("identity: %+v %+v %+v", a, b, shared)
	}
	var duplicate struct {
		Repository repositoryResourceView `json:"repository"`
	}
	requestJSON(t, handler, http.MethodPost, "/api/v2/projects/"+first.ID+"/repositories", map[string]any{"locator": rootA}, 200, &duplicate)
	if duplicate.Repository.RepoID != a.Repository.RepoID || duplicate.Repository.Version != a.Repository.Version {
		t.Fatal("duplicate admission changed association")
	}
	requestJSON(t, handler, http.MethodGet, "/api/v2/projects/"+first.ID, nil, 200, &first)
	if len(first.Repositories) != 2 || first.RepositoryBinding != nil {
		t.Fatalf("multi project: %+v", first)
	}
	var refs struct {
		Version uint64   `json:"version"`
		RepoIDs []string `json:"repoIds"`
	}
	requestJSON(t, handler, http.MethodGet, "/api/v2/rooms/"+topic.ID+"/resources", nil, 200, &refs)
	if len(refs.RepoIDs) != 0 {
		t.Fatal("multi repo Room silently selected all")
	}
	requestJSON(t, handler, http.MethodPut, "/api/v2/rooms/"+topic.ID+"/resources", map[string]any{"version": 0, "repoIds": []string{b.Repository.RepoID}}, 200, &refs)
	if len(refs.RepoIDs) != 1 || refs.RepoIDs[0] != b.Repository.RepoID {
		t.Fatal("Room selection lost")
	}
	requestJSON(t, handler, http.MethodPut, "/api/v2/rooms/"+second.DefaultRoomID+"/resources", map[string]any{"version": 0, "repoIds": []string{b.Repository.RepoID}}, 404, nil)
	requestJSON(t, handler, http.MethodPut, "/api/v2/rooms/"+topic.ID+"/resources", map[string]any{"version": 0, "repoIds": []string{a.Repository.RepoID}}, 409, nil)
	requestJSON(t, handler, http.MethodDelete, "/api/v2/projects/"+first.ID+"/repositories/"+a.Repository.RepoID+"?expectedVersion=1", map[string]any{}, 204, nil)
	requestJSON(t, handler, http.MethodGet, "/api/v2/projects/"+second.ID, nil, 200, &second)
	if len(second.Repositories) != 1 || second.Repositories[0].State != "active" {
		t.Fatal("detach changed another Project")
	}
	if _, err := os.Stat(filepath.Join(rootA, ".git")); err != nil {
		t.Fatal("detach removed original files")
	}
	requestJSON(t, handler, http.MethodPatch, "/api/v2/projects/"+first.ID, map[string]any{"name": "Renamed", "expectedVersion": first.Version}, 200, &first)
}

func TestTaskFirstDirectoryCancelDoesNotAddRepository(t *testing.T) {
	db := openTaskWorktreeResolverStore(t)
	defer db.Close()
	source := gitsource.Default{}
	service := app.NewService(app.Dependencies{Store: db, DataRoot: t.TempDir(), RepositorySource: source, Authorizer: localAuthorizer{}})
	server := &Server{store: db, service: service, repositorySource: source, directoryPicker: cancelResourcePicker{}}
	handler := server.Handler()
	var project resourceProjectTestView
	requestJSON(t, handler, http.MethodPost, "/api/v2/projects", map[string]any{"name": "Empty"}, 201, &project)
	var result struct {
		Cancelled bool `json:"cancelled"`
	}
	requestJSON(t, handler, http.MethodPost, "/api/v2/projects/"+project.ID+"/repositories/choose-directory", map[string]any{}, 200, &result)
	if !result.Cancelled {
		t.Fatal("cancel not preserved")
	}
	requestJSON(t, handler, http.MethodGet, "/api/v2/projects/"+project.ID, nil, 200, &project)
	if len(project.Repositories) != 0 {
		t.Fatal("cancel created association")
	}
}

func TestProjectDescriptionIsIndependentFromGeneralRoomBrief(t *testing.T) {
	db := openTaskWorktreeResolverStore(t)
	defer db.Close()
	service := app.NewService(app.Dependencies{Store: db, DataRoot: t.TempDir(), Authorizer: localAuthorizer{}})
	server := &Server{store: db, service: service, pathPiEnabled: true}
	handler := server.Handler()
	var project resourceProjectTestView
	requestJSON(t, handler, http.MethodPost, "/api/v2/projects", map[string]any{"name": "Payments", "description": "Long-lived Project purpose"}, http.StatusCreated, &project)
	if project.Description != "Long-lived Project purpose" || len(project.Rooms) != 1 || project.Rooms[0].Description == project.Description {
		t.Fatalf("Project description was derived from General Room: %+v", project)
	}
	general := project.Rooms[0]
	requestJSON(t, handler, http.MethodPatch, "/api/rooms/"+general.ID, map[string]any{"name": general.Name, "description": "Changed General discussion", "expectedVersion": general.Version}, http.StatusOK, &general)
	requestJSON(t, handler, http.MethodGet, "/api/v2/projects/"+project.ID, nil, http.StatusOK, &project)
	if project.Description != "Long-lived Project purpose" || len(project.Rooms) != 1 || project.Rooms[0].Description != "Changed General discussion" {
		t.Fatalf("General Room mutation changed Project description: %+v", project)
	}
}

func TestProjectRepositoryPagesReachAssociationsBeyondProjectDetailBound(t *testing.T) {
	db := openTaskWorktreeResolverStore(t)
	defer db.Close()
	source := repositoryPageSource{byPath: map[string]gitsource.Inspection{}}
	service := app.NewService(app.Dependencies{Store: db, DataRoot: t.TempDir(), RepositorySource: source, Authorizer: localAuthorizer{}})
	server := &Server{store: db, service: service, repositorySource: source, pathPiEnabled: true}
	handler := server.Handler()
	var project resourceProjectTestView
	requestJSON(t, handler, http.MethodPost, "/api/v2/projects", map[string]any{"name": "Many repositories"}, http.StatusCreated, &project)
	projectID, err := domain.ParseProjectID(project.ID)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	repositoryIDs := make([]string, 0, domain.RepositoryMaxPageSize+1)
	associations := make([]domain.ProjectRepository, 0, domain.RepositoryMaxPageSize+1)
	for index := 0; index <= domain.RepositoryMaxPageSize; index++ {
		id := domain.NewRepositoryID()
		checkout := filepath.Join("/repositories", id.String())
		common := filepath.Join("/git", id.String())
		identity := repositoryPageIdentity(index + 1)
		record := domain.RepositoryRecord{ID: id, Name: "repository-" + id.String(), Checkout: checkout, CommonGitDir: common, PhysicalIdentity: identity, IdentitySource: "inspected", CreatedAt: now}
		associations = append(associations, domain.ProjectRepository{ProjectID: projectID, Repository: record, State: "active", Version: 1, AddedAt: now, UpdatedAt: now})
		repositoryIDs = append(repositoryIDs, id.String())
		source.byPath[checkout] = gitsource.Inspection{CanonicalPath: checkout, CommonGitDir: common, PhysicalIdentity: identity, IsGit: true, HeadCommit: "0123456789012345678901234567890123456789", RootTree: "abcdefabcdefabcdefabcdefabcdefabcdefabcd", Branch: "main"}
	}
	if err = db.WithinWriteTx(context.Background(), func(tx storecontract.WriteTx) error {
		for _, association := range associations {
			if err := tx.InsertRepository(context.Background(), association.Repository); err != nil {
				return err
			}
			if err := tx.SaveProjectRepository(context.Background(), 0, association); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sort.Strings(repositoryIDs)
	requestJSON(t, handler, http.MethodGet, "/api/v2/projects/"+project.ID, nil, http.StatusOK, &project)
	if len(project.Repositories) != domain.RepositoryMaxPageSize || project.NextRepositoryCursor == "" {
		t.Fatalf("bounded Project detail page: repositories=%d cursor=%q", len(project.Repositories), project.NextRepositoryCursor)
	}
	if project.Repositories[len(project.Repositories)-1].RepoID != repositoryIDs[domain.RepositoryMaxPageSize-1] {
		t.Fatal("Project detail page is not stable by repository ID")
	}
	var next struct {
		Repositories []repositoryResourceView `json:"repositories"`
		NextCursor   string                   `json:"nextCursor"`
	}
	requestJSON(t, handler, http.MethodGet, "/api/v2/projects/"+project.ID+"/repositories?cursor="+project.NextRepositoryCursor+"&limit=200", nil, http.StatusOK, &next)
	if len(next.Repositories) != 1 || next.Repositories[0].RepoID != repositoryIDs[domain.RepositoryMaxPageSize] || next.NextCursor != "" {
		t.Fatalf("late repository is unreachable: %+v", next)
	}
	var second resourceProjectTestView
	requestJSON(t, handler, http.MethodPost, "/api/v2/projects", map[string]any{"name": "Different Project"}, http.StatusCreated, &second)
	requestJSON(t, handler, http.MethodGet, "/api/v2/projects/"+second.ID+"/repositories?cursor="+project.NextRepositoryCursor, nil, http.StatusBadRequest, nil)
}

type repositoryPageSource struct {
	byPath map[string]gitsource.Inspection
}

func (source repositoryPageSource) Inspect(_ context.Context, path string) (gitsource.Inspection, error) {
	inspection, ok := source.byPath[path]
	if !ok {
		return gitsource.Inspection{}, errors.New("repository unavailable")
	}
	return inspection, nil
}

func (repositoryPageSource) Clone(context.Context, string, string) (gitsource.Inspection, error) {
	return gitsource.Inspection{}, errors.New("clone unavailable")
}

func repositoryPageIdentity(value int) string {
	const hex = "0123456789abcdef"
	out := make([]byte, 64)
	for index := range out {
		out[index] = '0'
	}
	for index := len(out) - 1; value > 0; index-- {
		out[index] = hex[value&15]
		value >>= 4
	}
	return string(out)
}

type cancelResourcePicker struct{}

func (cancelResourcePicker) Choose(context.Context) (string, error) { return "", nil }
