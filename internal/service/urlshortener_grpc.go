package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/rs/zerolog"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	urlshortenerv1 "github.com/joshdurbin/k8s-service-routing-url-shortener/gen/go/urlshortener/v1"
	"github.com/joshdurbin/k8s-service-routing-url-shortener/internal/raftcluster"
	"github.com/joshdurbin/k8s-service-routing-url-shortener/internal/shortcode"
	"github.com/joshdurbin/k8s-service-routing-url-shortener/pkg"
)

// URLShortenerServer implements urlshortenerv1.URLShortenerServiceServer.
// ShortenURL is exposed via Envoy gRPC-JSON transcoding at the Istio ingress
// gateway (POST /shorten → gRPC). ResolveURL is gRPC-only; HTTP redirects
// are handled separately by the plain HTTP server on port 8080.
type URLShortenerServer struct {
	urlshortenerv1.UnimplementedURLShortenerServiceServer
	cluster   *raftcluster.Cluster
	cfg       *pkg.Config
	metrics   *pkg.Metrics
	log       zerolog.Logger
	adminAddr string
}

func NewURLShortenerServer(
	cluster *raftcluster.Cluster,
	cfg *pkg.Config,
	metrics *pkg.Metrics,
	log zerolog.Logger,
	adminAddr string,
) *URLShortenerServer {
	return &URLShortenerServer{
		cluster:   cluster,
		cfg:       cfg,
		metrics:   metrics,
		log:       log,
		adminAddr: adminAddr,
	}
}

func (s *URLShortenerServer) ShortenURL(ctx context.Context, req *urlshortenerv1.ShortenURLRequest) (*urlshortenerv1.ShortenURLResponse, error) {
	if !s.cluster.IsLeader() {
		return nil, status.Errorf(codes.FailedPrecondition,
			"not the leader; send to %s", s.cluster.LeaderAdminAddr())
	}
	if req.LongUrl == "" {
		return nil, status.Error(codes.InvalidArgument, "long_url is required")
	}

	counter, err := s.cluster.NextCounter(ctx)
	if err != nil {
		s.log.Error().Err(err).Msg("grpc ShortenURL: next counter")
		return nil, status.Errorf(codes.Internal, "counter: %v", err)
	}

	code := shortcode.Encode(uint64(counter), s.cfg.ShortCodeXORKey)

	result, err := s.cluster.Apply(raftcluster.CmdShortenURL, raftcluster.ShortenURLPayload{
		ShortCode: code,
		LongURL:   req.LongUrl,
	})
	if err != nil {
		s.log.Error().Err(err).Str("short_code", code).Msg("grpc ShortenURL: raft apply")
		return nil, status.Errorf(codes.Internal, "raft apply: %v", err)
	}
	if result.Err != nil {
		s.log.Error().Err(result.Err).Str("short_code", code).Msg("grpc ShortenURL: fsm apply")
		return nil, status.Errorf(codes.Internal, "fsm apply: %v", result.Err)
	}

	s.metrics.ShortenTotal.Add(ctx, 1)
	s.log.Info().Str("short_code", code).Str("long_url", req.LongUrl).Msg("URL shortened via gRPC")

	return &urlshortenerv1.ShortenURLResponse{
		ShortCode: code,
		ShortUrl:  fmt.Sprintf("http://%s/%s", s.cfg.PublicHost, code),
	}, nil
}

func (s *URLShortenerServer) ResolveURL(ctx context.Context, req *urlshortenerv1.ResolveURLRequest) (*urlshortenerv1.ResolveURLResponse, error) {
	if req.ShortCode == "" {
		return nil, status.Error(codes.InvalidArgument, "short_code is required")
	}

	u, err := s.cluster.Queries().GetURLByShortCode(ctx, req.ShortCode)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Errorf(codes.NotFound, "short code %q not found", req.ShortCode)
		}
		return nil, status.Errorf(codes.Internal, "lookup: %v", err)
	}

	RecordFollow(s.adminAddr, s.log, req.ShortCode, time.Now())

	s.metrics.ResolveTotal.Add(ctx, 1)
	return &urlshortenerv1.ResolveURLResponse{LongUrl: u.LongUrl}, nil
}
