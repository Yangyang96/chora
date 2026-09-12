package localweb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/gitsource"
	"github.com/Yangyang96/chora/internal/speccoding"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

type taskResourceSelection struct {
	RepoID             string                     `json:"repoId"`
	AssociationVersion uint64                     `json:"associationVersion"`
	Role               string                     `json:"role"`
	TargetRef          string                     `json:"targetRef"`
	Scope              domain.TaskRepositoryScope `json:"scope"`
	Checks             domain.TaskCheckPolicy     `json:"checks"`
}

func (server *Server) taskResourceOptions(w http.ResponseWriter, r *http.Request) {
	roomID, err := domain.ParseRoomID(r.PathValue("roomID"))
	if err != nil {
		writeProjectError(w, err)
		return
	}
	room, err := server.store.Reader().GetRoom(r.Context(), roomID)
	if err != nil {
		writeProjectError(w, err)
		return
	}
	if room.OwnershipKind() != domain.RoomOwnershipProject {
		writeProjectError(w, app.ErrProjectNotFound)
		return
	}
	project, err := server.service.GetProjectResources(r.Context(), room.ProjectID())
	if err != nil {
		writeProjectError(w, err)
		return
	}
	views := server.projectResourcesView(r.Context(), project)
	repos := views["repositories"].([]repositoryResourceView)
	nextRepositoryCursor, _ := views["nextRepositoryCursor"].(string)
	active := []repositoryResourceView{}
	eligible := map[string]bool{}
	for _, repo := range repos {
		if repo.State == "active" {
			active = append(active, repo)
			eligible[repo.RepoID] = true
		}
	}
	selected := []string{}
	source := "user_selection"
	if refs, err := server.store.Reader().GetRoomRepositoryReferences(r.Context(), roomID); err == nil {
		for _, id := range refs.RepositoryIDs {
			key := id.String()
			if !eligible[key] {
				association, lookupErr := server.store.Reader().GetProjectRepository(r.Context(), room.ProjectID(), id)
				if lookupErr != nil {
					writeProjectError(w, lookupErr)
					return
				}
				if association.State == "active" {
					active = append(active, server.resourceView(r.Context(), association))
					eligible[key] = true
				}
			}
			if eligible[key] {
				selected = append(selected, key)
			}
		}
		source = "room_selection"
	} else if !errors.Is(err, storecontract.ErrNotFound) {
		writeProjectError(w, err)
		return
	} else if len(active) == 1 && nextRepositoryCursor == "" {
		selected = []string{active[0].RepoID}
		source = "single_repository"
	}
	result := map[string]any{"repositories": active, "selectedRepoIds": selected, "selectionSource": source, "defaultCheckMode": "auto", "nextRepositoryCursor": nextRepositoryCursor}
	if old, err := server.store.Reader().GetProjectSettings(r.Context(), room.ProjectID()); err == nil {
		legacyRepoID := ""
		if binding, lookupErr := server.store.Reader().GetRepositoryBinding(r.Context(), roomID); lookupErr == nil {
			repository, repositoryErr := server.store.Reader().GetRepositoryByCheckout(r.Context(), binding.LocalLocator())
			if repositoryErr != nil {
				writeProjectError(w, repositoryErr)
				return
			}
			association, associationErr := server.store.Reader().GetProjectRepository(r.Context(), room.ProjectID(), repository.ID)
			if associationErr != nil {
				writeProjectError(w, associationErr)
				return
			}
			if association.State == "active" {
				legacyRepoID = repository.ID.String()
				if !eligible[legacyRepoID] {
					active = append(active, server.resourceView(r.Context(), association))
					eligible[legacyRepoID] = true
				}
			}
		} else if !errors.Is(lookupErr, storecontract.ErrNotFound) {
			writeProjectError(w, lookupErr)
			return
		}
		result["legacyScope"] = map[string]any{"repoId": legacyRepoID, "writableFiles": old.WritableFiles(), "writableDirectories": old.WritableDirectories()}
		checks := []domain.TaskCheckCommand{}
		for index, c := range old.VerificationCommands() {
			command, err := speccoding.CanonicalCheckCommand(c.Argv)
			if err != nil {
				command = strings.Join(c.Argv, " ")
			}
			checks = append(checks, domain.TaskCheckCommand{ID: fmt.Sprintf("legacy-%d", index+1), Name: fmt.Sprintf("Legacy check %d", index+1), Version: old.Version(), Command: command, Argv: c.Argv, WorkingDirectory: c.WorkingDirectory, Source: "legacy"})
		}
		result["legacyChecks"] = checks
		if old.NoChecks() {
			result["defaultCheckMode"] = "none"
		} else {
			result["defaultCheckMode"] = "named"
		}
	} else if !errors.Is(err, storecontract.ErrNotFound) {
		writeProjectError(w, err)
		return
	}
	result["repositories"] = active
	writeJSON(w, http.StatusOK, result)
}
func (server *Server) createResourceTask(w http.ResponseWriter, r *http.Request) {
	roomID, err := domain.ParseRoomID(r.PathValue("roomID"))
	if err != nil {
		writeProjectError(w, err)
		return
	}
	room, err := server.store.Reader().GetRoom(r.Context(), roomID)
	if err != nil {
		writeProjectError(w, err)
		return
	}
	var input struct {
		Requirement           string                       `json:"requirement"`
		Title                 string                       `json:"title"`
		AgentExecutionProfile domain.AgentExecutionProfile `json:"agentExecutionProfile"`
		RevisionIDs           []string                     `json:"revisionIds"`
		Resources             []taskResourceSelection      `json:"resources"`
	}
	if err = decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !server.pathPiEnabled || (input.AgentExecutionProfile != domain.AgentExecutionProfileTrustedLocal && input.AgentExecutionProfile != domain.AgentExecutionProfileIsolatedLocal) || room.OwnershipKind() != domain.RoomOwnershipProject || len(input.Resources) == 0 || len(input.Resources) > domain.TaskRepositoryLimit {
		writeError(w, http.StatusUnprocessableEntity, errors.New("select repositories and explicitly choose an execution mode"))
		return
	}
	if input.AgentExecutionProfile == domain.AgentExecutionProfileIsolatedLocal {
		if err := server.isolatedLocal.available(r.Context()); err != nil {
			writeError(w, http.StatusServiceUnavailable, err)
			return
		}
	}
	if strings.TrimSpace(input.Requirement) == "" {
		writeError(w, http.StatusBadRequest, errors.New("requirement is required"))
		return
	}
	snapshot, err := server.freezeTaskResources(r.Context(), room, input.Resources)
	if err != nil {
		writeProjectError(w, err)
		return
	}
	revisionIDs := []domain.ContextRevisionID{}
	for _, raw := range input.RevisionIDs {
		id, err := domain.ParseContextRevisionID(raw)
		if err != nil {
			writeProjectError(w, err)
			return
		}
		revisionIDs = append(revisionIDs, id)
	}
	title := strings.TrimSpace(input.Title)
	if title == "" {
		title = input.Requirement
		runes := []rune(title)
		if len(runes) > 96 {
			title = string(runes[:96])
		}
	}
	result, err := server.service.CreateTask(r.Context(), app.CreateTaskRequest{CommandMeta: requestCommandMeta(r, "create-task"), RoomID: roomID, Title: title, ExecutionProfile: app.TaskExecutionProfileRealSpecCoding, AgentExecutionProfile: input.AgentExecutionProfile, RevisionIDs: revisionIDs, RealSpecCoding: &app.RealSpecCodingInput{Resources: &snapshot, Requirement: input.Requirement, Constraints: []string{"Operate only in selected task repository worktrees, respecting access roles and limits. Report checks truthfully."}, OutOfScope: []string{"Changes to original checkouts, unselected repositories, automatic Commit/Push or host configuration."}, Criteria: []app.RealSpecCodingCriterion{{Title: "Requested behavior is implemented", Description: input.Requirement}}}})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	view, err := server.createdTaskReferenceView(r.Context(), result)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, view)
}
func (server *Server) freezeTaskResources(ctx context.Context, room domain.Room, selections []taskResourceSelection) (domain.TaskResourceSnapshot, error) {
	result := domain.TaskResourceSnapshot{SchemaVersion: domain.TaskResourceSchemaV2, ProjectID: room.ProjectID().String(), RoomID: room.ID().String(), SelectionSource: "user_start", Resources: []domain.TaskRepositoryResource{}}
	seen := map[string]bool{}
	reader := server.store.Reader()
	old, oldErr := reader.GetProjectSettings(ctx, room.ProjectID())
	if oldErr != nil && !errors.Is(oldErr, storecontract.ErrNotFound) {
		return result, oldErr
	}
	legacyBinding, bindingErr := reader.GetRepositoryBinding(ctx, room.ID())
	if bindingErr != nil && !errors.Is(bindingErr, storecontract.ErrNotFound) {
		return result, bindingErr
	}
	for _, selection := range selections {
		id, err := domain.ParseRepositoryID(selection.RepoID)
		if err != nil {
			return result, err
		}
		if seen[id.String()] {
			return result, domain.ErrInvalidArgument
		}
		seen[id.String()] = true
		a, err := reader.GetProjectRepository(ctx, room.ProjectID(), id)
		if err != nil {
			return result, err
		}
		if a.State != "active" || a.Version != selection.AssociationVersion {
			return result, storecontract.ErrVersionConflict
		}
		repo := a.Repository
		i, err := server.repositorySource.Inspect(ctx, repo.Checkout)
		if err != nil {
			return result, err
		}
		if i.Unborn {
			return result, fmt.Errorf("%w: repository %s has no commit", app.ErrRepositoryUnavailable, repo.Name)
		}
		if repo.IdentitySource == "legacy_unverified" {
			if err = gitsource.ProveRevision(ctx, repo.Checkout, repo.LegacyCommit, repo.LegacyTree); err != nil {
				return result, app.ErrRepositoryIdentityDrift
			}
			repo.CommonGitDir, repo.PhysicalIdentity, repo.IdentitySource = i.CommonGitDir, i.PhysicalIdentity, "inspected"
			if err = server.store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error { return tx.VerifyLegacyRepository(ctx, repo) }); err != nil {
				return result, err
			}
		} else if repo.PhysicalIdentity != i.PhysicalIdentity || repo.CommonGitDir != i.CommonGitDir || repo.Checkout != i.CanonicalPath {
			return result, app.ErrRepositoryIdentityDrift
		}
		baseRef := strings.TrimSpace(selection.TargetRef)
		baseCommit, baseTree := i.HeadCommit, i.RootTree
		if selection.Role == "write" {
			if baseRef == "" {
				configured, lookupErr := reader.GetRepositoryDeliveryDefault(ctx, id)
				if lookupErr != nil {
					if errors.Is(lookupErr, storecontract.ErrNotFound) {
						return result, fmt.Errorf("%w: select a target branch or configure a repository delivery default", domain.ErrInvalidArgument)
					}
					return result, lookupErr
				}
				baseRef = configured.TargetRef
			}
			baseCommit, baseTree, err = resolveLocalTargetRef(ctx, repo.Checkout, baseRef)
			if err != nil {
				return result, err
			}
		} else {
			baseRef = "HEAD"
		}
		scope := selection.Scope
		hasLegacy := oldErr == nil && bindingErr == nil && legacyBinding.LocalLocator() == repo.Checkout
		if hasLegacy {
			if scope.MigrationChoice == "legacy" {
				scope.Mode = "restricted"
				scope.WritableFiles = old.WritableFiles()
				scope.WritableDirectories = old.WritableDirectories()
			} else if scope.MigrationChoice != "repository" {
				return result, fmt.Errorf("%w: choose legacy or repository scope", domain.ErrInvalidArgument)
			}
		} else if scope.MigrationChoice != "not_needed" {
			return result, domain.ErrInvalidArgument
		}
		policy := selection.Checks
		revisionProven := false
		validateResourcePath := func(relative string, directory, requireExisting bool) error {
			if relative == "." && directory {
				return nil
			}
			if !revisionProven {
				if proofErr := proveRepositoryHeadRevision(ctx, repo.Checkout, baseCommit); proofErr != nil {
					return proofErr
				}
				revisionProven = true
			}
			if proofErr := validateRepositoryPathAtRevision(ctx, repo.Checkout, baseCommit, relative, directory, requireExisting); proofErr != nil {
				return fmt.Errorf("%w: %v", domain.ErrInvalidArgument, proofErr)
			}
			return nil
		}
		for _, relative := range scope.WritableFiles {
			if err = validateResourcePath(relative, false, false); err != nil {
				return result, err
			}
		}
		for _, paths := range [][]string{scope.WritableDirectories, scope.ProtectedDirectories} {
			for _, relative := range paths {
				if err = validateResourcePath(relative, true, false); err != nil {
					return result, err
				}
			}
		}
		validateCheckDirectory := func(relative string) error {
			return validateResourcePath(relative, true, true)
		}
		if policy.Mode == "auto" {
			directories := []string{"."}
			if scope.Mode == "restricted" {
				directories = append(directories, scope.WritableDirectories...)
			}
			policy.SelectionSource = "committed_configuration"
			policy.Commands, err = discoverAutomaticChecks(ctx, repo.Checkout, baseCommit, directories)
			if err != nil {
				return result, err
			}
		} else if policy.Mode == "none" {
			if len(policy.Commands) > 0 {
				return result, domain.ErrInvalidArgument
			}
		} else if policy.Mode == "named" {
			for index := range policy.Commands {
				c := &policy.Commands[index]
				if c.Source == "legacy" {
					if !hasLegacy || c.Version != old.Version() {
						return result, storecontract.ErrVersionConflict
					}
					matched := false
					for oldIndex, command := range old.VerificationCommands() {
						if c.ID == fmt.Sprintf("legacy-%d", oldIndex+1) {
							c.Argv = command.Argv
							c.WorkingDirectory = command.WorkingDirectory
							c.Command, err = speccoding.CanonicalCheckCommand(command.Argv)
							if err != nil {
								return result, err
							}
							matched = true
						}
					}
					if !matched {
						return result, domain.ErrInvalidArgument
					}
				} else {
					if c.Version != 0 {
						defs, lookupErr := server.store.Reader().ListRepositoryChecks(ctx, repo.ID)
						if lookupErr != nil {
							return result, lookupErr
						}
						found := false
						for _, d := range defs {
							if d.Command.ID == c.ID && d.Command.Version == c.Version {
								*c = d.Command
								found = true
								break
							}
						}
						if !found {
							return result, storecontract.ErrVersionConflict
						}
						continue
					}
					c.Argv, err = speccoding.ParseCheckCommand(c.Command)
					if err != nil {
						return result, err
					}
					c.Source = "user"
				}
				if c.WorkingDirectory == "" {
					c.WorkingDirectory = "."
				}
			}
		} else {
			return result, domain.ErrInvalidArgument
		}
		for _, c := range policy.Commands {
			if err = validateCheckDirectory(c.WorkingDirectory); err != nil {
				return result, err
			}
		}
		for index := range policy.Preparation {
			c := &policy.Preparation[index]
			c.Argv, err = speccoding.ParseCheckCommand(c.Command)
			if err != nil {
				return result, err
			}
			c.Source = "user"
			if c.WorkingDirectory == "" {
				c.WorkingDirectory = "."
			}
			if err = validateCheckDirectory(c.WorkingDirectory); err != nil {
				return result, err
			}
		}
		result.Resources = append(result.Resources, domain.TaskRepositoryResource{RepoID: id.String(), Name: repo.Name, Checkout: repo.Checkout, CommonGitDir: repo.CommonGitDir, PhysicalIdentity: repo.PhysicalIdentity, Role: selection.Role, BaseCommit: baseCommit, BaseTree: baseTree, BaseRef: baseRef, AssociationVersion: a.Version, Scope: scope, Checks: policy})
	}
	sort.Slice(result.Resources, func(i, j int) bool { return result.Resources[i].RepoID < result.Resources[j].RepoID })
	return result, nil
}

