package raftcluster

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"

	hraft "github.com/hashicorp/raft"
	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/joshdurbin/k8s-service-routing-url-shortener/internal/db"
	"github.com/joshdurbin/k8s-service-routing-url-shortener/pkg"
)

// FSM implements hashicorp/raft.FSM. Every committed log entry is applied here
// on all nodes (leader and followers) in identical order.
type FSM struct {
	sqlDB   *sql.DB
	queries *db.Queries
	metrics *pkg.Metrics
	log     zerolog.Logger
}

func newFSM(sqlDB *sql.DB, queries *db.Queries, metrics *pkg.Metrics, log zerolog.Logger) *FSM {
	return &FSM{
		sqlDB:   sqlDB,
		queries: queries,
		metrics: metrics,
		log:     log,
	}
}

// Apply is called by the raft library on every committed log entry.
// It must be deterministic — the same entry must produce the same result on
// every node.
func (f *FSM) Apply(l *hraft.Log) interface{} {
	if l.Type != hraft.LogCommand {
		return nil
	}

	var cmd Command
	if err := json.Unmarshal(l.Data, &cmd); err != nil {
		f.log.Error().Err(err).Msg("fsm: failed to unmarshal command")
		return &ApplyResult{Err: fmt.Errorf("unmarshal command: %w", err)}
	}

	f.metrics.RaftApplyTotal.Add(context.Background(), 1,
		metric.WithAttributes(attribute.Int("cmd_type", int(cmd.Type))))

	switch cmd.Type {
	case CmdShortenURL:
		return f.applyShortenURL(cmd.Payload)
	case CmdReserveBlock:
		return f.applyReserveBlock(cmd.Payload)
	case CmdRecordFollow:
		return f.applyRecordFollow(cmd.Payload)
	case CmdDeleteURL:
		return f.applyDeleteURL(cmd.Payload)
	default:
		return &ApplyResult{Err: fmt.Errorf("unknown command type %d", cmd.Type)}
	}
}

func (f *FSM) applyShortenURL(raw json.RawMessage) *ApplyResult {
	var p ShortenURLPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return &ApplyResult{Err: err}
	}
	u, err := f.queries.InsertURL(context.Background(), db.InsertURLParams{
		ShortCode: p.ShortCode,
		LongUrl:   p.LongURL,
	})
	if err != nil {
		return &ApplyResult{Err: err}
	}
	return &ApplyResult{ShortCode: u.ShortCode, LongURL: u.LongUrl, ID: u.ID}
}

func (f *FSM) applyReserveBlock(raw json.RawMessage) *ApplyResult {
	var p ReserveBlockPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return &ApplyResult{Err: err}
	}
	if err := f.queries.SetCounter(context.Background(), p.NewCounterValue); err != nil {
		return &ApplyResult{Err: err}
	}
	return &ApplyResult{}
}

func (f *FSM) applyRecordFollow(raw json.RawMessage) *ApplyResult {
	var p RecordFollowPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return &ApplyResult{Err: err}
	}
	ts := sql.NullInt64{Int64: p.At, Valid: true}
	err := f.queries.UpsertFollowStats(context.Background(), db.UpsertFollowStatsParams{
		ShortCode:   p.ShortCode,
		FirstFollow: ts,
		LastFollow:  ts,
	})
	if err != nil {
		return &ApplyResult{Err: err}
	}
	return &ApplyResult{}
}

func (f *FSM) applyDeleteURL(raw json.RawMessage) *ApplyResult {
	var p DeleteURLPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return &ApplyResult{Err: err}
	}
	// Delete stats first — SQLite FK enforcement is off by default.
	_ = f.queries.DeleteURLStats(context.Background(), p.ShortCode)
	if err := f.queries.DeleteURL(context.Background(), p.ShortCode); err != nil {
		return &ApplyResult{Err: err}
	}
	return &ApplyResult{}
}

// ---- Snapshot / Restore ----

type snapshotData struct {
	URLs    []urlSnapshot  `json:"urls"`
	Stats   []statSnapshot `json:"stats"`
	Counter int64          `json:"counter"`
}

type urlSnapshot struct {
	ID        int64  `json:"id"`
	ShortCode string `json:"short_code"`
	LongURL   string `json:"long_url"`
	CreatedAt int64  `json:"created_at"`
}

