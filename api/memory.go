package api

import (
	"net/http"

	"qmdsr/model"
)

func (s *Server) handleMemoryWrite(w http.ResponseWriter, r *http.Request) {
	var req model.MemoryWriteRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid JSON body")
		return
	}

	if req.Topic == "" || req.Summary == "" {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "topic and summary are required")
		return
	}

	if err := s.memWriter.WriteMemory(req); err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
