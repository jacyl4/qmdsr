package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	QMD         QMDConfig       `yaml:"qmd"`
	Server      ServerConfig    `yaml:"server"`
	Collections []CollectionCfg `yaml:"collections"`
	Search      SearchConfig    `yaml:"search"`
	Cache       CacheConfig     `yaml:"cache"`
	Scheduler   SchedulerConfig `yaml:"scheduler"`
	Guardian    GuardianConfig  `yaml:"guardian"`
	Logging     LoggingConfig   `yaml:"logging"`
	Runtime     RuntimeConfig   `yaml:"runtime"`
}

type QMDConfig struct {
	Bin     string `yaml:"bin"`
	IndexDB string `yaml:"index_db"`
	MCPPort int    `yaml:"mcp_port"`
}

type ServerConfig struct {
	GRPCListen    string `yaml:"grpc_listen"`
	SecurityModel string `yaml:"security_model"`
}

type CollectionCfg struct {
	Name            string   `yaml:"name"`
	Path            string   `yaml:"path"`
	Mask            string   `yaml:"mask"`
	Exclude         []string `yaml:"exclude"`
	Context         string   `yaml:"context"`
	Tier            int      `yaml:"tier"`
	Embed           bool     `yaml:"embed"`
	RequireExplicit bool     `yaml:"require_explicit"`
	SafetyPrompt    bool     `yaml:"safety_prompt"`
}

type SearchConfig struct {
	DefaultMode     string  `yaml:"default_mode"`
	CoarseK         int     `yaml:"coarse_k"`
	TopK            int     `yaml:"top_k"`
	MinScore        float64 `yaml:"min_score"`
	MaxChars        int     `yaml:"max_chars"`
	FallbackEnabled bool    `yaml:"fallback_enabled"`
}

type CacheConfig struct {
	Enabled         bool          `yaml:"enabled"`
	TTL             time.Duration `yaml:"ttl"`
	MaxEntries      int           `yaml:"max_entries"`
	CleanupInterval time.Duration `yaml:"cleanup_interval"`
	VersionAware    bool          `yaml:"version_aware"`
}

type SchedulerConfig struct {
	IndexRefresh     time.Duration `yaml:"index_refresh"`
	EmbedRefresh     time.Duration `yaml:"embed_refresh"`
	EmbedFullRefresh time.Duration `yaml:"embed_full_refresh"`
	CacheCleanup     time.Duration `yaml:"cache_cleanup"`
}

type GuardianConfig struct {
	CheckInterval     time.Duration `yaml:"check_interval"`
	Timeout           time.Duration `yaml:"timeout"`
	RestartMaxRetries int           `yaml:"restart_max_retries"`
}

type LoggingConfig struct {
	Level      string `yaml:"level"`
	File       string `yaml:"file"`
	MaxSize    string `yaml:"max_size"`
	MaxBackups int    `yaml:"max_backups"`
}

type RuntimeConfig struct {
	LowResourceMode        bool          `yaml:"low_resource_mode"`
	AllowCPUDeepQuery      bool          `yaml:"allow_cpu_deep_query"`
	SmartRouting           bool          `yaml:"smart_routing"`
	CPUDeepMinWords        int           `yaml:"cpu_deep_min_words"`
	CPUDeepMinChars        int           `yaml:"cpu_deep_min_chars"`
	CPUDeepMaxWords        int           `yaml:"cpu_deep_max_words"`
	CPUDeepMaxChars        int           `yaml:"cpu_deep_max_chars"`
	CPUDeepMaxAbstractCues int           `yaml:"cpu_deep_max_abstract_cues"`
	QueryMaxConcurrency    int           `yaml:"query_max_concurrency"`
	QueryTimeout           time.Duration `yaml:"query_timeout"`
	DeepFailTimeout        time.Duration `yaml:"deep_fail_timeout"`
	DeepNegativeTTL        time.Duration `yaml:"deep_negative_ttl"`
}

func Load(path string) (*Config, error) {
	path = expandPath(path)

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	cfg.normalize()

	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("validate config: %w", err)
	}

	return &cfg, nil
}

