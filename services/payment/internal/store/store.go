// Package store — PostgreSQL-хранилище payment-service. Методы принимают DBTX: работают и с пулом,
// и внутри транзакции (outbox и смена статуса пишутся атомарно).
package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/pavelgr1408/food-platform/services/payment/internal/domain"
)

// Ошибки хранилища.
var (
	ErrNotFound = errors.New("store: not found")
	// ErrConflict — нарушена уникальность «одна живая / одна успешная попытка на заказ».
	ErrConflict = errors.New("store: payment conflict")
)

// DBTX — общее подмножество *pgxpool.Pool и pgx.Tx.
type DBTX interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Store — доступ к данным.
type Store struct{}

// New создаёт Store.
func New() *Store { return &Store{} }

const orderCols = `order_id, revision, order_number, restaurant_id, fulfillment, method, amount, currency, owner_user_id, updated_at`

func scanOrder(row pgx.Row) (domain.Order, error) {
	var o domain.Order
	var m string
	err := row.Scan(&o.ID, &o.Revision, &o.Number, &o.Restaurant, &o.Fulfillment, &m, &o.Amount, &o.Currency, &o.OwnerUserID, &o.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return o, ErrNotFound
	}
	o.Method = domain.Method(m)
	return o, err
}

// UpsertOrder сохраняет реплику заказа; событие с revision ≤ сохранённой игнорируется.
// Возвращает true, если реплика изменилась.
func (s *Store) UpsertOrder(ctx context.Context, db DBTX, o domain.Order) (bool, error) {
	tag, err := db.Exec(ctx, `INSERT INTO payment_orders (`+orderCols+`)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		ON CONFLICT (order_id) DO UPDATE SET revision = EXCLUDED.revision, order_number = EXCLUDED.order_number,
			restaurant_id = EXCLUDED.restaurant_id, fulfillment = EXCLUDED.fulfillment, method = EXCLUDED.method,
			amount = EXCLUDED.amount, currency = EXCLUDED.currency, owner_user_id = EXCLUDED.owner_user_id,
			updated_at = EXCLUDED.updated_at
		WHERE payment_orders.revision < EXCLUDED.revision`,
		o.ID, o.Revision, o.Number, o.Restaurant, o.Fulfillment, string(o.Method), o.Amount, o.Currency, o.OwnerUserID, o.UpdatedAt)
	return tag.RowsAffected() > 0, err
}

// GetOrder читает реплику заказа. lock=true — FOR UPDATE (только внутри tx): сериализует создание платежей заказа.
func (s *Store) GetOrder(ctx context.Context, db DBTX, id string, lock bool) (domain.Order, error) {
	q := `SELECT ` + orderCols + ` FROM payment_orders WHERE order_id = $1`
	if lock {
		q += ` FOR UPDATE`
	}
	return scanOrder(db.QueryRow(ctx, q, id))
}

// SetOrderStatus запоминает статус заказа; более старое событие (по status_at) игнорируется.
func (s *Store) SetOrderStatus(ctx context.Context, db DBTX, orderID, status string, at time.Time) error {
	_, err := db.Exec(ctx, `INSERT INTO order_statuses (order_id, status, status_at) VALUES ($1,$2,$3)
		ON CONFLICT (order_id) DO UPDATE SET status = EXCLUDED.status, status_at = EXCLUDED.status_at
		WHERE order_statuses.status_at <= EXCLUDED.status_at`, orderID, status, at)
	return err
}

// OrderStatus возвращает последний известный статус заказа ("" — событий orders.status ещё не было).
func (s *Store) OrderStatus(ctx context.Context, db DBTX, orderID string) (string, error) {
	var st string
	err := db.QueryRow(ctx, `SELECT status FROM order_statuses WHERE order_id = $1`, orderID).Scan(&st)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return st, err
}

const payCols = `id, order_id, attempt, method, status, amount, currency, COALESCE(gateway_ref, ''), payment_url, return_url,
	decline_reason, refund_required, poll_attempts, next_poll_at, created_at, updated_at, paid_at`

func scanPayment(row pgx.Row) (domain.Payment, error) {
	var p domain.Payment
	var m, st string
	err := row.Scan(&p.ID, &p.OrderID, &p.Attempt, &m, &st, &p.Amount, &p.Currency, &p.GatewayRef, &p.PaymentURL, &p.ReturnURL,
		&p.DeclineReason, &p.RefundNeeded, &p.PollAttempts, &p.NextPollAt, &p.CreatedAt, &p.UpdatedAt, &p.PaidAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, ErrNotFound
	}
	p.Method, p.Status = domain.Method(m), domain.Status(st)
	return p, err
}

