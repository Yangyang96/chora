package apppreview

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	manifestSchema = "chora.app-preview.v1"
	manifestOwner  = "chora:internal/apppreview"
	defaultLogCap  = int64(2 << 20)
	maxManifest    = int64(1 << 20)
)

var (
	keyPattern           = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	imagePattern         = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
	containerNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
)

type manifest struct {
	Schema         string `json:"schema"`
	Owner          string `json:"owner"`
	Nonce          string `json:"nonce"`
	View           View   `json:"view"`
	PID            int    `json:"pid,omitempty"`
	PGID           int    `json:"pgid,omitempty"`
	Birth          string `json:"birth,omitempty"`
	ContainerID    string `json:"containerId,omitempty"`
	ContainerName  string `json:"containerName,omitempty"`
	ImageID        string `json:"imageId,omitempty"`
	EngineIdentity string `json:"engineIdentity,omitempty"`
	SourceSnapshot string `json:"sourceSnapshot,omitempty"`
	DockerLogBytes int64  `json:"dockerLogBytes,omitempty"`
}

type Manager struct {
	root      string
	logCap    int64
	resolve   func(context.Context) (IsolatedRuntime, error)
	mu        sync.Mutex
	children  map[string]*hostChild
	checkPort func(int) error
}

type hostChild struct {
	done chan struct{}
}

func New(options Options) (*Manager, error) {
	if strings.TrimSpace(options.RuntimeRoot) == "" {
		return nil, errors.New("app preview runtime root is required")
	}
	root, err := filepath.Abs(filepath.Clean(options.RuntimeRoot))
	if err != nil {
		return nil, fmt.Errorf("resolve app preview runtime root: %w", err)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create app preview runtime root: %w", err)
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("app preview runtime root must be a private non-symlink directory")
	}
	capacity := options.LogCapacity
	if capacity == 0 {
		capacity = defaultLogCap
	}
	if capacity < 1024 || capacity > 64<<20 {
		return nil, errors.New("app preview log capacity is invalid")
	}
	return &Manager{root: root, logCap: capacity, resolve: options.ResolveIsolated, children: make(map[string]*hostChild), checkPort: checkLoopbackPort}, nil
}

func (manager *Manager) Save(_ context.Context, target Target, config Config) (View, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if err := validateTarget(target); err != nil {
		return View{}, err
	}
	if err := validateConfig(target.Root, config); err != nil {
		return View{}, err
	}
	current, err := manager.load(target.Key)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return View{}, err
	}
	if err == nil && (current.View.State == StateRunning || current.View.State == StateStarting || current.View.CleanupRequired || current.View.State == StateRecoveryRequired) {
		return View{}, errors.New("cannot replace configuration while preview is active or requires recovery")
	}
	nonce, err := randomNonce()
	if err != nil {
		return View{}, err
	}
	view := View{Key: target.Key, State: StateIdle, Target: target, Config: config}
	record := manifest{Schema: manifestSchema, Owner: manifestOwner, Nonce: nonce, View: view}
	if err := manager.persist(record); err != nil {
		return View{}, err
	}
	return view, nil
}

func (manager *Manager) Start(ctx context.Context, target Target, config Config) (View, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if err := validateTarget(target); err != nil {
		return View{}, err
	}
	if err := validateConfig(target.Root, config); err != nil {
		return View{}, err
	}
	if existing, err := manager.load(target.Key); err == nil && (existing.View.State == StateRunning || existing.View.State == StateStarting || existing.View.CleanupRequired) {
		return existing.View, errors.New("app preview is already active or requires recovery")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return View{}, err
	}
	nonce, err := randomNonce()
	if err != nil {
		return View{}, err
	}
	now := time.Now().UTC()
	record := manifest{Schema: manifestSchema, Owner: manifestOwner, Nonce: nonce, View: View{Key: target.Key, State: StateStarting, Target: target, Config: config, StartedAt: &now}}
	if err := manager.persist(record); err != nil {
		return View{}, err
	}
	if target.Profile == ProfileIsolatedLocal {
		return manager.startIsolated(ctx, record)
	}
	return manager.startHost(record)
}

