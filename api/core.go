package api

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"qmdsr/executor"
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
	FilesOnly     bool
	FilesAll      bool
	TraceID       string
	Confirm       bool
}

type searchCoreResult struct {
	Response *model.SearchResponse
	RouteLog []string
}

var errCriticalOverloadShed = errors.New("cpu critical overload shed")

type searchAndGetCoreRequest struct {
	Query         string
	RequestedMode string
	Collections   []string
	AllowFallback bool
	TopK          int32
	MinScore      float64
	MaxGetDocs    int32
	MaxGetBytes   int32
	TraceID       string
	Confirm       bool
}

type searchAndGetCoreResult struct {
	Response *model.SearchAndGetResponse
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
		if req.FilesOnly && req.FilesAll {
			topK = 0
		} else {
			topK = s.cfg.Search.TopK
		}
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

	if s.orch.IsOverloaded() {
		mode = "search"
		disableDeepEscalation = true
		preDegraded = true
		preDegradeReason = "CPU_OVERLOAD_PROTECT"
	}

	if s.orch.IsCriticalOverloaded() {
		allowByCache := true
		for _, collection := range collections {
			if !s.orch.HasCachedResult(orchestrator.SearchParams{
				Query:      query,
				Mode:       mode,
				Collection: collection,
				N:          topK,
				MinScore:   minScore,
				Fallback:   req.AllowFallback,
				FilesOnly:  req.FilesOnly,
				FilesAll:   req.FilesAll,
			}) {
				allowByCache = false
				break
			}
		}
		if !allowByCache {
			s.log.Error("cpu critical overload shed request",
				"trace_id", traceID,
				"requested_mode", requestedMode,
				"query_len", len([]rune(query)),
			)
			return nil, errCriticalOverloadShed
		}
	}

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
			FilesOnly:             req.FilesOnly,
			FilesAll:              req.FilesAll,
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
	filesAllCapped := false
	if req.FilesOnly && req.FilesAll && s.cfg.Search.FilesAllMaxHits > 0 && len(combined) > s.cfg.Search.FilesAllMaxHits {
		combined = combined[:s.cfg.Search.FilesAllMaxHits]
		filesAllCapped = true
	}
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
	if filesAllCapped {
		degraded = true
		if degradeReason == "" {
			degradeReason = "FILES_ALL_CAPPED"
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
		Results:       combined,
		Meta:          meta,
		FormattedText: renderFormattedText(combined, meta, req.FilesOnly),
	}

	var routeLog []string
	if req.Explain {
		routeLog = buildRouteLog(requestedMode, req.AllowFallback, mode, meta, len(collections), len(combined))
	}

	return &searchCoreResult{Response: resp, RouteLog: routeLog}, nil
}

func (s *Server) executeSearchAndGetCore(ctx context.Context, req searchAndGetCoreRequest) (*searchAndGetCoreResult, error) {
	query := strings.TrimSpace(req.Query)
	if query == "" {
		return nil, fmt.Errorf("query is required")
	}

	maxGetDocs := int(req.MaxGetDocs)
	if maxGetDocs <= 0 {
		maxGetDocs = 3
	}

	maxGetBytes := int(req.MaxGetBytes)
	if maxGetBytes <= 0 {
		maxGetBytes = 12000
	}

	searchRes, err := s.executeSearchCore(ctx, searchCoreRequest{
		Query:         query,
		RequestedMode: req.RequestedMode,
		Collections:   req.Collections,
		AllowFallback: req.AllowFallback,
		TopK:          req.TopK,
		MinScore:      req.MinScore,
		FilesOnly:     true,
		TraceID:       req.TraceID,
		Confirm:       req.Confirm,
	})
	if err != nil {
		return nil, err
	}

	fileHits := searchRes.Response.Results
	if len(fileHits) == 0 {
		resp := &model.SearchAndGetResponse{
			FileHits:      []model.SearchResult{},
			Documents:     []model.Document{},
			Meta:          searchRes.Response.Meta,
			FormattedText: renderSearchAndGetText(fileHits, nil, nil, searchRes.Response.Meta),
		}
		return &searchAndGetCoreResult{Response: resp}, nil
	}

	limit := maxGetDocs
	if limit > len(fileHits) {
		limit = len(fileHits)
	}

	docs := make([]model.Document, 0, limit)
	truncated := make([]string, 0)
	remainingBytes := maxGetBytes
	type getOutcome struct {
		uri     string
		content string
		err     error
	}
	targets := make([]string, 0, limit)
	for i := 0; i < limit; i++ {
		uri := strings.TrimSpace(preferredHitURI(fileHits[i]))
		if uri == "" {
			continue
		}
		targets = append(targets, uri)
	}

	outcomes := make([]getOutcome, len(targets))
	var wg sync.WaitGroup
	wg.Add(len(targets))
	for i, uri := range targets {
		go func(idx int, docURI string) {
			defer wg.Done()
			content, getErr := s.exec.Get(ctx, docURI, executor.GetOpts{Full: true})
			outcomes[idx] = getOutcome{
				uri:     docURI,
				content: content,
				err:     getErr,
			}
		}(i, uri)
	}
	wg.Wait()

	for _, out := range outcomes {
		if out.err != nil {
			s.log.Warn("search_and_get get failed", "uri", out.uri, "err", out.err, "trace_id", searchRes.Response.Meta.TraceID)
			continue
		}

		contentBytes := len([]byte(out.content))
		if remainingBytes >= 0 && contentBytes > remainingBytes {
			truncated = append(truncated, out.uri)
			continue
		}

		docs = append(docs, model.Document{
			File:    out.uri,
			Content: out.content,
		})
		remainingBytes -= contentBytes
	}

	meta := searchRes.Response.Meta
	if len(truncated) > 0 {
		meta.Degraded = true
		if meta.DegradeReason == "" {
			meta.DegradeReason = "MAX_GET_BYTES_TRUNCATED"
		}
	}

	resp := &model.SearchAndGetResponse{
		FileHits:      fileHits,
		Documents:     docs,
		Meta:          meta,
		FormattedText: renderSearchAndGetText(fileHits, docs, truncated, meta),
	}
	return &searchAndGetCoreResult{Response: resp}, nil
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

	switch {
	case s.orch.IsCriticalOverloaded():
		resp.Mode = "cpu_critical_overloaded"
		resp.Status = "unhealthy"
		resp.Components = append(resp.Components, &qmdsrv1.ComponentHealth{
			Name:    "cpu_guard",
			Status:  "critical",
			Message: "critical overload shedding new uncached requests",
		})
	case s.orch.IsOverloaded():
		resp.Mode = "cpu_overloaded"
		if strings.EqualFold(resp.Status, "healthy") {
			resp.Status = "degraded"
		}
		resp.Components = append(resp.Components, &qmdsrv1.ComponentHealth{
			Name:    "cpu_guard",
			Status:  "degraded",
			Message: "overload protection active, forcing search mode and limiting concurrency",
		})
	}
	return resp
}

func (s *Server) buildStatusResponse(traceID string) *qmdsrv1.StatusResponse {
	if strings.TrimSpace(traceID) == "" {
		traceID = genRequestID()
	}

	return &qmdsrv1.StatusResponse{
		Version:                     version.Version,
		Commit:                      version.Commit,
		LowResourceMode:             s.cfg.Runtime.LowResourceMode,
		AllowCpuDeepQuery:           s.cfg.Runtime.AllowCPUDeepQuery,
		DeepQueryEnabled:            s.exec.HasCapability("deep_query"),
		VectorEnabled:               s.exec.HasCapability("vector"),
		QueryMaxConcurrency:         int32(s.cfg.Runtime.QueryMaxConcurrency),
		QueryTimeoutMs:              durationToInt32Milliseconds(s.cfg.Runtime.QueryTimeout),
		DeepFailTimeoutMs:           durationToInt32Milliseconds(s.cfg.Runtime.DeepFailTimeout),
		DeepNegativeTtlSec:          durationToInt32Seconds(s.cfg.Runtime.DeepNegativeTTL),
		TraceId:                     traceID,
		CpuOverloaded:               s.orch.IsOverloaded(),
		CpuCriticalOverloaded:       s.orch.IsCriticalOverloaded(),
		OverloadMaxConcurrentSearch: int32(s.cfg.Runtime.OverloadMaxConcurrentSearch),
	}
}
