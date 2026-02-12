package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"

	"qmdsr/config"
	"qmdsr/executor"
	"qmdsr/guardian"
	"qmdsr/heartbeat"
	"qmdsr/orchestrator"
	"qmdsr/scheduler"

	"google.golang.org/grpc"
)

type Server struct {
	cfg       *config.Config
	orch      *orchestrator.Orchestrator
	exec      executor.Executor
	sched     *scheduler.Scheduler
	guardian  *guardian.Guardian
	heartbeat *heartbeat.Heartbeat
	log       *slog.Logger

	grpcServer *grpc.Server
}

type Deps struct {
	Config       *config.Config
	Orchestrator *orchestrator.Orchestrator
	Executor     executor.Executor
	Scheduler    *scheduler.Scheduler
	Guardian     *guardian.Guardian
	Heartbeat    *heartbeat.Heartbeat
	Logger       *slog.Logger
}

func NewServer(deps Deps) *Server {
	return &Server{
		cfg:       deps.Config,
		orch:      deps.Orchestrator,
		exec:      deps.Executor,
		sched:     deps.Scheduler,
		guardian:  deps.Guardian,
		heartbeat: deps.Heartbeat,
		log:       deps.Logger,
	}
}

func (s *Server) Start() error {
	return s.startGRPC()
}

func (s *Server) Shutdown(ctx context.Context) error {
	if s.grpcServer == nil {
		return nil
	}

	done := make(chan struct{})
	go func() {
		s.grpcServer.GracefulStop()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		s.grpcServer.Stop()
		return ctx.Err()
	}
}

func genRequestID() string {
	b := make([]byte, 6)
	rand.Read(b)
	return hex.EncodeToString(b)
}
