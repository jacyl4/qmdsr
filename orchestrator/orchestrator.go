package orchestrator

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"qmdsr/cache"
	"qmdsr/config"
	"qmdsr/executor"
	"qmdsr/internal/resourceguard"
	"qmdsr/internal/searchutil"
	"qmdsr/internal/textutil"
	"qmdsr/model"
	"qmdsr/router"
)

type Orchestrator struct {
	cfg        *config.Config
	exec       executor.Executor
	cache      *cache.Cache
	log        *slog.Logger
	cpuMonitor *resourceguard.CPUMonitor

	deepNegMu          sync.Mutex
	deepNeg            map[string]time.Time
	deepNegScopeFails  map[string][]time.Time
	deepNegMarkCount   int64
	deepNegExactHitCnt int64
	deepNegScopeHitCnt int64

	obsMu          sync.Mutex
	obsCount       int64
	obsLatencyMSum int64
	obsHitsSum     int64
	obsHitZero     int64
	obsHitLow      int64
	obsHitMid      int64
	obsHitHigh     int64
	obsDegraded    int64
	obsLastLogAt   time.Time

	searchTokens chan struct{}
}

const maxSnippetCharsPerResult = 1500
const deepNegativeScopeFailThreshold = 3
const deepNegativeScopeFailWindow = 5 * time.Minute

func New(cfg *config.Config, exec executor.Executor, c *cache.Cache, logger *slog.Logger) *Orchestrator {
	if c == nil {
		c = cache.New(&cfg.Cache)
	}
	o := &Orchestrator{
		cfg:               cfg,
		exec:              exec,
		cache:             c,
		log:               logger,
		deepNeg:           make(map[string]time.Time),
		deepNegScopeFails: make(map[string][]time.Time),
		obsLastLogAt:      time.Now(),
	}
	maxConcurrentSearch := cfg.Runtime.OverloadMaxConcurrentSearch
	if maxConcurrentSearch <= 0 {
		maxConcurrentSearch = 2
	}
	o.searchTokens = make(chan struct{}, maxConcurrentSearch)
	o.cpuMonitor = resourceguard.NewCPUMonitor(resourceguard.CPUMonitorConfig{
		Enabled:         cfg.Runtime.CPUOverloadProtect,
		SampleInterval:  cfg.Runtime.CPUSampleInterval,
		OverloadPercent: cfg.Runtime.CPUOverloadThreshold,
		OverloadSustain: cfg.Runtime.CPUOverloadSustain,
		RecoverPercent:  cfg.Runtime.CPURecoverThreshold,
		RecoverSustain:  cfg.Runtime.CPURecoverSustain,
		CriticalPercent: cfg.Runtime.CPUCriticalThreshold,
		CriticalSustain: cfg.Runtime.CPUCriticalSustain,
	}, logger.With("component", "cpu_guard"))
	return o
}

func (o *Orchestrator) Start(ctx context.Context) {
	if o.cpuMonitor != nil {
		o.cpuMonitor.Start(ctx)
	}
}

func (o *Orchestrator) IsOverloaded() bool {
	if o.cpuMonitor == nil {
		return false
	}
	return o.cpuMonitor.IsOverloaded()
}

func (o *Orchestrator) IsCriticalOverloaded() bool {
	if o.cpuMonitor == nil {
		return false
	}
	return o.cpuMonitor.IsCriticalOverloaded()
}

func (o *Orchestrator) ClearCache() {
	if o.cache != nil {
		o.cache.Clear()
	}
}

func (o *Orchestrator) HasCachedResult(params SearchParams) bool {
	if o.cache == nil {
		return false
	}

	n := params.N
	if n <= 0 {
		if params.FilesOnly && params.FilesAll {
			n = 0
		} else {
			n = o.cfg.Search.TopK
		}
	}
	minScore := params.MinScore
	if minScore <= 0 {
		minScore = o.cfg.Search.MinScore
	}
	key := cache.MakeCacheKey(params.Query, params.Mode, params.Collection, minScore, n, params.Fallback, params.FilesOnly, params.FilesAll)
	_, ok := o.cache.Get(key)
	return ok
}

func (o *Orchestrator) EnsureCollections(ctx context.Context) error {
	existing, err := o.exec.CollectionList(ctx)
	if err != nil {
		o.log.Warn("failed to list collections, will try to add all", "err", err)
		existing = nil
	}

	existingMap := make(map[string]bool)
	for _, col := range existing {
		existingMap[col.Name] = true
	}

	for _, col := range o.cfg.Collections {
		if existingMap[col.Name] {
			o.log.Info("collection already registered", "name", col.Name)
			continue
		}

		o.log.Info("registering collection", "name", col.Name, "path", col.Path)
		mask := col.Mask
		if mask == "" {
			mask = "**/*.md"
		}
		if err := o.exec.CollectionAdd(ctx, col.Path, col.Name, mask); err != nil {
			o.log.Error("failed to add collection", "name", col.Name, "err", err)
			continue
		}

		if col.Context != "" {
			if err := o.exec.ContextAdd(ctx, col.Path, col.Context); err != nil {
				o.log.Warn("failed to add context", "name", col.Name, "err", err)
			}
		}
	}

	o.syncCollectionContexts(ctx)
	return nil
}

