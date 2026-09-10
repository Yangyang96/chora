package localweb

import (
	"context"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/http"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

var errTaskWorkspacePreparing = errors.New("Preparing task worktree…")

type projectDirectoryPicker interface {
	Choose(context.Context) (string, error)
}

type macOSProjectDirectoryPicker struct{}

// Fixed script: neither browser input nor a selected path becomes executable code.
const chooseProjectDirectoryScript = `activate
try
  set projectFolder to choose folder with prompt "Chora — 选择本机 Git 项目文件夹 / Choose a local Git project"
  return POSIX path of projectFolder
on error number -128
  return ""
end try`

func (macOSProjectDirectoryPicker) Choose(ctx context.Context) (string, error) {
	if runtime.GOOS != "darwin" {
		return "", errors.New("project folder selection requires macOS")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	output, err := exec.CommandContext(ctx, "/usr/bin/osascript", "-e", chooseProjectDirectoryScript).Output()
	if err != nil {
		return "", errors.New("could not open the folder chooser; close any pending dialog and try again")
	}
	path := strings.TrimSuffix(string(output), "\n")
	if path == "" {
		return "", nil // User cancelled the native dialog.
	}
	if !filepath.IsAbs(path) {
		return "", errors.New("folder chooser did not return an absolute directory")
	}
	return filepath.Clean(path), nil
}

func (server *Server) chooseProjectDirectory(writer http.ResponseWriter, request *http.Request) {
	// A native dialog belongs to the local desktop, not a remote browser client.
	host, _, err := net.SplitHostPort(request.Host)
	if err != nil {
		host = request.Host
	}
	if host != "localhost" && !net.ParseIP(host).IsLoopback() {
		writeError(writer, http.StatusForbidden, errors.New("folder selection requires a local Chora address"))
		return
	}
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeError(writer, http.StatusUnsupportedMediaType, errors.New("JSON request required"))
		return
	}
	var input struct{}
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	if server.projectsUnavailable(writer) {
		return
	}
	if server.directoryPicker == nil {
		writeError(writer, http.StatusServiceUnavailable, errors.New("project folder chooser is unavailable"))
		return
	}
	if !server.directoryPickerMu.TryLock() {
		writeError(writer, http.StatusConflict, errors.New("a folder chooser is already open; finish or cancel it first"))
		return
	}
	defer server.directoryPickerMu.Unlock()
	path, err := server.directoryPicker.Choose(request.Context())
	if err != nil {
		writeError(writer, http.StatusServiceUnavailable, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"cancelled": path == "", "locator": path})
}

// The client chooses an admitted identity and a fixed application, never a path
// or command. The provider receives one validated directory as an argv operand.
type externalDirectoryOpener interface {
	Open(context.Context, string, string) error
}
type macOSDirectoryOpener struct{}

func externalDirectoryArgs(application, path string) ([]string, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
		return nil, errors.New("invalid external directory")
	}
	definition, ok := externalApplicationDefinition(application)
	if !ok {
		return nil, errors.New("unsupported application")
	}
	return []string{"-a", strings.TrimSuffix(definition.Bundle, ".app"), path}, nil
}
func (macOSDirectoryOpener) Open(ctx context.Context, application, path string) error {
	args, err := externalDirectoryArgs(application, path)
	if err != nil {
		return err
	}
	if runtime.GOOS != "darwin" {
		return errors.New("external opening requires macOS")
	}
	applicationPath, err := installedApplicationPath(application)
	if err != nil {
		return err
	}
	args[1] = applicationPath
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := exec.CommandContext(ctx, "/usr/bin/open", args...).Run(); err != nil {
		return fmt.Errorf("could not open %s; install the application or open the directory manually: %w", application, err)
	}
	return nil
}

