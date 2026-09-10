package localweb

import (
	"bytes"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/Yangyang96/chora/internal/domain"
)

const taskBranchPageSize = 50

type taskBranchView struct {
	Ref    string `json:"ref"`
	Commit string `json:"commit"`
}

// repositoryBranches lists only local branch refs. The selected ref remains an
// explicit create-Task input and is resolved again when the snapshot is frozen.
func (server *Server) taskBranches(w http.ResponseWriter, r *http.Request) {
	repositoryID, err := domain.ParseRepositoryID(r.PathValue("repoID"))
	if err != nil {
		writeProjectError(w, err)
		return
	}
	repository, err := server.store.Reader().GetRepository(r.Context(), repositoryID)
	if err != nil {
		writeProjectError(w, err)
		return
	}
	after := r.URL.Query().Get("after")
	if after != "" && !domain.ValidTaskTargetRef(after) {
		writeError(w, http.StatusBadRequest, errors.New("invalid branch cursor"))
		return
	}
	limit := taskBranchPageSize
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 100 {
			writeError(w, http.StatusBadRequest, errors.New("limit must be between 1 and 100"))
			return
		}
	}
	raw, err := gitTargetOutputBounded(r.Context(), repository.Checkout, domain.RepositoryMetadataBytes,
		"for-each-ref", "--sort=refname", "--format=%(refname)%00%(objectname)%00", "refs/heads/")
	if err != nil {
		writeProjectError(w, err)
		return
	}
	parts := bytes.Split(raw, []byte{0})
	branches := make([]taskBranchView, 0, limit)
	nextCursor := ""
	for index := 0; index+1 < len(parts); index += 2 {
		ref := strings.TrimSpace(string(parts[index]))
		commit := strings.TrimSpace(string(parts[index+1]))
		if ref == "" || ref <= after {
			continue
		}
		if len(branches) == limit {
			nextCursor = branches[len(branches)-1].Ref
			break
		}
		if !domain.ValidTaskTargetRef(ref) || len(commit) != 40 {
			writeProjectError(w, domain.ErrInvalidArgument)
			return
		}
		branches = append(branches, taskBranchView{Ref: ref, Commit: commit})
	}
	writeJSON(w, http.StatusOK, map[string]any{"branches": branches, "nextCursor": nextCursor})
}
