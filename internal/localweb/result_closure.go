package localweb

import (
	"net/http"

	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
)

func (server *Server) getResultClosure(w http.ResponseWriter, r *http.Request) {
	id, err := domain.ParseRunID(r.PathValue("runID"))
	if err != nil {
		writeProjectError(w, err)
		return
	}
	v, err := server.service.LoadResultClosure(r.Context(), id)
	if err != nil {
		writeMutationConflictError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (server *Server) resultClosureCommand(w http.ResponseWriter, r *http.Request) {
	id, err := domain.ParseRunID(r.PathValue("runID"))
	if err != nil {
		writeProjectError(w, err)
		return
	}
	var input struct {
		ExpectedVersion uint64 `json:"expectedVersion"`
		ResultDigest    string `json:"resultDigest"`
		PreviewDigest   string `json:"previewDigest"`
	}
	if err = decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	action := r.PathValue("action")
	if action != "preview" && action != "confirm" {
		http.NotFound(w, r)
		return
	}
	meta, ok := requestCommandMetaForProduct(w, r, "result-closure-"+action, server.product)
	if !ok {
		return
	}
	req := app.ResultClosureRequest{CommandMeta: meta, RunID: id, ExpectedVersion: input.ExpectedVersion, ResultDigest: input.ResultDigest, PreviewDigest: input.PreviewDigest}
	var v app.ResultClosureView
	if action == "preview" {
		v, err = server.service.PreviewResultClosure(r.Context(), req)
	} else {
		v, err = server.service.CloseResult(r.Context(), req)
	}
	if err != nil {
		writeMutationConflictError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (server *Server) closedResultCleanupCommand(w http.ResponseWriter, r *http.Request) {
	id, err := domain.ParseRunID(r.PathValue("runID"))
	if err != nil {
		writeProjectError(w, err)
		return
	}
	var input struct {
		ExpectedVersion uint64 `json:"expectedVersion"`
		ResultDigest    string `json:"resultDigest"`
		RepoID          string `json:"repoId"`
		OperationID     string `json:"operationId"`
	}
	if err = decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	action := r.PathValue("action")
	if action != "preview" && action != "confirm" {
		http.NotFound(w, r)
		return
	}
	meta, ok := requestCommandMetaForProduct(w, r, "closed-result-cleanup-"+action, server.product)
	if !ok {
		return
	}
	req := app.DeliveryRequest{CommandMeta: meta, RunID: id, ExpectedVersion: input.ExpectedVersion, ResultDigest: input.ResultDigest, RepoID: input.RepoID, OperationID: input.OperationID}
	var view app.DeliveryOperationView
	if action == "preview" {
		view, err = server.service.PreviewClosedResultCleanup(r.Context(), req)
	} else {
		view, err = server.service.ConfirmClosedResultCleanup(r.Context(), req)
	}
	if err != nil {
		writeDeliveryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}
