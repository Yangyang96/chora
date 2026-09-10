package localweb

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

type repositoryResourceView struct {
	RepoID       string `json:"repoId"`
	Name         string `json:"name"`
	LocalLocator string `json:"localLocator"`
	State        string `json:"state"`
	Version      uint64 `json:"version"`
	Availability string `json:"availability"`
	Branch       string `json:"branch"`
	Head         string `json:"head"`
	Dirty        bool   `json:"dirty"`
	Reason       string `json:"reason,omitempty"`
}

func (server *Server) resourceView(ctx context.Context, a domain.ProjectRepository) repositoryResourceView {
	r := a.Repository
	v := repositoryResourceView{RepoID: r.ID.String(), Name: r.Name, LocalLocator: r.Checkout, State: a.State, Version: a.Version, Availability: r.IdentitySource}
	if server.repositorySource == nil {
		v.Availability = "unavailable"
		v.Reason = "Repository inspection unavailable"
		return v
	}
	i, err := server.repositorySource.Inspect(ctx, r.Checkout)
	if err != nil {
		v.Availability = "unavailable"
		v.Reason = err.Error()
		return v
	}
	v.Branch = i.Branch
	v.Head = i.HeadCommit
	v.Dirty = i.Dirty
	if i.Unborn {
		v.Availability = "unborn"
		v.Reason = "Create the first commit before starting a coding task"
		return v
	}
	if r.IdentitySource == "legacy_unverified" {
		v.Availability = "legacy_unverified"
		return v
	}
	if r.Checkout != i.CanonicalPath || r.CommonGitDir != i.CommonGitDir || r.PhysicalIdentity != i.PhysicalIdentity {
		v.Availability = "identity_drift"
		v.Reason = "Repository identity changed; original association preserved"
	} else {
		v.Availability = "ready"
	}
	return v
}

func (server *Server) repositoryResourcesView(ctx context.Context, associations []domain.ProjectRepository) []repositoryResourceView {
	repos := make([]repositoryResourceView, len(associations))
	var wg sync.WaitGroup
	slots := make(chan struct{}, 4)
	// A single page has a bounded observation budget; stale/unavailable is visible.
	ctx, cancel := context.WithTimeout(ctx, domain.RepositoryMetadataTimeout)
	defer cancel()
	for index, association := range associations {
		wg.Add(1)
		go func(index int, association domain.ProjectRepository) {
			defer wg.Done()
			select {
			case slots <- struct{}{}:
				defer func() { <-slots }()
				repos[index] = server.resourceView(ctx, association)
			case <-ctx.Done():
				repos[index] = repositoryResourceView{RepoID: association.Repository.ID.String(), Name: association.Repository.Name, LocalLocator: association.Repository.Checkout, State: association.State, Version: association.Version, Availability: "unavailable", Reason: "Repository status query timed out"}
			}
		}(index, association)
	}
	wg.Wait()
	return repos
}

