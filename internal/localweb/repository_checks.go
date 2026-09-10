package localweb

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/speccoding"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

func proveRepositoryHeadRevision(ctx context.Context, root, revision string) error {
	observed, err := gitTargetOutputBounded(ctx, root, 128, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(observed)) != revision {
		return fmt.Errorf("%w: repository HEAD changed while validating check directories", app.ErrRepositoryIdentityDrift)
	}
	return nil
}

func validateRepositoryCheckWorkingDirectory(ctx context.Context, root, revision, relative string) error {
	if relative == "." {
		return nil
	}
	return validateRepositoryPathAtRevision(ctx, root, revision, relative, true, true)
}

func validateRepositoryPathAtRevision(ctx context.Context, root, revision, relative string, directory, requireExisting bool) error {
	if !safeProjectSettingsPath(relative) {
		return fmt.Errorf("path %q must be a bounded repository-relative path", relative)
	}
	parts := strings.Split(relative, "/")
	for index := range parts {
		prefix := strings.Join(parts[:index+1], "/")
		raw, err := gitTargetOutputBounded(ctx, root, 64*1024, "ls-tree", "-z", revision, "--", ":(literal)"+prefix)
		if err != nil {
			return err
		}
		record := bytes.TrimSuffix(raw, []byte{0})
		if len(record) == 0 {
			if requireExisting {
				return fmt.Errorf("path %q must exist in the selected commit", relative)
			}
			break
		}
		tab := bytes.IndexByte(record, '\t')
		if tab < 0 || bytes.IndexByte(record[tab+1:], 0) >= 0 || string(record[tab+1:]) != prefix {
			return fmt.Errorf("path %q has ambiguous metadata in the selected commit", relative)
		}
		fields := strings.Fields(string(record[:tab]))
		mustTree := index < len(parts)-1 || directory
		regularFile := !mustTree && len(fields) == 3 && (fields[0] == "100644" || fields[0] == "100755") && fields[1] == "blob"
		if len(fields) != 3 || mustTree && (fields[0] != "040000" || fields[1] != "tree") || !mustTree && !regularFile {
			return fmt.Errorf("path %q has a symlink, submodule, or incompatible component in the selected commit", relative)
		}
	}
	if err := validateRepositoryScopePath(root, relative, directory, requireExisting); err != nil {
		return fmt.Errorf("path %q is unsafe: %w", relative, err)
	}
	return nil
}

func (server *Server) repositoryFromRoute(r *http.Request) (domain.ProjectRepository, error) {
	p, err := domain.ParseProjectID(r.PathValue("projectID"))
	if err != nil {
		return domain.ProjectRepository{}, err
	}
	id, err := domain.ParseRepositoryID(r.PathValue("repoID"))
	if err != nil {
		return domain.ProjectRepository{}, err
	}
	return server.store.Reader().GetProjectRepository(r.Context(), p, id)
}
func (server *Server) getRepositoryChecks(w http.ResponseWriter, r *http.Request) {
	a, err := server.repositoryFromRoute(r)
	if err != nil {
		writeProjectError(w, err)
		return
	}
	defs, err := server.store.Reader().ListRepositoryChecks(r.Context(), a.Repository.ID)
	if err != nil {
		writeProjectError(w, err)
		return
	}
	checks := []domain.TaskCheckCommand{}
	for _, d := range defs {
		checks = append(checks, d.Command)
	}
	writeJSON(w, http.StatusOK, map[string]any{"checks": checks})
}
func (server *Server) putRepositoryChecks(w http.ResponseWriter, r *http.Request) {
	a, err := server.repositoryFromRoute(r)
	if err != nil {
		writeProjectError(w, err)
		return
	}
	if a.State != "active" {
		writeProjectError(w, storecontract.ErrRoomStateForbidden)
		return
	}
	var input struct {
		Checks []domain.TaskCheckCommand `json:"checks"`
	}
	if err = decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if len(input.Checks) > 64 {
		writeError(w, http.StatusBadRequest, domain.ErrInvalidArgument)
		return
	}
	for index := range input.Checks {
		c := &input.Checks[index]
		if c.ID == "" || c.Name == "" {
			writeError(w, http.StatusBadRequest, domain.ErrInvalidArgument)
			return
		}
		c.Argv, err = speccoding.ParseCheckCommand(c.Command)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if c.WorkingDirectory == "" {
			c.WorkingDirectory = "."
		}
		if c.WorkingDirectory != "." && !safeProjectSettingsPath(c.WorkingDirectory) {
			writeError(w, http.StatusBadRequest, domain.ErrInvalidArgument)
			return
		}
		c.Source = "user"
	}
	needsDirectoryProof := false
	for _, c := range input.Checks {
		needsDirectoryProof = needsDirectoryProof || c.WorkingDirectory != "."
	}
	if needsDirectoryProof {
		if server.repositorySource == nil {
			writeProjectError(w, app.ErrRepositoryUnavailable)
			return
		}
		observed, inspectErr := server.inspectRepositoryEntries(r.Context(), a)
		if inspectErr != nil {
			writeRepositoryEntryError(w, inspectErr)
			return
		}
		if a.Repository.IdentitySource == "legacy_unverified" {
			// The shared inspection proves the canonical checkout and preserved
			// admitted commit/tree before establishing its physical identity.
			verified := a.Repository
			verified.CommonGitDir, verified.PhysicalIdentity, verified.IdentitySource = observed.CommonGitDir, observed.PhysicalIdentity, "inspected"
			if err = server.store.WithinWriteTx(r.Context(), func(tx storecontract.WriteTx) error {
				return tx.VerifyLegacyRepository(r.Context(), verified)
			}); err != nil {
				writeProjectError(w, err)
				return
			}
		}
		if proofErr := proveRepositoryHeadRevision(r.Context(), a.Repository.Checkout, observed.HeadCommit); proofErr != nil {
			writeProjectError(w, proofErr)
			return
		}
		for _, c := range input.Checks {
			if proofErr := validateRepositoryCheckWorkingDirectory(r.Context(), a.Repository.Checkout, observed.HeadCommit, c.WorkingDirectory); proofErr != nil {
				writeError(w, http.StatusBadRequest, fmt.Errorf("%w: %v", domain.ErrInvalidArgument, proofErr))
				return
			}
		}
	}
	err = server.store.WithinWriteTx(r.Context(), func(tx storecontract.WriteTx) error {
		project, err := tx.GetProject(r.Context(), a.ProjectID)
		if err != nil {
			return err
		}
		if project.State() != domain.ProjectStateActive {
			return storecontract.ErrRoomStateForbidden
		}
		for _, c := range input.Checks {
			expected := c.Version
			c.Version++
			if err = tx.InsertRepositoryCheck(r.Context(), expected, domain.RepositoryCheckDefinition{RepositoryID: a.Repository.ID, Command: c, CreatedAt: time.Now().UTC()}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		writeProjectError(w, err)
		return
	}
	server.getRepositoryChecks(w, r)
}
