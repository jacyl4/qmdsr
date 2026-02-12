package api

import (
	"net/http"
	"strings"

	"qmdsr/executor"
	"qmdsr/model"
	"qmdsr/orchestrator"
)

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	var req model.SearchRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid JSON body: "+err.Error())
		return
	}

	if req.Query == "" {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "query is required")
		return
	}

	if req.N <= 0 {
		req.N = s.cfg.Search.TopK
	}
	if req.MinScore <= 0 {
		req.MinScore = s.cfg.Search.MinScore
	}
	if req.Mode == "" {
		req.Mode = s.cfg.Search.DefaultMode
	}

	fallback := s.cfg.Search.FallbackEnabled
	if req.Fallback != nil {
		fallback = *req.Fallback
	}

	result, err := s.orch.Search(r.Context(), orchestrator.SearchParams{
		Query:      req.Query,
		Mode:       req.Mode,
		Collection: req.Collection,
		N:          req.N,
		MinScore:   req.MinScore,
		Fallback:   fallback,
		Format:     req.Format,
		Confirm:    req.Confirm,
	})
	if err != nil {
		if strings.Contains(err.Error(), "requires confirm=true") {
			writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
			return
		}
		if strings.Contains(err.Error(), "not found") {
			writeError(w, http.StatusNotFound, "NOT_FOUND", err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}

	if req.Format == "markdown" {
		writeMarkdown(w, result.Results)
		return
	}

	writeJSON(w, http.StatusOK, model.SearchResponse{
		Results: result.Results,
		Meta:    result.Meta,
	})
}

func (s *Server) handleQuickCore(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if q == "" {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "q parameter is required")
		return
	}

	result, err := s.orch.Search(r.Context(), orchestrator.SearchParams{
		Query:    q,
		Mode:     "auto",
		Fallback: false,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}

	writeMarkdown(w, result.Results)
}

func (s *Server) handleQuickBroad(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if q == "" {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "q parameter is required")
		return
	}

	result, err := s.orch.Search(r.Context(), orchestrator.SearchParams{
		Query:    q,
		Mode:     "auto",
		Fallback: true,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}

	writeMarkdown(w, result.Results)
}

func (s *Server) handleQuickDeep(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if q == "" {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "q parameter is required")
		return
	}

	result, err := s.orch.Search(r.Context(), orchestrator.SearchParams{
		Query:    q,
		Mode:     "query",
		Fallback: true,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}

	writeMarkdown(w, result.Results)
}

func (s *Server) handleGet(w http.ResponseWriter, r *http.Request) {
	var req model.GetRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid JSON body")
		return
	}

	if req.Ref == "" {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "ref is required")
		return
	}

	content, err := s.exec.Get(r.Context(), req.Ref, executor.GetOpts{
		Full:        req.Full,
		LineNumbers: req.LineNumbers,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}

	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Write([]byte(content))
}

func (s *Server) handleMultiGet(w http.ResponseWriter, r *http.Request) {
	var req model.MultiGetRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid JSON body")
		return
	}

	if req.Pattern == "" {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "pattern is required")
		return
	}

	docs, err := s.exec.MultiGet(r.Context(), req.Pattern, req.MaxBytes)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, docs)
}
