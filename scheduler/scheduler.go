package scheduler

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"qmdsr/cache"
	"qmdsr/config"
	"qmdsr/executor"
)

type Scheduler struct {
	cfg   *config.Config
	exec  executor.Executor
	cache *cache.Cache
	log   *slog.Logger

	mu        sync.Mutex
	running   map[string]bool
	cancel    context.CancelFunc
	lastEmbed time.Time
	lastFull  time.Time
}

func New(cfg *config.Config, exec executor.Executor, c *cache.Cache, logger *slog.Logger) *Scheduler {
	return &Scheduler{
		cfg:     cfg,
		exec:    exec,
		cache:   c,
		log:     logger,
		running: make(map[string]bool),
	}
}

func (s *Scheduler) Start(ctx context.Context) {
	ctx, s.cancel = context.WithCancel(ctx)

	go s.loop(ctx, "index_refresh", s.cfg.Scheduler.IndexRefresh, s.taskReindex)
	go s.loop(ctx, "embed_refresh", s.cfg.Scheduler.EmbedRefresh, s.taskEmbed)
	go s.loop(ctx, "embed_full_refresh", s.cfg.Scheduler.EmbedFullRefresh, s.taskEmbedFull)
	go s.loop(ctx, "cache_cleanup", s.cfg.Scheduler.CacheCleanup, s.taskCacheCleanup)

	s.log.Info("scheduler started",
		"index_refresh", s.cfg.Scheduler.IndexRefresh,
		"embed_refresh", s.cfg.Scheduler.EmbedRefresh,
		"embed_full_refresh", s.cfg.Scheduler.EmbedFullRefresh,
		"cache_cleanup", s.cfg.Scheduler.CacheCleanup,
	)
}

func (s *Scheduler) Stop() {
	if s.cancel != nil {
		s.cancel()
	}
}

func (s *Scheduler) TriggerReindex(ctx context.Context) error {
	return s.runTask("index_refresh", func() error {
		return s.taskReindex(ctx)
	})
}

func (s *Scheduler) TriggerEmbed(ctx context.Context, force bool) error {
	name := "embed_refresh"
	if force {
		name = "embed_full_refresh"
	}
	return s.runTask(name, func() error {
		if force {
			return s.taskEmbedFull(ctx)
		}
		return s.taskEmbed(ctx)
	})
}

func (s *Scheduler) loop(ctx context.Context, name string, interval time.Duration, task func(context.Context) error) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.runTask(name, func() error { return task(ctx) }); err != nil {
				s.log.Error("scheduled task failed", "task", name, "err", err)
			}
		}
	}
}

func (s *Scheduler) runTask(name string, fn func() error) error {
	s.mu.Lock()
	if s.running[name] {
		s.mu.Unlock()
		s.log.Debug("task already running, skipping", "task", name)
		return nil
	}
	s.running[name] = true
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		delete(s.running, name)
		s.mu.Unlock()
	}()

	s.log.Info("running scheduled task", "task", name)
	start := time.Now()
	err := fn()
	elapsed := time.Since(start)

	if err != nil {
		s.log.Error("task failed", "task", name, "elapsed", elapsed, "err", err)
		return s.retry(name, fn, 3)
	}

	s.log.Info("task completed", "task", name, "elapsed", elapsed)
	return nil
}

func (s *Scheduler) retry(name string, fn func() error, maxRetries int) error {
	var lastErr error
	for i := 1; i <= maxRetries; i++ {
		delay := time.Duration(i*i) * time.Second
		s.log.Info("retrying task", "task", name, "attempt", i, "delay", delay)
		time.Sleep(delay)

		if err := fn(); err != nil {
			lastErr = err
			s.log.Warn("retry failed", "task", name, "attempt", i, "err", err)
			continue
		}
		s.log.Info("retry succeeded", "task", name, "attempt", i)
		return nil
	}
	return lastErr
}

func (s *Scheduler) taskReindex(ctx context.Context) error {
	if err := s.exec.Update(ctx); err != nil {
		return err
	}
	version := time.Now().Format("20060102150405")
	s.cache.SetVersion(version)
	s.log.Info("index refreshed, cache version updated", "version", version)
	return nil
}

func (s *Scheduler) taskEmbed(ctx context.Context) error {
	s.lastEmbed = time.Now()
	return s.exec.Embed(ctx, false)
}

func (s *Scheduler) taskEmbedFull(ctx context.Context) error {
	s.lastFull = time.Now()
	return s.exec.Embed(ctx, true)
}

func (s *Scheduler) taskCacheCleanup(_ context.Context) error {
	removed := s.cache.Cleanup()
	if removed > 0 {
		s.log.Info("cache cleanup", "removed", removed)
	}
	return nil
}
