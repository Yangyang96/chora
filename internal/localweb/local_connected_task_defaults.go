package localweb

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/speccoding"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

const (
	localConnectedDefaultFileLimit        = 4096
	localConnectedDefaultPathLimit        = 512 * 1024
	localConnectedRepositoryByteLimit     = 128 * 1024 * 1024
	localConnectedRepositoryFileByteLimit = 8 * 1024 * 1024
)

// defaultLocalConnectedUserTask derives a bounded, reviewable authority from a
// Project's admitted Git tree without asking an intent-first user to fill in an
// infrastructure form. M2-S1 deliberately supports existing tracked regular
// files only; new-file authority can be added by a later explicit planning UI.
func defaultLocalConnectedUserTask(ctx context.Context, repositoryRoot string) (speccoding.DefaultUserTaskInput, error) {
	settings, err := detectedProjectSettings(ctx, domain.NewProjectID(), repositoryRoot)
	if err != nil {
		return speccoding.DefaultUserTaskInput{}, err
	}
	return defaultLocalConnectedUserTaskFromSettings(ctx, repositoryRoot, settings)
}

// resolveProjectSettings returns persisted settings when present, otherwise a
// version-zero detected draft. The latter deliberately remains incomplete when
// no known check exists, so only an explicit noChecks PUT can authorize no-check.
func (server *Server) resolveProjectSettings(ctx context.Context, project app.ProjectView) (domain.ProjectSettings, error) {
	settings, err := server.service.GetProjectSettings(ctx, project.Project.ID())
	if errors.Is(err, storecontract.ErrNotFound) {
		detected, detectErr := detectedProjectSettings(ctx, project.Project.ID(), project.RepositoryBinding.LocalLocator())
		if detectErr != nil {
			return domain.ProjectSettings{}, fmt.Errorf("%w: %v", domain.ErrInvalidArgument, detectErr)
		}
		return detected, nil
	}
	if err != nil {
		return domain.ProjectSettings{}, err
	}
	if err := validateProjectSettingsForRepository(ctx, project.RepositoryBinding.LocalLocator(), settings); err != nil {
		return domain.ProjectSettings{}, fmt.Errorf("%w: %v", domain.ErrInvalidArgument, err)
	}
	return settings, nil
}

func detectedProjectSettings(ctx context.Context, projectID domain.ProjectID, repositoryRoot string) (domain.ProjectSettings, error) {
	if !cleanAbsolutePath(repositoryRoot) {
		return domain.ProjectSettings{}, errors.New("Project repository root is invalid")
	}
	revision, entries, err := inspectBoundedRepository(ctx, repositoryRoot)
	if err != nil {
		return domain.ProjectSettings{}, err
	}
	classifications, err := classifyRepositoryTreeFiles(ctx, repositoryRoot, revision, entries)
	if err != nil {
		return domain.ProjectSettings{}, err
	}
	files := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.Mode != "100644" && entry.Mode != "100755" {
			continue
		}
		if reason := classifications[entry.Path]; reason != "" || !safePatchTargetPath(entry.Path) || !regularRepositoryPath(repositoryRoot, entry.Path) {
			continue
		}
		files = append(files, entry.Path)
	}
	if len(files) == 0 {
		return domain.ProjectSettings{}, errors.New("Project has no safe tracked regular files")
	}
	command, err := defaultLocalConnectedVerificationCommandWithRead(func(path string) ([]byte, error) {
		entry, err := gitTargetOutput(ctx, repositoryRoot, nil, "ls-tree", revision, "--", path)
		if err != nil || (!bytes.HasPrefix(entry, []byte("100644 blob ")) && !bytes.HasPrefix(entry, []byte("100755 blob "))) {
			return nil, os.ErrNotExist
		}
		data, err := gitTargetOutputBounded(ctx, repositoryRoot, 1024*1024, "show", revision+":"+path)
		if len(data) > 1024*1024 {
			return nil, errors.New("test configuration exceeds size limit")
		}
		return data, err
	})
	commands := []domain.ProjectVerificationCommand(nil)
	if err == nil {
		commands = []domain.ProjectVerificationCommand{{Argv: command, WorkingDirectory: "."}}
	}
	return domain.DetectProjectSettings(domain.ProjectSettingsParams{
		ProjectID: projectID, WritableFiles: files, VerificationCommands: commands,
	})
}

