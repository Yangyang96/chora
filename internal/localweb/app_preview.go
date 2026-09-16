package localweb

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/Yangyang96/chora/internal/apppreview"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/isolatedenv"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

type appPreviewRepository struct {
	RepoID string `json:"repoId"`
	Name   string `json:"name"`
}

type appPreviewState struct {
	State        string            `json:"state"`
	Config       apppreview.Config `json:"config"`
	URL          string            `json:"url,omitempty"`
	Logs         string            `json:"logs,omitempty"`
	LogTruncated bool              `json:"logTruncated,omitempty"`
	Reason       string            `json:"reason,omitempty"`
}

type appPreviewView struct {
	RunID        string                 `json:"runId"`
	TaskID       string                 `json:"taskId"`
	Profile      string                 `json:"profile"`
	Available    bool                   `json:"available"`
	Reason       string                 `json:"reason,omitempty"`
	Repositories []appPreviewRepository `json:"repositories"`
	RepoID       string                 `json:"repoId"`
	Preview      appPreviewState        `json:"preview"`
	Suggestions  []apppreview.Config    `json:"suggestions"`
}

// The preview mutex serializes starts with successor creation and worktree
// cleanup. Process lifetime belongs to the server, never an HTTP request.
func (s *Server) appPreviewManager() (*apppreview.Manager, error) {
	if s.ctx.Err() != nil {
		return nil, errors.New("Chora is stopping")
	}
	if !filepath.IsAbs(s.runtimeRoot) {
		return nil, errors.New("App preview runtime storage is unavailable")
	}
	if s.appPreviews == nil {
		manager, err := apppreview.New(apppreview.Options{
			RuntimeRoot: filepath.Join(s.runtimeRoot, "app-previews"),
			ResolveIsolated: func(ctx context.Context) (apppreview.IsolatedRuntime, error) {
				if s.isolatedLocal == nil {
					return apppreview.IsolatedRuntime{}, errors.New("Isolated Local preview is unavailable; prepare the environment and restart Chora")
				}
				record, err := isolatedenv.Load(ctx, s.isolatedLocal.dataRoot)
				if err != nil {
					return apppreview.IsolatedRuntime{}, err
				}
				return apppreview.IsolatedRuntime{Runner: record.Runner, ImageID: record.Source.ImageID(), EngineIdentity: record.EngineIdentity.Digest()}, nil
			},
		})
		if err != nil {
			return nil, err
		}
		s.appPreviews = manager
	}
	return s.appPreviews, nil
}