func (server *Server) continuityDirectory(ctx context.Context, roomText, taskText string) (string, error) {
	var project app.ProjectView
	var err error
	if taskText != "" {
		taskID, parseErr := domain.ParseTaskID(taskText)
		if parseErr != nil {
			return "", parseErr
		}
		task, readErr := server.store.Reader().GetTask(ctx, taskID)
		if readErr != nil {
			return "", readErr
		}
		room, readErr := server.store.Reader().GetRoom(ctx, task.RoomID())
		if readErr != nil {
			return "", readErr
		}
		owner, readErr := server.store.Reader().GetProject(ctx, room.ProjectID())
		if readErr != nil {
			return "", readErr
		}
		if roomText != owner.ID().String() && roomText != room.ID().String() {
			return "", errors.New("Task does not belong to the selected Project/Room")
		}
		if owner.State() != domain.ProjectStateActive || room.State() != domain.RoomStateActive {
			return "", errors.New("Project or Room is archived; restore it before continuing")
		}
		if _, resourceErr := server.store.Reader().GetTaskResourceSnapshot(ctx, taskID); resourceErr == nil {
			// Creation publishes the Task before CreateRun prepares its worktrees.
			// This is a read-only readiness probe; never provision from a GET.
			rows, rowErr := server.store.Reader().ListTaskRepositoryWorktrees(ctx, taskID)
			if rowErr != nil {
				return "", rowErr
			}
			pending := len(rows) == 0
			failed := false
			for _, row := range rows {
				pending = pending || row.State == "preparing"
				failed = failed || (row.State != "preparing" && row.State != "ready")
			}
			if pending && !failed {
				history, historyErr := server.store.Reader().ListTaskRunHistory(ctx, task.RoomID(), taskID)
				if historyErr != nil {
					return "", historyErr
				}
				// Missing records after a Run exists are corruption, not startup.
				if len(history) == 0 {
					return "", errTaskWorkspacePreparing
				}
			}
			return server.service.ResolveTaskResourceRoot(ctx, taskID)
		} else if !errors.Is(resourceErr, storecontract.ErrNotFound) {
			return "", resourceErr
		}
		// A historical Task keeps its own frozen single-repository binding even
		// after its Project gains other resources.
		project, err = server.service.GetProjectByID(ctx, owner.ID())
	} else {
		project, err = server.projectFromRoute(ctx, roomText)
	}
	if err != nil {
		return "", err
	}
	roomID := project.Project.DefaultRoomID()
	legacyRoomRoute := strings.HasPrefix(roomText, "room_")
	if legacyRoomRoute {
		roomID, _ = domain.ParseRoomID(roomText)
	}
	if project.RepositoryBinding.State() != domain.RepositoryBindingStateActive || project.Project.State() != domain.ProjectStateActive {
		return "", errors.New("Project is archived; restore it from Projects before continuing")
	}
	if server.repositorySource == nil {
		return "", errors.New("Git repository provider is unavailable; restart Chora with Workbench enabled")
	}
	expected := project.RepositoryBinding.LocalLocator()
	inspection, err := server.repositorySource.Inspect(ctx, expected)
	if err != nil {
		return "", fmt.Errorf("Project repository is unavailable; restore it at %s or reopen the repository from Projects: %w", expected, err)
	}
	if inspection.CanonicalPath != expected {
		return "", errors.New("Project repository path changed; restore the original directory before continuing")
	}
	if taskText == "" {
		return expected, nil
	}
	taskID, err := domain.ParseTaskID(taskText)
	if err != nil {
		return "", err
	}
	task, err := server.store.Reader().GetTask(ctx, taskID)
	if err != nil {
		return "", err
	}
	room, err := server.store.Reader().GetRoom(ctx, task.RoomID())
	if err != nil {
		return "", err
	}
	if room.ProjectID() != project.Project.ID() || (legacyRoomRoute && task.RoomID() != roomID) {
		return "", errors.New("Task does not belong to the selected Project/Room")
	}
	if room.State() != domain.RoomStateActive {
		return "", errors.New("Room is archived; restore it before continuing")
	}
	if server.worktreeResolver == nil {
		return "", errors.New("Task worktree resolver is unavailable")
	}
	root, err := server.worktreeResolver.ResolveExecutionRoot(ctx, task)
	if err != nil || root == "" {
		return "", fmt.Errorf("Task worktree is unavailable; restore its original directory and restart Chora before continuing: %v", err)
	}
	return root, nil
}

func (server *Server) projectContinuity(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Query().Get("taskId") == "" {
		owner, ownerErr := domain.ParseProjectID(request.PathValue("projectID"))
		if strings.HasPrefix(request.PathValue("projectID"), "room_") {
			roomID, parseErr := domain.ParseRoomID(request.PathValue("projectID"))
			ownerErr = parseErr
			if parseErr == nil {
				room, readErr := server.store.Reader().GetRoom(request.Context(), roomID)
				owner, ownerErr = room.ProjectID(), readErr
			}
		}
		if ownerErr == nil && errors.Is(server.requireLegacyProject(request.Context(), owner), app.ErrProjectUpgradeRequired) {
			writeJSON(writer, http.StatusOK, map[string]any{"available": true, "ready": false, "selectionRequired": true})
			return
		}
	}
	path, err := server.continuityDirectory(request.Context(), request.PathValue("projectID"), request.URL.Query().Get("taskId"))
	if errors.Is(err, errTaskResourceWorkspacesCleaned) {
		writeJSON(writer, http.StatusOK, map[string]any{"available": true, "ready": false, "cleanedUp": true, "reason": errTaskResourceWorkspacesCleaned.Error()})
		return
	}
	if errors.Is(err, errTaskWorkspacePreparing) {
		writeJSON(writer, http.StatusOK, map[string]any{"available": true, "ready": false, "preparing": true, "reason": errTaskWorkspacePreparing.Error(), "applications": installedExternalApplications()})
		return
	}
	if errors.Is(err, app.ErrProjectNotFound) {
		writeJSON(writer, http.StatusOK, map[string]any{"available": false})
		return
	}
	if err != nil {
		writeJSON(writer, http.StatusOK, map[string]any{"available": true, "ready": false, "reason": err.Error(), "applications": installedExternalApplications()})
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"available": true, "ready": true, "path": path, "applications": installedExternalApplications()})
}
func (server *Server) openExternalDirectory(writer http.ResponseWriter, request *http.Request) {
	host, _, err := net.SplitHostPort(request.Host)
	if err != nil {
		host = request.Host
	}
	if host != "localhost" && !net.ParseIP(host).IsLoopback() {
		writeError(writer, http.StatusForbidden, errors.New("external opening requires a local Chora address"))
		return
	}
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeError(writer, http.StatusUnsupportedMediaType, errors.New("JSON request required"))
		return
	}
	var input struct {
		TaskID      string `json:"taskId"`
		Application string `json:"application"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	if _, ok := externalApplicationDefinition(input.Application); !ok {
		writeError(writer, http.StatusBadRequest, errors.New("unsupported application"))
		return
	}
	path, err := server.continuityDirectory(request.Context(), request.PathValue("projectID"), input.TaskID)
	if err != nil {
		writeError(writer, http.StatusConflict, err)
		return
	}
	if server.externalOpener == nil {
		writeError(writer, http.StatusServiceUnavailable, errors.New("external opener is unavailable"))
		return
	}
	if err := server.externalOpener.Open(request.Context(), input.Application, path); err != nil {
		writeError(writer, http.StatusServiceUnavailable, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"opened": true, "path": path})
}