// OrderPayments возвращает попытки оплаты заказа по номеру попытки.
func (s *Store) OrderPayments(ctx context.Context, db DBTX, orderID string) ([]domain.Payment, error) {
	rows, err := db.Query(ctx, `SELECT `+payCols+` FROM payments WHERE order_id = $1 ORDER BY attempt`, orderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Payment
	for rows.Next() {
		p, err := scanPayment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetPayment читает платёж; lock=true — FOR UPDATE.
func (s *Store) GetPayment(ctx context.Context, db DBTX, id string, lock bool) (domain.Payment, error) {
	q := `SELECT ` + payCols + ` FROM payments WHERE id = $1`
	if lock {
		q += ` FOR UPDATE`
	}
	return scanPayment(db.QueryRow(ctx, q, id))
}

// InsertPayment создаёт попытку (status=pending, gateway_ref пуст). Нарушение уникальности
// «одна живая/успешная попытка» → ErrConflict.
func (s *Store) InsertPayment(ctx context.Context, db DBTX, p domain.Payment) error {
	_, err := db.Exec(ctx, `INSERT INTO payments (id, order_id, attempt, method, status, amount, currency, return_url,
			next_poll_at, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$10)`,
		p.ID, p.OrderID, p.Attempt, string(p.Method), string(p.Status), p.Amount, p.Currency, p.ReturnURL, p.NextPollAt, p.CreatedAt)
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23505" {
		return ErrConflict
	}
	return err
}

// SetGatewayOperation записывает операцию шлюза (после успешного Create).
func (s *Store) SetGatewayOperation(ctx context.Context, db DBTX, id, ref, url string, at time.Time) error {
	_, err := db.Exec(ctx, `UPDATE payments SET gateway_ref = $2, payment_url = $3, updated_at = $4 WHERE id = $1`, id, ref, url, at)
	return err
}

// Update описывает изменение статуса платежа.
type Update struct {
	Status       domain.Status
	Reason       string
	GatewayRef   string // "" — не менять
	PaidAt       *time.Time
	RefundNeeded bool
	NextPollAt   *time.Time // nil — опрос больше не нужен
	At           time.Time
}

// ApplyUpdate меняет статус и связанные поля. Допустимость перехода проверяет вызывающий под блокировкой.
func (s *Store) ApplyUpdate(ctx context.Context, db DBTX, id string, u Update) error {
	_, err := db.Exec(ctx, `UPDATE payments SET status = $2, decline_reason = CASE WHEN $3 <> '' THEN $3 ELSE decline_reason END,
			gateway_ref = COALESCE(gateway_ref, NULLIF($4, '')), paid_at = COALESCE($5, paid_at),
			refund_required = refund_required OR $6, next_poll_at = $7, updated_at = $8
		WHERE id = $1`, id, string(u.Status), u.Reason, u.GatewayRef, u.PaidAt, u.RefundNeeded, u.NextPollAt, u.At)
	return err
}

// ScheduleNextPoll двигает следующий опрос и увеличивает счётчик попыток.
func (s *Store) ScheduleNextPoll(ctx context.Context, db DBTX, id string, next time.Time) error {
	_, err := db.Exec(ctx, `UPDATE payments SET next_poll_at = $2, poll_attempts = poll_attempts + 1
		WHERE id = $1 AND status IN ('pending','authorized','unknown')`, id, next)
	return err
}

// ClaimDue выбирает платежи, которым пора опросить шлюз, и «арендует» их: сдвигает next_poll_at на lease,
// чтобы другой экземпляр не опрашивал те же платежи, а блокировка не держалась на время вызова шлюза.
func (s *Store) ClaimDue(ctx context.Context, db DBTX, now time.Time, lease time.Duration, limit int) ([]domain.Payment, error) {
	rows, err := db.Query(ctx, `WITH due AS (
			SELECT id FROM payments WHERE status IN ('pending','authorized','unknown') AND next_poll_at <= $1
			ORDER BY next_poll_at LIMIT $3 FOR UPDATE SKIP LOCKED)
		UPDATE payments SET next_poll_at = $1 + make_interval(secs => $2)
		WHERE id IN (SELECT id FROM due) RETURNING `+payCols, now, lease.Seconds(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Payment
	for rows.Next() {
		p, err := scanPayment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// TryRecordWebhook запоминает eventId уведомления; false — уже принимали (дубликат).
func (s *Store) TryRecordWebhook(ctx context.Context, db DBTX, eventID, source, paymentID string) (bool, error) {
	tag, err := db.Exec(ctx, `INSERT INTO webhook_events (event_id, source, payment_id) VALUES ($1,$2,$3) ON CONFLICT DO NOTHING`,
		eventID, source, paymentID)
	return tag.RowsAffected() == 1, err
}

// UnknownStats — число платежей в unknown и возраст самого старого (метрики/алерты).
func (s *Store) UnknownStats(ctx context.Context, db DBTX) (count int64, oldestSeconds float64, err error) {
	err = db.QueryRow(ctx, `SELECT count(*), COALESCE(EXTRACT(EPOCH FROM now() - min(updated_at)), 0)
		FROM payments WHERE status = 'unknown'`).Scan(&count, &oldestSeconds)
	return
}
