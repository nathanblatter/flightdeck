package store

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store is a thin layer over the sqlc Queries plus the underlying pgx pool,
// so handlers/MCP tools can run multi-statement transactions when needed.
type Store struct {
	*Queries
	Pool *pgxpool.Pool
}

// slowQueryThreshold: queries slower than this are logged (override via
// FLIGHTDECK_SLOW_QUERY_MS) so latency regressions surface without a profiler.
var slowQueryThreshold = envDuration("FLIGHTDECK_SLOW_QUERY_MS", 200) * time.Millisecond

// NewStore opens a tuned pgx pool against databaseURL and returns a ready Store.
func NewStore(ctx context.Context, databaseURL string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse db config: %w", err)
	}
	cfg.MaxConns = int32(envInt("FLIGHTDECK_DB_MAX_CONNS", 10))
	cfg.MinConns = 1
	cfg.MaxConnLifetime = time.Hour
	cfg.MaxConnIdleTime = 30 * time.Minute
	cfg.HealthCheckPeriod = time.Minute
	cfg.ConnConfig.Tracer = &slowQueryTracer{}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	return &Store{Queries: New(pool), Pool: pool}, nil
}

func (s *Store) Close() {
	if s.Pool != nil {
		s.Pool.Close()
	}
}

// WithTx runs fn inside a transaction, committing on success and rolling back
// on error. The provided *Queries is bound to the transaction.
func (s *Store) WithTx(ctx context.Context, fn func(q *Queries) error) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := fn(s.Queries.WithTx(tx)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// slowQueryTracer logs queries that exceed slowQueryThreshold.
type slowQueryTracer struct{}

type traceKey struct{}
type traceData struct {
	sql   string
	start time.Time
}

func (t *slowQueryTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	return context.WithValue(ctx, traceKey{}, traceData{sql: data.SQL, start: time.Now()})
}

func (t *slowQueryTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryEndData) {
	td, ok := ctx.Value(traceKey{}).(traceData)
	if !ok {
		return
	}
	if d := time.Since(td.start); d >= slowQueryThreshold {
		log.Printf("slow query (%s): %s", d.Round(time.Millisecond), firstLine(td.sql))
	}
}

func firstLine(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			return s[:i]
		}
	}
	return s
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envDuration(key string, defMs int) time.Duration {
	return time.Duration(envInt(key, defMs))
}