func projectRepositoryCursor(projectID domain.ProjectID, repositoryID string) string {
	if repositoryID == "" {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString([]byte("v1\x00" + projectID.String() + "\x00" + repositoryID))
}

func parseProjectRepositoryCursor(projectID domain.ProjectID, cursor string) (string, error) {
	if cursor == "" {
		return "", nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return "", domain.ErrInvalidArgument
	}
	parts := strings.Split(string(raw), "\x00")
	if len(parts) != 3 || parts[0] != "v1" || parts[1] != projectID.String() {
		return "", domain.ErrInvalidArgument
	}
	if _, err = domain.ParseRepositoryID(parts[2]); err != nil {
		return "", domain.ErrInvalidArgument
	}
	return parts[2], nil
}

func (server *Server) projectResourcesView(ctx context.Context, p app.ProjectResourcesView) map[string]any {
	rooms := []roomView{}
	var room roomView
	for _, r := range p.Rooms {
		v := roomViewOf(r)
		rooms = append(rooms, v)
		if r.ID() == p.Project.DefaultRoomID() {
			room = v
		}
	}
	repos := server.repositoryResourcesView(ctx, p.Repositories)
	var action *currentActionView
	if p.CurrentAction != nil {
		v := currentActionViewOf(*p.CurrentAction)
		action = &v
	}
	return map[string]any{"id": p.Project.ID().String(), "name": p.Project.Name(), "description": p.Project.Description(), "state": p.Project.State(), "version": p.Project.Version(), "defaultRoomId": p.Project.DefaultRoomID().String(), "rooms": rooms, "room": room, "repositories": repos, "nextRepositoryCursor": projectRepositoryCursor(p.Project.ID(), p.NextRepositoryCursor), "currentAction": action, "currentTaskTitle": p.CurrentTaskTitle, "lastActivityAt": p.LastActivityAt.Format(time.RFC3339Nano), "taskCounts": roomTaskCountsView{Total: p.TaskCounts.Total, Open: p.TaskCounts.Open, Terminal: p.TaskCounts.Terminal}}
}
func (server *Server) getProjectResources(w http.ResponseWriter, r *http.Request) {
	id, err := domain.ParseProjectID(r.PathValue("projectID"))
	if err != nil {
		writeProjectError(w, err)
		return
	}
	p, err := server.service.GetProjectResources(r.Context(), id)
	if err != nil {
		writeProjectError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, server.projectResourcesView(r.Context(), p))
}

// getProjectRepositoryPage exposes every association without expanding the
// bounded Project detail response or recursively inspecting repository trees.
func (server *Server) getProjectRepositoryPage(w http.ResponseWriter, r *http.Request) {
	id, err := domain.ParseProjectID(r.PathValue("projectID"))
	if err != nil {
		writeProjectError(w, err)
		return
	}
	if _, err = server.store.Reader().GetProject(r.Context(), id); err != nil {
		writeProjectError(w, err)
		return
	}
	limit := domain.RepositoryPageSize
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > domain.RepositoryMaxPageSize {
			writeProjectError(w, domain.ErrInvalidArgument)
			return
		}
	}
	after, err := parseProjectRepositoryCursor(id, r.URL.Query().Get("cursor"))
	if err != nil {
		writeProjectError(w, err)
		return
	}
	associations, err := server.store.Reader().ListProjectRepositories(r.Context(), id, after, limit+1)
	if err != nil {
		writeProjectError(w, err)
		return
	}
	next := ""
	if len(associations) > limit {
		associations = associations[:limit]
		next = projectRepositoryCursor(id, associations[len(associations)-1].Repository.ID.String())
	}
	writeJSON(w, http.StatusOK, map[string]any{"repositories": server.repositoryResourcesView(r.Context(), associations), "nextCursor": next})
}
func (server *Server) createEmptyProject(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	p, err := server.service.CreateEmptyProject(r.Context(), app.CreateEmptyProjectRequest{CommandMeta: requestCommandMeta(r, "create-project"), Name: input.Name, Description: input.Description})
	if err != nil {
		writeProjectError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, server.projectResourcesView(r.Context(), p))
}
func (server *Server) listResourceProjects(w http.ResponseWriter, r *http.Request) {
	state := domain.ProjectStateActive
	if raw := r.URL.Query().Get("state"); raw == "archived" {
		state = domain.ProjectStateArchived
	} else if raw != "" && raw != "active" {
		writeError(w, http.StatusBadRequest, domain.ErrInvalidArgument)
		return
	}
	limit := domain.RepositoryPageSize
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > domain.RepositoryMaxPageSize {
			writeError(w, http.StatusBadRequest, domain.ErrInvalidArgument)
			return
		}
		limit = n
	}
	projects, err := server.store.Reader().ListResourceProjects(r.Context(), state, r.URL.Query().Get("cursor"), limit+1)
	if err != nil {
		writeProjectError(w, err)
		return
	}
	next := ""
	if len(projects) > limit {
		projects = projects[:limit]
		next = projects[len(projects)-1].ID().String()
	}
	views := []map[string]any{}
	for _, p := range projects {
		if filter := r.URL.Query().Get("filter"); filter != "" && !strings.Contains(strings.ToLower(p.Name()), strings.ToLower(filter)) {
			continue
		}
		v, err := server.service.GetProjectResources(r.Context(), p.ID())
		if err != nil {
			writeProjectError(w, err)
			return
		}
		views = append(views, server.projectResourcesView(r.Context(), v))
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": views, "nextCursor": next})
}
func (server *Server) addProjectRepository(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Locator string `json:"locator"`
		Name    string `json:"name"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	server.admitProjectRepository(w, r, input.Locator, input.Name)
}
func (server *Server) admitProjectRepository(w http.ResponseWriter, r *http.Request, locator, name string) {
	id, err := domain.ParseProjectID(r.PathValue("projectID"))
	if err != nil {
		writeProjectError(w, err)
		return
	}
	repo, err := server.service.AddProjectRepository(r.Context(), app.AddProjectRepositoryRequest{CommandMeta: requestCommandMeta(r, "add-project-repository"), ProjectID: id, Locator: locator, Name: name})
	if err != nil {
		writeProjectError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"repository": server.resourceView(r.Context(), repo)})
}
func (server *Server) chooseProjectRepository(w http.ResponseWriter, r *http.Request) {
	var input struct{}
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	id, err := domain.ParseProjectID(r.PathValue("projectID"))
	if err != nil {
		writeProjectError(w, err)
		return
	}
	project, err := server.store.Reader().GetProject(r.Context(), id)
	if err != nil {
		writeProjectError(w, err)
		return
	}
	if project.State() != domain.ProjectStateActive {
		writeProjectError(w, storecontract.ErrRoomStateForbidden)
		return
	}
	if server.directoryPicker == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("folder chooser unavailable"))
		return
	}
	if !server.directoryPickerMu.TryLock() {
		writeError(w, http.StatusConflict, errors.New("finish the open folder chooser first"))
		return
	}
	defer server.directoryPickerMu.Unlock()
	path, err := server.directoryPicker.Choose(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err)
		return
	}
	if path == "" {
		writeJSON(w, http.StatusOK, map[string]any{"cancelled": true})
		return
	}
	server.admitProjectRepository(w, r, path, "")
}
func (server *Server) removeProjectRepository(w http.ResponseWriter, r *http.Request) {
	p, err := domain.ParseProjectID(r.PathValue("projectID"))
	if err != nil {
		writeProjectError(w, err)
		return
	}
	id, err := domain.ParseRepositoryID(r.PathValue("repoID"))
	if err != nil {
		writeProjectError(w, err)
		return
	}
	version, err := strconv.ParseUint(r.URL.Query().Get("expectedVersion"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	err = server.service.RemoveProjectRepository(r.Context(), app.RemoveProjectRepositoryRequest{CommandMeta: requestCommandMeta(r, "remove-project-repository"), ProjectID: p, RepositoryID: id, ExpectedVersion: version})
	if err != nil {
		writeProjectError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func roomResourcesView(refs domain.RoomRepositoryReferences) map[string]any {
	ids := []string{}
	for _, id := range refs.RepositoryIDs {
		ids = append(ids, id.String())
	}
	return map[string]any{"version": refs.Version, "repoIds": ids}
}
func (server *Server) getRoomResources(w http.ResponseWriter, r *http.Request) {
	id, err := domain.ParseRoomID(r.PathValue("roomID"))
	if err != nil {
		writeProjectError(w, err)
		return
	}
	room, err := server.store.Reader().GetRoom(r.Context(), id)
	if err != nil {
		writeProjectError(w, err)
		return
	}
	if room.OwnershipKind() != domain.RoomOwnershipProject {
		writeProjectError(w, app.ErrProjectNotFound)
		return
	}
	refs, err := server.store.Reader().GetRoomRepositoryReferences(r.Context(), id)
	if errors.Is(err, storecontract.ErrNotFound) {
		refs = domain.RoomRepositoryReferences{RoomID: id}
		err = nil
	}
	if err != nil {
		writeProjectError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, roomResourcesView(refs))
}
func (server *Server) putRoomResources(w http.ResponseWriter, r *http.Request) {
	id, err := domain.ParseRoomID(r.PathValue("roomID"))
	if err != nil {
		writeProjectError(w, err)
		return
	}
	var input struct {
		Version uint64   `json:"version"`
		RepoIDs []string `json:"repoIds"`
	}
	if err = decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	ids := []domain.RepositoryID{}
	for _, raw := range input.RepoIDs {
		id, err := domain.ParseRepositoryID(raw)
		if err != nil {
			writeProjectError(w, err)
			return
		}
		ids = append(ids, id)
	}
	refs, err := server.service.SetRoomRepositoryReferences(r.Context(), requestCommandMeta(r, "set-room-resources"), id, input.Version, ids)
	if err != nil {
		writeProjectError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, roomResourcesView(refs))
}
func (server *Server) changeResourceProject(w http.ResponseWriter, r *http.Request) {
	id, err := domain.ParseProjectID(r.PathValue("projectID"))
	if err != nil {
		writeProjectError(w, err)
		return
	}
	var input struct {
		Name            string `json:"name"`
		ExpectedVersion uint64 `json:"expectedVersion"`
	}
	if err = decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if r.Method == http.MethodPatch {
		_, err = server.service.RenameProject(r.Context(), app.RenameProjectRequest{CommandMeta: requestCommandMeta(r, "rename-project"), ProjectID: id, Name: input.Name, ExpectedVersion: input.ExpectedVersion})
	} else {
		state := domain.ProjectStateArchived
		if strings.HasSuffix(r.URL.Path, "/restore") {
			state = domain.ProjectStateActive
		}
		_, err = server.service.ChangeProjectLifecycle(r.Context(), app.ChangeProjectLifecycleRequest{CommandMeta: requestCommandMeta(r, "project-lifecycle"), ProjectID: id, ExpectedVersion: input.ExpectedVersion}, state)
	}
	if err != nil {
		writeProjectError(w, err)
		return
	}
	server.getProjectResources(w, r)
}

// Guard only legacy project-level HTTP operations. Historical Task readers keep
// resolving their original binding even after new associations are added.
func (server *Server) requireLegacyProject(ctx context.Context, id domain.ProjectID) error {
	project, err := server.store.Reader().GetProject(ctx, id)
	if err != nil {
		return err
	}
	rows, err := server.store.Reader().ListProjectRepositories(ctx, id, "", 2)
	if err != nil {
		return err
	}
	if len(rows) != 1 || rows[0].State != "active" {
		return app.ErrProjectUpgradeRequired
	}
	binding, err := server.store.Reader().GetRepositoryBinding(ctx, project.DefaultRoomID())
	if err != nil {
		return app.ErrProjectUpgradeRequired
	}
	if rows[0].Repository.Checkout != binding.LocalLocator() {
		return app.ErrProjectUpgradeRequired
	}
	return nil
}