func defaultLocalConnectedUserTaskFromSettings(ctx context.Context, repositoryRoot string, settings domain.ProjectSettings) (speccoding.DefaultUserTaskInput, error) {
	if err := validateProjectSettingsForRepository(ctx, repositoryRoot, settings); err != nil {
		return speccoding.DefaultUserTaskInput{}, err
	}
	if !settings.NoChecks() && len(settings.VerificationCommands()) == 0 {
		return speccoding.DefaultUserTaskInput{}, errors.New("Project checks are not configured; add a verification command or explicitly choose no checks")
	}
	commands := settings.VerificationCommands()
	verification := make([]speccoding.UserVerificationCommand, len(commands))
	for index, command := range commands {
		verification[index] = speccoding.UserVerificationCommand{Argv: command.Argv, WorkingDirectory: command.WorkingDirectory}
	}
	return speccoding.DefaultUserTaskInput{
		Constraints: []string{
			"Modify only the declared Project files and bounded directories in the admitted repository.",
			"Keep the change bounded to the stated requirement and report the declared verification result truthfully.",
		},
		OutOfScope: []string{
			"Staging, committing, pushing, changing Git repository configuration, or writing outside the declared scope.",
		},
		WritableFiles: settings.WritableFiles(), WritableDirectories: settings.WritableDirectories(),
		VerificationCommands: verification, NoChecks: settings.NoChecks(),
	}, nil
}

type repositoryTreeEntry struct {
	Path string
	Mode string
	Kind string
	Size int64
}

func inspectBoundedRepository(ctx context.Context, repositoryRoot string) (string, []repositoryTreeEntry, error) {
	if !cleanAbsolutePath(repositoryRoot) {
		return "", nil, errors.New("Project repository root is invalid")
	}
	resolved, err := filepath.EvalSymlinks(repositoryRoot)
	if err != nil || resolved != repositoryRoot {
		return "", nil, errors.New("Project repository root must be a real, symlink-free directory")
	}
	revision, _, _, err := currentTaskBase(ctx, repositoryRoot)
	if err != nil {
		return "", nil, err
	}
	raw, err := gitTargetOutputBounded(ctx, repositoryRoot, localConnectedDefaultPathLimit+localConnectedDefaultFileLimit*96, "ls-tree", "-r", "-l", "-z", revision, "--")
	if err != nil {
		if errors.Is(err, errGitMetadataLimit) {
			return "", nil, err
		}
		return "", nil, errors.New("Project tracked-file authority is unavailable")
	}
	parts := bytes.Split(raw, []byte{0})
	entries := make([]repositoryTreeEntry, 0, len(parts))
	pathBytes, totalBytes, tracked := 0, int64(0), 0
	for _, encoded := range parts {
		if len(encoded) == 0 {
			continue
		}
		metadata, name, found := bytes.Cut(encoded, []byte{'\t'})
		fields := strings.Fields(string(metadata))
		if !found || len(fields) != 4 {
			return "", nil, errors.New("Project tracked-file metadata is invalid")
		}
		size := int64(0)
		if fields[3] != "-" {
			var parseErr error
			size, parseErr = strconv.ParseInt(fields[3], 10, 64)
			if parseErr != nil || size < 0 {
				return "", nil, errors.New("Project tracked-file size is invalid")
			}
		}
		tracked++
		totalBytes += size
		if tracked > localConnectedDefaultFileLimit || totalBytes > localConnectedRepositoryByteLimit {
			return "", nil, fmt.Errorf("Project exceeds the supported launch limit (%d tracked entries and %d MiB); narrow or remove generated/dependency content before starting a Task", localConnectedDefaultFileLimit, localConnectedRepositoryByteLimit/(1024*1024))
		}
		relative := filepath.ToSlash(string(name))
		entries = append(entries, repositoryTreeEntry{Path: relative, Mode: fields[0], Kind: fields[1], Size: size})
		pathBytes += len(relative)
		if pathBytes > localConnectedDefaultPathLimit {
			return "", nil, errors.New("Project tracked paths exceed the supported launch metadata limit")
		}
	}
	return revision, entries, nil
}