func (o *Orchestrator) syncCollectionContexts(ctx context.Context) {
	existingContexts, err := o.exec.ContextList(ctx)
	if err != nil {
		o.log.Warn("failed to list contexts", "err", err)
		return
	}

	contextMap := make(map[string]string, len(existingContexts))
	for _, c := range existingContexts {
		contextMap[c.Path] = c.Description
	}

	for _, col := range o.cfg.Collections {
		if strings.TrimSpace(col.Context) == "" {
			continue
		}
		existingDesc, ok := contextMap[col.Path]
		if ok && existingDesc == col.Context {
			continue
		}

		if ok {
			o.log.Info("updating context", "path", col.Path)
			if err := o.exec.ContextRemove(ctx, col.Path); err != nil {
				o.log.Warn("failed to remove stale context", "path", col.Path, "err", err)
				continue
			}
		}

		if err := o.exec.ContextAdd(ctx, col.Path, col.Context); err != nil {
			o.log.Warn("failed to add/update context", "name", col.Name, "err", err)
			continue
		}
		contextMap[col.Path] = col.Context
	}
}

type SearchParams struct {
	Query                 string
	Mode                  string
	Collection            string
	N                     int
	MinScore              float64
	Fallback              bool
	FilesOnly             bool
	FilesAll              bool
	DisableDeepEscalation bool
	Confirm               bool
}

type SearchResult struct {
	Results []model.SearchResult
	Meta    model.SearchMeta
}

func (o *Orchestrator) Search(ctx context.Context, params SearchParams) (*SearchResult, error) {
	start := time.Now()

	if params.N <= 0 {
		params.N = o.cfg.Search.TopK
	}
	if params.MinScore <= 0 {
		params.MinScore = o.cfg.Search.MinScore
	}

	cacheKey := cache.MakeCacheKey(params.Query, params.Mode, params.Collection, params.MinScore, params.N, params.Fallback, params.FilesOnly, params.FilesAll)
	if o.cache != nil {
		if entry, ok := o.cache.Get(cacheKey); ok {
			collections := []string{entry.Collection}
			if strings.Contains(entry.Collection, ",") {
				parts := strings.Split(entry.Collection, ",")
				collections = collections[:0]
				for _, p := range parts {
					p = strings.TrimSpace(p)
					if p != "" {
						collections = append(collections, p)
					}
				}
			}
			return &SearchResult{
				Results: entry.Results,
				Meta: model.SearchMeta{
					ModeUsed:            entry.Mode,
					CollectionsSearched: collections,
					FallbackTriggered:   entry.FallbackTriggered,
					CacheHit:            true,
					Degraded:            entry.Degraded,
					DegradeReason:       entry.DegradeReason,
					LatencyMs:           time.Since(start).Milliseconds(),
				},
			}, nil
		}
	}

	mode := o.resolveMode(params.Mode, params.Query)

	if params.Collection != "" {
		if mode == router.ModeQuery {
			return o.searchSingleCollectionWithDeepFallback(ctx, params, cacheKey, start)
		}
		return o.searchSingleCollection(ctx, params, mode, cacheKey, start)
	}

	if mode == router.ModeQuery {
		return o.searchWithDeepFallback(ctx, params, cacheKey, start)
	}

	return o.searchWithFallback(ctx, params, mode, cacheKey, start)
}

func (o *Orchestrator) resolveMode(requested string, query string) router.Mode {
	isAuto := requested == "" || requested == "auto"
	var mode router.Mode
	if !isAuto {
		mode = router.Mode(requested)
	} else {
		mode = router.DetectMode(query, o.exec.HasCapability("vector"), o.exec.HasCapability("deep_query"))
	}

	if o.IsOverloaded() && mode != router.ModeSearch {
		o.log.Warn("cpu overload protection forcing search mode", "requested_mode", mode)
		return router.ModeSearch
	}

	switch mode {
	case router.ModeQuery:
		if !o.exec.HasCapability("deep_query") {
			o.log.Debug("query mode unavailable, fallback to search")
			return router.ModeSearch
		}
		if isAuto && !o.allowAutoDeepQuery(query) {
			o.log.Debug("auto query downgraded to search in smart_routing mode")
			return router.ModeSearch
		}
	case router.ModeVSearch:
		if !o.exec.HasCapability("vector") {
			o.log.Debug("vsearch mode unavailable, fallback to search")
			return router.ModeSearch
		}
	}

	return mode
}