func (manager *Manager) Inspect(ctx context.Context, key string) (View, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if !keyPattern.MatchString(key) {
		return View{}, errors.New("invalid app preview key")
	}
	record, err := manager.load(key)
	if errors.Is(err, os.ErrNotExist) {
		return View{Key: key, State: StateIdle}, nil
	}
	if err != nil {
		return View{}, err
	}
	active := record.View.State == StateRunning || record.View.State == StateStarting
	if record.View.Target.Profile == ProfileIsolatedLocal && record.View.CleanupRequired && record.ContainerName != "" {
		active = true
	}
	if !active {
		return manager.withLogSize(record.View), nil
	}
	if record.View.Target.Profile == ProfileIsolatedLocal {
		record = manager.inspectIsolated(ctx, record)
	} else {
		record = manager.inspectHost(record)
	}
	if err := manager.persist(record); err != nil {
		return View{}, err
	}
	return manager.withLogSize(record.View), nil
}

func (manager *Manager) Stop(ctx context.Context, key string) (View, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	record, err := manager.load(key)
	if errors.Is(err, os.ErrNotExist) {
		return View{Key: key, State: StateIdle}, nil
	}
	if err != nil {
		return View{}, err
	}
	if record.View.State != StateRunning && record.View.State != StateStarting && !record.View.CleanupRequired {
		return manager.withLogSize(record.View), nil
	}
	if record.View.Target.Profile == ProfileIsolatedLocal {
		err = manager.stopIsolated(ctx, &record)
	} else {
		err = manager.stopHost(&record)
	}
	if err != nil {
		record.View.State = StateRecoveryRequired
		record.View.CleanupRequired = true
		record.View.Reason = err.Error()
	}
	if persistErr := manager.persist(record); persistErr != nil {
		err = errors.Join(err, persistErr)
	}
	return manager.withLogSize(record.View), err
}

func (manager *Manager) StopAll(ctx context.Context) error {
	entries, err := os.ReadDir(manager.root)
	if err != nil {
		return err
	}
	var result error
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		record, readErr := manager.loadByDirectory(filepath.Join(manager.root, entry.Name()))
		if readErr != nil {
			result = errors.Join(result, readErr)
			continue
		}
		_, stopErr := manager.Stop(ctx, record.View.Key)
		result = errors.Join(result, stopErr)
	}
	return result
}

func (manager *Manager) Recover(ctx context.Context) ([]View, error) {
	entries, err := os.ReadDir(manager.root)
	if err != nil {
		return nil, err
	}
	views := make([]View, 0, len(entries))
	var result error
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		record, readErr := manager.loadByDirectory(filepath.Join(manager.root, entry.Name()))
		if readErr != nil {
			result = errors.Join(result, readErr)
			continue
		}
		view, inspectErr := manager.Inspect(ctx, record.View.Key)
		if inspectErr != nil {
			result = errors.Join(result, inspectErr)
			continue
		}
		views = append(views, view)
	}
	return views, result
}

