package localweb

import (
	"errors"
	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
	"net/http"
	"strconv"
)

func (server *Server) getTaskBoard(w http.ResponseWriter, r *http.Request) {
	id, err := domain.ParseProjectID(r.PathValue("projectID"))
	if err != nil {
		writeProjectError(w, err)
		return
	}
	values := r.URL.Query()
	q := app.TaskBoardQuery{RoomID: values.Get("roomId"), RepoID: values.Get("repoId"), Phase: values.Get("phase"), Attention: values.Get("attention"), Archived: values.Get("archived"), Cursor: values.Get("cursor"), OptionsCursor: values.Get("optionsCursor")}
	if raw := values.Get("limit"); raw != "" {
		q.Limit, err = strconv.Atoi(raw)
		if err != nil || q.Limit < 1 {
			writeProjectError(w, domain.ErrInvalidArgument)
			return
		}
	}
	v, err := server.service.GetTaskBoard(r.Context(), id, q)
	if errors.Is(err, app.ErrTaskBoardSnapshotChanged) {
		writeError(w, http.StatusConflict, err)
		return
	}
	if err != nil {
		writeProjectError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}