func validateProjectSettingsForRepository(ctx context.Context, repositoryRoot string, settings domain.ProjectSettings) error {
	revision, entries, err := inspectBoundedRepository(ctx, repositoryRoot)
	if err != nil {
		return err
	}
	committedTypes := committedRepositoryTypes(entries)
	for _, relative := range settings.WritableFiles() {
		if err := validateCommittedScopePath(committedTypes, relative, false, false); err != nil {
			return err
		}
		if err := validateRepositoryScopePath(repositoryRoot, relative, false, false); err != nil {
			return fmt.Errorf("writable file %q is unsafe: %w", relative, err)
		}
	}
	for _, relative := range settings.WritableDirectories() {
		if err := validateCommittedScopePath(committedTypes, relative, true, false); err != nil {
			return err
		}
		if err := validateRepositoryScopePath(repositoryRoot, relative, true, false); err != nil {
			return fmt.Errorf("writable directory %q is unsafe: %w", relative, err)
		}
	}
	for _, command := range settings.VerificationCommands() {
		if command.WorkingDirectory == "." {
			continue
		}
		if err := validateRepositoryScopePath(repositoryRoot, command.WorkingDirectory, true, true); err != nil {
			return fmt.Errorf("verification working directory %q is unsafe: %w", command.WorkingDirectory, err)
		}
		if err := validateCommittedScopePath(committedTypes, command.WorkingDirectory, true, true); err != nil {
			return err
		}
	}
	selected := selectedRepositoryEntries(entries, settings.WritableFiles(), settings.WritableDirectories())
	classifications, err := classifyRepositoryTreeFiles(ctx, repositoryRoot, revision, selected)
	if err != nil {
		return err
	}
	for _, entry := range selected {
		if reason := classifications[entry.Path]; reason != "" {
			return fmt.Errorf("writable scope includes unsupported %q: %s; remove it from scope or convert it to ordinary UTF-8 text", entry.Path, reason)
		}
	}
	return nil
}

func selectedRepositoryEntries(entries []repositoryTreeEntry, files, directories []string) []repositoryTreeEntry {
	wantedFiles := make(map[string]struct{}, len(files))
	for _, file := range files {
		wantedFiles[file] = struct{}{}
	}
	selected := make([]repositoryTreeEntry, 0)
	for _, entry := range entries {
		_, exact := wantedFiles[entry.Path]
		within := false
		for _, directory := range directories {
			if strings.HasPrefix(entry.Path, directory+"/") || entry.Path == directory {
				within = true
				break
			}
		}
		if exact || within {
			selected = append(selected, entry)
		}
	}
	return selected
}

func classifyRepositoryTreeFiles(ctx context.Context, root, revision string, entries []repositoryTreeEntry) (map[string]string, error) {
	result := make(map[string]string, len(entries))
	regular := make([]repositoryTreeEntry, 0, len(entries))
	for _, entry := range entries {
		switch {
		case entry.Mode == "120000":
			result[entry.Path] = "symbolic link"
		case entry.Mode == "160000" || entry.Kind == "commit":
			result[entry.Path] = "Git submodule"
		case entry.Kind != "blob" || (entry.Mode != "100644" && entry.Mode != "100755"):
			result[entry.Path] = "non-regular Git object"
		case entry.Size > localConnectedRepositoryFileByteLimit:
			result[entry.Path] = fmt.Sprintf("file exceeds the %d MiB text-file limit", localConnectedRepositoryFileByteLimit/(1024*1024))
		default:
			regular = append(regular, entry)
		}
	}
	if len(regular) == 0 {
		return result, nil
	}
	var input bytes.Buffer
	for _, entry := range regular {
		if !safeProjectSettingsPath(entry.Path) {
			result[entry.Path] = "unsafe repository path"
			continue
		}
		fmt.Fprintf(&input, "%s:%s\n", revision, entry.Path)
	}
	command := isolatedGitCommand(ctx, root, "cat-file", "--batch")
	command.Stdin = &input
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		return nil, errors.New("Project file classification could not start")
	}
	reader := bufio.NewReader(stdout)
	for _, entry := range regular {
		if result[entry.Path] != "" {
			continue
		}
		header, readErr := reader.ReadString('\n')
		fields := strings.Fields(header)
		if readErr != nil || len(fields) != 3 || fields[1] != "blob" {
			_ = command.Process.Kill()
			_ = command.Wait()
			return nil, fmt.Errorf("Project file %q could not be classified", entry.Path)
		}
		size, parseErr := strconv.ParseInt(fields[2], 10, 64)
		if parseErr != nil || size < 0 || size > localConnectedRepositoryFileByteLimit {
			_ = command.Process.Kill()
			_ = command.Wait()
			return nil, fmt.Errorf("Project file %q has invalid bounded size", entry.Path)
		}
		data := make([]byte, size)
		if _, readErr := io.ReadFull(reader, data); readErr != nil {
			_ = command.Process.Kill()
			_ = command.Wait()
			return nil, fmt.Errorf("Project file %q could not be read for classification", entry.Path)
		}
		if separator, readErr := reader.ReadByte(); readErr != nil || separator != '\n' {
			_ = command.Process.Kill()
			_ = command.Wait()
			return nil, fmt.Errorf("Project file %q classification framing is invalid", entry.Path)
		}
		switch {
		case bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data):
			result[entry.Path] = "binary or non-UTF-8 content"
		case bytes.HasPrefix(data, []byte("version https://git-lfs.github.com/spec/v1\n")):
			result[entry.Path] = "Git LFS pointer"
		}
	}
	if err := command.Wait(); err != nil {
		return nil, fmt.Errorf("Project file classification failed: %s", strings.TrimSpace(stderr.String()))
	}
	return result, nil
}