func (o *Orchestrator) allowAutoDeepQuery(query string) bool {
	if !(o.cfg.Runtime.LowResourceMode && o.cfg.Runtime.AllowCPUDeepQuery && o.cfg.Runtime.SmartRouting) {
		return true
	}

	q := strings.TrimSpace(query)
	if q == "" {
		return false
	}

	chars := runeLen(q)
	words := textutil.CountWordsMaxFieldsOrCJK(q)
	abstractCues := countAbstractCues(q)

	if chars < o.cfg.Runtime.CPUDeepMinChars {
		return false
	}
	if o.cfg.Runtime.CPUDeepMaxChars > 0 && chars > o.cfg.Runtime.CPUDeepMaxChars {
		return false
	}
	if o.cfg.Runtime.CPUDeepMaxWords > 0 && words > o.cfg.Runtime.CPUDeepMaxWords {
		return false
	}
	if o.cfg.Runtime.CPUDeepMaxAbstractCues > 0 && abstractCues > o.cfg.Runtime.CPUDeepMaxAbstractCues {
		return false
	}
	// Guard against OOM-prone abstract long-form prompts on low-resource hosts.
	if words >= 20 && abstractCues > 0 {
		return false
	}

	if words >= o.cfg.Runtime.CPUDeepMinWords {
		return true
	}

	if hasQuestionCue(q) {
		return words >= 4 || textutil.CountCJK(q) >= 6
	}

	return false
}

// AllowDeepQuery reports whether the current runtime budget allows executing deep query.
// In low-resource mode this enforces smart routing budgets; otherwise it always allows.
func (o *Orchestrator) AllowDeepQuery(query string) bool {
	return o.allowAutoDeepQuery(query)
}

func runeLen(s string) int {
	return len([]rune(s))
}

func hasQuestionCue(s string) bool {
	lower := strings.ToLower(s)
	cues := []string{
		"如何", "怎么", "怎样", "什么", "为什么", "为何", "是否", "能不能", "可以", "应该",
		"?", "？", "how ", "what ", "why ", "when ", "where ", "which ", "should ",
	}
	for _, cue := range cues {
		if strings.Contains(lower, cue) {
			return true
		}
	}
	return false
}

func countAbstractCues(s string) int {
	lower := strings.ToLower(s)
	cues := []string{
		"方案", "架构", "规划", "体系", "框架", "设计", "tradeoff", "strategy",
		"architecture", "design", "plan", "migration", "roadmap",
	}
	count := 0
	for _, cue := range cues {
		if strings.Contains(lower, cue) {
			count++
		}
	}
	return count
}

func (o *Orchestrator) searchSingleCollection(ctx context.Context, params SearchParams, mode router.Mode, cacheKey string, start time.Time) (*SearchResult, error) {
	colCfg := o.findCollection(params.Collection)
	if colCfg == nil {
		return nil, fmt.Errorf("collection %q not found", params.Collection)
	}

	if colCfg.RequireExplicit && colCfg.SafetyPrompt && !params.Confirm {
		return nil, fmt.Errorf("collection %q requires confirm=true", params.Collection)
	}

	results, err := o.execSearch(ctx, mode, params.Query, params.Collection, params)
	if err != nil {
		return nil, err
	}

	results = o.filterExclude(results, colCfg)
	results = o.filterMinScore(results, params.MinScore)
	results = o.finalizeResults(results, params.N, params.FilesOnly, params.FilesAll)

	return o.cacheAndBuildSearchResult(cacheKey, results, mode, []string{params.Collection}, false, false, "", start), nil
}

