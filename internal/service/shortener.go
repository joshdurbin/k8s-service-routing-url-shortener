package service

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/joshdurbin/k8s-service-routing-url-shortener/internal/raftcluster"
	"github.com/joshdurbin/k8s-service-routing-url-shortener/internal/shortcode"
	"github.com/joshdurbin/k8s-service-routing-url-shortener/pkg"
)

// Shortener handles the two public HTTP endpoints:
//
//	POST /shorten  — create a short URL (leader only)
//	GET  /{code}   — redirect to the original URL (any node)
type Shortener struct {
	cluster   *raftcluster.Cluster
	cfg       *pkg.Config
	metrics   *pkg.Metrics
	log       zerolog.Logger
	adminAddr string
	tracer    trace.Tracer
}

func NewShortener(
	cluster *raftcluster.Cluster,
	cfg *pkg.Config,
	metrics *pkg.Metrics,
	log zerolog.Logger,
	adminAddr string,
) *Shortener {
	return &Shortener{
		cluster:   cluster,
		cfg:       cfg,
		metrics:   metrics,
		log:       log,
		adminAddr: adminAddr,
		tracer:    otel.Tracer("urlshortener/shortener"),
	}
}

// RegisterRoutes attaches the handlers to mux.
func (s *Shortener) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/shorten", s.handleShorten)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/readyz", s.handleReadyz)
	// Catch-all for /{code} redirects.
	mux.HandleFunc("/", s.handleResolve)
}

// ---- POST /shorten ----

type shortenRequest struct {
	LongURL string `json:"long_url"`
}

type shortenResponse struct {
	ShortCode string `json:"short_code"`
	ShortURL  string `json:"short_url"`
}

func (s *Shortener) handleShorten(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx, span := s.tracer.Start(r.Context(), "ShortenURL")
	defer span.End()

	if !s.cluster.IsLeader() {
		leaderAddr := s.cluster.LeaderAdminAddr()
		w.Header().Set("X-Raft-Leader", leaderAddr)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":  "not the leader",
			"leader": leaderAddr,
		})
		return
	}

	var req shortenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	if req.LongURL == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "long_url is required"})
		return
	}

	counter, err := s.cluster.NextCounter(ctx)
	if err != nil {
		s.log.Error().Err(err).Msg("next counter")
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "counter error"})
		return
	}

	code := shortcode.Encode(uint64(counter), s.cfg.ShortCodeXORKey)
	span.SetAttributes(attribute.String("short_code", code), attribute.Int64("counter", counter))

	result, err := s.cluster.Apply(raftcluster.CmdShortenURL, raftcluster.ShortenURLPayload{
		ShortCode: code,
		LongURL:   req.LongURL,
	})
	if err != nil {
		s.log.Error().Err(err).Str("short_code", code).Msg("raft apply shorten")
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "raft apply failed"})
		return
	}
	if result.Err != nil {
		s.log.Error().Err(result.Err).Str("short_code", code).Msg("fsm apply shorten")
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": result.Err.Error()})
		return
	}

	s.metrics.ShortenTotal.Add(ctx, 1)
	s.log.Info().Str("short_code", code).Str("long_url", req.LongURL).Msg("URL shortened")

	writeJSON(w, http.StatusCreated, shortenResponse{
		ShortCode: code,
		ShortURL:  fmt.Sprintf("http://%s/%s", s.cfg.PublicHost, code),
	})
}

// ---- GET /{code} ----

func (s *Shortener) handleResolve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	code := strings.TrimPrefix(r.URL.Path, "/")
	if code == "" {
		http.NotFound(w, r)
		return
	}

	ctx, span := s.tracer.Start(r.Context(), "ResolveURL")
	defer span.End()
	span.SetAttributes(attribute.String("short_code", code))

	u, err := s.cluster.Queries().GetURLByShortCode(ctx, code)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		s.log.Error().Err(err).Str("short_code", code).Msg("resolve lookup")
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	RecordFollow(s.adminAddr, s.log, code, time.Now())

	s.metrics.ResolveTotal.Add(ctx, 1)
	http.Redirect(w, r, u.LongUrl, http.StatusFound)
}

// ---- /readyz ----

func (s *Shortener) handleReadyz(w http.ResponseWriter, r *http.Request) {
	if err := s.cluster.DB().PingContext(r.Context()); err != nil {
		http.Error(w, "db unavailable", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// ---- helpers ----

func writeJSON(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}
