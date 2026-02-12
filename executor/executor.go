package executor

import (
	"context"

	"qmdsr/model"
)

type SearchOpts struct {
	Collection string
	N          int
	MinScore   float64
	Format     string
	Full       bool
}

type GetOpts struct {
	Full        bool
	LineNumbers bool
}

type Executor interface {
	Search(ctx context.Context, query string, opts SearchOpts) ([]model.SearchResult, error)
	VSearch(ctx context.Context, query string, opts SearchOpts) ([]model.SearchResult, error)
	Query(ctx context.Context, query string, opts SearchOpts) ([]model.SearchResult, error)

	Get(ctx context.Context, docRef string, opts GetOpts) (string, error)
	MultiGet(ctx context.Context, pattern string, maxBytes int) ([]model.Document, error)

	CollectionAdd(ctx context.Context, path, name, mask string) error
	CollectionList(ctx context.Context) ([]model.CollectionInfo, error)
	Update(ctx context.Context) error
	Embed(ctx context.Context, force bool) error

	ContextAdd(ctx context.Context, path, description string) error
	ContextList(ctx context.Context) ([]model.PathContext, error)
	ContextRemove(ctx context.Context, path string) error

	Status(ctx context.Context) (*model.IndexStatus, error)

	MCPStart(ctx context.Context) error
	MCPStop(ctx context.Context) error
	MCPHealth(ctx context.Context) error

	Version(ctx context.Context) (string, error)
	HasCapability(cap string) bool
}

type Capabilities struct {
	Vector    bool
	DeepQuery bool
	MCP       bool
	Status    bool
}