func (o *Orchestrator) searchSingleCollectionWithDeepFallback(ctx context.Context, params SearchParams, cacheKey string, start time.Time) (*SearchResult, error) {
	colCfg := o.findCollection(params.Collection)
	if colCfg == nil {
		return nil, fmt.Errorf("collection %q not found", params.Collection)
	}

	if colCfg.RequireExplicit && colCfg.SafetyPrompt && !params.Confirm {
		return nil, fmt.Errorf("collection %q requires confirm=true", params.Collection)
	}

	if ok, reason := o.shouldSkipDeepByNegativeCache(params.Query, params.Collection); ok {
		broadResults, err := o.execSearch(ctx, router.ModeSearch, params.Query, params.Collection, params)
		if err != nil {
			o.log.Warn("broad fallback search failed before deep", "collection", params.Collection, "err", err)
			broadResults = nil
		}
		broadResults = o.filterExclude(broadResults, colCfg)
		broadResults = o.filterMinScore(broadResults, params.MinScore)
		broadResults = o.finalizeResults(broadResults, params.N, params.FilesOnly, params.FilesAll)
		return o.cacheAndBuildSearchResult(cacheKey, broadResults, router.ModeSearch, []string{params.Collection}, false, true, reason, start), nil
	}

	type resultPayload struct {
		results []model.SearchResult
	}
	type deepPayload struct {
		results []model.SearchResult
		err     error
	}

	broadCh := make(chan resultPayload, 1)
	deepCh := make(chan deepPayload, 1)

	go func() {
		broadResults, err := o.execSearch(ctx, router.ModeSearch, params.Query, params.Collection, params)
		if err != nil {
			o.log.Warn("broad fallback search failed before deep", "collection", params.Collection, "err", err)
			broadResults = nil
		}
		broadResults = o.filterExclude(broadResults, colCfg)
		broadResults = o.filterMinScore(broadResults, params.MinScore)
		broadResults = o.finalizeResults(broadResults, params.N, params.FilesOnly, params.FilesAll)
		broadCh <- resultPayload{results: broadResults}
	}()

	go func() {
		deepCtx, cancel := context.WithTimeout(ctx, o.deepFailTimeout())
		defer cancel()
		deepResults, deepErr := o.execSearch(deepCtx, router.ModeQuery, params.Query, params.Collection, params)
		if deepErr != nil {
			deepCh <- deepPayload{err: deepErr}
			return
		}
		deepResults = o.filterExclude(deepResults, colCfg)
		deepResults = o.filterMinScore(deepResults, params.MinScore)
		deepResults = o.finalizeResults(deepResults, params.N, params.FilesOnly, params.FilesAll)
		deepCh <- deepPayload{results: deepResults}
	}()

	broad := <-broadCh
	deep := <-deepCh

	if deep.err != nil {
		o.markDeepNegative(params.Query, params.Collection)
		return o.cacheAndBuildSearchResult(cacheKey, broad.results, router.ModeSearch, []string{params.Collection}, false, true, "deep_failed_fallback_broad", start), nil
	}

	if len(deep.results) == 0 {
		return o.cacheAndBuildSearchResult(cacheKey, broad.results, router.ModeSearch, []string{params.Collection}, false, true, "deep_empty_fallback_broad", start), nil
	}

	return o.cacheAndBuildSearchResult(cacheKey, deep.results, router.ModeQuery, []string{params.Collection}, false, false, "", start), nil
}

func (o *Orchestrator) searchWithDeepFallback(ctx context.Context, params SearchParams, cacheKey string, start time.Time) (*SearchResult, error) {
	if ok, reason := o.shouldSkipDeepByNegativeCache(params.Query, "all"); ok {
		broadResults, broadSearched, broadFallback := o.searchBroadAll(ctx, params)
		return o.cacheAndBuildSearchResult(cacheKey, broadResults, router.ModeSearch, broadSearched, broadFallback, true, reason, start), nil
	}

	type broadPayload struct {
		results  []model.SearchResult
		searched []string
		fallback bool
	}
	type deepPayload struct {
		results  []model.SearchResult
		searched []string
		err      error
	}

	broadCh := make(chan broadPayload, 1)
	deepCh := make(chan deepPayload, 1)

	go func() {
		broadResults, broadSearched, broadFallback := o.searchBroadAll(ctx, params)
		broadCh <- broadPayload{
			results:  broadResults,
			searched: broadSearched,
			fallback: broadFallback,
		}
	}()

	go func() {
		deepCtx, cancel := context.WithTimeout(ctx, o.deepFailTimeout())
		defer cancel()
		deepResults, deepSearched, deepErr := o.searchDeepTier1(deepCtx, params)
		deepCh <- deepPayload{
			results:  deepResults,
			searched: deepSearched,
			err:      deepErr,
		}
	}()

	broad := <-broadCh
	deep := <-deepCh

	if deep.err != nil {
		o.markDeepNegative(params.Query, "all")
		return o.cacheAndBuildSearchResult(cacheKey, broad.results, router.ModeSearch, broad.searched, broad.fallback, true, "deep_failed_fallback_broad", start), nil
	}

	deepResults := o.finalizeResults(o.filterMinScore(deep.results, params.MinScore), params.N, params.FilesOnly, params.FilesAll)
	if len(deepResults) == 0 {
		return o.cacheAndBuildSearchResult(cacheKey, broad.results, router.ModeSearch, broad.searched, broad.fallback, true, "deep_empty_fallback_broad", start), nil
	}

	return o.cacheAndBuildSearchResult(cacheKey, deepResults, router.ModeQuery, deep.searched, false, false, "", start), nil
}

