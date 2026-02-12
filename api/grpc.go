package api

import (
	"context"
	"errors"
	"net"
	"strings"

	"qmdsr/model"
	qmdsrv1 "qmdsr/pb/qmdsrv1"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	grpcHealth "google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"
	emptypb "google.golang.org/protobuf/types/known/emptypb"
)

type grpcQueryServer struct {
	qmdsrv1.UnimplementedQueryServiceServer
	s *Server
}

type grpcAdminServer struct {
	qmdsrv1.UnimplementedAdminServiceServer
	s *Server
}

func (s *Server) startGRPC() error {
	if s.cfg.Server.GRPCListen == "" {
		return nil
	}
	if s.grpcServer != nil {
		return nil
	}

	lis, err := net.Listen("tcp", s.cfg.Server.GRPCListen)
	if err != nil {
		return err
	}

	grpcSrv := grpc.NewServer()
	qmdsrv1.RegisterQueryServiceServer(grpcSrv, &grpcQueryServer{s: s})
	qmdsrv1.RegisterAdminServiceServer(grpcSrv, &grpcAdminServer{s: s})
	healthSrv := grpcHealth.NewServer()
	healthSrv.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	healthSrv.SetServingStatus("qmdsr.v1.QueryService", healthpb.HealthCheckResponse_SERVING)
	healthSrv.SetServingStatus("qmdsr.v1.AdminService", healthpb.HealthCheckResponse_SERVING)
	healthpb.RegisterHealthServer(grpcSrv, healthSrv)
	reflection.Register(grpcSrv)
	s.grpcServer = grpcSrv

	go func() {
		s.log.Info("gRPC server starting", "listen", s.cfg.Server.GRPCListen)
		if serveErr := grpcSrv.Serve(lis); serveErr != nil && !strings.Contains(serveErr.Error(), "use of closed network connection") {
			s.log.Error("gRPC server error", "err", serveErr)
		}
	}()

	return nil
}

func (g *grpcQueryServer) Search(ctx context.Context, req *qmdsrv1.SearchRequest) (*qmdsrv1.SearchResponse, error) {
	traceID := traceIDFromContext(ctx)
	requested := requestedModeFromProto(req.GetRequestedMode())
	allowFallback := allowFallbackFromProto(req, requested, g.s.cfg.Search.FallbackEnabled)

	result, err := g.s.executeSearchCore(ctx, searchCoreRequest{
		Query:         req.GetQuery(),
		RequestedMode: requested,
		Collections:   req.GetCollections(),
		AllowFallback: allowFallback,
		TimeoutMs:     req.GetTimeoutMs(),
		TopK:          req.GetTopK(),
		MinScore:      req.GetMinScore(),
		Explain:       req.GetExplain(),
		TraceID:       traceID,
	})
	if err != nil {
		return nil, mapSearchError(err)
	}

	return toProtoSearchResponse(result.Response, result.RouteLog), nil
}

func (g *grpcQueryServer) SearchStream(req *qmdsrv1.SearchRequest, stream grpc.ServerStreamingServer[qmdsrv1.SearchChunk]) error {
	resp, err := g.Search(stream.Context(), req)
	if err != nil {
		return err
	}

	for _, hit := range resp.GetHits() {
		if err := stream.Send(&qmdsrv1.SearchChunk{Payload: &qmdsrv1.SearchChunk_Hit{Hit: hit}}); err != nil {
			return err
		}
	}

	return stream.Send(&qmdsrv1.SearchChunk{Payload: &qmdsrv1.SearchChunk_Summary{Summary: resp}})
}

func (g *grpcQueryServer) Health(ctx context.Context, _ *qmdsrv1.HealthRequest) (*qmdsrv1.HealthResponse, error) {
	return g.s.buildHealthResponse(), nil
}

func (g *grpcQueryServer) Status(ctx context.Context, _ *qmdsrv1.StatusRequest) (*qmdsrv1.StatusResponse, error) {
	return g.s.buildStatusResponse(traceIDFromContext(ctx)), nil
}

func (g *grpcAdminServer) Reindex(ctx context.Context, _ *emptypb.Empty) (*qmdsrv1.OpResponse, error) {
	res, err := g.s.executeAdminReindexCore(ctx, traceIDFromContext(ctx))
	if err != nil {
		return nil, mapAdminRPCError(err)
	}
	return toProtoOpResponse(res), nil
}

func (g *grpcAdminServer) Embed(ctx context.Context, req *qmdsrv1.EmbedRequest) (*qmdsrv1.OpResponse, error) {
	res, err := g.s.executeAdminEmbedCore(ctx, traceIDFromContext(ctx), req.GetForce())
	if err != nil {
		return nil, mapAdminRPCError(err)
	}
	return toProtoOpResponse(res), nil
}

func (g *grpcAdminServer) CacheClear(ctx context.Context, _ *emptypb.Empty) (*qmdsrv1.OpResponse, error) {
	res, err := g.s.executeAdminCacheClearCore(traceIDFromContext(ctx))
	if err != nil {
		return nil, mapAdminRPCError(err)
	}
	return toProtoOpResponse(res), nil
}

