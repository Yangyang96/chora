package nativecapabilities

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/Yangyang96/chora/internal/domain"
)

const (
	maxConfigBytes = 64 << 10
	maxPaths       = 64
	maxServers     = 128
	maxPathBytes   = 4096
	maxServerBytes = 256
)

var (
	ErrConflict              = errors.New("native capabilities config version conflict")
	ErrInvalidConfig         = errors.New("invalid native capabilities config")
	errConfigDirectoryAbsent = errors.New("native capabilities config directory is absent")
	configMu                 sync.Mutex
)

// Config records Project-wide references to local native capability resources.
// It contains paths and server names only; referenced files and credentials are
// never copied into this document.
type Config struct {
	Version            uint64   `json:"version"`
	SkillPaths         []string `json:"skillPaths"`
	DisabledSkillPaths []string `json:"disabledSkillPaths"`
	BridgePath         string   `json:"bridgePath"`
	MCPConfigPath      string   `json:"mcpConfigPath"`
	DisabledMCPServers []string `json:"disabledMcpServers"`
}

// Read returns the configured Project references. A missing document is an
// unconfigured Project at version zero.
func Read(root string, projectID string) (Config, error) {
	configMu.Lock()
	defer configMu.Unlock()
	return read(root, projectID)
}

// Save atomically replaces one Project document when expected is current.
func Save(root string, projectID string, expected uint64, next Config) (Config, error) {
	configMu.Lock()
	defer configMu.Unlock()

	current, err := read(root, projectID)
	if err != nil {
		return Config{}, err
	}
	if current.Version != expected {
		return Config{}, ErrConflict
	}
	if expected == math.MaxUint64 {
		return Config{}, fmt.Errorf("%w: config version is exhausted", ErrInvalidConfig)
	}
	next.Version = expected + 1
	next = normalize(next)
	if err := validate(next); err != nil {
		return Config{}, err
	}
	directory, err := configDirectory(root, true)
	if err != nil {
		return Config{}, err
	}
	path, err := configPath(directory, projectID)
	if err != nil {
		return Config{}, err
	}
	if info, statErr := os.Lstat(path); statErr == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return Config{}, fmt.Errorf("%w: config target must be a regular file", ErrInvalidConfig)
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return Config{}, fmt.Errorf("inspect native capabilities config: %w", statErr)
	}

	document, err := json.Marshal(next)
	if err != nil {
		return Config{}, fmt.Errorf("encode native capabilities config: %w", err)
	}
	if len(document) > maxConfigBytes {
		return Config{}, fmt.Errorf("%w: config document exceeds %d bytes", ErrInvalidConfig, maxConfigBytes)
	}
	temporary, err := os.CreateTemp(directory, ".native-capabilities-*.tmp")
	if err != nil {
		return Config{}, fmt.Errorf("create native capabilities config: %w", err)
	}
	temporaryPath := temporary.Name()
	keepTemporary := true
	defer func() {
		_ = temporary.Close()
		if keepTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return Config{}, fmt.Errorf("protect native capabilities config: %w", err)
	}
	if _, err := temporary.Write(append(document, '\n')); err != nil {
		return Config{}, fmt.Errorf("write native capabilities config: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return Config{}, fmt.Errorf("sync native capabilities config: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return Config{}, fmt.Errorf("close native capabilities config: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return Config{}, fmt.Errorf("replace native capabilities config: %w", err)
	}
	keepTemporary = false
	if directoryHandle, openErr := os.Open(directory); openErr == nil {
		_ = directoryHandle.Sync()
		_ = directoryHandle.Close()
	}
	return next, nil
}

func read(root string, projectID string) (Config, error) {
	directory, err := configDirectory(root, false)
	if errors.Is(err, errConfigDirectoryAbsent) {
		if _, parseErr := domain.ParseProjectID(projectID); parseErr != nil {
			return Config{}, fmt.Errorf("%w: invalid Project ID", ErrInvalidConfig)
		}
		return emptyConfig(), nil
	}
	if err != nil {
		return Config{}, err
	}
	path, err := configPath(directory, projectID)
	if err != nil {
		return Config{}, err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return emptyConfig(), nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("inspect native capabilities config: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return Config{}, fmt.Errorf("%w: config must be a regular file", ErrInvalidConfig)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return Config{}, fmt.Errorf("%w: config permissions must be owner-only", ErrInvalidConfig)
	}
	file, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("open native capabilities config: %w", err)
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil || !os.SameFile(info, openedInfo) {
		return Config{}, fmt.Errorf("%w: config identity changed while opening", ErrInvalidConfig)
	}
	document, err := io.ReadAll(io.LimitReader(file, maxConfigBytes+1))
	if err != nil {
		return Config{}, fmt.Errorf("read native capabilities config: %w", err)
	}
	if len(document) > maxConfigBytes {
		return Config{}, fmt.Errorf("%w: config document exceeds %d bytes", ErrInvalidConfig, maxConfigBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.DisallowUnknownFields()
	var config Config
	if err := decoder.Decode(&config); err != nil {
		return Config{}, fmt.Errorf("%w: decode config: %v", ErrInvalidConfig, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Config{}, fmt.Errorf("%w: trailing config content", ErrInvalidConfig)
	}
	config = normalize(config)
	if config.Version == 0 {
		return Config{}, fmt.Errorf("%w: persisted version must be positive", ErrInvalidConfig)
	}
	if err := validate(config); err != nil {
		return Config{}, err
	}
	return config, nil
}

func configDirectory(root string, create bool) (string, error) {
	if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return "", fmt.Errorf("%w: data root must be a clean absolute path", ErrInvalidConfig)
	}
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return "", fmt.Errorf("inspect data root: %w", err)
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return "", fmt.Errorf("%w: data root must be a real directory", ErrInvalidConfig)
	}
	directory := filepath.Join(root, "native-capabilities")
	if create {
		if err := os.Mkdir(directory, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return "", fmt.Errorf("create native capabilities directory: %w", err)
		}
	}
	info, err := os.Lstat(directory)
	if errors.Is(err, os.ErrNotExist) {
		return "", errConfigDirectoryAbsent
	}
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", fmt.Errorf("%w: native capabilities directory must be a real directory", ErrInvalidConfig)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("%w: native capabilities directory permissions must be owner-only", ErrInvalidConfig)
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve data root: %w", err)
	}
	resolvedDirectory, err := filepath.EvalSymlinks(directory)
	if err != nil {
		return "", fmt.Errorf("resolve native capabilities directory: %w", err)
	}
	if resolvedDirectory != filepath.Join(resolvedRoot, "native-capabilities") {
		return "", fmt.Errorf("%w: native capabilities directory escapes data root", ErrInvalidConfig)
	}
	return directory, nil
}

func configPath(directory, projectID string) (string, error) {
	if _, err := domain.ParseProjectID(projectID); err != nil {
		return "", fmt.Errorf("%w: invalid Project ID", ErrInvalidConfig)
	}
	path := filepath.Join(directory, projectID+".json")
	if filepath.Dir(path) != directory {
		return "", fmt.Errorf("%w: config path escapes data root", ErrInvalidConfig)
	}
	return path, nil
}

func emptyConfig() Config {
	return Config{SkillPaths: []string{}, DisabledSkillPaths: []string{}, DisabledMCPServers: []string{}}
}

func normalize(config Config) Config {
	if config.SkillPaths == nil {
		config.SkillPaths = []string{}
	}
	if config.DisabledSkillPaths == nil {
		config.DisabledSkillPaths = []string{}
	}
	if config.DisabledMCPServers == nil {
		config.DisabledMCPServers = []string{}
	}
	return config
}

func validate(config Config) error {
	if err := validatePaths("skillPaths", config.SkillPaths, maxPaths); err != nil {
		return err
	}
	if err := validatePaths("disabledSkillPaths", config.DisabledSkillPaths, maxPaths); err != nil {
		return err
	}
	for name, path := range map[string]string{"bridgePath": config.BridgePath, "mcpConfigPath": config.MCPConfigPath} {
		if path != "" {
			if err := validatePath(name, path); err != nil {
				return err
			}
		}
	}
	if len(config.DisabledMCPServers) > maxServers {
		return fmt.Errorf("%w: disabledMcpServers exceeds %d entries", ErrInvalidConfig, maxServers)
	}
	seenServers := make(map[string]struct{}, len(config.DisabledMCPServers))
	for _, server := range config.DisabledMCPServers {
		if server == "" || strings.TrimSpace(server) != server || len(server) > maxServerBytes || !utf8.ValidString(server) || strings.IndexFunc(server, unicode.IsControl) >= 0 {
			return fmt.Errorf("%w: disabledMcpServers contains an invalid server name", ErrInvalidConfig)
		}
		if _, duplicate := seenServers[server]; duplicate {
			return fmt.Errorf("%w: disabledMcpServers contains a duplicate", ErrInvalidConfig)
		}
		seenServers[server] = struct{}{}
	}
	return nil
}

func validatePaths(name string, paths []string, limit int) error {
	if len(paths) > limit {
		return fmt.Errorf("%w: %s exceeds %d entries", ErrInvalidConfig, name, limit)
	}
	seen := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		if err := validatePath(name, path); err != nil {
			return err
		}
		if _, duplicate := seen[path]; duplicate {
			return fmt.Errorf("%w: %s contains a duplicate", ErrInvalidConfig, name)
		}
		seen[path] = struct{}{}
	}
	return nil
}

func validatePath(name, path string) error {
	if path == "" || len(path) > maxPathBytes || !utf8.ValidString(path) || strings.IndexFunc(path, unicode.IsControl) >= 0 || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return fmt.Errorf("%w: %s must contain clean absolute paths", ErrInvalidConfig, name)
	}
	return nil
}
