package guardian

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"qmdsr/config"
	"qmdsr/executor"
	"qmdsr/model"
)

type Guardian struct {
	cfg    *config.Config
	exec   executor.Executor
	log    *slog.Logger

	mu            sync.RWMutex
	health        model.HealthLevel
	lastCheck     time.Time
	lastHealthy   time.Time
	failCount     int
	restartCount  int
	cliMode       bool
	cancel        context.CancelFunc
}

func New(cfg *config.Config, exec executor.Executor, logger *slog.Logger) *Guardian {
	return &Guardian{
		cfg:  cfg,
		exec: exec,
		log:  logger,
	}
}

func (g *Guardian) Start(ctx context.Context) {
	if !g.exec.HasCapability("mcp") {
		g.log.Warn("MCP not available, guardian disabled, using CLI mode only")
		g.mu.Lock()
		g.cliMode = true
		g.health = model.Degraded
		g.mu.Unlock()
		return
	}

	ctx, g.cancel = context.WithCancel(ctx)

	if err := g.initialCheck(ctx); err != nil {
		g.log.Warn("MCP not healthy on startup, attempting start", "err", err)
		if err := g.startMCP(ctx); err != nil {
			g.log.Error("failed to start MCP daemon", "err", err)
			g.mu.Lock()
			g.cliMode = true
			g.health = model.Degraded
			g.mu.Unlock()
		}
	}

	go g.loop(ctx)
	g.log.Info("guardian started", "interval", g.cfg.Guardian.CheckInterval)
}

func (g *Guardian) Stop() {
	if g.cancel != nil {
		g.cancel()
	}
}

func (g *Guardian) Health() (model.HealthLevel, string) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	msg := ""
	if g.cliMode {
		msg = "MCP unavailable, using CLI fallback"
	}
	if g.failCount > 0 {
		msg = fmt.Sprintf("MCP health check failed %d times", g.failCount)
	}
	return g.health, msg
}

func (g *Guardian) IsCLIMode() bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.cliMode
}

func (g *Guardian) RestartMCP(ctx context.Context) error {
	g.log.Info("manual MCP restart requested")
	return g.restartMCP(ctx)
}

func (g *Guardian) initialCheck(ctx context.Context) error {
	checkCtx, cancel := context.WithTimeout(ctx, g.cfg.Guardian.Timeout)
	defer cancel()
	return g.exec.MCPHealth(checkCtx)
}

func (g *Guardian) loop(ctx context.Context) {
	ticker := time.NewTicker(g.cfg.Guardian.CheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			g.check(ctx)
		}
	}
}

func (g *Guardian) check(ctx context.Context) {
	checkCtx, cancel := context.WithTimeout(ctx, g.cfg.Guardian.Timeout)
	defer cancel()

	err := g.exec.MCPHealth(checkCtx)
	g.mu.Lock()
	defer g.mu.Unlock()

	g.lastCheck = time.Now()

	if err == nil {
		if g.failCount > 0 || g.cliMode {
			g.log.Info("MCP daemon recovered")
		}
		g.health = model.Healthy
		g.lastHealthy = time.Now()
		g.failCount = 0
		g.cliMode = false
		return
	}

	g.failCount++
	g.log.Warn("MCP health check failed", "fail_count", g.failCount, "err", err)

	if g.failCount >= g.cfg.Guardian.RestartMaxRetries {
		g.log.Error("MCP daemon unrecoverable, switching to CLI mode",
			"fail_count", g.failCount,
			"max_retries", g.cfg.Guardian.RestartMaxRetries)
		g.health = model.Degraded
		g.cliMode = true
		g.failCount = 0
		return
	}

	g.health = model.Unhealthy

	go func() {
		if err := g.restartMCP(ctx); err != nil {
			g.log.Error("MCP restart failed", "err", err)
		}
	}()
}

func (g *Guardian) startMCP(ctx context.Context) error {
	if err := g.exec.MCPStart(ctx); err != nil {
		return fmt.Errorf("start MCP: %w", err)
	}
	time.Sleep(2 * time.Second)

	checkCtx, cancel := context.WithTimeout(ctx, g.cfg.Guardian.Timeout)
	defer cancel()
	if err := g.exec.MCPHealth(checkCtx); err != nil {
		return fmt.Errorf("MCP started but not healthy: %w", err)
	}

	g.mu.Lock()
	g.health = model.Healthy
	g.lastHealthy = time.Now()
	g.cliMode = false
	g.mu.Unlock()

	g.log.Info("MCP daemon started successfully")
	return nil
}

func (g *Guardian) restartMCP(ctx context.Context) error {
	g.mu.Lock()
	g.restartCount++
	g.mu.Unlock()

	g.log.Info("restarting MCP daemon")

	_ = g.exec.MCPStop(ctx)
	time.Sleep(1 * time.Second)

	return g.startMCP(ctx)
}
