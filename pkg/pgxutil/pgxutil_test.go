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