func (s *Server) getAppPreview(w http.ResponseWriter, r *http.Request) {
	s.appPreviewMu.Lock()
	defer s.appPreviewMu.Unlock()
	v, _, err := s.loadAppPreview(r.Context(), r.PathValue("runID"), r.URL.Query().Get("repoId"))
	if err != nil {
		writeMutationConflictError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) appPreviewCommand(w http.ResponseWriter, r *http.Request) {
	s.appPreviewMu.Lock()
	defer s.appPreviewMu.Unlock()
	var input struct {
		RepoID          string             `json:"repoId"`
		ExpectedVersion uint64             `json:"expectedVersion"`
		Config          *apppreview.Config `json:"config"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	action := r.PathValue("action")
	if action != "save" && action != "start" && action != "stop" && action != "cleanup" {
		http.NotFound(w, r)
		return
	}
	v, target, err := s.loadAppPreview(r.Context(), r.PathValue("runID"), input.RepoID)
	if err != nil {
		writeMutationConflictError(w, err)
		return
	}
	id, _ := domain.ParseRunID(v.RunID)
	run, err := s.store.Reader().GetRun(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if input.RepoID == "" || input.RepoID != v.RepoID {
		writeError(w, http.StatusBadRequest, errors.New("select a Task repository"))
		return
	}
	// Stale views may still stop a process. They never grant execution authority.
	if action == "save" || action == "start" {
		if run.Version() != input.ExpectedVersion {
			writeError(w, http.StatusConflict, storecontract.ErrVersionConflict)
			return
		}
		if !v.Available {
			writeError(w, http.StatusConflict, errors.New(v.Reason))
			return
		}
	}
	manager, err := s.appPreviewManager()
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err)
		return
	}
	ctx, cancel := context.WithTimeout(s.ctx, 60*time.Second)
	defer cancel()
	switch action {
	case "save", "start":
		config := v.Preview.Config
		if input.Config != nil {
			config = *input.Config
		}
		if action == "save" {
			_, err = manager.Save(ctx, target, config)
		} else {
			_, err = manager.Start(ctx, target, config)
		}
	case "stop":
		_, err = manager.Stop(ctx, target.Key)
	case "cleanup":
		_, err = manager.Cleanup(ctx, target.Key)
	}
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	v, _, err = s.loadAppPreview(r.Context(), v.RunID, v.RepoID)
	if err != nil {
		writeMutationConflictError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) loadAppPreview(ctx context.Context, runID, repoID string) (appPreviewView, apppreview.Target, error) {
	v := appPreviewView{RunID: runID, Repositories: []appPreviewRepository{}, Suggestions: []apppreview.Config{}, Preview: appPreviewState{State: "idle"}}
	var target apppreview.Target
	id, err := domain.ParseRunID(runID)
	if err != nil {
		return v, target, err
	}
	reader := s.store.Reader()
	run, err := reader.GetRun(ctx, id)
	if err != nil {
		return v, target, err
	}
	v.TaskID = run.TaskID().String()
	task, err := reader.GetTask(ctx, run.TaskID())
	if err != nil {
		return v, target, err
	}
	record, err := reader.GetTaskResourceSnapshot(ctx, run.TaskID())
	if errors.Is(err, storecontract.ErrNotFound) {
		v.Reason = "App preview requires a Task repository workspace"
		return v, target, nil
	}
	if err != nil {
		return v, target, err
	}
	var snapshot domain.TaskResourceSnapshot
	if err = json.Unmarshal(record.CanonicalJSON, &snapshot); err != nil {
		return v, target, err
	}
	raw, digest, err := snapshot.CanonicalJSON()
	if err != nil || digest != record.Digest || string(raw) != string(record.CanonicalJSON) {
		return v, target, errors.New("Task resource identity cannot be verified")
	}
	var selected domain.TaskRepositoryResource
	for _, resource := range snapshot.Resources {
		if resource.Role != "write" {
			continue
		}
		v.Repositories = append(v.Repositories, appPreviewRepository{resource.RepoID, resource.Name})
		if resource.RepoID == repoID || (repoID == "" && selected.RepoID == "") {
			selected = resource
		}
	}
	if selected.RepoID == "" {
		v.Reason = "Select a writable Task repository"
		return v, target, nil
	}
	v.RepoID = selected.RepoID
	target.Key = v.TaskID + "-" + v.RepoID
	attempt, err := reader.GetCurrentAttempt(ctx, id)
	if err != nil {
		return v, target, err
	}
	target.AttemptID = attempt.ID().String()
	switch attempt.AgentExecutionProfileBinding().Profile() {
	case domain.AgentExecutionProfileTrustedLocal:
		target.Profile = "local_connected"
	case domain.AgentExecutionProfileIsolatedLocal:
		target.Profile = "isolated_local"
	default:
		v.Reason = "App preview supports Local Connected and Isolated Local Tasks"
	}
	v.Profile = target.Profile
	manager, err := s.appPreviewManager()
	if err != nil {
		v.Reason = err.Error()
		return v, target, nil
	}
	p, err := manager.Inspect(ctx, target.Key)
	if err != nil {
		return v, target, err
	}
	v.Preview = appPreviewState{State: p.State, Config: p.Config, URL: p.URL, Reason: p.Reason}
	if v.Preview.State == "cleanup_required" {
		v.Preview.State = "recovery_required"
	}
	offset := max(int64(0), p.LogSize-65536)
	logs, err := manager.Logs(target.Key, offset, 65536)
	if err == nil {
		v.Preview.Logs = string(logs.Data)
		v.Preview.LogTruncated = logs.Truncated || offset > 0
	}
	if v.Reason != "" {
		return v, target, nil
	}
	room, err := reader.GetRoom(ctx, task.RoomID())
	if err != nil {
		return v, target, err
	}
	if task.State() != domain.TaskStateOpen || task.Archived() || room.State() != domain.RoomStateActive {
		v.Reason = "Reopen the Task and Room before starting an app"
		return v, target, nil
	}
	history, err := reader.ListTaskRunHistory(ctx, task.RoomID(), task.ID())
	if err != nil {
		return v, target, err
	}
	if len(history) == 0 || history[0].Run.ID() != id {
		v.Reason = "App preview is available from the latest Task run"
		return v, target, nil
	}
	switch run.State() {
	case domain.RunStateAwaitingReview, domain.RunStateRevisionRequired, domain.RunStateAccepted, domain.RunStateCompleted:
	default:
		v.Reason = "Wait for Agent execution and checks to finish before starting an app"
		return v, target, nil
	}
	if s.appPreviewWorkspaces == nil {
		v.Reason = "Task preview workspaces are unavailable"
		return v, target, nil
	}
	// Prove the full Task identity and current branch before selecting a root.
	if _, err = s.appPreviewWorkspaces.ResolveHandoffRoot(ctx, task.ID()); err != nil {
		v.Reason = "Task workspaces are unavailable or changed; restore them before previewing"
		return v, target, nil
	}
	target.Root, err = (taskDeliveryWorkspaceResolver{s.appPreviewWorkspaces}).DeliveryRoot(ctx, task.ID(), selected)
	if err != nil {
		v.Reason = "This Task worktree is unavailable or has been cleaned up"
		return v, target, nil
	}
	if suggestions, discoverErr := apppreview.Discover(target.Root); discoverErr == nil {
		for _, suggestion := range suggestions {
			v.Suggestions = append(v.Suggestions, suggestion.Config)
		}
	}
	v.Available = true
	return v, target, nil
}

// Called under appPreviewMu before operations that replace or remove a Task's
// working files. Unknown ownership fails closed before the existing mutation.
func (s *Server) stopTaskAppPreviews(ctx context.Context, taskID domain.TaskID) error {
	if s.appPreviews == nil {
		if s.runtimeRoot == "" {
			return nil
		}
		if _, err := os.Stat(filepath.Join(s.runtimeRoot, "app-previews")); errors.Is(err, os.ErrNotExist) {
			return nil
		}
	}
	manager, err := s.appPreviewManager()
	if err != nil {
		return err
	}
	record, err := s.store.Reader().GetTaskResourceSnapshot(ctx, taskID)
	if errors.Is(err, storecontract.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	var snapshot domain.TaskResourceSnapshot
	if err = json.Unmarshal(record.CanonicalJSON, &snapshot); err != nil {
		return err
	}
	for _, resource := range snapshot.Resources {
		if _, err = manager.Stop(ctx, taskID.String()+"-"+resource.RepoID); err != nil {
			return err
		}
	}
	return nil
}
