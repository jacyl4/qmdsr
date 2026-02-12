package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"qmdsr/config"
	"qmdsr/executor"
	"qmdsr/guardian"
	"qmdsr/heartbeat"
	"qmdsr/memory"
	"qmdsr/model"
	"qmdsr/orchestrator"
	"qmdsr/scheduler"
)

type Server struct {
	cfg         *config.Config
	orch        *orchestrator.Orchestrator
	exec        executor.Executor
	sched       *scheduler.Scheduler
	guardian    *guardian.Guardian
	heartbeat   *heartbeat.Heartbeat
	memWriter   *memory.Writer
	stateMgr    *memory.StateManager
	log         *slog.Logger
	httpServer  *http.Server
}

type Deps struct {
	Config      *config.Config
	Orchestrator *orchestrator.Orchestrator
	Executor    executor.Executor
	Scheduler   *scheduler.Scheduler
	Guardian    *guardian.Guardian
	Heartbeat   *heartbeat.Heartbeat
	MemWriter   *memory.Writer
	StateMgr    *memory.StateManager
	Logger      *slog.Logger
}

func NewServer(deps Deps) *Server {
	s := &Server{
		cfg:       deps.Config,
		orch:      deps.Orchestrator,
		exec:      deps.Executor,
		sched:     deps.Scheduler,
		guardian:  deps.Guardian,
		heartbeat: deps.Heartbeat,
		memWriter: deps.MemWriter,
		stateMgr:  deps.StateMgr,
		log:       deps.Logger,
	}

	mux := http.NewServeMux()

	mux.HandleFunc("POST /api/search", s.handleSearch)
	mux.HandleFunc("POST /api/get", s.handleGet)
	mux.HandleFunc("POST /api/multi-get", s.handleMultiGet)

	mux.HandleFunc("POST /api/memory/write", s.handleMemoryWrite)
	mux.HandleFunc("POST /api/state/update", s.handleStateUpdate)

	mux.HandleFunc("GET /api/quick/core", s.handleQuickCore)
	mux.HandleFunc("GET /api/quick/broad", s.handleQuickBroad)
	mux.HandleFunc("GET /api/quick/deep", s.handleQuickDeep)

	mux.HandleFunc("GET /api/status", s.handleStatus)
	mux.HandleFunc("POST /api/admin/reindex", s.handleAdminReindex)
	mux.HandleFunc("POST /api/admin/embed", s.handleAdminEmbed)
	mux.HandleFunc("POST /api/admin/cache/clear", s.handleAdminCacheClear)
	mux.HandleFunc("GET /api/admin/collections", s.handleAdminCollections)
	mux.HandleFunc("POST /api/admin/mcp/restart", s.handleAdminMCPRestart)

	mux.HandleFunc("GET /health", s.handleHealth)

	s.httpServer = &http.Server{
		Addr:         s.cfg.Server.Listen,
		Handler:      s.withLogging(mux),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 120 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	return s
}

func (s *Server) Start() error {
	s.log.Info("HTTP server starting", "listen", s.cfg.Server.Listen)
	return s.httpServer.ListenAndServe()
}

func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpServer.Shutdown(ctx)
}

func (s *Server) withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &responseWriter{ResponseWriter: w, status: 200}
		next.ServeHTTP(rw, r)
		s.log.Info("http request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rw.status,
			"latency_ms", time.Since(start).Milliseconds(),
		)
	})
}

type responseWriter struct {
	http.ResponseWriter
	status int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.status = code
	rw.ResponseWriter.WriteHeader(code)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, model.ErrorResponse{
		Error: model.ErrorDetail{
			Code:      code,
			Message:   message,
			RequestID: genRequestID(),
		},
	})
}

func genRequestID() string {
	b := make([]byte, 6)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func writeMarkdown(w http.ResponseWriter, results []model.SearchResult) {
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	for _, r := range results {
		w.Write([]byte("### " + r.Title + "\n"))
		w.Write([]byte("*" + r.File + "* (score: "))
		w.Write([]byte(formatScore(r.Score)))
		w.Write([]byte(")\n\n"))
		w.Write([]byte(r.Snippet + "\n\n---\n\n"))
	}
}

func formatScore(s float64) string {
	return fmt.Sprintf("%.2f", s)
}

func decodeJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(v)
}
