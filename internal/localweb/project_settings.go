package localweb

import (
	"errors"
	"net/http"
	"time"

	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
)

type projectVerificationCommandView struct {
	Argv             []string `json:"argv"`
	WorkingDirectory string   `json:"workingDirectory"`
}

type projectSettingsView struct {
	Version              uint64                           `json:"version"`
	WritableFiles        []string                         `json:"writableFiles"`
	WritableDirectories  []string                         `json:"writableDirectories"`
	VerificationCommands []projectVerificationCommandView `json:"verificationCommands"`
	NoChecks             bool                             `json:"noChecks"`
}

func projectSettingsViewOf(settings domain.ProjectSettings) projectSettingsView {
	commands := settings.VerificationCommands()
	viewCommands := make([]projectVerificationCommandView, len(commands))
	for index, command := range commands {
		viewCommands[index] = projectVerificationCommandView{Argv: command.Argv, WorkingDirectory: command.WorkingDirectory}
	}
	files, directories := settings.WritableFiles(), settings.WritableDirectories()
	if files == nil {
		files = []string{}
	}
	if directories == nil {
		directories = []string{}
	}
	return projectSettingsView{
		Version: settings.Version(), WritableFiles: files, WritableDirectories: directories,
		VerificationCommands: viewCommands, NoChecks: settings.NoChecks(),
	}
}

func (server *Server) getProjectSettings(writer http.ResponseWriter, request *http.Request) {
	projectID, err := domain.ParseProjectID(request.PathValue("projectID"))
	if err != nil {
		writeProjectError(writer, err)
		return
	}
	if err := server.requireLegacyProject(request.Context(), projectID); err != nil {
		writeProjectError(writer, err)
		return
	}
	project, err := server.service.GetProjectByID(request.Context(), projectID)
	if err != nil {
		writeProjectError(writer, err)
		return
	}
	settings, err := server.resolveProjectSettings(request.Context(), project)
	if err != nil {
		writeProjectError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, projectSettingsViewOf(settings))
}

func (server *Server) putProjectSettings(writer http.ResponseWriter, request *http.Request) {
	projectID, err := domain.ParseProjectID(request.PathValue("projectID"))
	if err != nil {
		writeProjectError(writer, err)
		return
	}
	if err := server.requireLegacyProject(request.Context(), projectID); err != nil {
		writeProjectError(writer, err)
		return
	}
	project, err := server.service.GetProjectByID(request.Context(), projectID)
	if err != nil {
		writeProjectError(writer, err)
		return
	}
	var input projectSettingsView
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	commands := make([]domain.ProjectVerificationCommand, len(input.VerificationCommands))
	for index, command := range input.VerificationCommands {
		commands[index] = domain.ProjectVerificationCommand{Argv: command.Argv, WorkingDirectory: command.WorkingDirectory}
	}
	// Construct once before persistence so structural errors and repository
	// scope errors produce an actionable 400 without a partial write.
	candidate, err := domain.NewProjectSettings(domain.ProjectSettingsParams{
		ProjectID: projectID, WritableFiles: input.WritableFiles, WritableDirectories: input.WritableDirectories,
		VerificationCommands: commands, NoChecks: input.NoChecks, UpdatedAt: time.Now().UTC(),
	})
	if err != nil {
		writeProjectError(writer, err)
		return
	}
	if err := validateProjectSettingsForRepository(request.Context(), project.RepositoryBinding.LocalLocator(), candidate); err != nil {
		writeProjectError(writer, errors.Join(domain.ErrInvalidArgument, err))
		return
	}
	settings, err := server.service.UpdateProjectSettings(request.Context(), app.UpdateProjectSettingsRequest{
		CommandMeta: requestCommandMeta(request, "put-project-settings"), ProjectID: projectID, ExpectedVersion: input.Version,
		WritableFiles: input.WritableFiles, WritableDirectories: input.WritableDirectories,
		VerificationCommands: commands, NoChecks: input.NoChecks,
	})
	if err != nil {
		writeProjectError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, projectSettingsViewOf(settings))
}
