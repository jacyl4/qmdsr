package api

import (
	"net/http"

	"qmdsr/model"
)

func (s *Server) handleStateUpdate(w http.ResponseWriter, r *http.Request) {
	var req model.StateUpdateRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid JSON body")
		return
	}

	if err := s.stateMgr.UpdateState(req); err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
