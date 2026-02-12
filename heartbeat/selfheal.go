package heartbeat

import (
	"context"
	"log/slog"
	"os"

	"qmdsr/config"
	"qmdsr/executor"
	"qmdsr/model"
)

type SelfHealer struct {
	cfg  *config.Config
	exec executor.Executor
	log  *slog.Logger
}

func NewSelfHealer(cfg *config.Config, exec executor.Executor, logger *slog.Logger) *SelfHealer {
	return &SelfHealer{
		cfg:  cfg,
		exec: exec,
		log:  logger,
	}
}

func (s *SelfHealer) CheckQMDCLI(ctx context.Context) (model.HealthLevel, string) {
	_, err := s.exec.Version(ctx)
	if err != nil {
		if _, statErr := os.Stat(s.cfg.QMD.Bin); statErr != nil {
			return model.Critical, "qmd binary not found: " + s.cfg.QMD.Bin
		}
		return model.Unhealthy, "qmd cli not responding: " + err.Error()
	}
	return model.Healthy, ""
}

func (s *SelfHealer) CheckIndexDB(_ context.Context) (model.HealthLevel, string) {
	if s.cfg.QMD.IndexDB == "" {
		return model.Healthy, ""
	}
	info, err := os.Stat(s.cfg.QMD.IndexDB)
	if err != nil {
		return model.Critical, "index database not found: " + s.cfg.QMD.IndexDB
	}
	if info.Size() == 0 {
		return model.Critical, "index database is empty: " + s.cfg.QMD.IndexDB
	}
	return model.Healthy, ""
}

func (s *SelfHealer) CheckEmbeddings(ctx context.Context) (model.HealthLevel, string) {
	if !s.exec.HasCapability("status") {
		return model.Healthy, "status capability not available, skipping embed check"
	}
	status, err := s.exec.Status(ctx)
	if err != nil {
		return model.Degraded, "cannot check embeddings: " + err.Error()
	}
	if status.Vectors == 0 {
		return model.Degraded, "no embeddings found, vsearch/query may not work"
	}
	return model.Healthy, ""
}
