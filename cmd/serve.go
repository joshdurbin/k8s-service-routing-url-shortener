package cmd

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"

	adminv1 "github.com/joshdurbin/k8s-service-routing-url-shortener/gen/go/admin/v1"
	urlshortenerv1 "github.com/joshdurbin/k8s-service-routing-url-shortener/gen/go/urlshortener/v1"
	"github.com/joshdurbin/k8s-service-routing-url-shortener/internal/raftcluster"
	"github.com/joshdurbin/k8s-service-routing-url-shortener/internal/service"
	"github.com/joshdurbin/k8s-service-routing-url-shortener/pkg"
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Run a URL shortener node",
	RunE:  runServe,
}

func runServe(_ *cobra.Command, _ []string) error {
	cfg, err := pkg.LoadConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	log := pkg.NewLogger("url-shortener", cfg.LogLevel)

	// ---- OTEL ----
	bgCtx := context.Background()

	tracerShutdown, err := pkg.InitTracer(bgCtx, "url-shortener", cfg.PodName, cfg.OTELEndpoint)
	if err != nil {
		// Non-fatal: continue without tracing when the collector is unavailable.
		log.Warn().Err(err).Msg("tracing init failed — continuing without traces")
		tracerShutdown = func(context.Context) error { return nil }
	}

	metrics, metricsHandler, metricsShutdown, err := pkg.InitMetrics("url-shortener")
	if err != nil {
		return fmt.Errorf("metrics init: %w", err)
	}

	// ---- Raft cluster ----
	cluster, err := raftcluster.New(cfg, metrics, log)
	if err != nil {
		return fmt.Errorf("raft cluster: %w", err)
	}

	// ---- Service layer ----
	// Admin address for follow stats recording - Istio routes this to the leader.
	adminAddr := fmt.Sprintf("url-shortener-admin.%s.svc.cluster.local:%d", cfg.K8sNamespace, cfg.GRPCAdminPort)
	shortener := service.NewShortener(cluster, cfg, metrics, log, adminAddr)
	adminSrv := service.NewAdminServer(cluster, cfg, log)
	publicGRPCSrv := service.NewURLShortenerServer(cluster, cfg, metrics, log, adminAddr)

	// ---- Signal-aware context ----
	ctx, stop := signal.NotifyContext(bgCtx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	g, ctx := errgroup.WithContext(ctx)

	// 1. Raft cluster lifecycle.
	g.Go(func() error {
		return cluster.Run(ctx)
	})

	// 2. Public HTTP server — POST /shorten, GET /{code}.
	g.Go(func() error {
		mux := http.NewServeMux()
		shortener.RegisterRoutes(mux)
		srv := &http.Server{
			Addr:    fmt.Sprintf(":%d", cfg.HTTPPort),
			Handler: otelhttp.NewHandler(mux, "http.public"),
		}
		go func() {
			<-ctx.Done()
			srv.Shutdown(bgCtx) //nolint:errcheck
		}()
		log.Info().Int("port", cfg.HTTPPort).Msg("public HTTP server listening")
		if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("public http: %w", err)
		}
		return nil
	})

	// 3. Admin gRPC server.
	g.Go(func() error {
		grpcSrv := grpc.NewServer(
			grpc.StatsHandler(otelgrpc.NewServerHandler()),
		)
		adminv1.RegisterAdminServiceServer(grpcSrv, adminSrv)
		reflection.Register(grpcSrv)

		lis, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.GRPCAdminPort))
		if err != nil {
			return fmt.Errorf("admin grpc listen: %w", err)
		}
		go func() {
			<-ctx.Done()
			grpcSrv.GracefulStop()
		}()
		log.Info().Int("port", cfg.GRPCAdminPort).Msg("admin gRPC server listening")
		if err := grpcSrv.Serve(lis); err != nil {
			return fmt.Errorf("admin grpc: %w", err)
		}
		return nil
	})

	// 4. Public gRPC server — URLShortenerService (transcoded from HTTP/JSON by Envoy).
	g.Go(func() error {
		grpcSrv := grpc.NewServer(
			grpc.StatsHandler(otelgrpc.NewServerHandler()),
		)
		urlshortenerv1.RegisterURLShortenerServiceServer(grpcSrv, publicGRPCSrv)
		reflection.Register(grpcSrv)

		lis, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.GRPCPublicPort))
		if err != nil {
			return fmt.Errorf("public grpc listen: %w", err)
		}
		go func() {
			<-ctx.Done()
			grpcSrv.GracefulStop()
		}()
		log.Info().Int("port", cfg.GRPCPublicPort).Msg("public gRPC server listening")
		if err := grpcSrv.Serve(lis); err != nil {
			return fmt.Errorf("public grpc: %w", err)
		}
		return nil
	})

	// 5. Metrics + health HTTP server.
	g.Go(func() error {
		mux := http.NewServeMux()
		mux.Handle("/metrics", metricsHandler)
		mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
		mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
			if err := cluster.DB().PingContext(r.Context()); err != nil {
				http.Error(w, "db unavailable", http.StatusServiceUnavailable)
				return
			}
			w.WriteHeader(http.StatusOK)
		})
		srv := &http.Server{
			Addr:    fmt.Sprintf(":%d", cfg.MetricsPort),
			Handler: mux,
		}
		go func() {
			<-ctx.Done()
			srv.Shutdown(bgCtx) //nolint:errcheck
		}()
		log.Info().Int("port", cfg.MetricsPort).Msg("metrics HTTP server listening")
		if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("metrics http: %w", err)
		}
		return nil
	})

	// 6. Flush telemetry on shutdown.
	g.Go(func() error {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(bgCtx, pkg.ShutdownTimeout)
		defer cancel()
		if err := tracerShutdown(shutCtx); err != nil {
			log.Warn().Err(err).Msg("tracer shutdown")
		}
		if err := metricsShutdown(shutCtx); err != nil {
			log.Warn().Err(err).Msg("metrics shutdown")
		}
		return nil
	})

	return g.Wait()
}
