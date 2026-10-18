// Package pgtest — изолированная БД для интеграционных тестов.
//
// Нужен TEST_DATABASE_URL (пользователь с правом CREATEDB), например:
//
//	TEST_DATABASE_URL=postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable
//
// Без переменной тесты пропускаются (t.Skip), CI её выставляет.
package pgtest

import (
	"context"
	"fmt"
	"math/rand/v2"
	"net/url"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// New создаёт временную БД и пул к ней; БД удаляется в t.Cleanup.
func New(t testing.TB) *pgxpool.Pool {
	t.Helper()
	admin := os.Getenv("TEST_DATABASE_URL")
	if admin == "" {
		t.Skip("TEST_DATABASE_URL is not set; skipping Postgres integration test")
	}
	ctx := context.Background()
	ap, err := pgxpool.New(ctx, admin)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	name := fmt.Sprintf("test_%08x", rand.Uint32())
	if _, err := ap.Exec(ctx, `CREATE DATABASE `+name); err != nil {
		ap.Close()
		t.Fatalf("create db: %v", err)
	}
	u, err := url.Parse(admin)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	pool, err := pgxpool.New(ctx, u.String())
	if err != nil {
		t.Fatalf("connect test db: %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = ap.Exec(ctx, `DROP DATABASE IF EXISTS `+name+` WITH (FORCE)`)
		ap.Close()
	})
	return pool
}
