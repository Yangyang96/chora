package localweb

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path"
	"sort"
	"strings"
	"unicode"

	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/speccoding"
)

const maxAutomaticCheckDirectories = 256

var errAutomaticCheckDiscovery = errors.New("automatic check discovery failed")

// discoverAutomaticChecks reads only well-known configuration paths from the
// frozen commit. It does not list the repository tree, inspect dirty files, or
// execute any discovered command.
func discoverAutomaticChecks(ctx context.Context, root, revision string, directories []string) ([]domain.TaskCheckCommand, error) {
	if !validAutomaticCheckRevision(revision) {
		return nil, fmt.Errorf("%w: invalid frozen revision", errAutomaticCheckDiscovery)
	}
	directories, err := normalizedCheckDirectories(directories)
	if err != nil {
		return nil, err
	}
	discoveryCtx, cancel := context.WithTimeout(ctx, domain.RepositoryMetadataTimeout)
	defer cancel()
	resolved, err := gitTargetOutputBounded(discoveryCtx, root, domain.RepositoryMetadataBytes, "rev-parse", "--verify", revision+"^{commit}")
	if err != nil {
		if discoveryCtx.Err() != nil {
			return nil, discoveryCtx.Err()
		}
		return nil, fmt.Errorf("%w: frozen revision unavailable", errAutomaticCheckDiscovery)
	}
	if strings.TrimSpace(string(resolved)) != revision {
		return nil, fmt.Errorf("%w: frozen revision drifted", errAutomaticCheckDiscovery)
	}

	remaining := int64(domain.RepositoryMetadataBytes)
	result := make([]domain.TaskCheckCommand, 0)
	for _, directory := range directories {
		files := make(map[string][]byte)
		for _, name := range []string{"package.json", "GNUmakefile", "Makefile", "makefile", "go.mod", "pytest.ini", "pyproject.toml", "setup.cfg", "tox.ini", "pom.xml"} {
			relative := name
			if directory != "." {
				relative = path.Join(directory, name)
			}
			content, exists, readErr := readCommittedCheckConfig(discoveryCtx, root, revision, relative, &remaining)
			if readErr != nil {
				return nil, readErr
			}
			if exists {
				files[name] = content
			}
		}

		if content, ok := files["package.json"]; ok {
			var manifest struct {
				Scripts map[string]string `json:"scripts"`
			}
			if err := json.Unmarshal(content, &manifest); err != nil {
				return nil, fmt.Errorf("%w: %s/package.json is invalid JSON", errAutomaticCheckDiscovery, directory)
			}
			for _, script := range []string{"test", "typecheck", "lint", "build"} {
				if strings.TrimSpace(manifest.Scripts[script]) != "" {
					result = append(result, automaticCheckCommand("Node "+script, directory, []string{"npm", "run", script}))
				}
			}
		}

		makeContent := files["GNUmakefile"]
		if makeContent == nil {
			makeContent = files["Makefile"]
		}
		if makeContent == nil {
			makeContent = files["makefile"]
		}
		for _, target := range []string{"test", "typecheck", "lint", "build"} {
			if makeTargetDeclared(makeContent, target) {
				result = append(result, automaticCheckCommand("Make "+target, directory, []string{"make", target}))
			}
		}

		if _, ok := files["go.mod"]; ok {
			result = append(result, automaticCheckCommand("Go tests", directory, []string{"go", "test", "./..."}))
		}
		if pytestConfigured(files) {
			result = append(result, automaticCheckCommand("Python tests", directory, []string{"python", "-m", "pytest"}))
		}
		if _, ok := files["pom.xml"]; ok {
			result = append(result, automaticCheckCommand("Maven tests", directory, []string{"mvn", "test"}))
		}
	}
	return result, nil
}

