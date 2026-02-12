package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"qmdsr/cache"
	"qmdsr/config"
	"qmdsr/executor"
	"qmdsr/model"
	"qmdsr/router"
)

type Orchestrator struct {
	cfg     *config.Config
	exec    executor.Executor
	cache   *cache.Cache
	log     *slog.Logger
}

func New(cfg *config.Config, exec executor.Executor, c *cache.Cache, logger *slog.Logger) *Orchestrator {
	return &Orchestrator{
		cfg:   cfg,
		exec:  exec,
		cache: c,
		log:   logger,
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
	Query      string
	Mode       string
	Collection string
	N          int
	MinScore   float64
	Fallback   bool
	Format     string
	Confirm    bool
}

type SearchResult struct {
	Results  []model.SearchResult
	Meta     model.SearchMeta
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
		return &SearchResult{
			Results: entry.Results,
			Meta: model.SearchMeta{
				ModeUsed:            entry.Mode,
				CollectionsSearched: []string{entry.Collection},
				CacheHit:            true,
				LatencyMs:           time.Since(start).Milliseconds(),
			},
		}, nil
	}

	mode := o.resolveMode(params.Mode, params.Query)

	if params.Collection != "" {
		return o.searchSingleCollection(ctx, params, mode, cacheKey, start)
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

	if runeLen(q) < o.cfg.Runtime.CPUDeepMinChars {
		return false
	}

	words := countWords(q)
	if words >= o.cfg.Runtime.CPUDeepMinWords {
		return true
	}

	if hasQuestionCue(q) {
		return words >= 4 || countCJK(q) >= 6
	}

	return false
}

func runeLen(s string) int {
	return len([]rune(s))
}

func countWords(s string) int {
	asciiWords := len(strings.Fields(s))
	cjkWords := countCJK(s)
	if cjkWords > asciiWords {
		return cjkWords
	}
	return asciiWords
}

func countCJK(s string) int {
	n := 0
	for _, r := range s {
		if isCJK(r) {
			n++
		}
	}
	return n
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

func isCJK(r rune) bool {
	return unicode.Is(unicode.Han, r)
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

	o.cacheResults(cacheKey, results, string(mode), params.Collection)

	return &SearchResult{
		Results: results,
		Meta: model.SearchMeta{
			ModeUsed:            string(mode),
			CollectionsSearched: []string{params.Collection},
			LatencyMs:           time.Since(start).Milliseconds(),
		},
	}, nil
}

func (o *Orchestrator) searchWithFallback(ctx context.Context, params SearchParams, mode router.Mode, cacheKey string, start time.Time) (*SearchResult, error) {
	tier1 := o.collectionsByTier(1)
	var allResults []model.SearchResult
	var searched []string

	for _, col := range tier1 {
		results, err := o.execSearch(ctx, mode, params.Query, col.Name, params)
		if err != nil {
			o.log.Warn("search failed", "collection", col.Name, "err", err)
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

	if mode == router.ModeSearch && len(filtered) == 0 && o.exec.HasCapability("deep_query") {
		if o.allowAutoDeepQuery(params.Query) {
			o.log.Info("BM25 returned no results, escalating to query mode")
			mode = router.ModeQuery
			allResults = nil
			for _, col := range tier1 {
				results, err := o.execSearch(ctx, mode, params.Query, col.Name, params)
				if err != nil {
					continue
				}
				results = o.filterExclude(results, &col)
				allResults = append(allResults, results...)
			}
			filtered = o.filterMinScore(allResults, params.MinScore)
		}
	}

	filtered = dedup(filtered)
	sortByScore(filtered)
	if len(filtered) > params.N {
		filtered = filtered[:params.N]
	}

	o.cacheResults(cacheKey, filtered, string(mode), strings.Join(searched, ","))

	return &SearchResult{
		Results: filtered,
		Meta: model.SearchMeta{
			ModeUsed:            string(mode),
			CollectionsSearched: searched,
			FallbackTriggered:   fallbackTriggered,
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

func (o *Orchestrator) cacheResults(key string, results []model.SearchResult, mode, collection string) {
	o.cache.Put(key, cache.Entry{
		Results:    results,
		Query:      key,
		Mode:       mode,
		Collection: collection,
	})
}

func dedup(results []model.SearchResult) []model.SearchResult {
	seen := make(map[string]bool)
	deduped := make([]model.SearchResult, 0, len(results))
	for _, r := range results {
		key := r.File
		if r.DocID != "" {
			key = r.DocID
		}
		if !seen[key] {
			seen[key] = true
			deduped = append(deduped, r)
		}
	}
	return deduped
}

func sortByScore(results []model.SearchResult) {
	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})
}
