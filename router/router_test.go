package router

import "testing"

func TestDetectMode_PrefersVSearchForMiddleQueriesWhenVectorAvailable(t *testing.T) {
	mode := DetectMode("memory retrieval fallback behavior", true, false)
	if mode != ModeVSearch {
		t.Fatalf("expected vsearch, got %s", mode)
	}
}

func TestDetectMode_DoesNotUseVSearchWhenVectorUnavailable(t *testing.T) {
	mode := DetectMode("memory retrieval fallback behavior", false, false)
	if mode != ModeSearch {
		t.Fatalf("expected search, got %s", mode)
	}
}

func TestDetectMode_PrefersDeepForComplexQueries(t *testing.T) {
	mode := DetectMode("为什么最近的网络重构方案会出现回退问题并且如何修复", true, true)
	if mode != ModeQuery {
		t.Fatalf("expected query, got %s", mode)
	}
}

func TestDetectMode_UsesVSearchForShortNonASCIISemanticQuery(t *testing.T) {
	mode := DetectMode("网络 架构 优化", true, false)
	if mode != ModeVSearch {
		t.Fatalf("expected vsearch, got %s", mode)
	}
}
