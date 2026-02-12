package heartbeat

import (
	"sync"
	"time"

	"qmdsr/model"
)

type SystemHealthTracker struct {
	mu        sync.RWMutex
	components map[string]*model.ComponentHealth
	startedAt time.Time
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
