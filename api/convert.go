package api

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"qmdsr/model"
)

func requestedModeToOrchestratorMode(mode string) string {
	switch mode {
	case "core", "broad":
		return "search"
	case "deep":
		return "query"
	default:
		return "auto"
	}
}

func normalizeRequestedMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "core", "search":
		return "core"
	case "broad", "vsearch":
		return "broad"
	case "deep", "query":
		return "deep"
	default:
		return "auto"
	}
}

func normalizeCollections(cols []string) []string {
	if len(cols) == 0 {
		return nil
	}
	set := make(map[string]struct{}, len(cols))
	for _, c := range cols {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		set[c] = struct{}{}
	}
	return sortedKeys(set)
}

func sortedKeys(set map[string]struct{}) []string {
	if len(set) == 0 {
		return []string{}
	}
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func deriveServedMode(requestedMode, modeUsed string, fallbackTriggered, degraded bool) string {
	if modeUsed == "query" {
		return "deep"
	}

	switch requestedMode {
	case "deep":
		return "broad"
	case "broad":
		return "broad"
	case "core":
		if fallbackTriggered {
			return "broad"
		}
		return "core"
	default:
		if fallbackTriggered || degraded {
			return "broad"
		}
		return "core"
	}
}

func buildRouteLog(requestedMode string, allowFallback bool, orchestratorMode string, meta model.SearchMeta, collectionCount int, hitCount int) []string {
	return []string{
		"requested_mode=" + requestedMode,
		"orchestrator_mode=" + orchestratorMode,
		fmt.Sprintf("allow_fallback=%t", allowFallback),
		fmt.Sprintf("collections=%d", collectionCount),
		"mode_used=" + meta.ModeUsed,
		"served_mode=" + meta.ServedMode,
		fmt.Sprintf("degraded=%t", meta.Degraded),
		"degrade_reason=" + meta.DegradeReason,
		fmt.Sprintf("hits=%d", hitCount),
		fmt.Sprintf("cache_hit=%t", meta.CacheHit),
	}
}

func durationToInt32Milliseconds(d time.Duration) int32 {
	if d <= 0 {
		return 0
	}
	ms := d.Milliseconds()
	if ms > int64(math.MaxInt32) {
		return math.MaxInt32
	}
	return int32(ms)
}

func durationToInt32Seconds(d time.Duration) int32 {
	if d <= 0 {
		return 0
	}
	sec := int64(d / time.Second)
	if sec > int64(math.MaxInt32) {
		return math.MaxInt32
	}
	return int32(sec)
}