func safeProjectSettingsPath(value string) bool {
	return safePatchTargetPath(value) && utf8.ValidString(value) && strings.IndexFunc(value, unicode.IsControl) < 0
}

func validateRepositoryScopePath(root, relative string, directory, requireExisting bool) error {
	if !safePatchTargetPath(relative) {
		return errors.New("path must be bounded, slash-relative, and outside .git")
	}
	current := root
	parts := strings.Split(relative, "/")
	for index, part := range parts {
		current = filepath.Join(current, filepath.FromSlash(part))
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			if requireExisting {
				return errors.New("path does not exist")
			}
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("symlink components are not allowed")
		}
		if index < len(parts)-1 && !info.IsDir() {
			return errors.New("ancestor is not a directory")
		}
		if index == len(parts)-1 {
			if directory && !info.IsDir() {
				return errors.New("path is not a directory")
			}
			if !directory && !info.Mode().IsRegular() {
				return errors.New("path is not a regular file")
			}
		}
	}
	return nil
}

func regularRepositoryPath(root, relative string) bool {
	current := root
	parts := strings.Split(relative, "/")
	for index, part := range parts {
		current = filepath.Join(current, filepath.FromSlash(part))
		info, err := os.Lstat(current)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return false
		}
		if index < len(parts)-1 && !info.IsDir() {
			return false
		}
		if index == len(parts)-1 && !info.Mode().IsRegular() {
			return false
		}
	}
	return true
}

func defaultLocalConnectedVerificationCommand(root string) ([]string, error) {
	return defaultLocalConnectedVerificationCommandWithRead(func(path string) ([]byte, error) {
		if !regularRepositoryPath(root, path) {
			return nil, os.ErrNotExist
		}
		return os.ReadFile(filepath.Join(root, path))
	})
}

func defaultLocalConnectedVerificationCommandWithRead(read func(string) ([]byte, error)) ([]string, error) {
	if data, err := read("Makefile"); err == nil && len(data) <= 1024*1024 && makefileHasTarget(data, "test") {
		return []string{"make", "test"}, nil
	}
	if _, err := read("go.mod"); err == nil {
		return []string{"go", "test", "./..."}, nil
	}
	if data, err := read("package.json"); err == nil && len(data) <= 1024*1024 {
		var manifest struct {
			Scripts map[string]string `json:"scripts"`
		}
		if json.Unmarshal(data, &manifest) == nil {
			if strings.TrimSpace(manifest.Scripts["test"]) != "" {
				return []string{"npm", "test"}, nil
			}
			if strings.TrimSpace(manifest.Scripts["web:test"]) != "" {
				return []string{"npm", "run", "web:test"}, nil
			}
		}
	}
	_, pyprojectErr := read("pyproject.toml")
	_, pytestErr := read("pytest.ini")
	if pyprojectErr == nil || pytestErr == nil {
		return []string{"python3", "-m", "pytest"}, nil
	}
	return nil, errors.New("Project has no supported default test command (make test, go test, npm test, or pytest)")
}

func makefileHasTarget(data []byte, target string) bool {
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, target+":") || strings.HasPrefix(line, target+" :") {
			return true
		}
	}
	return false
}

// A dirty checkout must never conceal a symlink/submodule in the frozen base.
func committedRepositoryTypes(entries []repositoryTreeEntry) map[string]string {
	types := make(map[string]string)
	for _, entry := range entries {
		types[entry.Path] = entry.Mode
		parts := strings.Split(entry.Path, "/")
		for i := 1; i < len(parts); i++ {
			types[strings.Join(parts[:i], "/")] = "040000"
		}
	}
	return types
}

func validateCommittedScopePath(types map[string]string, relative string, directory, required bool) error {
	components := strings.Split(relative, "/")
	for index := range components {
		prefix := strings.Join(components[:index+1], "/")
		mode, exists := types[prefix]
		if !exists {
			if required {
				return fmt.Errorf("verification working directory %q must exist in the selected commit; commit it or choose another directory", relative)
			}
			return nil
		}
		mustTree := index < len(components)-1 || directory
		if mode == "120000" || mode == "160000" || (mustTree && mode != "040000") || (!mustTree && mode != "100644" && mode != "100755") {
			return fmt.Errorf("scope path %q has an unsupported file type or non-directory component in the selected commit", relative)
		}
	}
	return nil
}