func normalizedCheckDirectories(input []string) ([]string, error) {
	if len(input) == 0 {
		input = []string{"."}
	}
	if len(input) > maxAutomaticCheckDirectories {
		return nil, fmt.Errorf("%w: at most %d module directories may be inspected", errAutomaticCheckDiscovery, maxAutomaticCheckDirectories)
	}
	seen := make(map[string]struct{}, len(input))
	result := make([]string, 0, len(input))
	for _, directory := range input {
		if !safeAutomaticCheckDirectory(directory) {
			return nil, fmt.Errorf("%w: module directory %q is unsafe", errAutomaticCheckDiscovery, directory)
		}
		if _, exists := seen[directory]; exists {
			continue
		}
		seen[directory] = struct{}{}
		result = append(result, directory)
	}
	sort.Strings(result)
	return result, nil
}

func readCommittedCheckConfig(ctx context.Context, root, revision, relative string, remaining *int64) ([]byte, bool, error) {
	if remaining == nil || *remaining < 0 {
		return nil, false, fmt.Errorf("%w: invalid configuration budget", errAutomaticCheckDiscovery)
	}
	content, err := gitTargetOutputBounded(ctx, root, *remaining, "show", revision+":"+relative)
	if err == nil {
		*remaining -= int64(len(content))
		return content, true, nil
	}
	if ctx.Err() != nil {
		return nil, false, ctx.Err()
	}
	if errors.Is(err, errGitMetadataLimit) {
		return nil, false, fmt.Errorf("%w: committed check configuration exceeds the %d-byte discovery budget", errGitMetadataLimit, domain.RepositoryMetadataBytes)
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return nil, false, nil
	}
	return nil, false, fmt.Errorf("%w: read committed configuration: %v", errAutomaticCheckDiscovery, err)
}

func automaticCheckCommand(name, directory string, argv []string) domain.TaskCheckCommand {
	canonical, err := speccoding.CanonicalCheckCommand(argv)
	if err != nil {
		panic("invalid built-in automatic check command: " + err.Error())
	}
	digest := sha256.New()
	for _, value := range append([]string{"chora.automatic-check.v1", directory}, argv...) {
		_, _ = digest.Write([]byte(value))
		_, _ = digest.Write([]byte{0})
	}
	id := "auto-" + hex.EncodeToString(digest.Sum(nil))[:24]
	return domain.TaskCheckCommand{
		ID: id, Name: name, Version: 1, Command: canonical, Argv: append([]string(nil), argv...), WorkingDirectory: directory, Source: "automatic",
	}
}

func makeTargetDeclared(content []byte, target string) bool {
	if len(content) == 0 {
		return false
	}
	for _, line := range bytes.Split(content, []byte{'\n'}) {
		if len(line) == 0 || line[0] == ' ' || line[0] == '\t' || line[0] == '#' {
			continue
		}
		definition, _, found := bytes.Cut(line, []byte{':'})
		if !found || bytes.ContainsAny(definition, "=$(){}") {
			continue
		}
		for _, candidate := range strings.Fields(string(definition)) {
			if candidate == target {
				return true
			}
		}
	}
	return false
}

func pytestConfigured(files map[string][]byte) bool {
	if _, ok := files["pytest.ini"]; ok {
		return true
	}
	return bytes.Contains(files["pyproject.toml"], []byte("[tool.pytest.ini_options]")) ||
		bytes.Contains(files["setup.cfg"], []byte("[tool:pytest]")) ||
		bytes.Contains(files["tox.ini"], []byte("[pytest]"))
}

func safeAutomaticCheckDirectory(value string) bool {
	if value == "." {
		return true
	}
	if value == "" || strings.HasPrefix(value, "/") || strings.Contains(value, "\\") || path.Clean(value) != value || value == ".." || strings.HasPrefix(value, "../") || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return false
	}
	for _, component := range strings.Split(value, "/") {
		if component == "" || component == "." || component == ".." || component == ".git" {
			return false
		}
	}
	return true
}

func validAutomaticCheckRevision(value string) bool {
	if len(value) != 40 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}
