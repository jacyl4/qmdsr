package api

import (
	"context"
	"errors"
	"math"
	"strings"
	"time"

	"qmdsr/model"
)

var errGuardianUnavailable = errors.New("guardian not available")

type adminOpResult struct {
	Message   string
	TraceID   string
	LatencyMs int64
}

type adminCollectionsResult struct {
	Collections []model.CollectionInfo
	TraceID     string
	LatencyMs   int64
}

func (s *Server) executeAdminReindexCore(ctx context.Context, traceID string) (*adminOpResult, error) {
	start := time.Now()
	traceID = normalizeTraceID(traceID)

	err := s.sched.TriggerReindex(ctx)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		s.logAdminCall("Reindex", traceID, latency, false, err)
		return nil, err
	}

	res := &adminOpResult{
		Message:   "reindex triggered",
		TraceID:   traceID,
		LatencyMs: latency,
	}
	s.logAdminCall("Reindex", traceID, latency, true, nil)
	return res, nil
}

func (s *Server) executeAdminEmbedCore(ctx context.Context, traceID string, force bool) (*adminOpResult, error) {
	start := time.Now()
	traceID = normalizeTraceID(traceID)

	message := "embed triggered"
	if force {
		message = "full embed triggered"
	}

	if s.cfg.Runtime.LowResourceMode && !(s.cfg.Runtime.AllowCPUVSearch || s.cfg.Runtime.AllowCPUDeepQuery) {
		res := &adminOpResult{
			Message:   "embed disabled in low_resource_mode",
			TraceID:   traceID,
			LatencyMs: time.Since(start).Milliseconds(),
		}
		s.logAdminCall("Embed", traceID, res.LatencyMs, true, nil)
		return res, nil
	}

	err := s.sched.TriggerEmbed(ctx, force)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		s.logAdminCall("Embed", traceID, latency, false, err)
		return nil, err
	}

	res := &adminOpResult{
		Message:   message,
		TraceID:   traceID,
		LatencyMs: latency,
	}
	s.logAdminCall("Embed", traceID, latency, true, nil)
	return res, nil
}

func (s *Server) executeAdminCacheClearCore(traceID string) (*adminOpResult, error) {
	start := time.Now()
	traceID = normalizeTraceID(traceID)

	s.orch.ClearCache()
	latency := time.Since(start).Milliseconds()
	res := &adminOpResult{
		Message:   "cache cleared",
		TraceID:   traceID,
		LatencyMs: latency,
	}
	s.logAdminCall("CacheClear", traceID, latency, true, nil)
	return res, nil
}

func (s *Server) executeAdminCollectionsCore(ctx context.Context, traceID string) (*adminCollectionsResult, error) {
	start := time.Now()
	traceID = normalizeTraceID(traceID)

	collections, err := s.exec.CollectionList(ctx)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		s.logAdminCall("Collections", traceID, latency, false, err)
		return nil, err
	}

	res := &adminCollectionsResult{
		Collections: collections,
		TraceID:     traceID,
		LatencyMs:   latency,
	}
	s.logAdminCall("Collections", traceID, latency, true, nil)
	return res, nil
}

func (s *Server) executeAdminMCPRestartCore(ctx context.Context, traceID string) (*adminOpResult, error) {
	start := time.Now()
	traceID = normalizeTraceID(traceID)

	if s.guardian == nil {
		err := errGuardianUnavailable
		latency := time.Since(start).Milliseconds()
		s.logAdminCall("MCPRestart", traceID, latency, false, err)
		return nil, err
	}

	err := s.guardian.RestartMCP(ctx)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		s.logAdminCall("MCPRestart", traceID, latency, false, err)
		return nil, err
	}

	res := &adminOpResult{
		Message:   "mcp restart triggered",
		TraceID:   traceID,
		LatencyMs: latency,
	}
	s.logAdminCall("MCPRestart", traceID, latency, true, nil)
	return res, nil
}

func normalizeTraceID(traceID string) string {
	traceID = strings.TrimSpace(traceID)
	if traceID == "" {
		return genRequestID()
	}
	return traceID
}

func (s *Server) logAdminCall(method, traceID string, latencyMs int64, ok bool, err error) {
	if err != nil {
		s.log.Error("admin rpc failed",
			"method", method,
			"trace_id", traceID,
			"latency_ms", latencyMs,
			"ok", ok,
			"err", err,
		)
		return
	}

	s.log.Info("admin rpc served",
		"method", method,
		"trace_id", traceID,
		"latency_ms", latencyMs,
		"ok", ok,
	)
}

func intToInt32(value int) int32 {
	if value > math.MaxInt32 {
		return math.MaxInt32
	}
	if value < math.MinInt32 {
		return math.MinInt32
	}
	return int32(value)
}
