package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"qmdsr/config"
	"qmdsr/model"
)

const defaultTimeout = 30 * time.Second
const queryTimeout = 120 * time.Second

type CLIExecutor struct {
	bin         string
	caps        Capabilities
	log         *slog.Logger
	lowResource bool
}

func NewCLI(cfg *config.Config, logger *slog.Logger) (*CLIExecutor, error) {
	e := &CLIExecutor{
		bin:         cfg.QMD.Bin,
		log:         logger,
		lowResource: cfg.Runtime.LowResourceMode,
	}
	if err := e.probe(context.Background()); err != nil {
		return nil, err
	}
	return e, nil
}

func (e *CLIExecutor) probe(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	out, err := e.run(ctx, "--version")
	if err != nil {
		// Some qmd builds don't support --version and exit non-zero while still being usable.
		_, helpErr := e.run(ctx, "--help")
		if helpErr != nil {
			return fmt.Errorf("qmd not available (version check failed: %v; help check failed: %w)", err, helpErr)
		}
		e.log.Info("qmd detected (without --version support)", "probe", "--help")
	} else {
		e.log.Info("qmd detected", "version", strings.TrimSpace(out))
	}

	if _, err := e.run(ctx, "vsearch", "--help"); err == nil {
		e.caps.Vector = true
	}
	if _, err := e.run(ctx, "query", "--help"); err == nil {
		e.caps.DeepQuery = true
	}
	if _, err := e.run(ctx, "mcp", "--help"); err == nil {
		e.caps.MCP = true
	}
	if _, err := e.run(ctx, "status", "--help"); err == nil {
		e.caps.Status = true
	}

	if e.lowResource {
		if e.caps.Vector || e.caps.DeepQuery {
			e.log.Info("low_resource_mode enabled, disabling vector/deep-query capabilities")
		}
		e.caps.Vector = false
		e.caps.DeepQuery = false
	}

	e.log.Info("qmd capabilities",
		"vector", e.caps.Vector,
		"deep_query", e.caps.DeepQuery,
		"mcp", e.caps.MCP,
		"status", e.caps.Status,
	)
	return nil
}

func (e *CLIExecutor) HasCapability(cap string) bool {
	switch cap {
	case "vector":
		return e.caps.Vector
	case "deep_query":
		return e.caps.DeepQuery
	case "mcp":
		return e.caps.MCP
	case "status":
		return e.caps.Status
	default:
		return false
	}
}

func (e *CLIExecutor) Search(ctx context.Context, query string, opts SearchOpts) ([]model.SearchResult, error) {
	args := []string{"search", query, "--json"}
	args = appendSearchArgs(args, opts)
	return e.execSearch(ctx, args, defaultTimeout)
}

func (e *CLIExecutor) VSearch(ctx context.Context, query string, opts SearchOpts) ([]model.SearchResult, error) {
	if !e.caps.Vector {
		return nil, fmt.Errorf("vsearch not available")
	}
	args := []string{"vsearch", query, "--json"}
	args = appendSearchArgs(args, opts)
	return e.execSearch(ctx, args, defaultTimeout)
}

func (e *CLIExecutor) Query(ctx context.Context, query string, opts SearchOpts) ([]model.SearchResult, error) {
	if !e.caps.DeepQuery {
		return nil, fmt.Errorf("query not available")
	}
	args := []string{"query", query, "--json"}
	args = appendSearchArgs(args, opts)
	return e.execSearch(ctx, args, queryTimeout)
}

func (e *CLIExecutor) Get(ctx context.Context, docRef string, opts GetOpts) (string, error) {
	args := []string{"get", docRef}
	if opts.Full {
		args = append(args, "--full")
	}
	if opts.LineNumbers {
		args = append(args, "--line-numbers")
	}
	ctx, cancel := context.WithTimeout(ctx, defaultTimeout)
	defer cancel()
	return e.run(ctx, args...)
}

