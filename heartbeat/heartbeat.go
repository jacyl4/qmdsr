package heartbeat

import (
	"context"
	"log/slog"
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
