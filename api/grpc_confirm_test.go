package api

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"qmdsr/config"
	"qmdsr/executor"
	"qmdsr/model"
	"qmdsr/orchestrator"
	qmdsrv1 "qmdsr/pb/qmdsrv1"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type fakeConfirmExec struct {
	searchCalls int
	getCalls    int
}

func (f *fakeConfirmExec) Search(context.Context, string, executor.SearchOpts) ([]model.SearchResult, error) {
	f.searchCalls++
	return []model.SearchResult{
		{
			Title:      "private note",
			File:       "personal/private-note.md",
			Collection: "personal",
			Score:      0.9,
			Snippet:    "sensitive snippet",
		},
	}, nil
}

func (f *fakeConfirmExec) VSearch(ctx context.Context, query string, opts executor.SearchOpts) ([]model.SearchResult, error) {
	return f.Search(ctx, query, opts)
}

func (f *fakeConfirmExec) Query(ctx context.Context, query string, opts executor.SearchOpts) ([]model.SearchResult, error) {
	return f.Search(ctx, query, opts)
}

func (f *fakeConfirmExec) Get(context.Context, string, executor.GetOpts) (string, error) {
	f.getCalls++
	return "private content", nil
}

func (f *fakeConfirmExec) MultiGet(context.Context, string, int) ([]model.Document, error) {
	return nil, nil
}

func (f *fakeConfirmExec) CollectionAdd(context.Context, string, string, string) error { return nil }

func (f *fakeConfirmExec) CollectionList(context.Context) ([]model.CollectionInfo, error) {
	return nil, nil
}

func (f *fakeConfirmExec) Update(context.Context) error { return nil }

func (f *fakeConfirmExec) Embed(context.Context, bool) error { return nil }

func (f *fakeConfirmExec) ContextAdd(context.Context, string, string) error { return nil }

func (f *fakeConfirmExec) ContextList(context.Context) ([]model.PathContext, error) { return nil, nil }

func (f *fakeConfirmExec) ContextRemove(context.Context, string) error { return nil }

func (f *fakeConfirmExec) Status(context.Context) (*model.IndexStatus, error) {
	return &model.IndexStatus{}, nil
}

func (f *fakeConfirmExec) MCPStart(context.Context) error { return nil }

func (f *fakeConfirmExec) MCPStop(context.Context) error { return nil }

func (f *fakeConfirmExec) MCPHealth(context.Context) error { return nil }

func (f *fakeConfirmExec) Version(context.Context) (string, error) { return "", nil }

func (f *fakeConfirmExec) HasCapability(string) bool { return false }

func newConfirmTestServer(t *testing.T) *Server {
	t.Helper()

	cfg := &config.Config{
		Collections: []config.CollectionCfg{
			{
				Name:            "personal",
				Path:            "/personal",
				Tier:            99,
				RequireExplicit: true,
				SafetyPrompt:    true,
			},
		},
		Search: config.SearchConfig{
			TopK:        3,
			MinScore:    0.0,
			MaxChars:    4500,
			CoarseK:     20,
			DefaultMode: "auto",
		},
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	exec := &fakeConfirmExec{}
	orch := orchestrator.New(cfg, exec, nil, logger)

	return &Server{
		cfg:  cfg,
		orch: orch,
		exec: exec,
		log:  logger,
	}
}

func TestGRPCSearch_ConfirmRequiredForPersonalCollection(t *testing.T) {
	srv := newConfirmTestServer(t)
	g := &grpcQueryServer{s: srv}

	_, err := g.Search(context.Background(), &qmdsrv1.SearchRequest{
		Query:         "my private notes",
		RequestedMode: qmdsrv1.Mode_MODE_CORE,
		Collections:   []string{"personal"},
		Confirm:       false,
	})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected FAILED_PRECONDITION, got err=%v code=%v", err, status.Code(err))
	}

	resp, err := g.Search(context.Background(), &qmdsrv1.SearchRequest{
		Query:         "my private notes",
		RequestedMode: qmdsrv1.Mode_MODE_CORE,
		Collections:   []string{"personal"},
		Confirm:       true,
	})
	if err != nil {
		t.Fatalf("expected success with confirm=true, got err=%v", err)
	}
	if len(resp.GetHits()) != 1 {
		t.Fatalf("expected one hit, got %d", len(resp.GetHits()))
	}
}

func TestGRPCSearchAndGet_ConfirmRequiredForPersonalCollection(t *testing.T) {
	srv := newConfirmTestServer(t)
	g := &grpcQueryServer{s: srv}

	_, err := g.SearchAndGet(context.Background(), &qmdsrv1.SearchAndGetRequest{
		Query:         "my private notes",
		RequestedMode: qmdsrv1.Mode_MODE_CORE,
		Collections:   []string{"personal"},
		Confirm:       false,
	})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected FAILED_PRECONDITION, got err=%v code=%v", err, status.Code(err))
	}

	resp, err := g.SearchAndGet(context.Background(), &qmdsrv1.SearchAndGetRequest{
		Query:         "my private notes",
		RequestedMode: qmdsrv1.Mode_MODE_CORE,
		Collections:   []string{"personal"},
		MaxGetDocs:    1,
		MaxGetBytes:   4096,
		Confirm:       true,
	})
	if err != nil {
		t.Fatalf("expected success with confirm=true, got err=%v", err)
	}
	if len(resp.GetFileHits()) != 1 {
		t.Fatalf("expected one file hit, got %d", len(resp.GetFileHits()))
	}
	if len(resp.GetDocuments()) != 1 {
		t.Fatalf("expected one document, got %d", len(resp.GetDocuments()))
	}
}
