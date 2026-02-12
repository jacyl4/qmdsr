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
	"time"

	"qmdsr/cache"
	"qmdsr/config"
	"qmdsr/executor"
	"qmdsr/internal/searchutil"
	"qmdsr/internal/textutil"
	"qmdsr/model"
	"qmdsr/router"
)

type Orchestrator struct {
	cfg       *config.Config
	exec      executor.Executor
	cache     *cache.Cache
	log       *slog.Logger
	deepNegMu sync.Mutex
	deepNeg   map[string]time.Time
}

func New(cfg *config.Config, exec executor.Executor, c *cache.Cache, logger *slog.Logger) *Orchestrator {
	return &Orchestrator{
		cfg:     cfg,
		exec:    exec,
		cache:   c,
		log:     logger,
		deepNeg: make(map[string]time.Time),
	}
}

func (o *Orchestrator) ClearCache() {
	o.cache.Clear()
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
	return nil
}

type SearchParams struct {
	Query                 string
	Mode                  string
	Collection            string
	N                     int
	MinScore              float64
	Fallback              bool
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

	cacheKey := cache.MakeCacheKey(params.Query, params.Mode, params.Collection, params.MinScore, params.N, params.Fallback)
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

	broadResults, err := o.execSearch(ctx, router.ModeSearch, params.Query, params.Collection, params)
	if err != nil {
		o.log.Warn("broad fallback search failed before deep", "collection", params.Collection, "err", err)
		broadResults = nil
	}
	broadResults = o.filterExclude(broadResults, colCfg)
	broadResults = o.filterMinScore(broadResults, params.MinScore)
	broadResults = o.finalizeResults(broadResults, params.N)

	if ok, reason := o.shouldSkipDeepByNegativeCache(params.Query, params.Collection); ok {
		return o.cacheAndBuildSearchResult(cacheKey, broadResults, router.ModeSearch, []string{params.Collection}, false, true, reason, start), nil
	}

	deepCtx, cancel := context.WithTimeout(ctx, o.deepFailTimeout())
	defer cancel()
	deepResults, deepErr := o.execSearch(deepCtx, router.ModeQuery, params.Query, params.Collection, params)
	if deepErr != nil {
		o.markDeepNegative(params.Query, params.Collection)
		return o.cacheAndBuildSearchResult(cacheKey, broadResults, router.ModeSearch, []string{params.Collection}, false, true, "deep_failed_fallback_broad", start), nil
	}

	deepResults = o.filterExclude(deepResults, colCfg)
	deepResults = o.filterMinScore(deepResults, params.MinScore)
	deepResults = o.finalizeResults(deepResults, params.N)

	if len(deepResults) == 0 {
		return o.cacheAndBuildSearchResult(cacheKey, broadResults, router.ModeSearch, []string{params.Collection}, false, true, "deep_empty_fallback_broad", start), nil
	}

	return o.cacheAndBuildSearchResult(cacheKey, deepResults, router.ModeQuery, []string{params.Collection}, false, false, "", start), nil
}

func (o *Orchestrator) searchWithDeepFallback(ctx context.Context, params SearchParams, cacheKey string, start time.Time) (*SearchResult, error) {
	broadResults, broadSearched, broadFallback := o.searchBroadAll(ctx, params)

	if ok, reason := o.shouldSkipDeepByNegativeCache(params.Query, "all"); ok {
		return o.cacheAndBuildSearchResult(cacheKey, broadResults, router.ModeSearch, broadSearched, broadFallback, true, reason, start), nil
	}

	deepCtx, cancel := context.WithTimeout(ctx, o.deepFailTimeout())
	defer cancel()
	deepResults, deepSearched, deepErr := o.searchDeepTier1(deepCtx, params)
	if deepErr != nil {
		o.markDeepNegative(params.Query, "all")
		return o.cacheAndBuildSearchResult(cacheKey, broadResults, router.ModeSearch, broadSearched, broadFallback, true, "deep_failed_fallback_broad", start), nil
	}

	deepResults = o.finalizeResults(o.filterMinScore(deepResults, params.MinScore), params.N)
	if len(deepResults) == 0 {
		return o.cacheAndBuildSearchResult(cacheKey, broadResults, router.ModeSearch, broadSearched, broadFallback, true, "deep_empty_fallback_broad", start), nil
	}

	return o.cacheAndBuildSearchResult(cacheKey, deepResults, router.ModeQuery, deepSearched, false, false, "", start), nil
}

