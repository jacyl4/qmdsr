package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"qmdsr/api"
	"qmdsr/cache"
	"qmdsr/config"
	"qmdsr/executor"
	"qmdsr/guardian"
	"qmdsr/heartbeat"
	"qmdsr/internal/version"
	"qmdsr/model"
	"qmdsr/orchestrator"
	"qmdsr/scheduler"
)

func main() {
	configPath := flag.String("config", "/etc/qmdsr/qmdsr.yaml", "path to config file")
	showVersionShort := flag.Bool("v", false, "print version information")
	showVersion := flag.Bool("version", false, "print version information")
	flag.Parse()

	if *showVersionShort || *showVersion {
		fmt.Printf(
			"qmdsr %s\ncommit: %s\nbuild: %s\n",
			version.Version,
			version.Commit,
			version.BuildTime,
		)
		return
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		slog.Error("failed to load config", "err", err)
		os.Exit(1)
	}

	logger := setupLogger(cfg)

	startupAttrs := []any{
		"grpc_listen", cfg.Server.GRPCListen,
		"collections", len(cfg.Collections),
		"low_resource_mode", cfg.Runtime.LowResourceMode,
		"allow_cpu_deep_query", cfg.Runtime.AllowCPUDeepQuery,
		"allow_cpu_vsearch", cfg.Runtime.AllowCPUVSearch,
		"smart_routing", cfg.Runtime.SmartRouting,
		"query_max_concurrency", cfg.Runtime.QueryMaxConcurrency,
		"query_timeout", cfg.Runtime.QueryTimeout,
		"deep_fail_timeout", cfg.Runtime.DeepFailTimeout,
		"deep_negative_ttl", cfg.Runtime.DeepNegativeTTL,
		"deep_negative_scope_cooldown", cfg.Runtime.DeepNegativeScopeCooldown,
		"cpu_overload_protect", cfg.Runtime.CPUOverloadProtect,
		"cpu_overload_threshold", cfg.Runtime.CPUOverloadThreshold,
		"cpu_overload_sustain", cfg.Runtime.CPUOverloadSustain,
		"cpu_recover_threshold", cfg.Runtime.CPURecoverThreshold,
		"cpu_recover_sustain", cfg.Runtime.CPURecoverSustain,
		"cpu_critical_threshold", cfg.Runtime.CPUCriticalThreshold,
		"cpu_critical_sustain", cfg.Runtime.CPUCriticalSustain,
		"overload_max_concurrent_search", cfg.Runtime.OverloadMaxConcurrentSearch,
		"cpu_sample_interval", cfg.Runtime.CPUSampleInterval,
		"version", version.Version,
		"commit", version.Commit,
		"build_time", version.BuildTime,
	}
	if cfg.Runtime.AllowCPUDeepQuery {
		startupAttrs = append(startupAttrs,
			"cpu_deep_min_words", cfg.Runtime.CPUDeepMinWords,
			"cpu_deep_min_chars", cfg.Runtime.CPUDeepMinChars,
			"cpu_deep_max_words", cfg.Runtime.CPUDeepMaxWords,
			"cpu_deep_max_chars", cfg.Runtime.CPUDeepMaxChars,
			"cpu_deep_max_abstract_cues", cfg.Runtime.CPUDeepMaxAbstractCues,
		)
	}
	logger.Info("qmdsr starting", startupAttrs...)

	exec, err := executor.NewCLI(cfg, logger.With("component", "executor"))
	if err != nil {
		logger.Error("failed to initialize executor", "err", err)
		os.Exit(1)
	}
	if !exec.HasCapability("deep_query") && !exec.HasCapability("vector") {
		logger.Warn("both deep_query and vector are disabled; running in BM25-only mode")
	}

	c := cache.New(&cfg.Cache)

	orch := orchestrator.New(cfg, exec, c, logger.With("component", "orchestrator"))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	orch.Start(ctx)

	if err := orch.EnsureCollections(ctx); err != nil {
		logger.Error("failed to ensure collections", "err", err)
	}

	sched := scheduler.New(cfg, exec, c, orch.CleanupDeepNegativeCache, logger.With("component", "scheduler"))
	sched.Start(ctx)

	guard := guardian.New(cfg, exec, logger.With("component", "guardian"))
	guard.Start(ctx)

	healer := heartbeat.NewSelfHealer(cfg, exec, logger.With("component", "selfheal"))
	hb := heartbeat.New(60*time.Second, logger.With("component", "heartbeat"))

	hb.Register("qmd_cli", healer.CheckQMDCLI)
	hb.Register("index_db", healer.CheckIndexDB)
	hb.Register("embeddings", healer.CheckEmbeddings)
	hb.Register("cache", func(_ context.Context) (model.HealthLevel, string) {
		if c.Healthy() {
			return model.Healthy, ""
		}
		return model.Unhealthy, "cache unhealthy"
	})
	hb.Register("mcp_daemon", func(_ context.Context) (model.HealthLevel, string) {
		return guard.Health()
	})
	hb.Start(ctx)

	srv := api.NewServer(api.Deps{
		Config:       cfg,
		Orchestrator: orch,
		Executor:     exec,
		Scheduler:    sched,
		Guardian:     guard,
		Heartbeat:    hb,
		Logger:       logger.With("component", "api"),
	})

	if err := srv.Start(); err != nil {
		logger.Error("gRPC server start failed", "err", err)
		os.Exit(1)
	}

	go watchdog()

	logger.Info("qmdsr ready",
		"grpc_listen", cfg.Server.GRPCListen,
		"pid", os.Getpid(),
	)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig

	logger.Info("shutting down...")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	sched.Stop()
	guard.Stop()
	hb.Stop()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("gRPC server shutdown error", "err", err)
	}

	cancel()
	logger.Info("qmdsr stopped")
}

func setupLogger(cfg *config.Config) *slog.Logger {
	var level slog.Level
	switch cfg.Logging.Level {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{Level: level}

	if cfg.Logging.File != "" {
		dir := filepath.Dir(cfg.Logging.File)
		os.MkdirAll(dir, 0755)

		f, err := os.OpenFile(cfg.Logging.File, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			slog.Error("failed to open log file, using stderr", "err", err)
			return slog.New(slog.NewJSONHandler(os.Stderr, opts))
		}
		return slog.New(slog.NewJSONHandler(f, opts))
	}

	return slog.New(slog.NewJSONHandler(os.Stderr, opts))
}

func watchdog() {
	sock := os.Getenv("NOTIFY_SOCKET")
	if sock == "" {
		return
	}

	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		conn, err := syscall.Socket(syscall.AF_UNIX, syscall.SOCK_DGRAM, 0)
		if err != nil {
			continue
		}
		sa := &syscall.SockaddrUnix{Name: sock}
		_ = syscall.Sendmsg(conn, []byte("WATCHDOG=1"), nil, sa, 0)
		syscall.Close(conn)
	}
}