func (e *CLIExecutor) MultiGet(ctx context.Context, pattern string, maxBytes int) ([]model.Document, error) {
	args := []string{"multi-get", pattern, "--json"}
	if maxBytes > 0 {
		args = append(args, "--max-bytes", strconv.Itoa(maxBytes))
	}
	ctx, cancel := context.WithTimeout(ctx, defaultTimeout)
	defer cancel()
	out, err := e.run(ctx, args...)
	if err != nil {
		return nil, err
	}
	var docs []model.Document
	if err := json.Unmarshal([]byte(out), &docs); err != nil {
		return nil, fmt.Errorf("parse multi-get output: %w", err)
	}
	return docs, nil
}

func (e *CLIExecutor) CollectionAdd(ctx context.Context, path, name, mask string) error {
	args := []string{"collection", "add", path, "--name", name}
	if mask != "" {
		args = append(args, "--mask", mask)
	}
	ctx, cancel := context.WithTimeout(ctx, defaultTimeout)
	defer cancel()
	_, err := e.run(ctx, args...)
	return err
}

func (e *CLIExecutor) CollectionList(ctx context.Context) ([]model.CollectionInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, defaultTimeout)
	defer cancel()
	out, err := e.run(ctx, "collection", "list", "--json")
	if err != nil {
		return nil, err
	}

	cols, err := parseCollectionListJSON(out)
	if err == nil {
		return cols, nil
	}

	cols, textErr := parseCollectionListText(out)
	if textErr == nil {
		e.log.Info("parsed collection list from text output", "count", len(cols))
		return cols, nil
	}

	return nil, fmt.Errorf("parse collection list: json parse failed: %v; text parse failed: %w", err, textErr)
}

func parseCollectionListJSON(out string) ([]model.CollectionInfo, error) {
	trimmed := strings.TrimSpace(out)
	if trimmed == "" {
		return []model.CollectionInfo{}, nil
	}

	var cols []model.CollectionInfo
	if err := json.Unmarshal([]byte(trimmed), &cols); err == nil {
		return cols, nil
	}

	var wrapped struct {
		Collections []model.CollectionInfo `json:"collections"`
	}
	if err := json.Unmarshal([]byte(trimmed), &wrapped); err == nil && wrapped.Collections != nil {
		return wrapped.Collections, nil
	}

	return nil, fmt.Errorf("invalid json output")
}

func parseCollectionListText(out string) ([]model.CollectionInfo, error) {
	lines := strings.Split(out, "\n")
	cols := make([]model.CollectionInfo, 0, 8)
	indexByName := make(map[string]int)

	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "Collections") {
			continue
		}

		switch {
		case strings.Contains(line, " (qmd://") && strings.HasSuffix(line, ")"):
			name := strings.TrimSpace(strings.SplitN(line, " (qmd://", 2)[0])
			if name == "" {
				continue
			}
			if _, exists := indexByName[name]; !exists {
				indexByName[name] = len(cols)
				cols = append(cols, model.CollectionInfo{Name: name})
			}
		case strings.HasPrefix(line, "qmd://") && strings.Contains(line, "/"):
			rest := strings.TrimPrefix(line, "qmd://")
			name := strings.TrimSpace(strings.SplitN(rest, "/", 2)[0])
			if name == "" {
				continue
			}
			if _, exists := indexByName[name]; !exists {
				indexByName[name] = len(cols)
				cols = append(cols, model.CollectionInfo{Name: name})
			}
		case strings.HasPrefix(line, "Pattern:"):
			if len(cols) == 0 {
				continue
			}
			cols[len(cols)-1].Mask = strings.TrimSpace(strings.TrimPrefix(line, "Pattern:"))
		case strings.HasPrefix(line, "Files:"):
			if len(cols) == 0 {
				continue
			}
			value := strings.TrimSpace(strings.TrimPrefix(line, "Files:"))
			fields := strings.Fields(value)
			if len(fields) == 0 {
				continue
			}
			n, err := strconv.Atoi(fields[0])
			if err != nil {
				continue
			}
			cols[len(cols)-1].Files = n
		}
	}

	if len(cols) == 0 {
		return nil, fmt.Errorf("no collections parsed")
	}
	return cols, nil
}

func (e *CLIExecutor) Update(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	_, err := e.run(ctx, "update")
	return err
}

func (e *CLIExecutor) Embed(ctx context.Context, force bool) error {
	args := []string{"embed"}
	if force {
		args = append(args, "-f")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	_, err := e.run(ctx, args...)
	return err
}

func (e *CLIExecutor) ContextAdd(ctx context.Context, path, description string) error {
	ctx, cancel := context.WithTimeout(ctx, defaultTimeout)
	defer cancel()
	_, err := e.run(ctx, "context", "add", path, description)
	return err
}

func (e *CLIExecutor) ContextList(ctx context.Context) ([]model.PathContext, error) {
	ctx, cancel := context.WithTimeout(ctx, defaultTimeout)
	defer cancel()
	out, err := e.run(ctx, "context", "list", "--json")
	if err != nil {
		return nil, err
	}
	var contexts []model.PathContext
	if err := json.Unmarshal([]byte(out), &contexts); err != nil {
		return nil, fmt.Errorf("parse context list: %w", err)
	}
	return contexts, nil
}

func (e *CLIExecutor) ContextRemove(ctx context.Context, path string) error {
	ctx, cancel := context.WithTimeout(ctx, defaultTimeout)
	defer cancel()
	_, err := e.run(ctx, "context", "remove", path)
	return err
}

func (e *CLIExecutor) Status(ctx context.Context) (*model.IndexStatus, error) {
	if !e.caps.Status {
		return nil, fmt.Errorf("status not available")
	}
	ctx, cancel := context.WithTimeout(ctx, defaultTimeout)
	defer cancel()
	out, err := e.run(ctx, "status", "--json")
	if err != nil {
		return &model.IndexStatus{Raw: out}, err
	}

	status, parseErr := parseStatusJSON(out)
	if parseErr == nil {
		status.Raw = out
		return status, nil
	}

	status, textErr := parseStatusText(out)
	if textErr == nil {
		status.Raw = out
		e.log.Info("parsed status from text output", "vectors", status.Vectors, "collections", len(status.Collections))
		return status, nil
	}

	return &model.IndexStatus{Raw: out}, fmt.Errorf("parse status: json parse failed: %v; text parse failed: %w", parseErr, textErr)
}

func parseStatusJSON(out string) (*model.IndexStatus, error) {
	trimmed := strings.TrimSpace(out)
	if trimmed == "" {
		return nil, fmt.Errorf("empty status output")
	}

	var status model.IndexStatus
	if err := json.Unmarshal([]byte(trimmed), &status); err == nil {
		return &status, nil
	}

	var wrapped struct {
		Status *model.IndexStatus `json:"status"`
	}
	if err := json.Unmarshal([]byte(trimmed), &wrapped); err == nil && wrapped.Status != nil {
		return wrapped.Status, nil
	}

	var generic map[string]any
	if err := json.Unmarshal([]byte(trimmed), &generic); err != nil {
		return nil, fmt.Errorf("invalid json output")
	}

	st := &model.IndexStatus{}
	if v, ok := intFromAny(generic["vectors"]); ok {
		st.Vectors = v
		return st, nil
	}

	if docs, ok := generic["documents"].(map[string]any); ok {
		if v, ok := intFromAny(docs["vectors"]); ok {
			st.Vectors = v
			return st, nil
		}
	}

	return nil, fmt.Errorf("vectors field not found")
}

func parseStatusText(out string) (*model.IndexStatus, error) {
	lines := strings.Split(out, "\n")
	st := &model.IndexStatus{
		Collections: make([]model.CollectionInfo, 0, 8),
	}
	indexByName := make(map[string]int)
	parsedVectors := false

	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}

		switch {
		case strings.HasPrefix(line, "Vectors:"):
			value := strings.TrimSpace(strings.TrimPrefix(line, "Vectors:"))
			fields := strings.Fields(value)
			if len(fields) == 0 {
				continue
			}
			v, err := strconv.Atoi(fields[0])
			if err != nil {
				continue
			}
			st.Vectors = v
			parsedVectors = true
		case strings.Contains(line, " (qmd://") && strings.HasSuffix(line, ")"):
			name := strings.TrimSpace(strings.SplitN(line, " (qmd://", 2)[0])
			if name == "" {
				continue
			}
			if _, exists := indexByName[name]; !exists {
				indexByName[name] = len(st.Collections)
				st.Collections = append(st.Collections, model.CollectionInfo{Name: name})
			}
		case strings.HasPrefix(line, "Files:"):
			if len(st.Collections) == 0 {
				continue
			}
			value := strings.TrimSpace(strings.TrimPrefix(line, "Files:"))
			fields := strings.Fields(value)
			if len(fields) == 0 {
				continue
			}
			n, err := strconv.Atoi(fields[0])
			if err != nil {
				continue
			}
			st.Collections[len(st.Collections)-1].Files = n
		}
	}

	if !parsedVectors {
		return nil, fmt.Errorf("vectors line not found")
	}
	return st, nil
}

func intFromAny(v any) (int, bool) {
	switch x := v.(type) {
	case float64:
		return int(x), true
	case int:
		return x, true
	case int64:
		return int(x), true
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(x))
		if err != nil {
			return 0, false
		}
		return n, true
	default:
		return 0, false
	}
}

func (e *CLIExecutor) MCPStart(ctx context.Context) error {
	if !e.caps.MCP {
		return fmt.Errorf("mcp not available")
	}
	ctx, cancel := context.WithTimeout(ctx, defaultTimeout)
	defer cancel()
	_, err := e.run(ctx, "mcp", "--http", "--daemon")
	return err
}

func (e *CLIExecutor) MCPStop(ctx context.Context) error {
	if !e.caps.MCP {
		return fmt.Errorf("mcp not available")
	}
	ctx, cancel := context.WithTimeout(ctx, defaultTimeout)
	defer cancel()
	_, err := e.run(ctx, "mcp", "stop")
	return err
}

func (e *CLIExecutor) MCPHealth(ctx context.Context) error {
	if !e.caps.MCP {
		return fmt.Errorf("mcp not available")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, err := e.run(ctx, "mcp", "health")
	return err
}

func (e *CLIExecutor) Version(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := e.run(ctx, "--version")
	if err != nil {
		if _, helpErr := e.run(ctx, "--help"); helpErr != nil {
			return "", fmt.Errorf("qmd version check failed: %v; help check failed: %w", err, helpErr)
		}
		return "unknown", nil
	}
	return strings.TrimSpace(out), nil
}

func (e *CLIExecutor) run(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, e.bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	e.log.Debug("exec qmd", "args", args)
	if err := cmd.Run(); err != nil {
		e.log.Debug("exec qmd failed", "args", args, "stderr", stderr.String(), "err", err)
		return stdout.String(), fmt.Errorf("qmd %s: %w: %s", strings.Join(args, " "), err, stderr.String())
	}
	return stdout.String(), nil
}

func (e *CLIExecutor) execSearch(ctx context.Context, args []string, timeout time.Duration) ([]model.SearchResult, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	out, err := e.run(ctx, args...)
	if err != nil {
		return nil, err
	}

	var results []model.SearchResult
	if err := json.Unmarshal([]byte(out), &results); err != nil {
		return nil, fmt.Errorf("parse search results: %w (output: %.200s)", err, out)
	}
	return results, nil
}

func appendSearchArgs(args []string, opts SearchOpts) []string {
	if opts.Collection != "" {
		args = append(args, "--collection", opts.Collection)
	}
	if opts.N > 0 {
		args = append(args, "-n", strconv.Itoa(opts.N))
	}
	if opts.MinScore > 0 {
		args = append(args, "--min-score", strconv.FormatFloat(opts.MinScore, 'f', 2, 64))
	}
	if opts.Full {
		args = append(args, "--full")
	}
	return args
}
