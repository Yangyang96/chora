package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

type projectVerificationCommandJSON struct {
	Argv             []string `json:"argv"`
	WorkingDirectory string   `json:"workingDirectory"`
}

func scanProjectSettings(scanner interface{ Scan(...any) error }) (domain.ProjectSettings, error) {
	var projectIDText, writableFilesJSON, writableDirectoriesJSON, commandsJSON, updated string
	var version uint64
	var noChecks bool
	if err := scanner.Scan(&projectIDText, &version, &writableFilesJSON, &writableDirectoriesJSON, &commandsJSON, &noChecks, &updated); err != nil {
		return domain.ProjectSettings{}, err
	}
	projectID, err := domain.ParseProjectID(projectIDText)
	if err != nil {
		return domain.ProjectSettings{}, err
	}
	var files, directories []string
	var encodedCommands []projectVerificationCommandJSON
	if err := json.Unmarshal([]byte(writableFilesJSON), &files); err != nil {
		return domain.ProjectSettings{}, fmt.Errorf("decode Project writable files: %w", err)
	}
	if err := json.Unmarshal([]byte(writableDirectoriesJSON), &directories); err != nil {
		return domain.ProjectSettings{}, fmt.Errorf("decode Project writable directories: %w", err)
	}
	if err := json.Unmarshal([]byte(commandsJSON), &encodedCommands); err != nil {
		return domain.ProjectSettings{}, fmt.Errorf("decode Project verification commands: %w", err)
	}
	commands := make([]domain.ProjectVerificationCommand, len(encodedCommands))
	for index, command := range encodedCommands {
		commands[index] = domain.ProjectVerificationCommand{Argv: command.Argv, WorkingDirectory: command.WorkingDirectory}
	}
	updatedAt, err := parseTime(updated)
	if err != nil {
		return domain.ProjectSettings{}, err
	}
	return domain.RestoreProjectSettings(domain.ProjectSettingsRecord{
		ProjectID: projectID, Version: version, WritableFiles: files, WritableDirectories: directories,
		VerificationCommands: commands, NoChecks: noChecks, UpdatedAt: updatedAt,
	})
}

func (reader *reader) GetProjectSettings(ctx context.Context, projectID domain.ProjectID) (domain.ProjectSettings, error) {
	settings, err := scanProjectSettings(reader.q.QueryRowContext(ctx, `SELECT project_id,version,writable_files_json,writable_directories_json,verification_commands_json,no_checks,updated_at FROM project_settings WHERE project_id=?`, projectID.String()))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ProjectSettings{}, storecontract.ErrNotFound
	}
	return settings, err
}

func (tx *writeTx) SaveProjectSettingsCAS(ctx context.Context, expectedVersion uint64, settings domain.ProjectSettings) error {
	if !settings.ProjectID().Valid() || settings.Version() != expectedVersion+1 {
		return fmt.Errorf("%w: invalid Project settings successor", domain.ErrInvalidArgument)
	}
	filesJSON, directoriesJSON, commandsJSON, err := encodeProjectSettings(settings)
	if err != nil {
		return err
	}
	if expectedVersion == 0 {
		if _, err := tx.GetProject(ctx, settings.ProjectID()); err != nil {
			return err
		}
		result, err := tx.tx.ExecContext(ctx, `INSERT INTO project_settings(project_id,version,writable_files_json,writable_directories_json,verification_commands_json,no_checks,updated_at)
VALUES(?,?,?,?,?,?,?) ON CONFLICT(project_id) DO NOTHING`, settings.ProjectID().String(), settings.Version(), filesJSON, directoriesJSON, commandsJSON, settings.NoChecks(), timeText(settings.UpdatedAt()))
		if err != nil {
			return mapWriteError(err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if affected != 1 {
			return storecontract.ErrVersionConflict
		}
		return nil
	}
	current, err := tx.GetProjectSettings(ctx, settings.ProjectID())
	if err != nil {
		if errors.Is(err, storecontract.ErrNotFound) {
			return storecontract.ErrVersionConflict
		}
		return err
	}
	want, err := current.Update(domain.ProjectSettingsParams{
		ProjectID: settings.ProjectID(), WritableFiles: settings.WritableFiles(), WritableDirectories: settings.WritableDirectories(),
		VerificationCommands: settings.VerificationCommands(), NoChecks: settings.NoChecks(), UpdatedAt: settings.UpdatedAt(),
	}, expectedVersion)
	if err != nil || !sameProjectSettings(want, settings) {
		return fmt.Errorf("%w: invalid Project settings successor", domain.ErrInvalidArgument)
	}
	result, err := tx.tx.ExecContext(ctx, `UPDATE project_settings SET version=?,writable_files_json=?,writable_directories_json=?,verification_commands_json=?,no_checks=?,updated_at=? WHERE project_id=? AND version=?`,
		settings.Version(), filesJSON, directoriesJSON, commandsJSON, settings.NoChecks(), timeText(settings.UpdatedAt()), settings.ProjectID().String(), expectedVersion)
	if err != nil {
		return mapWriteError(err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return storecontract.ErrVersionConflict
	}
	return nil
}

func encodeProjectSettings(settings domain.ProjectSettings) (string, string, string, error) {
	writableFiles := settings.WritableFiles()
	if writableFiles == nil {
		writableFiles = []string{}
	}
	files, err := json.Marshal(writableFiles)
	if err != nil {
		return "", "", "", err
	}
	writableDirectories := settings.WritableDirectories()
	if writableDirectories == nil {
		writableDirectories = []string{}
	}
	directories, err := json.Marshal(writableDirectories)
	if err != nil {
		return "", "", "", err
	}
	commands := settings.VerificationCommands()
	if commands == nil {
		commands = []domain.ProjectVerificationCommand{}
	}
	encodedCommands := make([]projectVerificationCommandJSON, len(commands))
	for index, command := range commands {
		encodedCommands[index] = projectVerificationCommandJSON{Argv: command.Argv, WorkingDirectory: command.WorkingDirectory}
	}
	encoded, err := json.Marshal(encodedCommands)
	if err != nil {
		return "", "", "", err
	}
	return string(files), string(directories), string(encoded), nil
}

func sameProjectSettings(left, right domain.ProjectSettings) bool {
	leftFiles, leftDirectories, leftCommands, err := encodeProjectSettings(left)
	if err != nil {
		return false
	}
	rightFiles, rightDirectories, rightCommands, err := encodeProjectSettings(right)
	return err == nil && left.ProjectID() == right.ProjectID() && left.Version() == right.Version() && leftFiles == rightFiles && leftDirectories == rightDirectories && leftCommands == rightCommands && left.NoChecks() == right.NoChecks() && left.UpdatedAt().Equal(right.UpdatedAt())
}
