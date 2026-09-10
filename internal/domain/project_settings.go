package domain

import (
	"fmt"
	"path"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	maxProjectSettingScopes        = 4096
	maxProjectVerificationCommands = 64
	maxProjectCommandArguments     = 128
	maxProjectSettingTextBytes     = 512 * 1024
)

// ProjectVerificationCommand is an argv-only command declared by the user.
// WorkingDirectory is repository-relative; "." means the repository root.
type ProjectVerificationCommand struct {
	Argv             []string
	WorkingDirectory string
}

type ProjectSettingsParams struct {
	ProjectID            ProjectID
	WritableFiles        []string
	WritableDirectories  []string
	VerificationCommands []ProjectVerificationCommand
	NoChecks             bool
	UpdatedAt            time.Time
}

type ProjectSettingsRecord struct {
	ProjectID            ProjectID
	Version              uint64
	WritableFiles        []string
	WritableDirectories  []string
	VerificationCommands []ProjectVerificationCommand
	NoChecks             bool
	UpdatedAt            time.Time
}

// ProjectSettings is the persistent Project-level default that is frozen into
// each new Task. Version zero is reserved for an unpersisted detected draft and
// may have neither a detected command nor an explicit no-check selection.
type ProjectSettings struct {
	projectID            ProjectID
	version              uint64
	writableFiles        []string
	writableDirectories  []string
	verificationCommands []ProjectVerificationCommand
	noChecks             bool
	updatedAt            time.Time
}

func NewProjectSettings(params ProjectSettingsParams) (ProjectSettings, error) {
	return restoreProjectSettings(ProjectSettingsRecord{
		ProjectID: params.ProjectID, Version: 1, WritableFiles: params.WritableFiles,
		WritableDirectories: params.WritableDirectories, VerificationCommands: params.VerificationCommands,
		NoChecks: params.NoChecks, UpdatedAt: params.UpdatedAt,
	}, false)
}

// DetectProjectSettings constructs an editable version-zero response. A caller
// must explicitly select noChecks or provide a command before persisting it.
func DetectProjectSettings(params ProjectSettingsParams) (ProjectSettings, error) {
	return restoreProjectSettings(ProjectSettingsRecord{
		ProjectID: params.ProjectID, Version: 0, WritableFiles: params.WritableFiles,
		WritableDirectories: params.WritableDirectories, VerificationCommands: params.VerificationCommands,
		NoChecks: params.NoChecks,
	}, true)
}

func RestoreProjectSettings(record ProjectSettingsRecord) (ProjectSettings, error) {
	return restoreProjectSettings(record, false)
}

func restoreProjectSettings(record ProjectSettingsRecord, detected bool) (ProjectSettings, error) {
	if !record.ProjectID.Valid() || (detected && record.Version != 0) || (!detected && (record.Version == 0 || record.UpdatedAt.IsZero())) {
		return ProjectSettings{}, fmt.Errorf("%w: invalid Project settings identity", ErrInvalidArgument)
	}
	files, fileBytes, err := normalizedProjectPaths(record.WritableFiles, false)
	if err != nil {
		return ProjectSettings{}, err
	}
	directories, directoryBytes, err := normalizedProjectPaths(record.WritableDirectories, true)
	if err != nil {
		return ProjectSettings{}, err
	}
	if len(files)+len(directories) == 0 || len(files)+len(directories) > maxProjectSettingScopes || fileBytes+directoryBytes > maxProjectSettingTextBytes {
		return ProjectSettings{}, fmt.Errorf("%w: Project settings require a bounded writable scope", ErrInvalidArgument)
	}
	commands, commandBytes, err := normalizedProjectCommands(record.VerificationCommands)
	if err != nil {
		return ProjectSettings{}, err
	}
	if commandBytes+fileBytes+directoryBytes > maxProjectSettingTextBytes {
		return ProjectSettings{}, fmt.Errorf("%w: Project settings exceed the size limit", ErrInvalidArgument)
	}
	if record.NoChecks && len(commands) != 0 {
		return ProjectSettings{}, fmt.Errorf("%w: noChecks cannot be combined with verification commands", ErrInvalidArgument)
	}
	if !record.NoChecks && len(commands) == 0 && !detected {
		return ProjectSettings{}, fmt.Errorf("%w: choose at least one verification command or explicitly select noChecks", ErrInvalidArgument)
	}
	return ProjectSettings{
		projectID: record.ProjectID, version: record.Version, writableFiles: files,
		writableDirectories: directories, verificationCommands: commands,
		noChecks: record.NoChecks, updatedAt: record.UpdatedAt,
	}, nil
}

