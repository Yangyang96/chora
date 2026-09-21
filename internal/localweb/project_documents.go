package localweb

import (
	"net/http"

	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
)

func (server *Server) getProjectDocument(w http.ResponseWriter, r *http.Request) {
	id, err := domain.ParseTaskID(r.PathValue("taskID"))
	if err != nil {
		writeProjectError(w, err)
		return
	}
	view, err := server.service.GetProjectDocument(r.Context(), id)
	if err != nil {
		writeProjectError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (server *Server) saveProjectDocument(w http.ResponseWriter, r *http.Request) {
	id, err := domain.ParseTaskID(r.PathValue("taskID"))
	if err != nil {
		writeProjectError(w, err)
		return
	}
	var input struct {
		ExpectedVersion uint64 `json:"expectedVersion"`
		Body            string `json:"body"`
		Note            string `json:"note"`
		SourceAttemptID string `json:"sourceAttemptId"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var attemptID domain.AttemptID
	if input.SourceAttemptID != "" {
		attemptID, err = domain.ParseAttemptID(input.SourceAttemptID)
		if err != nil {
			writeProjectError(w, err)
			return
		}
	}
	meta, ok := requestCommandMetaForProduct(w, r, "save-project-document", server.product)
	if !ok {
		return
	}
	result, err := server.service.SaveProjectDocument(r.Context(), app.SaveProjectDocumentRequest{CommandMeta: meta, TaskID: id, ExpectedVersion: input.ExpectedVersion, Body: input.Body, Note: input.Note, SourceAttemptID: attemptID})
	if err != nil {
		writeMutationConflictError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result.Document)
}

func (server *Server) reviewProjectDocument(w http.ResponseWriter, r *http.Request) {
	id, err := domain.ParseTaskID(r.PathValue("taskID"))
	if err != nil {
		writeProjectError(w, err)
		return
	}
	var input struct {
		ExpectedVersion uint64                           `json:"expectedVersion"`
		Kind            domain.ProjectDocumentReviewKind `json:"kind"`
		Note            string                           `json:"note"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	meta, ok := requestCommandMetaForProduct(w, r, "review-project-document", server.product)
	if !ok {
		return
	}
	result, err := server.service.ReviewProjectDocument(r.Context(), app.ReviewProjectDocumentRequest{CommandMeta: meta, TaskID: id, ExpectedVersion: input.ExpectedVersion, Kind: input.Kind, Note: input.Note})
	if err != nil {
		writeMutationConflictError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result.Document)
}
