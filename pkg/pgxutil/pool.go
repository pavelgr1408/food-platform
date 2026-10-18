// Package pgxutil — подключение к PostgreSQL (pgx), транзакции и мигратор.
package pgxutil

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Options — параметры пула.
type Options struct {
	AppName  string
	MaxConns int32 // по умолчанию 10
	MinConns int32
}

// Open создаёт пул и дожидается доступности БД (до 30 с, с backoff) —
// в k8s/compose БД может подняться позже сервиса.
func Open(ctx context.Context, url string, o Options) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	if o.MaxConns == 0 {
		o.MaxConns = 10
	}
	cfg.MaxConns, cfg.MinConns = o.MaxConns, o.MinConns
	if o.AppName != "" {
		cfg.ConnConfig.RuntimeParams["application_name"] = o.AppName
	}
	cfg.MaxConnLifetime = 30 * time.Minute
	cfg.HealthCheckPeriod = 30 * time.Second
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := WaitReady(ctx, pool, 30*time.Second); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}

// WaitReady пингует БД с экспоненциальным backoff до timeout.
func WaitReady(ctx context.Context, pool *pgxpool.Pool, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	delay := 100 * time.Millisecond
	for {
		pctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		err := pool.Ping(pctx)
		cancel()
		if err == nil {
			return nil
		}
		if time.Now().Add(delay).After(deadline) || ctx.Err() != nil {
			return fmt.Errorf("database not ready: %w", err)
		}
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return ctx.Err()
		}
		if delay < 2*time.Second {
			delay *= 2
		}
	}
}

// Ping — проверка для readiness.
func Ping(pool *pgxpool.Pool) func(context.Context) error {
	return func(ctx context.Context) error { return pool.Ping(ctx) }
}

// InTx выполняет fn в транзакции: commit при nil, иначе rollback.
func InTx(ctx context.Context, pool *pgxpool.Pool, fn func(tx pgx.Tx) error) (err error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback(context.Background())
			panic(p)
		}
		if err != nil {
			_ = tx.Rollback(context.Background())
		}
	}()
	if err = fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// MigrateOnly сообщает, запрошен ли режим «только миграции» (k8s Job):
// флаг --migrate-only среди args либо MIGRATE_ONLY=true.
func MigrateOnly(args []string) bool {
	for _, a := range args {
		if a == "--migrate-only" || a == "-migrate-only" {
			return true
		}
	}
	return strings.EqualFold(os.Getenv("MIGRATE_ONLY"), "true")
}
