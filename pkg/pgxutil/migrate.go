package pgxutil

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// advisoryLockID — константа блокировки мигратора (одна БД на сервис, коллизий нет).
const advisoryLockID int64 = 0x666f6f64 // "food"

// Source — набор миграций одного модуля. Файлы вида NNN_name.sql в корне FS.
// Module — пространство имён версий: "service" для миграций сервиса, "kafka" для
// outbox/inbox из pkg/kafka и т.д. Так версии разных источников не конфликтуют.
type Source struct {
	Module string
	FS     fs.FS
}

type migration struct {
	module   string
	version  int64
	name     string
	sql      string
	checksum string
}

// Migrate применяет новые миграции по порядку источников и версий. Безопасно для
// параллельного запуска (advisory lock): несколько реплик или Job + сервис.
// Применённые миграции неизменяемы: расхождение checksum — ошибка (дрейф схемы).
func Migrate(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger, sources ...Source) error {
	var all []migration
	for _, s := range sources {
		ms, err := load(s)
		if err != nil {
			return err
		}
		all = append(all, ms...)
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, advisoryLockID); err != nil {
		return err
	}
	defer conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, advisoryLockID)

	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		module     TEXT        NOT NULL,
		version    BIGINT      NOT NULL,
		name       TEXT        NOT NULL,
		checksum   TEXT        NOT NULL,
		applied_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		PRIMARY KEY (module, version))`); err != nil {
		return err
	}
	for _, m := range all {
		var sum string
		err := conn.QueryRow(ctx, `SELECT checksum FROM schema_migrations WHERE module=$1 AND version=$2`, m.module, m.version).Scan(&sum)
		if err == nil {
			if sum != m.checksum {
				return fmt.Errorf("migration %s/%03d_%s was modified after being applied", m.module, m.version, m.name)
			}
			continue
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		tx, err := conn.Begin(ctx)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, m.sql); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("apply %s/%03d_%s: %w", m.module, m.version, m.name, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations(module, version, name, checksum) VALUES ($1,$2,$3,$4)`,
			m.module, m.version, m.name, m.checksum); err != nil {
			_ = tx.Rollback(ctx)
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		if log != nil {
			log.InfoContext(ctx, "migration applied", "module", m.module, "version", m.version, "name", m.name)
		}
	}
	return nil
}

func load(s Source) ([]migration, error) {
	entries, err := fs.ReadDir(s.FS, ".")
	if err != nil {
		return nil, fmt.Errorf("read migrations of %s: %w", s.Module, err)
	}
	var out []migration
	seen := map[int64]string{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		num, rest, ok := strings.Cut(strings.TrimSuffix(e.Name(), ".sql"), "_")
		v, err := strconv.ParseInt(num, 10, 64)
		if !ok || err != nil {
			return nil, fmt.Errorf("invalid migration file name %q (want NNN_name.sql)", e.Name())
		}
		if prev, dup := seen[v]; dup {
			return nil, fmt.Errorf("duplicate migration version %d in %s: %s, %s", v, s.Module, prev, e.Name())
		}
		seen[v] = e.Name()
		b, err := fs.ReadFile(s.FS, e.Name())
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(b)
		out = append(out, migration{module: s.Module, version: v, name: rest, sql: string(b), checksum: hex.EncodeToString(sum[:])})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	return out, nil
}