func (o *Orchestrator) searchWithFallback(ctx context.Context, params SearchParams, mode router.Mode, cacheKey string, start time.Time) (*SearchResult, error) {
	filtered, searched, fallbackTriggered := o.searchPrimaryWithTierFallback(ctx, params, mode, "search failed")
	degraded := false
	degradeReason := ""

	if !params.DisableDeepEscalation && mode == router.ModeSearch && len(filtered) == 0 && o.exec.HasCapability("deep_query") && !o.IsOverloaded() {
		if o.allowAutoDeepQuery(params.Query) {
			if ok, reason := o.shouldSkipDeepByNegativeCache(params.Query, "all"); ok {
				degraded = true
				degradeReason = reason
			} else {
				o.log.Info("BM25 returned no results, escalating to query mode")
				deepCtx, cancel := context.WithTimeout(ctx, o.deepFailTimeout())
				deepResults, deepSearched, err := o.searchDeepTier1(deepCtx, params)
				cancel()
				if err != nil {
					o.markDeepNegative(params.Query, "all")
					degraded = true
					degradeReason = "deep_failed_fallback_broad"
				} else {
					deepResults = o.filterMinScore(deepResults, params.MinScore)
					if len(deepResults) > 0 {
						mode = router.ModeQuery
						filtered = deepResults
						searched = deepSearched
					} else {
						degraded = true
						degradeReason = "deep_empty_fallback_broad"
					}
				}
			}
		}
	}

	filtered = o.finalizeResults(filtered, params.N, params.FilesOnly, params.FilesAll)

	o.cacheResults(cacheKey, filtered, string(mode), strings.Join(searched, ","), fallbackTriggered, degraded, degradeReason)

	res := &SearchResult{
		Results: filtered,
		Meta: model.SearchMeta{
			ModeUsed:            string(mode),
			CollectionsSearched: searched,
			FallbackTriggered:   fallbackTriggered,
			Degraded:            degraded,
			DegradeReason:       degradeReason,
			LatencyMs:           time.Since(start).Milliseconds(),
		},
	}
	o.observeSearchSample(res.Meta.LatencyMs, len(filtered), degraded)
	return res, nil
}

func (o *Orchestrator) searchTierParallel(ctx context.Context, cols []config.CollectionCfg, mode router.Mode, params SearchParams, logMsg string) ([]model.SearchResult, []string, error) {
	var mu sync.Mutex
	var allResults []model.SearchResult
	var searched []string
	var firstErr error
	var wg sync.WaitGroup

	for _, col := range cols {
		wg.Add(1)
		go func(c config.CollectionCfg) {
			defer wg.Done()
			results, err := o.execSearch(ctx, mode, params.Query, c.Name, params)
			if err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
				o.log.Warn(logMsg, "collection", c.Name, "err", err)
				return
			}
			results = o.filterExclude(results, &c)
			mu.Lock()
			allResults = append(allResults, results...)
			searched = append(searched, c.Name)
			mu.Unlock()
		}(col)
	}
	wg.Wait()
	return allResults, searched, firstErr
}

func (o *Orchestrator) searchBroadAll(ctx context.Context, params SearchParams) ([]model.SearchResult, []string, bool) {
	filtered, searched, fallbackTriggered := o.searchPrimaryWithTierFallback(ctx, params, router.ModeSearch, "broad search failed")
	filtered = o.finalizeResults(filtered, params.N, params.FilesOnly, params.FilesAll)
	return filtered, searched, fallbackTriggered
}

func (o *Orchestrator) searchDeepTier1(ctx context.Context, params SearchParams) ([]model.SearchResult, []string, error) {
	tier1 := o.collectionsByTier(1)
	if len(tier1) == 0 {
		return nil, nil, fmt.Errorf("no tier-1 collection configured")
	}
	allResults, searched, firstErr := o.searchTierParallel(ctx, tier1, router.ModeQuery, params, "deep search failed")
	if len(allResults) == 0 && firstErr != nil {
		return nil, searched, firstErr
	}
	return allResults, searched, nil
}

func (o *Orchestrator) execSearch(ctx context.Context, mode router.Mode, query, collection string, params SearchParams) ([]model.SearchResult, error) {
	token, err := o.acquireOverloadSearchToken(ctx)
	if err != nil {
		return nil, err
	}
	defer o.releaseOverloadSearchToken(token)

	coarseK := o.effectiveCoarseK()
	opts := executor.SearchOpts{
		Collection: collection,
		N:          coarseK,
		MinScore:   params.MinScore,
		FilesOnly:  params.FilesOnly,
		All:        params.FilesOnly && params.FilesAll,
	}
	if opts.FilesOnly && opts.All {
		// Let qmd return all file paths; capped later by files_all_max_hits.
		opts.N = 0
	}

	switch mode {
	case router.ModeVSearch:
		return o.exec.VSearch(ctx, query, opts)
	case router.ModeQuery:
		return o.exec.Query(ctx, query, opts)
	default:
		return o.exec.Search(ctx, query, opts)
	}
}