func (settings ProjectSettings) Update(params ProjectSettingsParams, expectedVersion uint64) (ProjectSettings, error) {
	if settings.Version() != expectedVersion || params.ProjectID != settings.ProjectID() || params.UpdatedAt.IsZero() || params.UpdatedAt.Before(settings.UpdatedAt()) {
		return ProjectSettings{}, fmt.Errorf("%w: Project settings update rejected", ErrInvalidArgument)
	}
	return RestoreProjectSettings(ProjectSettingsRecord{
		ProjectID: settings.ProjectID(), Version: settings.Version() + 1,
		WritableFiles: params.WritableFiles, WritableDirectories: params.WritableDirectories,
		VerificationCommands: params.VerificationCommands, NoChecks: params.NoChecks, UpdatedAt: params.UpdatedAt,
	})
}

func normalizedProjectPaths(values []string, directory bool) ([]string, int, error) {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	bytes := 0
	for _, value := range values {
		if !validProjectRelativePath(value, directory) {
			return nil, 0, fmt.Errorf("%w: unsafe repository-relative path %q", ErrInvalidArgument, value)
		}
		if _, exists := seen[value]; exists {
			return nil, 0, fmt.Errorf("%w: duplicate repository-relative path %q", ErrInvalidArgument, value)
		}
		seen[value] = struct{}{}
		result = append(result, value)
		bytes += len(value)
	}
	return result, bytes, nil
}

func validProjectRelativePath(value string, directory bool) bool {
	if value == "" || value == "." || strings.HasPrefix(value, "/") || strings.Contains(value, "\\") || strings.ContainsRune(value, 0) || path.Clean(value) != value || !utf8.ValidString(value) {
		return false
	}
	for _, component := range strings.Split(value, "/") {
		if component == "" || component == "." || component == ".." || component == ".git" || strings.IndexFunc(component, unicode.IsControl) >= 0 {
			return false
		}
	}
	// Both file and directory scopes deliberately reject repository root. The
	// separate flag documents that this predicate is shared by both authorities.
	_ = directory
	return true
}

func normalizedProjectCommands(values []ProjectVerificationCommand) ([]ProjectVerificationCommand, int, error) {
	if len(values) > maxProjectVerificationCommands {
		return nil, 0, fmt.Errorf("%w: too many verification commands", ErrInvalidArgument)
	}
	result := make([]ProjectVerificationCommand, 0, len(values))
	bytes := 0
	for _, value := range values {
		cwd := value.WorkingDirectory
		if cwd == "" {
			cwd = "."
		}
		if cwd != "." && !validProjectRelativePath(cwd, true) {
			return nil, 0, fmt.Errorf("%w: unsafe verification working directory %q", ErrInvalidArgument, value.WorkingDirectory)
		}
		if len(value.Argv) == 0 || len(value.Argv) > maxProjectCommandArguments {
			return nil, 0, fmt.Errorf("%w: verification command requires bounded argv", ErrInvalidArgument)
		}
		argv := append([]string(nil), value.Argv...)
		for index, argument := range argv {
			if argument == "" || !utf8.ValidString(argument) || strings.ContainsRune(argument, 0) || strings.IndexFunc(argument, unicode.IsControl) >= 0 {
				return nil, 0, fmt.Errorf("%w: invalid verification argument", ErrInvalidArgument)
			}
			if index == 0 {
				executablePath := strings.TrimPrefix(argument, "./")
				if path.IsAbs(argument) || strings.Contains(argument, "\\") || (strings.Contains(argument, "/") && !validProjectRelativePath(executablePath, false)) {
					return nil, 0, fmt.Errorf("%w: verification executable must be a bounded repository-relative or PATH command", ErrInvalidArgument)
				}
			}
			bytes += len(argument)
		}
		bytes += len(cwd)
		result = append(result, ProjectVerificationCommand{Argv: argv, WorkingDirectory: cwd})
	}
	return result, bytes, nil
}

func (settings ProjectSettings) ProjectID() ProjectID { return settings.projectID }
func (settings ProjectSettings) Version() uint64      { return settings.version }
func (settings ProjectSettings) WritableFiles() []string {
	return append([]string(nil), settings.writableFiles...)
}
func (settings ProjectSettings) WritableDirectories() []string {
	return append([]string(nil), settings.writableDirectories...)
}
func (settings ProjectSettings) VerificationCommands() []ProjectVerificationCommand {
	result := make([]ProjectVerificationCommand, len(settings.verificationCommands))
	for index, command := range settings.verificationCommands {
		result[index] = ProjectVerificationCommand{Argv: append([]string(nil), command.Argv...), WorkingDirectory: command.WorkingDirectory}
	}
	return result
}
func (settings ProjectSettings) NoChecks() bool       { return settings.noChecks }
func (settings ProjectSettings) UpdatedAt() time.Time { return settings.updatedAt }
