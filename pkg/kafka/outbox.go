package kafka

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Execer — то, во что можно писать outbox (pgx.Tx, *pgxpool.Pool, pgx.Conn).
type Execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// Enqueue кладёт событие в outbox. Вызывать в ТОЙ ЖЕ транзакции, что и изменение
// бизнес-данных — иначе теряется смысл паттерна.
func Enqueue(ctx context.Context, tx Execer, ev Event) error {
	_, err := tx.Exec(ctx, `INSERT INTO outbox (event_id, topic, key, event_type, payload, traceparent, occurred_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`, ev.ID, ev.Topic, ev.Key, ev.Type, ev.Payload, ev.Traceparent, ev.OccurredAt)
	if err != nil {
		return fmt.Errorf("outbox enqueue: %w", err)
	}
	return nil
}

// Relay читает неопубликованные события и отправляет в Kafka.
// Несколько реплик безопасны (FOR UPDATE SKIP LOCKED); между репликами порядок
// не гарантируется — потребители защищаются revision/inbox. Падение между
// Publish и COMMIT даёт повторную публикацию (at-least-once) — снимается inbox'ом.
type Relay struct {
	Pool       *pgxpool.Pool
	Pub        Publisher
	Log        *slog.Logger
	BatchSize  int           // по умолчанию 100
	Interval   time.Duration // опрос при простое, по умолчанию 500ms
	MaxBackoff time.Duration // потолок паузы при ошибках, по умолчанию 10s
	Retention  time.Duration // хранить опубликованные, по умолчанию 24h

	wake chan struct{}
}

func (r *Relay) init() {
	if r.BatchSize <= 0 {
		r.BatchSize = 100
	}
	if r.Interval <= 0 {
		r.Interval = 500 * time.Millisecond
	}
	if r.MaxBackoff <= 0 {
		r.MaxBackoff = 10 * time.Second
	}
	if r.Retention <= 0 {
		r.Retention = 24 * time.Hour
	}
	if r.wake == nil {
		r.wake = make(chan struct{}, 1)
	}
	if r.Log == nil {
		r.Log = slog.Default()
	}
}

// Wake будит relay сразу после коммита транзакции с Enqueue (снижает задержку).
func (r *Relay) Wake() {
	r.init()
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// Flush выполняет одну итерацию: публикует до BatchSize событий. Возвращает число опубликованных.
func (r *Relay) Flush(ctx context.Context) (int, error) {
	r.init()
	tx, err := r.Pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(context.Background()) //nolint:errcheck

	rows, err := tx.Query(ctx, `SELECT id, event_id, topic, key, event_type, payload, traceparent, occurred_at
		FROM outbox WHERE published_at IS NULL ORDER BY id LIMIT $1 FOR UPDATE SKIP LOCKED`, r.BatchSize)
	if err != nil {
		return 0, err
	}
	var ids []int64
	var evs []Event
	for rows.Next() {
		var id int64
		var e Event
		if err := rows.Scan(&id, &e.ID, &e.Topic, &e.Key, &e.Type, &e.Payload, &e.Traceparent, &e.OccurredAt); err != nil {
			rows.Close()
			return 0, err
		}
		ids, evs = append(ids, id), append(evs, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(evs) == 0 {
		return 0, nil
	}
	pctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if perr := r.Pub.Publish(pctx, evs...); perr != nil {
		if _, err := tx.Exec(ctx, `UPDATE outbox SET attempts = attempts + 1, last_error = $2 WHERE id = ANY($1)`, ids, perr.Error()); err == nil {
			_ = tx.Commit(ctx)
		}
		return 0, fmt.Errorf("outbox publish: %w", perr)
	}
	if _, err := tx.Exec(ctx, `UPDATE outbox SET published_at = now(), last_error = NULL WHERE id = ANY($1)`, ids); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return len(evs), nil
}

// Run крутит Flush до отмены ctx; при ошибках — экспоненциальная пауза.
func (r *Relay) Run(ctx context.Context) error {
	r.init()
	backoff := r.Interval
	purge := time.NewTicker(time.Hour)
	defer purge.Stop()
	for {
		n, err := r.Flush(ctx)
		switch {
		case err != nil && ctx.Err() == nil:
			r.Log.ErrorContext(ctx, "outbox relay failed", "error", err, "retry_in", backoff)
			if !r.sleep(ctx, backoff, nil) {
				return ctx.Err()
			}
			if backoff *= 2; backoff > r.MaxBackoff {
				backoff = r.MaxBackoff
			}
			continue
		case ctx.Err() != nil:
			return ctx.Err()
		}
		backoff = r.Interval
		if n >= r.BatchSize {
			continue // очередь не пуста — без паузы
		}
		if !r.sleep(ctx, r.Interval, purge.C) {
			return ctx.Err()
		}
	}
}

func (r *Relay) sleep(ctx context.Context, d time.Duration, purge <-chan time.Time) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-t.C:
			return true
		case <-r.wake:
			return true
		case <-purge:
			if _, err := r.Pool.Exec(ctx, `DELETE FROM outbox WHERE published_at < now() - $1::interval`, r.Retention.String()); err != nil {
				r.Log.WarnContext(ctx, "outbox purge failed", "error", err)
			}
		}
	}
}

// ProcessOnce — inbox: выполняет h ровно один раз на (consumer, event.ID).
// Запись в processed_events и эффект h — в одной транзакции: либо оба, либо ни одного.
// Возвращает processed=false, если событие уже обрабатывалось.
func ProcessOnce(ctx context.Context, pool *pgxpool.Pool, consumer string, ev Event, h TxHandler) (processed bool, err error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(context.Background()) //nolint:errcheck
	tag, err := tx.Exec(ctx, `INSERT INTO processed_events (consumer, event_id) VALUES ($1,$2) ON CONFLICT DO NOTHING`, consumer, ev.ID)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() == 0 {
		return false, nil
	}
	if err := h(ev.Context(ctx), tx, ev); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

// TxHandler обрабатывает событие внутри транзакции inbox.
type TxHandler func(ctx context.Context, tx pgx.Tx, ev Event) error