func (o *Orchestrator) acquireOverloadSearchToken(ctx context.Context) (chan struct{}, error) {
	if !o.IsOverloaded() || o.searchTokens == nil {
		return nil, nil
	}

	select {
	case o.searchTokens <- struct{}{}:
		return o.searchTokens, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("overload search queue busy: %w", ctx.Err())
	}
}

func (o *Orchestrator) releaseOverloadSearchToken(ch chan struct{}) {
	if ch == nil {
		return
	}
	select {
	case <-ch:
	default:
	}
}

func (o *Orchestrator) findCollection(name string) *config.CollectionCfg {
	for i := range o.cfg.Collections {
		if o.cfg.Collections[i].Name == name {
			return &o.cfg.Collections[i]
		}
	}
	return nil
}

func (o *Orchestrator) collectionsByTier(tier int) []config.CollectionCfg {
	var result []config.CollectionCfg
	for _, col := range o.cfg.Collections {
		if col.Tier == tier && !col.RequireExplicit {
			result = append(result, col)
		}
	}
	return result
}

func (o *Orchestrator) filterExclude(results []model.SearchResult, col *config.CollectionCfg) []model.SearchResult {
	if len(col.Exclude) == 0 {
		return results
	}

	filtered := make([]model.SearchResult, 0, len(results))
	for _, r := range results {
		excluded := false
		relPath := r.File
		if strings.HasPrefix(relPath, col.Path) {
			relPath = strings.TrimPrefix(relPath, col.Path)
			relPath = strings.TrimPrefix(relPath, "/")
		}
		relPath = filepath.Clean(relPath)

		for _, pattern := range col.Exclude {
			matched, err := filepath.Match(pattern, relPath)
			if err == nil && matched {
				excluded = true
				break
			}
			dir := filepath.Dir(relPath)
			for dir != "." && dir != "/" {
				testPath := dir + "/"
				if matched, err := filepath.Match(pattern, testPath); err == nil && matched {
					excluded = true
					break
				}
				dir = filepath.Dir(dir)
			}
			if excluded {
				break
			}
			if strings.HasSuffix(pattern, "/**") {
				prefix := strings.TrimSuffix(pattern, "/**")
				if strings.HasPrefix(relPath, prefix+"/") || relPath == prefix {
					excluded = true
					break
				}
			}
		}
		if !excluded {
			filtered = append(filtered, r)
		}
	}
	return filtered
}

func (o *Orchestrator) filterMinScore(results []model.SearchResult, minScore float64) []model.SearchResult {
	if minScore <= 0 {
		return results
	}
	filtered := make([]model.SearchResult, 0, len(results))
	for _, r := range results {
		if r.Score >= minScore {
			filtered = append(filtered, r)
		}
	}
	return filtered
}

func (o *Orchestrator) cacheResults(key string, results []model.SearchResult, mode, collection string, fallbackTriggered bool, degraded bool, degradeReason string) {
	if o.cache == nil {
		return
	}
	o.cache.Put(key, cache.Entry{
		Results:           results,
		Query:             key,
		Mode:              mode,
		Collection:        collection,
		FallbackTriggered: fallbackTriggered,
		Degraded:          degraded,
		DegradeReason:     degradeReason,
	})
}

func (o *Orchestrator) finalizeResults(results []model.SearchResult, n int, filesOnly bool, filesAll bool) []model.SearchResult {
	if filesOnly {
		results = searchutil.DedupSortLimit(results, n)
		if filesAll {
			return o.enforceFilesAllMaxHits(results)
		}
		return results
	}
	results = o.cleanResultSnippets(results)
	results = searchutil.DedupSortLimit(results, n)
	return o.enforceMaxChars(results)
}

func (o *Orchestrator) enforceFilesAllMaxHits(results []model.SearchResult) []model.SearchResult {
	limit := o.cfg.Search.FilesAllMaxHits
	if limit <= 0 || len(results) <= limit {
		return results
	}
	o.log.Warn("files_all result capped by files_all_max_hits", "total_hits", len(results), "max_hits", limit)
	return results[:limit]
}

func (o *Orchestrator) cleanResultSnippets(results []model.SearchResult) []model.SearchResult {
	for i := range results {
		results[i].Snippet = textutil.CleanSnippet(results[i].Snippet, maxSnippetCharsPerResult)
	}
	return results
}

func (o *Orchestrator) enforceMaxChars(results []model.SearchResult) []model.SearchResult {
	maxChars := o.cfg.Search.MaxChars
	if maxChars <= 0 {
		return results
	}

	total := 0
	for i := range results {
		snippetChars := utf8.RuneCountInString(results[i].Snippet)
		nextTotal := total + snippetChars
		if nextTotal <= maxChars {
			total = nextTotal
			continue
		}

		remain := maxChars - total
		if remain <= 0 {
			return results[:i]
		}

		results[i].Snippet = truncateWithEllipsis(results[i].Snippet, remain)
		return results[:i+1]
	}
	return results
}

