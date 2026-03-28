package pkg

import (
	"context"
	"fmt"
	"net/http"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

// Metrics holds the instruments used across the service.
type Metrics struct {
	ShortenTotal  metric.Int64Counter
	ResolveTotal  metric.Int64Counter
	RaftApplyTotal metric.Int64Counter
}

// InitMetrics registers a Prometheus exporter as the global meter provider and
// returns the populated Metrics bundle plus a /metrics HTTP handler.
// The returned shutdown function must be called on process exit.
func InitMetrics(service string) (m *Metrics, handler http.Handler, shutdown func(context.Context) error, err error) {
	exporter, err := prometheus.New()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("create prometheus exporter: %w", err)
	}

	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(exporter))
	otel.SetMeterProvider(mp)

	meter := mp.Meter(service)

	shortenTotal, err := meter.Int64Counter("urlshortener_shorten_total",
		metric.WithDescription("Total ShortenURL requests handled"))
	if err != nil {
		return nil, nil, nil, err
	}

	resolveTotal, err := meter.Int64Counter("urlshortener_resolve_total",
		metric.WithDescription("Total ResolveURL requests handled"))
	if err != nil {
		return nil, nil, nil, err
	}

	raftApplyTotal, err := meter.Int64Counter("urlshortener_raft_apply_total",
		metric.WithDescription("Total raft log entries applied by the FSM"))
	if err != nil {
		return nil, nil, nil, err
	}

	m = &Metrics{
		ShortenTotal:   shortenTotal,
		ResolveTotal:   resolveTotal,
		RaftApplyTotal: raftApplyTotal,
	}

	return m, promhttp.Handler(), func(ctx context.Context) error {
		return mp.Shutdown(ctx)
	}, nil
}
