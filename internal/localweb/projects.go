package localweb

import (
	"context"
	"errors"
	"mime"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

const projectsUnavailableMessage = "project repository source is unavailable"

type repositoryBindingView struct {
	RoomID         string `json:"roomId"`
	Name           string `json:"name"`
	LocalLocator   string `json:"localLocator"`
	SourceKind     string `json:"sourceKind"`
	CloneURL       string `json:"cloneUrl"`
	AdmittedBase   string `json:"admittedBase"`
	BaseIdentity   string `json:"baseIdentity"`
	TargetWorktree string `json:"targetWorktree"`
	DirtyAdmitted  bool   `json:"dirtyAdmitted"`
	State          string `json:"state"`
	Version        uint64 `json:"version"`
	CreatedAt      string `json:"createdAt"`
	UpdatedAt      string `json:"updatedAt"`
}

type projectView struct {
	ID                string                `json:"id"`
	Name              string                `json:"name"`
	State             string                `json:"state"`
	Version           uint64                `json:"version"`
	DefaultRoomID     string                `json:"defaultRoomId"`
	Rooms             []roomView            `json:"rooms"`
	CurrentAction     *currentActionView    `json:"currentAction"`
	CurrentTaskTitle  string                `json:"currentTaskTitle"`
	Room              roomView              `json:"room"`
	RepositoryBinding repositoryBindingView `json:"repositoryBinding"`
	LastActivityAt    string                `json:"lastActivityAt"`
	TaskCounts        roomTaskCountsView    `json:"taskCounts"`
}

func projectViewOf(project app.ProjectView) projectView {
	binding := project.RepositoryBinding
	var action *currentActionView
	if project.CurrentAction != nil {
		view := currentActionViewOf(*project.CurrentAction)
		action = &view
	}
	rooms := make([]roomView, 0, len(project.Rooms))
	for _, room := range project.Rooms {
		rooms = append(rooms, roomViewOf(room))
	}
	return projectView{
		ID: project.Project.ID().String(), Name: project.Project.Name(), State: string(project.Project.State()), Version: project.Project.Version(), DefaultRoomID: project.Project.DefaultRoomID().String(), Rooms: rooms,
		CurrentAction: action, CurrentTaskTitle: project.CurrentTaskTitle,
		Room: roomViewOf(project.Room),
		RepositoryBinding: repositoryBindingView{
			RoomID: binding.RoomID().String(), Name: binding.Name(), LocalLocator: binding.LocalLocator(),
			SourceKind: string(binding.SourceKind()), CloneURL: binding.CloneURL(), AdmittedBase: binding.AdmittedBase(),
			BaseIdentity: binding.BaseIdentity(), TargetWorktree: binding.TargetWorktree(), DirtyAdmitted: binding.DirtyAdmitted(),
			State: string(binding.State()), Version: binding.Version(),
			CreatedAt: binding.CreatedAt().Format(time.RFC3339Nano), UpdatedAt: binding.UpdatedAt().Format(time.RFC3339Nano),
		},
		LastActivityAt: project.LastActivityAt.Format(time.RFC3339Nano),
		TaskCounts:     roomTaskCountsView{Total: project.TaskCounts.Total, Open: project.TaskCounts.Open, Terminal: project.TaskCounts.Terminal},
	}
}

// projectsUnavailable guards only the two mutating routes (open/clone), which
// require the RepositorySource to inspect or clone. The read and delete routes
// (list/get/remove) intentionally remain available without a RepositorySource:
// they operate purely on persisted bindings, so a server in a mode without a
// Git provider can still list and clean up projects (returning an empty list
// and 404 for get/remove).
func (server *Server) projectsUnavailable(writer http.ResponseWriter) bool {
	if server.repositorySource == nil {
		writeError(writer, http.StatusServiceUnavailable, errors.New(projectsUnavailableMessage))
		return true
	}
	return false
}

func (server *Server) openProject(writer http.ResponseWriter, request *http.Request) {
	if server.projectsUnavailable(writer) {
		return
	}
	host, _, err := net.SplitHostPort(request.Host)
	if err != nil {
		host = request.Host
	}
	if host != "localhost" && !net.ParseIP(host).IsLoopback() {
		writeError(writer, http.StatusForbidden, errors.New("project opening requires a local Chora address"))
		return
	}
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeError(writer, http.StatusUnsupportedMediaType, errors.New("JSON request required"))
		return
	}
	var input struct {
		Locator string `json:"locator"`
		Name    string `json:"name"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	result, err := server.service.OpenProject(request.Context(), app.OpenProjectRequest{
		CommandMeta: requestCommandMeta(request, "open-project"), Locator: input.Locator, Name: input.Name,
	})
	if err != nil {
		writeProjectError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, projectViewOf(result.Project))
}

func (server *Server) cloneProject(writer http.ResponseWriter, request *http.Request) {
	if server.projectsUnavailable(writer) {
		return
	}
	writeError(writer, http.StatusGone, errors.New("cloning is no longer available; choose an existing local Git project"))
}

func (server *Server) listProjects(writer http.ResponseWriter, request *http.Request) {
	filter := request.URL.Query().Get("filter")
	state := domain.ProjectStateActive
	if request.URL.Query().Get("state") == "archived" {
		state = domain.ProjectStateArchived
	} else if raw := request.URL.Query().Get("state"); raw != "" && raw != "active" {
		writeError(writer, http.StatusBadRequest, errors.New("state must be active or archived"))
		return
	}
	projects, err := server.service.ListProjectsByState(request.Context(), filter, state)
	if err != nil {
		writeProjectError(writer, err)
		return
	}
	view := make([]projectView, 0, len(projects))
	for _, project := range projects {
		if err := server.requireLegacyProject(request.Context(), project.Project.ID()); err != nil {
			writeProjectError(writer, err)
			return
		}
		view = append(view, projectViewOf(project))
	}
	writeJSON(writer, http.StatusOK, map[string]any{"projects": view})
}

// projectFromRoute accepts true Project IDs. The old room_ route is an explicit
// compatibility lookup, never a Project ID alias; responses carry the real ID.
func (server *Server) projectFromRoute(ctx context.Context, value string) (app.ProjectView, error) {
	if strings.HasPrefix(value, "room_") {
		roomID, err := domain.ParseRoomID(value)
		if err != nil {
			return app.ProjectView{}, err
		}
		project, err := server.service.GetProject(ctx, roomID)
		if err == nil {
			err = server.requireLegacyProject(ctx, project.Project.ID())
		}
		return project, err
	}
	id, err := domain.ParseProjectID(value)
	if err != nil {
		return app.ProjectView{}, err
	}
	if err := server.requireLegacyProject(ctx, id); err != nil {
		return app.ProjectView{}, err
	}
	return server.service.GetProjectByID(ctx, id)
}

func (server *Server) getProject(writer http.ResponseWriter, request *http.Request) {
	if !strings.HasPrefix(request.PathValue("projectID"), "room_") {
		id, err := domain.ParseProjectID(request.PathValue("projectID"))
		if err != nil {
			writeProjectError(writer, err)
			return
		}
		if err = server.requireLegacyProject(request.Context(), id); err != nil {
			writeProjectError(writer, err)
			return
		}
	}
	project, err := server.projectFromRoute(request.Context(), request.PathValue("projectID"))
	if err != nil {
		writeProjectError(writer, err)
		return
	}
	if strings.HasPrefix(request.PathValue("projectID"), "room_") {
		writer.Header().Set("Deprecation", "true")
		writer.Header().Set("Content-Location", "/api/projects/"+project.Project.ID().String())
	}
	writeJSON(writer, http.StatusOK, projectViewOf(project))
}

func (server *Server) removeProject(writer http.ResponseWriter, request *http.Request) {
	project, err := server.projectFromRoute(request.Context(), request.PathValue("projectID"))
	if err != nil {
		writeProjectError(writer, err)
		return
	}
	expectedVersion, err := strconv.ParseUint(request.URL.Query().Get("expectedVersion"), 10, 64)
	if err != nil {
		writeError(writer, http.StatusBadRequest, errors.New("expectedVersion query parameter is required"))
		return
	}
	if strings.HasPrefix(request.PathValue("projectID"), "room_") {
		// Legacy delete used the repository binding version; keep its explicit contract.
		roomID, _ := domain.ParseRoomID(request.PathValue("projectID"))
		_, err = server.service.RemoveProject(request.Context(), app.RemoveProjectRequest{CommandMeta: requestCommandMeta(request, "remove-project"), RoomID: roomID, ExpectedVersion: expectedVersion})
	} else {
		_, err = server.service.ArchiveProject(request.Context(), app.ChangeProjectLifecycleRequest{CommandMeta: requestCommandMeta(request, "archive-project"), ProjectID: project.Project.ID(), ExpectedVersion: expectedVersion})
	}
	if err != nil {
		writeProjectError(writer, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (server *Server) createProjectRoom(writer http.ResponseWriter, request *http.Request) {
	id, err := domain.ParseProjectID(request.PathValue("projectID"))
	if err != nil {
		writeProjectError(writer, err)
		return
	}
	var input struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	result, err := server.service.CreateProjectRoom(request.Context(), app.CreateProjectRoomRequest{CommandMeta: requestCommandMeta(request, "create-project-room"), ProjectID: id, Name: input.Name, Description: input.Description})
	if err != nil {
		writeProjectError(writer, err)
		return
	}
	writeJSON(writer, http.StatusCreated, roomViewOf(result.Room))
}

func (server *Server) renameProject(writer http.ResponseWriter, request *http.Request) {
	id, err := domain.ParseProjectID(request.PathValue("projectID"))
	if err != nil {
		writeProjectError(writer, err)
		return
	}
	if err = server.requireLegacyProject(request.Context(), id); err != nil {
		writeProjectError(writer, err)
		return
	}
	var input struct {
		Name            string `json:"name"`
		ExpectedVersion uint64 `json:"expectedVersion"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	result, err := server.service.RenameProject(request.Context(), app.RenameProjectRequest{CommandMeta: requestCommandMeta(request, "rename-project"), ProjectID: id, Name: input.Name, ExpectedVersion: input.ExpectedVersion})
	if err != nil {
		writeProjectError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, projectViewOf(result))
}

func (server *Server) archiveProject(writer http.ResponseWriter, request *http.Request) {
	server.changeProjectLifecycle(writer, request, domain.ProjectStateArchived)
}
func (server *Server) restoreProject(writer http.ResponseWriter, request *http.Request) {
	server.changeProjectLifecycle(writer, request, domain.ProjectStateActive)
}
func (server *Server) changeProjectLifecycle(writer http.ResponseWriter, request *http.Request, state domain.ProjectState) {
	id, err := domain.ParseProjectID(request.PathValue("projectID"))
	if err != nil {
		writeProjectError(writer, err)
		return
	}
	if err = server.requireLegacyProject(request.Context(), id); err != nil {
		writeProjectError(writer, err)
		return
	}
	var input struct {
		ExpectedVersion uint64 `json:"expectedVersion"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	result, err := server.service.ChangeProjectLifecycle(request.Context(), app.ChangeProjectLifecycleRequest{CommandMeta: requestCommandMeta(request, "project-lifecycle"), ProjectID: id, ExpectedVersion: input.ExpectedVersion}, state)
	if err != nil {
		writeProjectError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, projectViewOf(result))
}

func (server *Server) renameRoom(writer http.ResponseWriter, request *http.Request) {
	id, err := domain.ParseRoomID(request.PathValue("roomID"))
	if err != nil {
		writeProjectError(writer, err)
		return
	}
	var input struct {
		Name            string `json:"name"`
		Description     string `json:"description"`
		ExpectedVersion uint64 `json:"expectedVersion"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	result, err := server.service.RenameRoom(request.Context(), app.RenameRoomRequest{CommandMeta: requestCommandMeta(request, "rename-room"), RoomID: id, Name: input.Name, Description: input.Description, ExpectedVersion: input.ExpectedVersion})
	if err != nil {
		writeProjectError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, roomViewOf(result))
}

func writeProjectError(writer http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, app.ErrProjectNotFound), errors.Is(err, storecontract.ErrNotFound):
		writeError(writer, http.StatusNotFound, err)
	case errors.Is(err, app.ErrProjectOverlap), errors.Is(err, app.ErrProjectUpgradeRequired), errors.Is(err, app.ErrRepositoryIdentityDrift), errors.Is(err, storecontract.ErrRepositoryBindingConflict):
		writeError(writer, http.StatusConflict, err)
	case errors.Is(err, app.ErrRepositoryNotGit), errors.Is(err, app.ErrRepositoryBare), errors.Is(err, app.ErrRepositoryLinkedWorktree):
		writeError(writer, http.StatusUnprocessableEntity, err)
	case errors.Is(err, app.ErrCloneFailed):
		writeError(writer, http.StatusBadGateway, err)
	case errors.Is(err, app.ErrRepositoryUnavailable):
		writeError(writer, http.StatusServiceUnavailable, err)
	case errors.Is(err, storecontract.ErrVersionConflict), errors.Is(err, storecontract.ErrIdempotencyConflict), errors.Is(err, storecontract.ErrRoomStateForbidden):
		writeError(writer, http.StatusConflict, err)
	case errors.Is(err, app.ErrUnauthorizedCommand):
		writeError(writer, http.StatusForbidden, err)
	case errors.Is(err, app.ErrInvalidCommand), errors.Is(err, domain.ErrInvalidArgument), errors.Is(err, domain.ErrInvalidID):
		writeError(writer, http.StatusBadRequest, err)
	default:
		writeError(writer, http.StatusInternalServerError, err)
	}
}
