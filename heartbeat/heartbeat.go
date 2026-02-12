package heartbeat

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"qmdsr/model"
)

type ComponentChecker func(ctx context.Context) (model.HealthLevel, string)

type Heartbeat struct {
	checkers map[string]ComponentChecker
	health   *SystemHealthTracker
	log      *slog.Logger
	interval time.Duration
	cancel   context.CancelFunc
}

func New(interval time.Duration, logger *slog.Logger) *Heartbeat {
	return &Heartbeat{
		checkers: make(map[string]ComponentChecker),
		health:   NewSystemHealthTracker(),
		log:      logger,
		interval: interval,
	}
}

func (h *Heartbeat) Register(name string, checker ComponentChecker) {
	h.checkers[name] = checker
}

func (h *Heartbeat) Start(ctx context.Context) {
	ctx, h.cancel = context.WithCancel(ctx)
	go h.loop(ctx)
	h.log.Info("heartbeat started", "interval", h.interval, "components", len(h.checkers))
}

func (h *Heartbeat) Stop() {
	if h.cancel != nil {
		h.cancel()
	}
}

func (h *Heartbeat) GetHealth() *model.SystemHealth {
	return h.health.GetHealth()
}

func (h *Heartbeat) loop(ctx context.Context) {
	h.runChecks(ctx)

	ticker := time.NewTicker(h.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			h.runChecks(ctx)
		}
	}
}

func (h *Heartbeat) runChecks(ctx context.Context) {
	for name, checker := range h.checkers {
		level, msg := checker(ctx)
		prev := h.health.GetComponentLevel(name)

		h.health.Update(name, level, msg)

		if prev != level {
			h.logTransition(name, prev, level, msg)
		}
	}
}

func (h *Heartbeat) logTransition(name string, from, to model.HealthLevel, msg string) {
	switch {
	case to == model.Healthy && from > model.Healthy:
		h.log.Info("component recovered", "component", name, "from", from.String())
	case to == model.Critical:
		h.log.Error("component critical", "component", name, "message", msg)
	case to == model.Unhealthy:
		h.log.Error("component unhealthy", "component", name, "message", msg)
	case to == model.Degraded:
		h.log.Warn("component degraded", "component", name, "message", msg)
	}
}

type SystemHealthTracker struct {
	mu         sync.RWMutex
	components map[string]*model.ComponentHealth
	startedAt  time.Time
}

func NewSystemHealthTracker() *SystemHealthTracker {
	return &SystemHealthTracker{
		components: make(map[string]*model.ComponentHealth),
		startedAt:  time.Now(),
	}
}

func (s *SystemHealthTracker) Update(name string, level model.HealthLevel, msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	comp, ok := s.components[name]
	if !ok {
		comp = &model.ComponentHealth{Name: name}
		s.components[name] = comp
	}

	comp.Level = level
	comp.LevelStr = level.String()
	comp.LastCheck = time.Now()
	comp.Message = msg

	if level == model.Healthy {
		comp.LastHealthy = time.Now()
		comp.FailCount = 0
	} else {
		comp.FailCount++
	}
}

func (s *SystemHealthTracker) GetComponentLevel(name string) model.HealthLevel {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if comp, ok := s.components[name]; ok {
		return comp.Level
	}
	return model.Healthy
}

func (s *SystemHealthTracker) GetHealth() *model.SystemHealth {
	s.mu.RLock()
	defer s.mu.RUnlock()

	overall := model.Healthy
	comps := make(map[string]*model.ComponentHealth)

	for name, comp := range s.components {
		c := *comp
		comps[name] = &c
		if comp.Level > overall {
			overall = comp.Level
		}
	}

	mode := "normal"
	switch overall {
	case model.Degraded:
		mode = "cli_fallback"
	case model.Unhealthy:
		mode = "degraded"
	case model.Critical:
		mode = "critical"
	}

	return &model.SystemHealth{
		Overall:    overall,
		OverallStr: overall.String(),
		Components: comps,
		StartedAt:  s.startedAt,
		UptimeSec:  int64(time.Since(s.startedAt).Seconds()),
		Mode:       mode,
	}
}
