package pgxutil_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/jackc/pgx/v5"
	"github.com/pavelgr1408/food-platform/pkg/pgxutil"
	"github.com/pavelgr1408/food-platform/pkg/pgxutil/pgtest"
)

func fsOf(files map[string]string) fstest.MapFS {
	m := fstest.MapFS{}
	for k, v := range files {
		m[k] = &fstest.MapFile{Data: []byte(v)}
	}
	return m
}

func TestMigrateAppliesOnceAndDetectsDrift(t *testing.T) {
	pool := pgtest.New(t)
	ctx := context.Background()
	src := pgxutil.Source{Module: "svc", FS: fsOf(map[string]string{
		"001_create.sql": `CREATE TABLE t(id int primary key);`,
		"002_seed.sql":   `INSERT INTO t VALUES (1);`,
	})}
	for i := 0; i < 2; i++ { // повтор — no-op
		if err := pgxutil.Migrate(ctx, pool, nil, src); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM t`).Scan(&n)
	if n != 1 {
		t.Fatalf("seed applied %d times", n)
	}
	changed := pgxutil.Source{Module: "svc", FS: fsOf(map[string]string{
		"001_create.sql": `CREATE TABLE t(id int primary key, extra text);`,
		"002_seed.sql":   `INSERT INTO t VALUES (1);`,
	})}
	if err := pgxutil.Migrate(ctx, pool, nil, changed); err == nil || !strings.Contains(err.Error(), "modified") {
		t.Fatalf("drift not detected: %v", err)
	}
}

func TestMigrateModulesIndependentAndBadSQLRollsBack(t *testing.T) {
	pool := pgtest.New(t)
	ctx := context.Background()
	a := pgxutil.Source{Module: "a", FS: fsOf(map[string]string{"001_x.sql": `CREATE TABLE a1(id int);`})}
	b := pgxutil.Source{Module: "b", FS: fsOf(map[string]string{"001_x.sql": `CREATE TABLE b1(id int);`})}
	if err := pgxutil.Migrate(ctx, pool, nil, a, b); err != nil {
		t.Fatal(err)
	}
	bad := pgxutil.Source{Module: "a", FS: fsOf(map[string]string{
		"001_x.sql":   `CREATE TABLE a1(id int);`,
		"002_bad.sql": `CREATE TABLE a2(id int); SELECT broken;`,
	})}
	if err := pgxutil.Migrate(ctx, pool, nil, bad); err == nil {
		t.Fatal("expected error")
	}
	var exists bool
	_ = pool.QueryRow(ctx, `SELECT to_regclass('a2') IS NOT NULL`).Scan(&exists)
	if exists {
		t.Fatal("failed migration must roll back")
	}
}

func TestMigrateConcurrent(t *testing.T) {
	pool := pgtest.New(t)
	src := pgxutil.Source{Module: "svc", FS: fsOf(map[string]string{"001_c.sql": `CREATE TABLE c(id int);`})}
	var wg sync.WaitGroup
	errs := make(chan error, 5)
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- pgxutil.Migrate(context.Background(), pool, nil, src) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestInTx(t *testing.T) {
	pool := pgtest.New(t)
	ctx := context.Background()
	_, _ = pool.Exec(ctx, `CREATE TABLE x(id int)`)
	boom := errors.New("boom")
	err := pgxutil.InTx(ctx, pool, func(tx pgx.Tx) error {
		_, _ = tx.Exec(ctx, `INSERT INTO x VALUES (1)`)
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatal(err)
	}
	var n int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM x`).Scan(&n)
	if n != 0 {
		t.Fatal("rollback failed")
	}
	if err := pgxutil.InTx(ctx, pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO x VALUES (1)`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM x`).Scan(&n)
	if n != 1 {
		t.Fatal("commit failed")
	}
}

func TestMigrateOnlyFlag(t *testing.T) {
	if !pgxutil.MigrateOnly([]string{"svc", "--migrate-only"}) || pgxutil.MigrateOnly([]string{"svc"}) {
		t.Fatal("flag parsing")
	}
	t.Setenv("MIGRATE_ONLY", "true")
	if !pgxutil.MigrateOnly(nil) {
		t.Fatal("env parsing")
	}
}

func TestAdoptLegacyKeepsLiveSchemaAndContinues(t *testing.T) {
	pool := pgtest.New(t)
	ctx := context.Background()
	// БД «как у старого auth»: собственная таблица версий и уже применённые 001/002.
	if _, err := pool.Exec(ctx, `
		CREATE TABLE schema_migrations(version BIGINT PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT now());
		INSERT INTO schema_migrations(version) VALUES (1),(2);
		CREATE TABLE t(id int primary key);
		INSERT INTO t VALUES (1)`); err != nil {
		t.Fatal(err)
	}
	src := pgxutil.Source{Module: "svc", FS: fsOf(map[string]string{
		"001_create.sql": `CREATE TABLE t(id int primary key);`,
		"002_seed.sql":   `INSERT INTO t VALUES (1);`,
		"003_more.sql":   `ALTER TABLE t ADD COLUMN extra text;`,
	})}
	for i := 0; i < 2; i++ { // повтор — no-op
		if err := pgxutil.AdoptLegacy(ctx, pool, nil, src); err != nil {
			t.Fatal(err)
		}
	}
	// 001/002 не применяются повторно (иначе CREATE TABLE t упал бы), 003 применяется.
	if err := pgxutil.Migrate(ctx, pool, nil, src); err != nil {
		t.Fatalf("migrate after adopt: %v", err)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM schema_migrations WHERE module='svc'`).Scan(&n); err != nil || n != 3 {
		t.Fatalf("applied rows = %d, err=%v", n, err)
	}
	var legacy int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM schema_migrations_legacy`).Scan(&legacy); err != nil || legacy != 2 {
		t.Fatalf("legacy rows = %d, err=%v", legacy, err)
	}
	// после перехода применённые файлы неизменяемы
	changed := pgxutil.Source{Module: "svc", FS: fsOf(map[string]string{
		"001_create.sql": `CREATE TABLE t(id int primary key, x int);`,
		"002_seed.sql":   `INSERT INTO t VALUES (1);`,
		"003_more.sql":   `ALTER TABLE t ADD COLUMN extra text;`,
	})}
	if err := pgxutil.Migrate(ctx, pool, nil, changed); err == nil || !strings.Contains(err.Error(), "modified") {
		t.Fatalf("drift not detected: %v", err)
	}
}

func TestAdoptLegacyNoopForFreshAndRejectsUnknownVersion(t *testing.T) {
	pool := pgtest.New(t)
	ctx := context.Background()
	src := pgxutil.Source{Module: "svc", FS: fsOf(map[string]string{"001_a.sql": `CREATE TABLE a(id int);`})}
	if err := pgxutil.AdoptLegacy(ctx, pool, nil, src); err != nil { // нет таблицы вообще
		t.Fatal(err)
	}
	if err := pgxutil.Migrate(ctx, pool, nil, src); err != nil {
		t.Fatal(err)
	}
	if err := pgxutil.AdoptLegacy(ctx, pool, nil, src); err != nil { // уже в новом формате
		t.Fatal(err)
	}
	pool2 := pgtest.New(t)
	if _, err := pool2.Exec(ctx, `CREATE TABLE schema_migrations(version BIGINT PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT now());
		INSERT INTO schema_migrations(version) VALUES (1),(7)`); err != nil {
		t.Fatal(err)
	}
	if err := pgxutil.AdoptLegacy(ctx, pool2, nil, src); err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("unknown legacy version must be rejected: %v", err)
	}
}