func (c *Config) normalize() {
	c.QMD.Bin = expandClean(c.QMD.Bin)
	c.QMD.IndexDB = expandClean(c.QMD.IndexDB)
	c.Logging.File = expandClean(c.Logging.File)

	for i := range c.Collections {
		c.Collections[i].Path = expandClean(c.Collections[i].Path)
	}

	if c.Server.GRPCListen == "" {
		c.Server.GRPCListen = "127.0.0.1:19091"
	}
	if c.Server.SecurityModel == "" {
		c.Server.SecurityModel = "loopback_trust"
	}
	if c.QMD.MCPPort == 0 {
		c.QMD.MCPPort = 8181
	}
	if c.Search.DefaultMode == "" {
		c.Search.DefaultMode = "auto"
	}
	if c.Search.CoarseK == 0 {
		c.Search.CoarseK = 20
	}
	if c.Search.TopK == 0 {
		c.Search.TopK = 8
	}
	if c.Search.MinScore == 0 {
		c.Search.MinScore = 0.3
	}
	if c.Search.MaxChars == 0 {
		c.Search.MaxChars = 9000
	}
	if c.Cache.TTL == 0 {
		c.Cache.TTL = 30 * time.Minute
	}
	if c.Cache.MaxEntries == 0 {
		c.Cache.MaxEntries = 500
	}
	if c.Cache.CleanupInterval == 0 {
		c.Cache.CleanupInterval = time.Hour
	}
	if c.Scheduler.IndexRefresh == 0 {
		c.Scheduler.IndexRefresh = 30 * time.Minute
	}
	if c.Scheduler.EmbedRefresh == 0 {
		c.Scheduler.EmbedRefresh = 24 * time.Hour
	}
	if c.Scheduler.EmbedFullRefresh == 0 {
		c.Scheduler.EmbedFullRefresh = 7 * 24 * time.Hour
	}
	if c.Scheduler.CacheCleanup == 0 {
		c.Scheduler.CacheCleanup = time.Hour
	}
	if c.Guardian.CheckInterval == 0 {
		c.Guardian.CheckInterval = 60 * time.Second
	}
	if c.Guardian.Timeout == 0 {
		c.Guardian.Timeout = 5 * time.Second
	}
	if c.Guardian.RestartMaxRetries == 0 {
		c.Guardian.RestartMaxRetries = 3
	}
	if c.Logging.Level == "" {
		c.Logging.Level = "info"
	}

	c.applyRuntimeDefaults()
}

func (c *Config) applyRuntimeDefaults() {
	queryTimeoutUnset := c.Runtime.QueryTimeout == 0
	queryConcurrencyUnset := c.Runtime.QueryMaxConcurrency == 0
	deepFailTimeoutUnset := c.Runtime.DeepFailTimeout == 0
	deepNegativeTTLUnset := c.Runtime.DeepNegativeTTL == 0

	if queryTimeoutUnset {
		c.Runtime.QueryTimeout = 120 * time.Second
	}
	if queryConcurrencyUnset {
		c.Runtime.QueryMaxConcurrency = 2
	}
	if deepFailTimeoutUnset {
		c.Runtime.DeepFailTimeout = 15 * time.Second
	}
	if deepNegativeTTLUnset {
		c.Runtime.DeepNegativeTTL = 10 * time.Minute
	}

	if c.Runtime.LowResourceMode && c.Runtime.AllowCPUDeepQuery {
		c.applyLowResourceRuntimeProfile(queryTimeoutUnset, queryConcurrencyUnset, deepFailTimeoutUnset, deepNegativeTTLUnset)
	}
}

func (c *Config) applyLowResourceRuntimeProfile(queryTimeoutUnset, queryConcurrencyUnset, deepFailTimeoutUnset, deepNegativeTTLUnset bool) {
	c.Runtime.SmartRouting = true

	if c.Runtime.CPUDeepMinWords == 0 {
		c.Runtime.CPUDeepMinWords = 10
	}
	if c.Runtime.CPUDeepMinChars == 0 {
		c.Runtime.CPUDeepMinChars = 24
	}
	if c.Runtime.CPUDeepMaxWords == 0 {
		c.Runtime.CPUDeepMaxWords = 28
	}
	if c.Runtime.CPUDeepMaxChars == 0 {
		c.Runtime.CPUDeepMaxChars = 160
	}
	if c.Runtime.CPUDeepMaxAbstractCues == 0 {
		c.Runtime.CPUDeepMaxAbstractCues = 2
	}
	if queryConcurrencyUnset {
		c.Runtime.QueryMaxConcurrency = 1
	}
	if queryTimeoutUnset {
		c.Runtime.QueryTimeout = 45 * time.Second
	}
	if deepFailTimeoutUnset {
		c.Runtime.DeepFailTimeout = 12 * time.Second
	}
	if deepNegativeTTLUnset {
		c.Runtime.DeepNegativeTTL = 15 * time.Minute
	}
}

func (c *Config) validate() error {
	if c.QMD.Bin == "" {
		return fmt.Errorf("qmd.bin is required")
	}
	if _, err := os.Stat(c.QMD.Bin); err != nil {
		return fmt.Errorf("qmd binary not found at %s: %w", c.QMD.Bin, err)
	}
	if len(c.Collections) == 0 {
		return fmt.Errorf("at least one collection is required")
	}
	for _, col := range c.Collections {
		if col.Name == "" {
			return fmt.Errorf("collection name is required")
		}
		if col.Path == "" {
			return fmt.Errorf("collection %s: path is required", col.Name)
		}
		if col.Tier == 0 {
			return fmt.Errorf("collection %s: tier is required", col.Name)
		}
	}
	return nil
}

func expandPath(p string) string {
	p = os.ExpandEnv(p)
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(home, p[2:])
		}
	}
	return filepath.Clean(p)
}

func expandClean(p string) string {
	if p == "" {
		return ""
	}
	return expandPath(p)
}
