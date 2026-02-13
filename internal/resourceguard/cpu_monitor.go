package resourceguard

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

type CPUMonitorConfig struct {
	Enabled         bool
	SampleInterval  time.Duration
	OverloadPercent int
	OverloadSustain time.Duration
	RecoverPercent  int
	RecoverSustain  time.Duration
	CriticalPercent int
	CriticalSustain time.Duration
}

type CPUSnapshot struct {
	Overloaded         bool
	CriticalOverloaded bool
	UsagePct           float64
	UpdatedAt          time.Time
}

type CPUMonitor struct {
	cfg CPUMonitorConfig
	log *slog.Logger

	mu         sync.RWMutex
	overloaded bool
	critical   bool
	usagePct   float64
	updatedAt  time.Time
}

func NewCPUMonitor(cfg CPUMonitorConfig, log *slog.Logger) *CPUMonitor {
	if cfg.SampleInterval <= 0 {
		cfg.SampleInterval = time.Second
	}
	if cfg.OverloadPercent <= 0 {
		cfg.OverloadPercent = 90
	}
	if cfg.OverloadSustain <= 0 {
		cfg.OverloadSustain = 10 * time.Second
	}
	if cfg.RecoverPercent <= 0 {
		cfg.RecoverPercent = 75
	}
	if cfg.RecoverSustain <= 0 {
		cfg.RecoverSustain = 12 * time.Second
	}
	if cfg.CriticalPercent <= 0 {
		cfg.CriticalPercent = 95
	}
	if cfg.CriticalSustain <= 0 {
		cfg.CriticalSustain = 5 * time.Second
	}
	if cfg.CriticalPercent < cfg.OverloadPercent {
		cfg.CriticalPercent = cfg.OverloadPercent
	}
	return &CPUMonitor{
		cfg: cfg,
		log: log,
	}
}

func (m *CPUMonitor) Start(ctx context.Context) {
	if !m.cfg.Enabled {
		return
	}
	go m.loop(ctx)
}

// Snapshot exposes current guard state for future Status/Health diagnostics.
func (m *CPUMonitor) Snapshot() CPUSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return CPUSnapshot{
		Overloaded:         m.overloaded,
		CriticalOverloaded: m.critical,
		UsagePct:           m.usagePct,
		UpdatedAt:          m.updatedAt,
	}
}

func (m *CPUMonitor) IsOverloaded() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.overloaded
}

func (m *CPUMonitor) IsCriticalOverloaded() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.critical
}

func (m *CPUMonitor) loop(ctx context.Context) {
	ticker := time.NewTicker(m.cfg.SampleInterval)
	defer ticker.Stop()

	prevIdle, prevTotal, err := readSystemCPUJiffies()
	if err != nil {
		m.log.Warn("cpu monitor disabled: failed to read /proc/stat", "err", err)
		return
	}

	var aboveCount int
	var belowCount int
	var criticalAboveCount int
	var criticalBelowCount int

	needAboveSamples := requiredSamples(m.cfg.OverloadSustain, m.cfg.SampleInterval)
	needBelowSamples := requiredSamples(m.cfg.RecoverSustain, m.cfg.SampleInterval)
	needCriticalSamples := requiredSamples(m.cfg.CriticalSustain, m.cfg.SampleInterval)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			idle, total, err := readSystemCPUJiffies()
			if err != nil {
				m.log.Debug("cpu monitor sample failed", "err", err)
				continue
			}
			dIdle := idle - prevIdle
			dTotal := total - prevTotal
			prevIdle, prevTotal = idle, total
			if dTotal <= 0 {
				continue
			}

			usage := (float64(dTotal-dIdle) / float64(dTotal)) * 100.0
			m.setUsage(usage)

			switch {
			case usage >= float64(m.cfg.OverloadPercent):
				aboveCount++
				belowCount = 0
				if aboveCount >= needAboveSamples {
					m.setOverloaded(true, usage)
				}
			case usage <= float64(m.cfg.RecoverPercent):
				belowCount++
				aboveCount = 0
				if belowCount >= needBelowSamples {
					m.setOverloaded(false, usage)
				}
			default:
				aboveCount = 0
				belowCount = 0
			}

			switch {
			case usage >= float64(m.cfg.CriticalPercent):
				criticalAboveCount++
				criticalBelowCount = 0
				if criticalAboveCount >= needCriticalSamples {
					m.setCritical(true, usage)
				}
			// Critical recovery intentionally reuses the overload threshold to avoid
			// flapping between overloaded and critical states when usage hovers in 90-95%.
			case usage <= float64(m.cfg.OverloadPercent):
				criticalBelowCount++
				criticalAboveCount = 0
				// Critical recovery also reuses the standard recover sustain window so
				// activation is fast (critical_sustain) but recovery is conservative.
				if criticalBelowCount >= needBelowSamples {
					m.setCritical(false, usage)
				}
			default:
				criticalAboveCount = 0
				criticalBelowCount = 0
			}
		}
	}
}

func (m *CPUMonitor) setUsage(usage float64) {
	m.mu.Lock()
	m.usagePct = usage
	m.updatedAt = time.Now()
	m.mu.Unlock()
}

func (m *CPUMonitor) setOverloaded(on bool, usage float64) {
	m.mu.Lock()
	changed := m.overloaded != on
	m.overloaded = on
	m.usagePct = usage
	m.updatedAt = time.Now()
	m.mu.Unlock()

	if !changed {
		return
	}
	if on {
		m.log.Warn("cpu overload protection activated", "usage_pct", fmt.Sprintf("%.2f", usage), "threshold_pct", m.cfg.OverloadPercent)
		return
	}
	m.log.Info("cpu overload protection recovered", "usage_pct", fmt.Sprintf("%.2f", usage), "recover_pct", m.cfg.RecoverPercent)
}

func (m *CPUMonitor) setCritical(on bool, usage float64) {
	m.mu.Lock()
	changed := m.critical != on
	m.critical = on
	m.usagePct = usage
	m.updatedAt = time.Now()
	m.mu.Unlock()

	if !changed {
		return
	}
	if on {
		m.log.Error("cpu critical overload activated", "usage_pct", fmt.Sprintf("%.2f", usage), "critical_pct", m.cfg.CriticalPercent)
		return
	}
	m.log.Info("cpu critical overload recovered", "usage_pct", fmt.Sprintf("%.2f", usage), "critical_pct", m.cfg.CriticalPercent)
}

func requiredSamples(duration, interval time.Duration) int {
	if duration <= 0 || interval <= 0 {
		return 1
	}
	n := int(duration / interval)
	if duration%interval != 0 {
		n++
	}
	if n < 1 {
		return 1
	}
	return n
}

func readSystemCPUJiffies() (idle uint64, total uint64, err error) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "cpu ") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 5 {
			return 0, 0, fmt.Errorf("unexpected cpu line: %q", line)
		}

		var vals []uint64
		for _, s := range fields[1:] {
			v, convErr := strconv.ParseUint(s, 10, 64)
			if convErr != nil {
				return 0, 0, convErr
			}
			vals = append(vals, v)
		}
		for _, v := range vals {
			total += v
		}
		idle = vals[3]
		if len(vals) > 4 {
			idle += vals[4]
		}
		return idle, total, nil
	}
	if err := scanner.Err(); err != nil {
		return 0, 0, err
	}
	return 0, 0, fmt.Errorf("cpu line not found in /proc/stat")
}
