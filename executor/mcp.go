package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"qmdsr/config"
	"qmdsr/model"
)

type MCPExecutor struct {
	baseURL string
	client  *http.Client
	log     *slog.Logger
}

func NewMCP(cfg *config.Config, logger *slog.Logger) *MCPExecutor {
	return &MCPExecutor{
		baseURL: fmt.Sprintf("http://127.0.0.1:%d", cfg.QMD.MCPPort),
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
		log: logger,
	}
}

func (m *MCPExecutor) Health(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.baseURL+"/health", nil)
	if err != nil {
		return err
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return fmt.Errorf("mcp health check failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("mcp health check returned %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

type mcpToolCall struct {
	JSONRPC string      `json:"jsonrpc"`
	Method  string      `json:"method"`
	Params  mcpParams   `json:"params"`
	ID      int         `json:"id"`
}

type mcpParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

type mcpResponse struct {
	JSONRPC string     `json:"jsonrpc"`
	Result  mcpResult  `json:"result"`
	Error   *mcpError  `json:"error,omitempty"`
	ID      int        `json:"id"`
}

type mcpResult struct {
	Content []mcpContent `json:"content"`
}

type mcpContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type mcpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (m *MCPExecutor) callTool(ctx context.Context, tool string, args map[string]any) (string, error) {
	call := mcpToolCall{
		JSONRPC: "2.0",
		Method:  "tools/call",
		Params: mcpParams{
			Name:      tool,
			Arguments: args,
		},
		ID: 1,
	}

	body, err := json.Marshal(call)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.baseURL+"/mcp", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := m.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("mcp call %s: %w", tool, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read mcp response: %w", err)
	}

	var mcpResp mcpResponse
	if err := json.Unmarshal(respBody, &mcpResp); err != nil {
		return "", fmt.Errorf("parse mcp response: %w", err)
	}

	if mcpResp.Error != nil {
		return "", fmt.Errorf("mcp error %d: %s", mcpResp.Error.Code, mcpResp.Error.Message)
	}

	if len(mcpResp.Result.Content) > 0 {
		return mcpResp.Result.Content[0].Text, nil
	}
	return "", nil
}

func (m *MCPExecutor) Search(ctx context.Context, query string, opts SearchOpts) ([]model.SearchResult, error) {
	args := map[string]any{"query": query}
	if opts.Collection != "" {
		args["collection"] = opts.Collection
	}
	if opts.N > 0 {
		args["n"] = opts.N
	}
	return m.execSearch(ctx, "qmd_search", args)
}

func (m *MCPExecutor) VSearch(ctx context.Context, query string, opts SearchOpts) ([]model.SearchResult, error) {
	args := map[string]any{"query": query}
	if opts.Collection != "" {
		args["collection"] = opts.Collection
	}
	if opts.N > 0 {
		args["n"] = opts.N
	}
	return m.execSearch(ctx, "qmd_vector_search", args)
}

func (m *MCPExecutor) Query(ctx context.Context, query string, opts SearchOpts) ([]model.SearchResult, error) {
	args := map[string]any{"query": query}
	if opts.Collection != "" {
		args["collection"] = opts.Collection
	}
	if opts.N > 0 {
		args["n"] = opts.N
	}
	return m.execSearch(ctx, "qmd_deep_search", args)
}

func (m *MCPExecutor) execSearch(ctx context.Context, tool string, args map[string]any) ([]model.SearchResult, error) {
	text, err := m.callTool(ctx, tool, args)
	if err != nil {
		return nil, err
	}
	var results []model.SearchResult
	if err := json.Unmarshal([]byte(text), &results); err != nil {
		return nil, fmt.Errorf("parse mcp search results: %w", err)
	}
	return results, nil
}