func (o *Orchestrator) searchWithFallback(ctx context.Context, params SearchParams, mode router.Mode, cacheKey string, start time.Time) (*SearchResult, error) {
	filtered, searched, fallbackTriggered := o.searchPrimaryWithTierFallback(ctx, params, mode, "search failed")
	degraded := false
	degradeReason := ""

	if !params.DisableDeepEscalation && mode == router.ModeSearch && len(filtered) == 0 && o.exec.HasCapability("deep_query") {
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

	filtered = o.finalizeResults(filtered, params.N)

	o.cacheResults(cacheKey, filtered, string(mode), strings.Join(searched, ","), fallbackTriggered, degraded, degradeReason)

	return &SearchResult{
		Results: filtered,
		Meta: model.SearchMeta{
			ModeUsed:            string(mode),
			CollectionsSearched: searched,
			FallbackTriggered:   fallbackTriggered,
			Degraded:            degraded,
			DegradeReason:       degradeReason,
			LatencyMs:           time.Since(start).Milliseconds(),
		},
	}, nil
}

func (o *Orchestrator) searchTierParallel(ctx context.Context, cols []config.CollectionCfg, mode router.Mode, params SearchParams) []model.SearchResult {
	var mu sync.Mutex
	var allResults []model.SearchResult
	var wg sync.WaitGroup

	for _, col := range cols {
		wg.Add(1)
		go func(c config.CollectionCfg) {
			defer wg.Done()
			results, err := o.execSearch(ctx, mode, params.Query, c.Name, params)
			if err != nil {
				o.log.Warn("parallel search failed", "collection", c.Name, "err", err)
				return
			}
			results = o.filterExclude(results, &c)
			mu.Lock()
			allResults = append(allResults, results...)
			mu.Unlock()
		}(col)
	}
	wg.Wait()
	return allResults
}

func (o *Orchestrator) searchBroadAll(ctx context.Context, params SearchParams) ([]model.SearchResult, []string, bool) {
	filtered, searched, fallbackTriggered := o.searchPrimaryWithTierFallback(ctx, params, router.ModeSearch, "broad search failed")
	filtered = o.finalizeResults(filtered, params.N)
	return filtered, searched, fallbackTriggered
}

func (o *Orchestrator) searchDeepTier1(ctx context.Context, params SearchParams) ([]model.SearchResult, []string, error) {
	tier1 := o.collectionsByTier(1)
	if len(tier1) == 0 {
		return nil, nil, fmt.Errorf("no tier-1 collection configured")
	}

	var allResults []model.SearchResult
	var searched []string
	var firstErr error

	for _, col := range tier1 {
		results, err := o.execSearch(ctx, router.ModeQuery, params.Query, col.Name, params)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			o.log.Warn("deep search failed", "collection", col.Name, "err", err)
			continue
		}
		results = o.filterExclude(results, &col)
		allResults = append(allResults, results...)
		searched = append(searched, col.Name)
	}

	if len(allResults) == 0 && firstErr != nil {
		return nil, searched, firstErr
	}
	return allResults, searched, nil
}

func (o *Orchestrator) execSearch(ctx context.Context, mode router.Mode, query, collection string, params SearchParams) ([]model.SearchResult, error) {
	opts := executor.SearchOpts{
		Collection: collection,
		N:          o.cfg.Search.CoarseK,
		MinScore:   params.MinScore,
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

func (o *Orchestrator) finalizeResults(results []model.SearchResult, n int) []model.SearchResult {
	return searchutil.DedupSortLimit(results, n)
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

	key := o.deepNegativeKey(query, scope)

	o.deepNegMu.Lock()
	defer o.deepNegMu.Unlock()

	expiry, ok := o.deepNeg[key]
	if !ok {
		return false, ""
	}
	if time.Now().After(expiry) {
		delete(o.deepNeg, key)
		return false, ""
	}
	return true, "deep_negative_cached_fallback_broad"
}

func (o *Orchestrator) markDeepNegative(query, scope string) {
	ttl := o.cfg.Runtime.DeepNegativeTTL
	if ttl <= 0 {
		return
	}

	key := o.deepNegativeKey(query, scope)
	expiry := time.Now().Add(ttl)

	o.deepNegMu.Lock()
	o.deepNeg[key] = expiry
	o.deepNegMu.Unlock()
}

func (o *Orchestrator) deepNegativeKey(query, scope string) string {
	q := strings.ToLower(strings.TrimSpace(query))
	q = strings.Join(strings.Fields(q), " ")
	runes := []rune(q)
	if len(runes) > 64 {
		q = string(runes[:64])
	}
	sum := sha1.Sum([]byte(scope + "|" + q))
	return hex.EncodeToString(sum[:])
}

func (o *Orchestrator) searchPrimaryWithTierFallback(ctx context.Context, params SearchParams, mode router.Mode, logMsg string) ([]model.SearchResult, []string, bool) {
	tier1 := o.collectionsByTier(1)
	var allResults []model.SearchResult
	var searched []string

	for _, col := range tier1 {
		results, err := o.execSearch(ctx, mode, params.Query, col.Name, params)
		if err != nil {
			o.log.Warn(logMsg, "collection", col.Name, "err", err)
			continue
		}
		results = o.filterExclude(results, &col)
		allResults = append(allResults, results...)
		searched = append(searched, col.Name)
	}

	filtered := o.filterMinScore(allResults, params.MinScore)
	fallbackTriggered := false

	if len(filtered) == 0 && params.Fallback && o.cfg.Search.FallbackEnabled {
		tier2 := o.collectionsByTier(2)
		if len(tier2) > 0 {
			fallbackTriggered = true
			t2Results := o.searchTierParallel(ctx, tier2, mode, params)
			filtered = o.filterMinScore(t2Results, params.MinScore)
			for _, col := range tier2 {
				searched = append(searched, col.Name)
			}
		}
	}

	return filtered, searched, fallbackTriggered
}

func (o *Orchestrator) cacheAndBuildSearchResult(cacheKey string, results []model.SearchResult, mode router.Mode, searched []string, fallbackTriggered bool, degraded bool, degradeReason string, start time.Time) *SearchResult {
	o.cacheResults(cacheKey, results, string(mode), strings.Join(searched, ","), fallbackTriggered, degraded, degradeReason)
	return &SearchResult{
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
}
