package localweb

import (
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

func (server *Server) repositoryDeliveryDefaults(w http.ResponseWriter, r *http.Request) {
	id, err := domain.ParseRepositoryID(r.PathValue("repoID"))
	if err != nil {
		writeProjectError(w, err)
		return
	}
	repository, err := server.store.Reader().GetRepository(r.Context(), id)
	if err != nil {
		writeProjectError(w, err)
		return
	}
	targetRef := ""
	version := uint64(0)
	if configured, lookupErr := server.store.Reader().GetRepositoryDeliveryDefault(r.Context(), id); lookupErr == nil {
		targetRef, version = configured.TargetRef, configured.Version
	} else if !errors.Is(lookupErr, storecontract.ErrNotFound) {
		writeProjectError(w, lookupErr)
		return
	}
	suggested, reason := suggestRepositoryTargetRef(r, repository.Checkout)
	writeJSON(w, http.StatusOK, map[string]any{"targetRef": targetRef, "version": version, "suggestedTargetRef": suggested, "reason": reason})
}

func (server *Server) saveRepositoryDeliveryDefaults(w http.ResponseWriter, r *http.Request) {
	id, err := domain.ParseRepositoryID(r.PathValue("repoID"))
	if err != nil {
		writeProjectError(w, err)
		return
	}
	var input struct {
		ExpectedVersion uint64 `json:"expectedVersion"`
		TargetRef       string `json:"targetRef"`
	}
	if err = decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	repository, err := server.store.Reader().GetRepository(r.Context(), id)
	if err != nil {
		writeProjectError(w, err)
		return
	}
	if _, _, err = resolveLocalTargetRef(r.Context(), repository.Checkout, input.TargetRef); err != nil {
		writeProjectError(w, err)
		return
	}
	result, err := server.service.SaveRepositoryDeliveryDefault(r.Context(), app.SaveRepositoryDeliveryDefaultRequest{
		CommandMeta: requestCommandMeta(r, "save-repository-delivery-default"), RepositoryID: id,
		TargetRef: input.TargetRef, ExpectedVersion: input.ExpectedVersion,
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"targetRef": result.TargetRef, "version": result.Version})
}

func suggestRepositoryTargetRef(r *http.Request, root string) (string, string) {
	raw, err := gitTargetOutputBounded(r.Context(), root, domain.RepositoryMetadataBytes,
		"for-each-ref", "--format=%(symref)%00", "refs/remotes/*/HEAD")
	if err != nil {
		return "", "remote default branch metadata is unavailable"
	}
	candidates := map[string]bool{}
	for _, part := range strings.Split(string(raw), "\x00") {
		remoteRef := strings.TrimSpace(part)
		components := strings.Split(remoteRef, "/")
		if len(components) < 4 || components[0] != "refs" || components[1] != "remotes" {
			continue
		}
		localRef := "refs/heads/" + strings.Join(components[3:], "/")
		if !domain.ValidTaskTargetRef(localRef) {
			continue
		}
		if isolatedGitCommand(r.Context(), root, "show-ref", "--verify", "--quiet", localRef).Run() == nil {
			candidates[localRef] = true
		}
	}
	values := make([]string, 0, len(candidates))
	for candidate := range candidates {
		values = append(values, candidate)
	}
	sort.Strings(values)
	if len(values) == 1 {
		return values[0], "suggested from cached local remote HEAD metadata; current remote state is not confirmed and confirmation is required"
	}
	if len(values) > 1 {
		return "", "remote default branch metadata is ambiguous; choose a local target branch"
	}
	return "", "no reliable local remote default metadata; choose a local target branch"
}
