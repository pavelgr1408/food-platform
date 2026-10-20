package pgxutil

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
)

// AdoptLegacy переводит БД сервиса, который до перехода на pgxutil вёл свою таблицу
// schema_migrations(version BIGINT PRIMARY KEY, applied_at), на формат мигратора по модулям.
//
// Зачем: у auth-service (перенесён из Java-версии) была собственная таблица с тем же именем, но без
// колонок module/name/checksum. Без перехода Migrate либо упал бы на запросе колонки module, либо
// (CREATE TABLE IF NOT EXISTS пропускает существующую таблицу) повторно применил бы 001/002 и сломал
// живую БД. Вызывать ДО Migrate; для БД без legacy-таблицы и для уже переведённых — no-op.
//
// Старая таблица не удаляется, а переименовывается в schema_migrations_legacy (аудит). Каждая применённая
// ранее версия должна присутствовать в src — иначе ошибка: БД знает миграцию, которой нет в коде
// (откат бинаря или чужая БД), и молча «усыновлять» её нельзя. Checksum берётся из файлов в src,
// поэтому уже применённые legacy-файлы после перехода неизменяемы, как и остальные.
func AdoptLegacy(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger, src Source) error {
	ms, err := load(src)
	if err != nil {
		return err
	}
	byVersion := make(map[int64]migration, len(ms))
	for _, m := range ms {
		byVersion[m.version] = m
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

	var legacy bool
	if err := conn.QueryRow(ctx, `SELECT
		EXISTS (SELECT 1 FROM information_schema.columns
		        WHERE table_schema = current_schema() AND table_name = 'schema_migrations' AND column_name = 'version')
		AND NOT EXISTS (SELECT 1 FROM information_schema.columns
		        WHERE table_schema = current_schema() AND table_name = 'schema_migrations' AND column_name = 'module')`).Scan(&legacy); err != nil {
		return err
	}
	if !legacy {
		return nil
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())

	rows, err := tx.Query(ctx, `SELECT version FROM schema_migrations ORDER BY version`)
	if err != nil {
		return err
	}
	var versions []int64
	for rows.Next() {
		var v int64
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return err
		}
		versions = append(versions, v)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, v := range versions {
		if _, ok := byVersion[v]; !ok {
			return fmt.Errorf("legacy schema_migrations has version %d unknown to module %s", v, src.Module)
		}
	}
	if _, err := tx.Exec(ctx, `ALTER TABLE schema_migrations RENAME TO schema_migrations_legacy`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, createMigrationsTable); err != nil {
		return err
	}
	for _, v := range versions {
		m := byVersion[v]
		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations(module, version, name, checksum) VALUES ($1,$2,$3,$4)`,
			src.Module, m.version, m.name, m.checksum); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	if log != nil {
		log.InfoContext(ctx, "legacy schema_migrations adopted", "module", src.Module, "versions", versions)
	}
	return nil
}