func (g *grpcAdminServer) Collections(ctx context.Context, _ *emptypb.Empty) (*qmdsrv1.CollectionsResponse, error) {
	res, err := g.s.executeAdminCollectionsCore(ctx, traceIDFromContext(ctx))
	if err != nil {
		return nil, mapAdminRPCError(err)
	}
	collections := make([]*qmdsrv1.CollectionInfo, 0, len(res.Collections))
	for _, col := range res.Collections {
		collections = append(collections, &qmdsrv1.CollectionInfo{
			Name:  col.Name,
			Path:  col.Path,
			Mask:  col.Mask,
			Files: intToInt32(col.Files),
		})
	}
	return &qmdsrv1.CollectionsResponse{
		Collections: collections,
		TraceId:     res.TraceID,
		LatencyMs:   res.LatencyMs,
	}, nil
}

func (g *grpcAdminServer) MCPRestart(ctx context.Context, _ *emptypb.Empty) (*qmdsrv1.OpResponse, error) {
	res, err := g.s.executeAdminMCPRestartCore(ctx, traceIDFromContext(ctx))
	if err != nil {
		return nil, mapAdminRPCError(err)
	}
	return toProtoOpResponse(res), nil
}

func requestedModeFromProto(mode qmdsrv1.Mode) string {
	switch mode {
	case qmdsrv1.Mode_MODE_CORE:
		return "core"
	case qmdsrv1.Mode_MODE_BROAD:
		return "broad"
	case qmdsrv1.Mode_MODE_DEEP:
		return "deep"
	case qmdsrv1.Mode_MODE_AUTO:
		return "auto"
	default:
		return "auto"
	}
}

func allowFallbackFromProto(req *qmdsrv1.SearchRequest, requestedMode string, fallbackDefault bool) bool {
	if req.GetAllowFallback() {
		return true
	}

	switch requestedMode {
	case "deep", "broad":
		return true
	case "core":
		return false
	default:
		return fallbackDefault
	}
}

func toProtoSearchResponse(resp *model.SearchResponse, routeLog []string) *qmdsrv1.SearchResponse {
	hits := make([]*qmdsrv1.Hit, 0, len(resp.Results))
	for _, r := range resp.Results {
		hits = append(hits, &qmdsrv1.Hit{
			Uri:        r.File,
			Title:      r.Title,
			Snippet:    r.Snippet,
			Score:      r.Score,
			Collection: r.Collection,
		})
	}

	return &qmdsrv1.SearchResponse{
		Hits:          hits,
		ServedMode:    servedModeToProto(resp.Meta.ServedMode),
		Degraded:      resp.Meta.Degraded,
		DegradeReason: strings.ToUpper(resp.Meta.DegradeReason),
		LatencyMs:     resp.Meta.LatencyMs,
		TraceId:       resp.Meta.TraceID,
		RouteLog:      routeLog,
	}
}

func servedModeToProto(mode string) qmdsrv1.ServedMode {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "deep", "query":
		return qmdsrv1.ServedMode_SERVED_DEEP
	case "broad", "search", "vsearch":
		return qmdsrv1.ServedMode_SERVED_BROAD
	case "core":
		return qmdsrv1.ServedMode_SERVED_CORE
	default:
		return qmdsrv1.ServedMode_SERVED_UNSPECIFIED
	}
}

func mapSearchError(err error) error {
	if err == nil {
		return nil
	}

	msg := err.Error()
	lower := strings.ToLower(msg)

	switch {
	case errors.Is(err, context.DeadlineExceeded) || strings.Contains(lower, "deadline exceeded"):
		return status.Error(codes.DeadlineExceeded, "QMD_TIMEOUT: "+msg)
	case strings.Contains(lower, "outofmemory") || strings.Contains(lower, "resource exhausted"):
		return status.Error(codes.ResourceExhausted, "RESOURCE_EXHAUSTED: "+msg)
	case strings.Contains(lower, "requires confirm=true"):
		return status.Error(codes.FailedPrecondition, "FAILED_PRECONDITION: "+msg)
	case strings.Contains(lower, "not found"):
		return status.Error(codes.NotFound, msg)
	case strings.Contains(lower, "unavailable"):
		return status.Error(codes.Unavailable, msg)
	case strings.Contains(lower, "invalid") || strings.Contains(lower, "required"):
		return status.Error(codes.InvalidArgument, msg)
	default:
		return status.Error(codes.Internal, msg)
	}
}

func mapAdminRPCError(err error) error {
	if err == nil {
		return nil
	}

	msg := err.Error()
	lower := strings.ToLower(msg)

	switch {
	case errors.Is(err, context.DeadlineExceeded) || strings.Contains(lower, "deadline exceeded"):
		return status.Error(codes.DeadlineExceeded, msg)
	case errors.Is(err, errGuardianUnavailable) || strings.Contains(lower, "guardian not available"):
		return status.Error(codes.Unavailable, msg)
	case strings.Contains(lower, "requires confirm=true"):
		return status.Error(codes.FailedPrecondition, msg)
	case strings.Contains(lower, "not found"):
		return status.Error(codes.NotFound, msg)
	case strings.Contains(lower, "invalid") || strings.Contains(lower, "required"):
		return status.Error(codes.InvalidArgument, msg)
	default:
		return status.Error(codes.Internal, msg)
	}
}

func toProtoOpResponse(res *adminOpResult) *qmdsrv1.OpResponse {
	return &qmdsrv1.OpResponse{
		Ok:        true,
		Message:   res.Message,
		TraceId:   res.TraceID,
		LatencyMs: res.LatencyMs,
	}
}

func traceIDFromContext(ctx context.Context) string {
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if values := md.Get("x-trace-id"); len(values) > 0 {
			trace := strings.TrimSpace(values[0])
			if trace != "" {
				return trace
			}
		}
	}
	return genRequestID()
}
