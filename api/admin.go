package api

import (
	"net/http"

	"qmdsr/guardian"
)

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	health := s.heartbeat.GetHealth()
	writeJSON(w, http.StatusOK, health)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	health := s.heartbeat.GetHealth()

	status := http.StatusOK
	if health.Overall > 1 {
		status = http.StatusServiceUnavailable
	}

	resp := map[string]any{
		"status": health.OverallStr,
		"uptime": health.UptimeSec,
		"mode":   health.Mode,
	}

	components := make(map[string]string)
	for name, comp := range health.Components {
		components[name] = comp.LevelStr
	}
	resp["components"] = components

	writeJSON(w, status, resp)
}

func (s *Server) handleAdminReindex(w http.ResponseWriter, r *http.Request) {
	if err := s.sched.TriggerReindex(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "reindex triggered"})
}

func (s *Server) handleAdminEmbed(w http.ResponseWriter, r *http.Request) {
	force := r.URL.Query().Get("force") == "1"
	if err := s.sched.TriggerEmbed(r.Context(), force); err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	msg := "embed triggered"
	if force {
		msg = "full embed triggered"
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": msg})
}

func (s *Server) handleAdminCacheClear(w http.ResponseWriter, r *http.Request) {
	s.orch.ClearCache()
	writeJSON(w, http.StatusOK, map[string]string{"status": "cache cleared"})
}

func (s *Server) handleAdminCollections(w http.ResponseWriter, r *http.Request) {
	cols, err := s.exec.CollectionList(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, cols)
}

func (s *Server) handleAdminMCPRestart(w http.ResponseWriter, r *http.Request) {
	g, ok := s.getGuardian()
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "guardian not available")
		return
	}
	if err := g.RestartMCP(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "mcp restart triggered"})
}

func (s *Server) getGuardian() (*guardian.Guardian, bool) {
	if s.guardian == nil {
		return nil, false
	}
	return s.guardian, true
}
