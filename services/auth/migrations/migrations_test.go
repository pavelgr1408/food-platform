package migrations_test

import (
	"context"
	"testing"

	"github.com/pavelgr1408/food-platform/pkg/pgxutil"
	"github.com/pavelgr1408/food-platform/pkg/pgxutil/pgtest"
	"github.com/pavelgr1408/food-platform/services/auth/migrations"
)

// Апгрейд живой БД «как до S5»: схема и seed Java-версии, собственная schema_migrations(version).
func TestUpgradeFromPreS5Database(t *testing.T) {
	pool := pgtest.New(t)
	ctx := context.Background()
	for _, f := range []string{"001_create_auth_schema.sql", "002_seed_dev_users.sql"} {
		b, err := migrations.FS.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, string(b)); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
	}
	if _, err := pool.Exec(ctx, `
		CREATE TABLE schema_migrations(version BIGINT PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT now());
		INSERT INTO schema_migrations(version) VALUES (1),(2);
		-- пользователь с ролями Java-версии: KITCHEN+COURIER (останется без ролей) и CUSTOMER+KITCHEN
		INSERT INTO users(id,phone,password_hash,status,created_at,updated_at) VALUES
		 ('00000000-0000-0000-0000-0000000000a1','+79990000011','x','ACTIVE',now(),now()),
		 ('00000000-0000-0000-0000-0000000000a2','+79990000012','x','ACTIVE',now(),now());
		INSERT INTO user_roles(user_id,role) VALUES
		 ('00000000-0000-0000-0000-0000000000a1','KITCHEN'),('00000000-0000-0000-0000-0000000000a1','COURIER'),
		 ('00000000-0000-0000-0000-0000000000a2','CUSTOMER'),('00000000-0000-0000-0000-0000000000a2','KITCHEN')`); err != nil {
		t.Fatal(err)
	}

	src := pgxutil.Source{Module: "auth", FS: migrations.FS}
	for i := 0; i < 2; i++ { // повторный запуск (вторая реплика / перезапуск) — no-op
		if err := pgxutil.AdoptLegacy(ctx, pool, nil, src); err != nil {
			t.Fatal(err)
		}
		if err := pgxutil.Migrate(ctx, pool, nil, src); err != nil {
			t.Fatalf("migrate after adopt: %v", err)
		}
	}

	roles := func(id string) []string {
		rows, err := pool.Query(ctx, `SELECT role FROM user_roles WHERE user_id=$1 ORDER BY role`, id)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var r string
			_ = rows.Scan(&r)
			out = append(out, r)
		}
		return out
	}
	eq := func(got []string, want ...string) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("roles = %v, want %v", got, want)
		}
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("roles = %v, want %v", got, want)
			}
		}
	}
	eq(roles("00000000-0000-0000-0000-000000000002"), "HQ_STAFF") // бывший ADMIN
	eq(roles("00000000-0000-0000-0000-000000000001"), "CUSTOMER")
	eq(roles("00000000-0000-0000-0000-0000000000a1"), "CUSTOMER") // остался без ролей → клиент
	eq(roles("00000000-0000-0000-0000-0000000000a2"), "CUSTOMER") // KITCHEN снят

	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM schema_migrations WHERE module='auth'`).Scan(&n); err != nil || n != 3 {
		t.Fatalf("applied migrations = %d (err %v), want 3", n, err)
	}
	// старые роли больше не вставляются
	if _, err := pool.Exec(ctx, `INSERT INTO user_roles(user_id,role) VALUES ('00000000-0000-0000-0000-000000000001','ADMIN')`); err == nil {
		t.Fatal("legacy role ADMIN must be rejected by the CHECK constraint")
	}
}

func TestFreshDatabase(t *testing.T) {
	pool := pgtest.New(t)
	src := pgxutil.Source{Module: "auth", FS: migrations.FS}
	ctx := context.Background()
	if err := pgxutil.AdoptLegacy(ctx, pool, nil, src); err != nil {
		t.Fatal(err)
	}
	if err := pgxutil.Migrate(ctx, pool, nil, src); err != nil {
		t.Fatal(err)
	}
}
