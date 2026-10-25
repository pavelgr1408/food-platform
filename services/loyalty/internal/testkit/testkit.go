// Package testkit — помощники тестов loyalty-service: БД с миграциями, управляемые часы, authorizer.
// Используется только из _test.go.
package testkit

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pavelgr1408/food-platform/pkg/idempotency"
	"github.com/pavelgr1408/food-platform/pkg/kafka"
	"github.com/pavelgr1408/food-platform/pkg/pgxutil"
	"github.com/pavelgr1408/food-platform/pkg/pgxutil/pgtest"
	"github.com/pavelgr1408/food-platform/services/loyalty/migrations"
)

// Log — молчаливый логгер тестов.
func Log() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// Pool — временная БД с миграциями loyalty + kafka + idempotency.
func Pool(t testing.TB) *pgxpool.Pool {
	t.Helper()
	pool := pgtest.New(t)
	if err := pgxutil.Migrate(context.Background(), pool, Log(),
		pgxutil.Source{Module: "loyalty", FS: migrations.FS}, kafka.Migrations(), idempotency.Migrations()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return pool
}

// Clock — управляемые часы.
type Clock struct {
	mu sync.Mutex
	t  time.Time
}

// NewClock стартует с фиксированного момента.
func NewClock() *Clock { return &Clock{t: time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)} }

// Now — текущее время часов.
func (c *Clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }

// Advance сдвигает часы.
func (c *Clock) Advance(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }
