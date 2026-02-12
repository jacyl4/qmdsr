package memory

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"qmdsr/config"
	"qmdsr/model"
)

type Writer struct {
	cfg *config.Config
	log *slog.Logger
	mu  sync.Mutex
}

func NewWriter(cfg *config.Config, logger *slog.Logger) *Writer {
	return &Writer{
		cfg: cfg,
		log: logger,
	}
}

func (w *Writer) WriteMemory(req model.MemoryWriteRequest) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	memoryCol := w.findMemoryCollection()
	if memoryCol == nil {
		return fmt.Errorf("memory collection (tier 1) not found")
	}

	topicPath := filepath.Join(memoryCol.Path, sanitizePath(req.Topic))
	dir := filepath.Dir(topicPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create directory %s: %w", dir, err)
	}

	if !strings.HasSuffix(topicPath, ".md") {
		topicPath += ".md"
	}

	now := time.Now()
	content := formatMemoryEntry(req, now)

	existing, _ := os.ReadFile(topicPath)

	var final []byte
	if len(existing) > 0 {
		final = append(existing, []byte("\n\n---\n\n")...)
		final = append(final, []byte(content)...)
	} else {
		header := fmt.Sprintf("# %s\n\n", filepath.Base(strings.TrimSuffix(topicPath, ".md")))
		final = append([]byte(header), []byte(content)...)
	}

	if err := os.WriteFile(topicPath, final, 0644); err != nil {
		return fmt.Errorf("write memory file %s: %w", topicPath, err)
	}

	w.log.Info("memory written",
		"topic", req.Topic,
		"file", topicPath,
		"importance", req.Importance,
		"long_term", req.LongTerm,
	)
	return nil
}

func (w *Writer) findMemoryCollection() *config.CollectionCfg {
	for i := range w.cfg.Collections {
		if w.cfg.Collections[i].Tier == 1 {
			return &w.cfg.Collections[i]
		}
	}
	return nil
}

func formatMemoryEntry(req model.MemoryWriteRequest, t time.Time) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("## %s\n\n", t.Format("2006-01-02 15:04")))
	sb.WriteString(req.Summary)
	sb.WriteString("\n\n")

	if req.Source != "" {
		sb.WriteString(fmt.Sprintf("- source: %s\n", req.Source))
	}
	if req.Importance != "" {
		sb.WriteString(fmt.Sprintf("- importance: %s\n", req.Importance))
	}
	if req.LongTerm {
		sb.WriteString("- retention: long-term\n")
	}

	return sb.String()
}

func sanitizePath(p string) string {
	p = strings.ReplaceAll(p, "..", "")
	p = filepath.Clean(p)
	return p
}