func truncateWithEllipsis(s string, maxChars int) string {
	if maxChars <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= maxChars {
		return s
	}
	if maxChars <= 3 {
		rs := []rune(s)
		return string(rs[:maxChars])
	}
	rs := []rune(s)
	return string(rs[:maxChars-3]) + "..."
}

func (o *Orchestrator) deepFailTimeout() time.Duration {
	if o.cfg.Runtime.DeepFailTimeout > 0 {
		return o.cfg.Runtime.DeepFailTimeout
	}
	if o.cfg.Runtime.QueryTimeout > 0 {
		return o.cfg.Runtime.QueryTimeout
	}
	return 12 * time.Second
}

func (o *Orchestrator) shouldSkipDeepByNegativeCache(query, scope string) (bool, string) {
	ttl := o.cfg.Runtime.DeepNegativeTTL
	if ttl <= 0 {
		return false, ""
	}

	now := time.Now()
	exactKey := o.deepNegativeExactKey(query, scope)
	scopeCooldownKey := o.deepNegativeScopeCooldownKey(scope)

	o.deepNegMu.Lock()
	defer o.deepNegMu.Unlock()

	if expiry, ok := o.deepNeg[exactKey]; ok {
		if now.After(expiry) {
			delete(o.deepNeg, exactKey)
		} else {
			o.deepNegExactHitCnt++
			return true, "deep_negative_cached_fallback_broad"
		}
	}

	// Scope cooldown is only meaningful when deep query is enabled.
	if !o.cfg.Runtime.AllowCPUDeepQuery {
		return false, ""
	}

	if expiry, ok := o.deepNeg[scopeCooldownKey]; ok {
		if now.After(expiry) {
			delete(o.deepNeg, scopeCooldownKey)
		} else {
			o.deepNegScopeHitCnt++
			return true, "deep_negative_scope_cooldown"
		}
	}
	return false, ""
}

func (o *Orchestrator) markDeepNegative(query, scope string) {
	ttl := o.cfg.Runtime.DeepNegativeTTL
	if ttl <= 0 {
		return
	}

	now := time.Now()
	exactKey := o.deepNegativeExactKey(query, scope)
	exactExpiry := now.Add(ttl)

	o.deepNegMu.Lock()
	o.deepNeg[exactKey] = exactExpiry
	if o.cfg.Runtime.AllowCPUDeepQuery {
		o.markScopeCooldownLocked(scope, now)
	}
	o.deepNegMarkCount++
	o.deepNegMu.Unlock()
}

func (o *Orchestrator) markScopeCooldownLocked(scope string, now time.Time) {
	scope = strings.TrimSpace(scope)
	if scope == "" {
		scope = "all"
	}
	cooldown := o.cfg.Runtime.DeepNegativeScopeCooldown
	if cooldown <= 0 {
		return
	}

	fails := o.deepNegScopeFails[scope]
	dst := fails[:0]
	for _, ts := range fails {
		if now.Sub(ts) <= deepNegativeScopeFailWindow {
			dst = append(dst, ts)
		}
	}
	dst = append(dst, now)
	o.deepNegScopeFails[scope] = dst

	if len(dst) < deepNegativeScopeFailThreshold {
		return
	}

	key := o.deepNegativeScopeCooldownKey(scope)
	o.deepNeg[key] = now.Add(cooldown)
	delete(o.deepNegScopeFails, scope)
	o.log.Warn("deep negative scope cooldown activated", "scope", scope, "cooldown", cooldown)
}

func (o *Orchestrator) deepNegativeExactKey(query, scope string) string {
	q := strings.ToLower(strings.TrimSpace(query))
	q = strings.Join(strings.Fields(q), " ")
	runes := []rune(q)
	if len(runes) > 64 {
		q = string(runes[:64])
	}
	sum := sha1.Sum([]byte(scope + "|" + q))
	return hex.EncodeToString(sum[:])
}

func (o *Orchestrator) deepNegativeScopeCooldownKey(scope string) string {
	scope = strings.TrimSpace(scope)
	if scope == "" {
		scope = "all"
	}
	sum := sha1.Sum([]byte("scope_cooldown|" + scope))
	return hex.EncodeToString(sum[:])
}

func (o *Orchestrator) CleanupDeepNegativeCache() int {
	o.deepNegMu.Lock()
	defer o.deepNegMu.Unlock()

	now := time.Now()
	removed := 0
	for k, expiry := range o.deepNeg {
		if now.After(expiry) {
			delete(o.deepNeg, k)
			removed++
		}
	}

	for scope, failures := range o.deepNegScopeFails {
		dst := failures[:0]
		for _, ts := range failures {
			if now.Sub(ts) <= deepNegativeScopeFailWindow {
				dst = append(dst, ts)
			}
		}
		if len(dst) == 0 {
			delete(o.deepNegScopeFails, scope)
			continue
		}
		o.deepNegScopeFails[scope] = dst
	}

	return removed
}

