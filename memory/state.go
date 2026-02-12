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

type StateManager struct {
	cfg *config.Config
	log *slog.Logger
	mu  sync.Mutex
}

func NewStateManager(cfg *config.Config, logger *slog.Logger) *StateManager {
	return &StateManager{
		cfg: cfg,
		log: logger,
	}
}

func (s *StateManager) UpdateState(req model.StateUpdateRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	memoryCol := s.findMemoryCollection()
	if memoryCol == nil {
		return fmt.Errorf("memory collection (tier 1) not found")
	}

	stateDir := filepath.Join(memoryCol.Path, ".state")
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}

	statePath := filepath.Join(stateDir, "current-state.md")
	content := formatState(req, time.Now())

	if err := os.WriteFile(statePath, []byte(content), 0644); err != nil {
		return fmt.Errorf("write state file: %w", err)
	}

	s.log.Info("state updated", "file", statePath)
	return nil
}

func (s *StateManager) findMemoryCollection() *config.CollectionCfg {
	for i := range s.cfg.Collections {
		if s.cfg.Collections[i].Tier == 1 {
			return &s.cfg.Collections[i]
		}
	}
	return nil
}

func formatState(req model.StateUpdateRequest, t time.Time) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("# Current State\n\n*Updated: %s*\n\n", t.Format("2006-01-02 15:04:05")))

	if req.Goal != "" {
		sb.WriteString(fmt.Sprintf("## Goal\n\n%s\n\n", req.Goal))
	}
	if req.Progress != "" {
		sb.WriteString(fmt.Sprintf("## Progress\n\n%s\n\n", req.Progress))
	}
	if req.Facts != "" {
		sb.WriteString(fmt.Sprintf("## Key Facts\n\n%s\n\n", req.Facts))
	}
	if req.OpenIssues != "" {
		sb.WriteString(fmt.Sprintf("## Open Issues\n\n%s\n\n", req.OpenIssues))
	}
	if req.Next != "" {
		sb.WriteString(fmt.Sprintf("## Next Steps\n\n%s\n\n", req.Next))
	}

	return sb.String()
}