type statSnapshot struct {
	ShortCode   string `json:"short_code"`
	FollowCount int64  `json:"follow_count"`
	FirstFollow *int64 `json:"first_follow,omitempty"`
	LastFollow  *int64 `json:"last_follow,omitempty"`
}

func (f *FSM) Snapshot() (hraft.FSMSnapshot, error) {
	ctx := context.Background()

	urlRows, err := f.sqlDB.QueryContext(ctx,
		"SELECT id, short_code, long_url, created_at FROM urls ORDER BY id")
	if err != nil {
		return nil, fmt.Errorf("snapshot: query urls: %w", err)
	}
	defer urlRows.Close()

	var snap snapshotData
	for urlRows.Next() {
		var u urlSnapshot
		if err := urlRows.Scan(&u.ID, &u.ShortCode, &u.LongURL, &u.CreatedAt); err != nil {
			return nil, err
		}
		snap.URLs = append(snap.URLs, u)
	}
	if err := urlRows.Err(); err != nil {
		return nil, err
	}

	statRows, err := f.sqlDB.QueryContext(ctx,
		"SELECT short_code, follow_count, first_follow, last_follow FROM url_stats")
	if err != nil {
		return nil, fmt.Errorf("snapshot: query stats: %w", err)
	}
	defer statRows.Close()

	for statRows.Next() {
		var s statSnapshot
		var first, last sql.NullInt64
		if err := statRows.Scan(&s.ShortCode, &s.FollowCount, &first, &last); err != nil {
			return nil, err
		}
		if first.Valid {
			s.FirstFollow = &first.Int64
		}
		if last.Valid {
			s.LastFollow = &last.Int64
		}
		snap.Stats = append(snap.Stats, s)
	}
	if err := statRows.Err(); err != nil {
		return nil, err
	}

	ctr, err := f.queries.GetCounter(ctx)
	if err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("snapshot: get counter: %w", err)
	}
	snap.Counter = ctr.Value

	return &fsmSnapshot{data: snap}, nil
}

func (f *FSM) Restore(rc io.ReadCloser) error {
	defer rc.Close()

	var snap snapshotData
	if err := json.NewDecoder(rc).Decode(&snap); err != nil {
		return fmt.Errorf("restore: decode snapshot: %w", err)
	}

	tx, err := f.sqlDB.Begin()
	if err != nil {
		return fmt.Errorf("restore: begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	for _, stmt := range []string{
		"DELETE FROM url_stats",
		"DELETE FROM urls",
		"DELETE FROM counter",
	} {
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("restore: %s: %w", stmt, err)
		}
	}

	for _, u := range snap.URLs {
		if _, err := tx.Exec(
			"INSERT INTO urls (id, short_code, long_url, created_at) VALUES (?, ?, ?, ?)",
			u.ID, u.ShortCode, u.LongURL, u.CreatedAt,
		); err != nil {
			return fmt.Errorf("restore: insert url %s: %w", u.ShortCode, err)
		}
	}

	for _, s := range snap.Stats {
		var first, last interface{}
		if s.FirstFollow != nil {
			first = *s.FirstFollow
		}
		if s.LastFollow != nil {
			last = *s.LastFollow
		}
		if _, err := tx.Exec(
			"INSERT INTO url_stats (short_code, follow_count, first_follow, last_follow) VALUES (?, ?, ?, ?)",
			s.ShortCode, s.FollowCount, first, last,
		); err != nil {
			return fmt.Errorf("restore: insert stat %s: %w", s.ShortCode, err)
		}
	}

	if _, err := tx.Exec(
		"INSERT INTO counter (id, value) VALUES (1, ?)", snap.Counter,
	); err != nil {
		return fmt.Errorf("restore: insert counter: %w", err)
	}

	return tx.Commit()
}

// fsmSnapshot holds a point-in-time copy of the DB state for raft snapshotting.
type fsmSnapshot struct {
	data snapshotData
}

func (s *fsmSnapshot) Persist(sink hraft.SnapshotSink) error {
	if err := json.NewEncoder(sink).Encode(s.data); err != nil {
		sink.Cancel() //nolint:errcheck
		return err
	}
	return sink.Close()
}

func (s *fsmSnapshot) Release() {}