func (manager *Manager) Cleanup(ctx context.Context, key string) (View, error) {
	view, err := manager.Stop(ctx, key)
	if err != nil || view.CleanupRequired || view.State == StateRunning || view.State == StateStarting {
		return view, errors.Join(err, errors.New("app preview ownership must be stopped before cleanup"))
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	record, err := manager.load(key)
	if err != nil {
		return View{}, err
	}
	if record.SourceSnapshot != "" {
		expected := filepath.Join(manager.directory(key), "source")
		if record.SourceSnapshot != expected {
			return View{}, errors.New("app preview snapshot ownership is invalid")
		}
		if err := os.RemoveAll(expected); err != nil {
			return View{}, err
		}
	}
	record.PID, record.PGID, record.ContainerID = 0, 0, ""
	record.Birth, record.ContainerName, record.SourceSnapshot = "", "", ""
	record.View.State, record.View.CleanupRequired, record.View.Reason = StateIdle, false, ""
	record.View.URL, record.View.StartedAt, record.View.ExitCode = "", nil, nil
	if err := manager.persist(record); err != nil {
		return View{}, err
	}
	return manager.withLogSize(record.View), nil
}

func (manager *Manager) Logs(key string, offset int64, limit int) (LogSlice, error) {
	if !keyPattern.MatchString(key) || offset < 0 || limit < 0 || limit > 1<<20 {
		return LogSlice{}, errors.New("invalid app preview log range")
	}
	path := manager.logPath(key)
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return LogSlice{}, nil
	}
	if err != nil {
		return LogSlice{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > manager.logCap {
		return LogSlice{}, errors.New("app preview log ownership or bounds are invalid")
	}
	if offset > info.Size() {
		return LogSlice{}, errors.New("app preview log offset exceeds size")
	}
	count := int64(limit)
	if count > info.Size()-offset {
		count = info.Size() - offset
	}
	data := make([]byte, count)
	n, err := file.ReadAt(data, offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return LogSlice{}, err
	}
	data = data[:n]
	record, _ := manager.load(key)
	return LogSlice{Data: data, NextOffset: offset + int64(n), Size: info.Size(), Truncated: record.View.LogTruncated}, nil
}

func validateTarget(target Target) error {
	if !keyPattern.MatchString(target.Key) || strings.TrimSpace(target.AttemptID) == "" || (target.Profile != ProfileLocalConnected && target.Profile != ProfileIsolatedLocal) {
		return errors.New("invalid app preview target")
	}
	if !filepath.IsAbs(target.Root) || filepath.Clean(target.Root) != target.Root {
		return errors.New("app preview worktree root must be absolute and clean")
	}
	info, err := os.Stat(target.Root)
	if err != nil || !info.IsDir() {
		return errors.New("app preview worktree root is unavailable")
	}
	return nil
}

func validateConfig(root string, config Config) error {
	if strings.TrimSpace(config.Command) == "" || len(config.Command) > 16<<10 || strings.IndexByte(config.Command, 0) >= 0 || config.Port < 1 || config.Port > 65535 {
		return errors.New("invalid app preview configuration")
	}
	if config.WorkingDirectory == "" {
		config.WorkingDirectory = "."
	}
	if filepath.IsAbs(config.WorkingDirectory) || filepath.Clean(config.WorkingDirectory) != config.WorkingDirectory || config.WorkingDirectory == ".." || strings.HasPrefix(config.WorkingDirectory, ".."+string(filepath.Separator)) {
		return errors.New("app preview working directory must stay within the worktree")
	}
	resolved := filepath.Join(root, config.WorkingDirectory)
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	realDir, err := filepath.EvalSymlinks(resolved)
	if err != nil {
		return errors.New("app preview working directory is unavailable")
	}
	relative, err := filepath.Rel(realRoot, realDir)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("app preview working directory escaped the worktree")
	}
	info, err := os.Stat(realDir)
	if err != nil || !info.IsDir() {
		return errors.New("app preview working directory is not a directory")
	}
	return nil
}

func (manager *Manager) directory(key string) string {
	digest := sha256.Sum256([]byte(key))
	return filepath.Join(manager.root, hex.EncodeToString(digest[:]))
}

func (manager *Manager) logPath(key string) string {
	return filepath.Join(manager.directory(key), "preview.log")
}

func (manager *Manager) persist(record manifest) error {
	directory := manager.directory(record.View.Key)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(record)
	if err != nil || len(data) > int(maxManifest) {
		return errors.New("app preview manifest is invalid")
	}
	temporary, err := os.CreateTemp(directory, ".manifest-")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err := temporary.Chmod(0o600); err == nil {
		_, err = temporary.Write(data)
	}
	err = errors.Join(err, temporary.Sync(), temporary.Close())
	if err != nil {
		return err
	}
	return os.Rename(name, filepath.Join(directory, "manifest.json"))
}

func (manager *Manager) load(key string) (manifest, error) {
	if !keyPattern.MatchString(key) {
		return manifest{}, errors.New("invalid app preview key")
	}
	return manager.loadByDirectory(manager.directory(key))
}

func (manager *Manager) loadByDirectory(directory string) (manifest, error) {
	path := filepath.Join(directory, "manifest.json")
	info, err := os.Lstat(path)
	if err != nil {
		return manifest{}, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() > maxManifest {
		return manifest{}, errors.New("app preview manifest ownership or bounds are invalid")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return manifest{}, err
	}
	var record manifest
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil || decoder.Decode(new(any)) != io.EOF || record.Schema != manifestSchema || record.Owner != manifestOwner || !keyPattern.MatchString(record.View.Key) || manager.directory(record.View.Key) != directory {
		return manifest{}, errors.New("app preview manifest is invalid")
	}
	return record, nil
}

func randomNonce() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

func (manager *Manager) withLogSize(view View) View {
	if info, err := os.Stat(manager.logPath(view.Key)); err == nil && info.Mode().IsRegular() {
		view.LogSize = info.Size()
	}
	return view
}
