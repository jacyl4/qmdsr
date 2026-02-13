package orchestrator

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"qmdsr/config"
	"qmdsr/executor"
	"qmdsr/model"
)

type fakeEnsureExec struct {
	collections []model.CollectionInfo
	contexts    map[string]string

	collectionAddCalls int
	contextAddCalls    []model.PathContext
	contextRemoveCalls []string
}

func newFakeEnsureExec(existingCollections []string, contexts map[string]string) *fakeEnsureExec {
	cols := make([]model.CollectionInfo, 0, len(existingCollections))
	for _, name := range existingCollections {
		cols = append(cols, model.CollectionInfo{Name: name})
	}
	copiedContexts := make(map[string]string, len(contexts))
	for k, v := range contexts {
		copiedContexts[k] = v
	}
	return &fakeEnsureExec{
		collections: cols,
		contexts:    copiedContexts,
	}
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func (f *fakeEnsureExec) Search(context.Context, string, executor.SearchOpts) ([]model.SearchResult, error) {
	return nil, nil
}

func (f *fakeEnsureExec) VSearch(context.Context, string, executor.SearchOpts) ([]model.SearchResult, error) {
	return nil, nil
}

func (f *fakeEnsureExec) Query(context.Context, string, executor.SearchOpts) ([]model.SearchResult, error) {
	return nil, nil
}

func (f *fakeEnsureExec) Get(context.Context, string, executor.GetOpts) (string, error) {
	return "", nil
}

func (f *fakeEnsureExec) MultiGet(context.Context, string, int) ([]model.Document, error) {
	return nil, nil
}

func (f *fakeEnsureExec) CollectionAdd(_ context.Context, path, name, mask string) error {
	f.collectionAddCalls++
	f.collections = append(f.collections, model.CollectionInfo{Name: name, Path: path, Mask: mask})
	return nil
}

func (f *fakeEnsureExec) CollectionList(context.Context) ([]model.CollectionInfo, error) {
	return f.collections, nil
}

func (f *fakeEnsureExec) Update(context.Context) error { return nil }

func (f *fakeEnsureExec) Embed(context.Context, bool) error { return nil }

func (f *fakeEnsureExec) ContextAdd(_ context.Context, path, description string) error {
	f.contexts[path] = description
	f.contextAddCalls = append(f.contextAddCalls, model.PathContext{Path: path, Description: description})
	return nil
}

func (f *fakeEnsureExec) ContextList(context.Context) ([]model.PathContext, error) {
	out := make([]model.PathContext, 0, len(f.contexts))
	for p, d := range f.contexts {
		out = append(out, model.PathContext{Path: p, Description: d})
	}
	return out, nil
}

func (f *fakeEnsureExec) ContextRemove(_ context.Context, path string) error {
	delete(f.contexts, path)
	f.contextRemoveCalls = append(f.contextRemoveCalls, path)
	return nil
}

func (f *fakeEnsureExec) Status(context.Context) (*model.IndexStatus, error) {
	return &model.IndexStatus{}, nil
}

func (f *fakeEnsureExec) MCPStart(context.Context) error { return nil }

func (f *fakeEnsureExec) MCPStop(context.Context) error { return nil }

func (f *fakeEnsureExec) MCPHealth(context.Context) error { return nil }

func (f *fakeEnsureExec) Version(context.Context) (string, error) { return "", nil }

func (f *fakeEnsureExec) HasCapability(string) bool { return false }

func TestEnsureCollections_UpdatesAndAddsContexts(t *testing.T) {
	cfg := &config.Config{
		Collections: []config.CollectionCfg{
			{Name: "alpha", Path: "/a", Context: "new-a", Tier: 1},
			{Name: "beta", Path: "/b", Context: "keep-b", Tier: 1},
			{Name: "delta", Path: "/d", Context: "add-d", Tier: 1},
		},
	}
	fakeExec := newFakeEnsureExec(
		[]string{"alpha", "beta", "delta"},
		map[string]string{
			"/a": "old-a",
			"/b": "keep-b",
		},
	)

	o := New(cfg, fakeExec, nil, testLogger())
	if err := o.EnsureCollections(context.Background()); err != nil {
		t.Fatalf("EnsureCollections failed: %v", err)
	}

	if fakeExec.collectionAddCalls != 0 {
		t.Fatalf("unexpected collection add calls: %d", fakeExec.collectionAddCalls)
	}
	if len(fakeExec.contextRemoveCalls) != 1 || fakeExec.contextRemoveCalls[0] != "/a" {
		t.Fatalf("unexpected context remove calls: %+v", fakeExec.contextRemoveCalls)
	}

	added := make(map[string]string, len(fakeExec.contextAddCalls))
	for _, c := range fakeExec.contextAddCalls {
		added[c.Path] = c.Description
	}
	if added["/a"] != "new-a" {
		t.Fatalf("expected updated context for /a, got: %q", added["/a"])
	}
	if added["/d"] != "add-d" {
		t.Fatalf("expected added context for /d, got: %q", added["/d"])
	}
	if _, ok := added["/b"]; ok {
		t.Fatalf("did not expect context add for unchanged /b")
	}
}

func TestEnsureCollections_DoesNotDoubleAddContextForNewCollection(t *testing.T) {
	cfg := &config.Config{
		Collections: []config.CollectionCfg{
			{Name: "alpha", Path: "/a", Context: "ctx-a", Tier: 1},
		},
	}
	fakeExec := newFakeEnsureExec(nil, nil)

	o := New(cfg, fakeExec, nil, testLogger())
	if err := o.EnsureCollections(context.Background()); err != nil {
		t.Fatalf("EnsureCollections failed: %v", err)
	}

	if fakeExec.collectionAddCalls != 1 {
		t.Fatalf("expected one collection add, got %d", fakeExec.collectionAddCalls)
	}
	if len(fakeExec.contextAddCalls) != 1 {
		t.Fatalf("expected one context add, got %d", len(fakeExec.contextAddCalls))
	}
	if len(fakeExec.contextRemoveCalls) != 0 {
		t.Fatalf("unexpected context remove calls: %+v", fakeExec.contextRemoveCalls)
	}
}