func (o *Orchestrator) searchPrimaryWithTierFallback(ctx context.Context, params SearchParams, mode router.Mode, logMsg string) ([]model.SearchResult, []string, bool) {
	tier1 := o.collectionsByTier(1)
	allResults, searched, _ := o.searchTierParallel(ctx, tier1, mode, params, logMsg)

	filtered := o.filterMinScore(allResults, params.MinScore)
	fallbackTriggered := false

	if len(filtered) == 0 && params.Fallback && o.cfg.Search.FallbackEnabled {
		tier2 := o.collectionsByTier(2)
		if len(tier2) > 0 {
			fallbackTriggered = true
			t2Results, t2Searched, _ := o.searchTierParallel(ctx, tier2, mode, params, "parallel search failed")
			filtered = o.filterMinScore(t2Results, params.MinScore)
			searched = append(searched, t2Searched...)
		}
	}

	return filtered, searched, fallbackTriggered
}

func (o *Orchestrator) cacheAndBuildSearchResult(cacheKey string, results []model.SearchResult, mode router.Mode, searched []string, fallbackTriggered bool, degraded bool, degradeReason string, start time.Time) *SearchResult {
	o.cacheResults(cacheKey, results, string(mode), strings.Join(searched, ","), fallbackTriggered, degraded, degradeReason)
	res := &SearchResult{
		Results: results,
		Meta: model.SearchMeta{
			ModeUsed:            string(mode),
			CollectionsSearched: searched,
			FallbackTriggered:   fallbackTriggered,
			Degraded:            degraded,
			DegradeReason:       degradeReason,
			LatencyMs:           time.Since(start).Milliseconds(),
		},
	}
	o.observeSearchSample(res.Meta.LatencyMs, len(results), degraded)
	return res
}

func (o *Orchestrator) effectiveCoarseK() int {
	k := o.cfg.Search.CoarseK
	if k <= 0 {
		return 20
	}
	return k
}

func (o *Orchestrator) observeSearchSample(latencyMs int64, hits int, degraded bool) {
	now := time.Now()
	total := atomic.AddInt64(&o.obsCount, 1)
	atomic.AddInt64(&o.obsLatencyMSum, latencyMs)
	atomic.AddInt64(&o.obsHitsSum, int64(hits))
	switch {
	case hits == 0:
		atomic.AddInt64(&o.obsHitZero, 1)
	case hits <= 3:
		atomic.AddInt64(&o.obsHitLow, 1)
	case hits <= 8:
		atomic.AddInt64(&o.obsHitMid, 1)
	default:
		atomic.AddInt64(&o.obsHitHigh, 1)
	}
	if degraded {
		atomic.AddInt64(&o.obsDegraded, 1)
	}
	shouldLog := total%50 == 0
	o.obsMu.Lock()
	if !shouldLog && now.Sub(o.obsLastLogAt) >= 30*time.Minute {
		shouldLog = true
	}
	if shouldLog {
		o.obsLastLogAt = now
	}
	o.obsMu.Unlock()

	if !shouldLog {
		return
	}

	latencySum := atomic.LoadInt64(&o.obsLatencyMSum)
	hitsSum := atomic.LoadInt64(&o.obsHitsSum)
	avgLatency := float64(latencySum) / float64(total)
	avgHits := float64(hitsSum) / float64(total)
	hitZero := atomic.LoadInt64(&o.obsHitZero)
	hitLow := atomic.LoadInt64(&o.obsHitLow)
	hitMid := atomic.LoadInt64(&o.obsHitMid)
	hitHigh := atomic.LoadInt64(&o.obsHitHigh)
	degradedCnt := atomic.LoadInt64(&o.obsDegraded)

	o.deepNegMu.Lock()
	deepNegMarks := o.deepNegMarkCount
	deepNegExactHits := o.deepNegExactHitCnt
	deepNegScopeHits := o.deepNegScopeHitCnt
	o.deepNegMu.Unlock()

	o.log.Info("search_observation",
		"samples", total,
		"avg_latency_ms", fmt.Sprintf("%.2f", avgLatency),
		"avg_hits", fmt.Sprintf("%.2f", avgHits),
		"hit_zero", hitZero,
		"hit_1_3", hitLow,
		"hit_4_8", hitMid,
		"hit_9_plus", hitHigh,
		"degraded_count", degradedCnt,
		"deep_negative_mark_count", deepNegMarks,
		"deep_negative_exact_hit_count", deepNegExactHits,
		"deep_negative_scope_hit_count", deepNegScopeHits,
	)
}
