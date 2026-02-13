package orchestrator

import (
	"testing"
	"time"

	"qmdsr/config"
	"qmdsr/model"
)

func TestFinalizeResults_FilesAllMaxHits(t *testing.T) {
	cfg := &config.Config{
		Search: config.SearchConfig{
			FilesAllMaxHits: 2,
		},
	}
	o := New(cfg, newFakeEnsureExec(nil, nil), nil, testLogger())

	in := []model.SearchResult{
		{DocID: "a1", File: "a.md", Score: 0.9},
		{DocID: "b1", File: "b.md", Score: 0.8},
		{DocID: "c1", File: "c.md", Score: 0.7},
	}
	out := o.finalizeResults(in, 0, true, true)
	if len(out) != 2 {
		t.Fatalf("expected 2 results after files_all cap, got %d", len(out))
	}
}

func TestDeepNegativeCache_ExactKeyOnly(t *testing.T) {
	cfg := &config.Config{
		Runtime: config.RuntimeConfig{
			DeepNegativeTTL: time.Minute,
		},
	}
	o := New(cfg, newFakeEnsureExec(nil, nil), nil, testLogger())

	o.markDeepNegative("how to debug grpc timeout", "all")

	if ok, _ := o.shouldSkipDeepByNegativeCache("how to debug grpc timeout", "all"); !ok {
		t.Fatalf("expected exact negative cache hit for identical query")
	}

	if ok, _ := o.shouldSkipDeepByNegativeCache("why grpc stream times out", "all"); ok {
		t.Fatalf("did not expect non-exact negative cache hit")
	}
}

func TestDeepNegativeCache_ScopeCooldown(t *testing.T) {
	cfg := &config.Config{
		Runtime: config.RuntimeConfig{
			AllowCPUDeepQuery:         true,
			DeepNegativeTTL:           time.Minute,
			DeepNegativeScopeCooldown: 10 * time.Minute,
		},
	}
	o := New(cfg, newFakeEnsureExec(nil, nil), nil, testLogger())

	o.markDeepNegative("first deep fail", "all")
	o.markDeepNegative("second deep fail", "all")
	o.markDeepNegative("third deep fail", "all")

	if ok, reason := o.shouldSkipDeepByNegativeCache("another deep query", "all"); !ok || reason != "deep_negative_scope_cooldown" {
		t.Fatalf("expected scope cooldown hit, got ok=%v reason=%q", ok, reason)
	}
}

func TestDeepNegativeCache_ScopeCooldownDisabledWhenDeepOff(t *testing.T) {
	cfg := &config.Config{
		Runtime: config.RuntimeConfig{
			AllowCPUDeepQuery:         false,
			DeepNegativeTTL:           time.Minute,
			DeepNegativeScopeCooldown: 10 * time.Minute,
		},
	}
	o := New(cfg, newFakeEnsureExec(nil, nil), nil, testLogger())

	o.markDeepNegative("first deep fail", "all")
	o.markDeepNegative("second deep fail", "all")
	o.markDeepNegative("third deep fail", "all")

	if ok, reason := o.shouldSkipDeepByNegativeCache("another deep query", "all"); ok && reason == "deep_negative_scope_cooldown" {
		t.Fatalf("did not expect scope cooldown when deep query is disabled")
	}
}

func TestCleanupDeepNegativeCache_RemovesExpiredEntries(t *testing.T) {
	cfg := &config.Config{
		Runtime: config.RuntimeConfig{
			DeepNegativeTTL:           time.Minute,
			DeepNegativeScopeCooldown: 10 * time.Minute,
		},
	}
	o := New(cfg, newFakeEnsureExec(nil, nil), nil, testLogger())

	o.deepNegMu.Lock()
	o.deepNeg["expired"] = time.Now().Add(-time.Minute)
	o.deepNeg["active"] = time.Now().Add(time.Minute)
	o.deepNegScopeFails["all"] = []time.Time{
		time.Now().Add(-10 * time.Minute),
		time.Now().Add(-6 * time.Minute),
	}
	o.deepNegMu.Unlock()

	removed := o.CleanupDeepNegativeCache()
	if removed != 1 {
		t.Fatalf("expected one expired entry removed, got %d", removed)
	}

	o.deepNegMu.Lock()
	defer o.deepNegMu.Unlock()
	if _, ok := o.deepNeg["expired"]; ok {
		t.Fatalf("expected expired key removed")
	}
	if _, ok := o.deepNeg["active"]; !ok {
		t.Fatalf("expected active key kept")
	}
	if _, ok := o.deepNegScopeFails["all"]; ok {
		t.Fatalf("expected stale scope fail window removed")
	}
}
