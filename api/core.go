package api

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"qmdsr/internal/searchutil"
	"qmdsr/internal/version"
	"qmdsr/model"
	"qmdsr/orchestrator"
	qmdsrv1 "qmdsr/pb/qmdsrv1"
)

type searchCoreRequest struct {
	Query         string
	RequestedMode string
	Collections   []string
	AllowFallback bool
	TimeoutMs     int32
	TopK          int32
	MinScore      float64
	Explain       bool
	TraceID       string
	Confirm       bool
}

type searchCoreResult struct {
	Response *model.SearchResponse
	RouteLog []string
}

func (s *Server) executeSearchCore(ctx context.Context, req searchCoreRequest) (*searchCoreResult, error) {
	query := strings.TrimSpace(req.Query)
	if query == "" {
		return nil, fmt.Errorf("query is required")
	}

	requestedMode := normalizeRequestedMode(req.RequestedMode)
	traceID := strings.TrimSpace(req.TraceID)
	if traceID == "" {
		traceID = genRequestID()
	}

	topK := int(req.TopK)
	if topK <= 0 {
		topK = s.cfg.Search.TopK
	}
	minScore := req.MinScore
	if minScore <= 0 {
		minScore = s.cfg.Search.MinScore
	}

	searchCtx := ctx
	var cancel context.CancelFunc
	if req.TimeoutMs > 0 {
		searchCtx, cancel = context.WithTimeout(ctx, time.Duration(req.TimeoutMs)*time.Millisecond)
		defer cancel()
	}

	collections := normalizeCollections(req.Collections)
	if len(collections) == 0 {
		collections = []string{""}
	}

	start := time.Now()
	mode := requestedModeToOrchestratorMode(requestedMode)
	disableDeepEscalation := requestedMode == "core" || requestedMode == "broad"
	preDegraded := false
	preDegradeReason := ""

	// Explicit deep requests are still guarded in low-resource mode when fallback is allowed.
	// This prevents known OOM-prone deep paths from destabilizing the service.
	if requestedMode == "deep" && req.AllowFallback && !s.orch.AllowDeepQuery(query) {
		mode = "search"
		disableDeepEscalation = true
		preDegraded = true
		preDegradeReason = "DEEP_GATE_REJECTED"
	}

	combined := make([]model.SearchResult, 0, topK*len(collections))
	searchedSet := make(map[string]struct{})
	modeUsed := ""
	fallbackTriggered := false
	cacheHit := false
	degraded := false
	degradeReason := ""
	firstErr := error(nil)
	successCount := 0

	for _, collection := range collections {
		result, err := s.orch.Search(searchCtx, orchestrator.SearchParams{
			Query:                 query,
			Mode:                  mode,
			Collection:            collection,
			N:                     topK,
			MinScore:              minScore,
			Fallback:              req.AllowFallback,
			DisableDeepEscalation: disableDeepEscalation,
			Confirm:               req.Confirm,
		})
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}

		successCount++
		combined = append(combined, result.Results...)

		if result.Meta.ModeUsed == "query" || modeUsed == "" {
			modeUsed = result.Meta.ModeUsed
		}
		fallbackTriggered = fallbackTriggered || result.Meta.FallbackTriggered
		cacheHit = cacheHit || result.Meta.CacheHit
		degraded = degraded || result.Meta.Degraded
		if degradeReason == "" && result.Meta.DegradeReason != "" {
			degradeReason = result.Meta.DegradeReason
		}
		if len(result.Meta.CollectionsSearched) == 0 {
			if collection != "" {
				searchedSet[collection] = struct{}{}
			}
			continue
		}
		for _, c := range result.Meta.CollectionsSearched {
			if c != "" {
				searchedSet[c] = struct{}{}
			}
		}
	}

	if successCount == 0 {
		if firstErr != nil {
			return nil, firstErr
		}
		return nil, fmt.Errorf("search failed")
	}

	if modeUsed == "" {
		modeUsed = "search"
	}

	combined = searchutil.DedupSortLimit(combined, topK)
	collectionsSearched := sortedKeys(searchedSet)
	servedMode := deriveServedMode(requestedMode, modeUsed, fallbackTriggered, degraded)
	if requestedMode == "deep" && servedMode != "deep" {
		degraded = true
		if degradeReason == "" {
			degradeReason = "DEEP_GATE_REJECTED"
		}
	}
	if preDegraded {
		degraded = true
		if degradeReason == "" {
			degradeReason = preDegradeReason
		}
	}

	meta := model.SearchMeta{
		ModeUsed:            modeUsed,
		ServedMode:          servedMode,
		CollectionsSearched: collectionsSearched,
		FallbackTriggered:   fallbackTriggered,
		CacheHit:            cacheHit,
		Degraded:            degraded,
		DegradeReason:       degradeReason,
		TraceID:             traceID,
		LatencyMs:           time.Since(start).Milliseconds(),
	}
	s.log.Info("search served",
		"trace_id", traceID,
		"requested_mode", requestedMode,
		"served_mode", meta.ServedMode,
		"degraded", meta.Degraded,
		"degrade_reason", meta.DegradeReason,
		"hits", len(combined),
		"latency_ms", meta.LatencyMs,
	)

	resp := &model.SearchResponse{
		Results: combined,
		Meta:    meta,
	}

	var routeLog []string
	if req.Explain {
		routeLog = buildRouteLog(requestedMode, req.AllowFallback, mode, meta, len(collections), len(combined))
	}

	return &searchCoreResult{Response: resp, RouteLog: routeLog}, nil
}

func (s *Server) buildHealthResponse() *qmdsrv1.HealthResponse {
	h := s.heartbeat.GetHealth()
	resp := &qmdsrv1.HealthResponse{
		Status:    h.OverallStr,
		Mode:      h.Mode,
		UptimeSec: h.UptimeSec,
	}
	if resp.Mode == "" {
		resp.Mode = "normal"
	}

	names := make([]string, 0, len(h.Components))
	for name := range h.Components {
		names = append(names, name)
	}
	sort.Strings(names)

	resp.Components = make([]*qmdsrv1.ComponentHealth, 0, len(names))
	for _, name := range names {
		comp := h.Components[name]
		if comp == nil {
			continue
		}
		resp.Components = append(resp.Components, &qmdsrv1.ComponentHealth{
			Name:    name,
			Status:  comp.LevelStr,
			Message: comp.Message,
		})
	}
	return resp
}

func (s *Server) buildStatusResponse(traceID string) *qmdsrv1.StatusResponse {
	if strings.TrimSpace(traceID) == "" {
		traceID = genRequestID()
	}

	return &qmdsrv1.StatusResponse{
		Version:             version.Version,
		Commit:              version.Commit,
		LowResourceMode:     s.cfg.Runtime.LowResourceMode,
		AllowCpuDeepQuery:   s.cfg.Runtime.AllowCPUDeepQuery,
		DeepQueryEnabled:    s.exec.HasCapability("deep_query"),
		VectorEnabled:       s.exec.HasCapability("vector"),
		QueryMaxConcurrency: int32(s.cfg.Runtime.QueryMaxConcurrency),
		QueryTimeoutMs:      durationToInt32Milliseconds(s.cfg.Runtime.QueryTimeout),
		DeepFailTimeoutMs:   durationToInt32Milliseconds(s.cfg.Runtime.DeepFailTimeout),
		DeepNegativeTtlSec:  durationToInt32Seconds(s.cfg.Runtime.DeepNegativeTTL),
		TraceId:             traceID,
	}
}