func resolveLocalTargetRef(ctx context.Context, root, ref string) (string, string, error) {
	return resolveLocalTargetRefWithOutput(ctx, root, ref, gitTargetOutputBounded)
}

func resolveLocalTargetRefWithOutput(ctx context.Context, root, ref string, output func(context.Context, string, int64, ...string) ([]byte, error)) (string, string, error) {
	if !domain.ValidTaskTargetRef(ref) {
		return "", "", fmt.Errorf("%w: targetRef must be a local refs/heads ref", domain.ErrInvalidArgument)
	}
	if err := isolatedGitCommand(ctx, root, "show-ref", "--verify", "--quiet", ref).Run(); err != nil {
		return "", "", fmt.Errorf("%w: target branch is unavailable", app.ErrRepositoryUnavailable)
	}
	commit, err := output(ctx, root, domain.RepositoryMetadataBytes, "rev-parse", "--verify", ref+"^{commit}")
	if err != nil {
		return "", "", fmt.Errorf("%w: target branch commit is unavailable", app.ErrRepositoryUnavailable)
	}
	tree, err := output(ctx, root, domain.RepositoryMetadataBytes, "rev-parse", "--verify", strings.TrimSpace(string(commit))+"^{tree}")
	if err != nil {
		return "", "", fmt.Errorf("%w: target branch tree is unavailable", app.ErrRepositoryUnavailable)
	}
	return strings.TrimSpace(string(commit)), strings.TrimSpace(string(tree)), nil
}
func (server *Server) getTaskResources(w http.ResponseWriter, r *http.Request) {
	id, err := domain.ParseTaskID(r.PathValue("taskID"))
	if err != nil {
		writeProjectError(w, err)
		return
	}
	record, err := server.store.Reader().GetTaskResourceSnapshot(r.Context(), id)
	if err != nil {
		writeProjectError(w, err)
		return
	}
	var snapshot domain.TaskResourceSnapshot
	if err = json.Unmarshal(record.CanonicalJSON, &snapshot); err != nil {
		writeProjectError(w, err)
		return
	}
	bindings, err := server.store.Reader().ListTaskRepositoryWorktrees(r.Context(), id)
	if err != nil {
		writeProjectError(w, err)
		return
	}
	worktrees := make([]map[string]any, 0, len(bindings))
	for _, binding := range bindings {
		worktrees = append(worktrees, map[string]any{"repoId": binding.RepositoryID.String(), "state": binding.State, "reason": binding.Reason, "updatedAt": binding.UpdatedAt})
	}
	writeJSON(w, http.StatusOK, map[string]any{"snapshot": snapshot, "worktrees": worktrees})
}
