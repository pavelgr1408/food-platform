// Package store — PostgreSQL-хранилище delivery-service. Методы принимают DBTX: работают и с пулом,
// и внутри транзакции (смена статуса и событие outbox пишутся атомарно).
package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/pavelgr1408/food-platform/services/delivery/internal/domain"
)

// ErrNotFound — записи нет.
var ErrNotFound = errors.New("store: not found")

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

const cols = `id, order_id, order_number, restaurant_id, owner_user_id, revision, status, fail_reason, address,
	dest_lat, dest_lon, contact_name, contact_phone, amount, currency, pay_on_delivery, payment_url, link_pushed,
	courier_ref, courier_id, courier_name, courier_phone, eta_at, paid_reported, cancelled, search_started_at,
	next_poll_at, poll_attempts, created_at, updated_at`

func scan(row pgx.Row) (domain.Delivery, error) {
	var d domain.Delivery
	var st, fr string
	err := row.Scan(&d.ID, &d.OrderID, &d.OrderNumber, &d.RestaurantID, &d.OwnerUserID, &d.Revision, &st, &fr, &d.Address,
		&d.DestLat, &d.DestLon, &d.ContactName, &d.ContactPhone, &d.Amount, &d.Currency, &d.PayOnDelivery, &d.PaymentURL, &d.LinkPushed,
		&d.CourierRef, &d.CourierID, &d.CourierName, &d.CourierPhone, &d.EtaAt, &d.PaidReported, &d.Cancelled, &d.SearchStartedAt,
		&d.NextPollAt, &d.PollAttempts, &d.CreatedAt, &d.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return d, ErrNotFound
	}
	d.Status, d.FailReason = domain.Status(st), domain.FailReason(fr)
	return d, err
}

// GetByOrder читает доставку по заказу; forUpdate блокирует строку на время транзакции.
func (s *Store) GetByOrder(ctx context.Context, db DBTX, orderID string, forUpdate bool) (domain.Delivery, error) {
	q := `SELECT ` + cols + ` FROM deliveries WHERE order_id = $1`
	if forUpdate {
		q += ` FOR UPDATE`
	}
	return scan(db.QueryRow(ctx, q, orderID))
}

// GetByID читает доставку по идентификатору.
func (s *Store) GetByID(ctx context.Context, db DBTX, id string, forUpdate bool) (domain.Delivery, error) {
	q := `SELECT ` + cols + ` FROM deliveries WHERE id = $1`
	if forUpdate {
		q += ` FOR UPDATE`
	}
	return scan(db.QueryRow(ctx, q, id))
}

// Insert создаёт доставку; false — доставка по этому заказу уже есть (гонка/повтор события).
func (s *Store) Insert(ctx context.Context, db DBTX, d domain.Delivery) (bool, error) {
	tag, err := db.Exec(ctx, `INSERT INTO deliveries (id, order_id, order_number, restaurant_id, owner_user_id, revision, status,
			address, dest_lat, dest_lon, contact_name, contact_phone, amount, currency, pay_on_delivery, payment_url,
			search_started_at, next_poll_at, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20)
		ON CONFLICT (order_id) DO NOTHING`,
		d.ID, d.OrderID, d.OrderNumber, d.RestaurantID, d.OwnerUserID, d.Revision, string(d.Status), d.Address, d.DestLat, d.DestLon,
		d.ContactName, d.ContactPhone, d.Amount, d.Currency, d.PayOnDelivery, d.PaymentURL, d.SearchStartedAt, d.NextPollAt,
		d.CreatedAt, d.UpdatedAt)
	return tag.RowsAffected() == 1, err
}

// BumpRevision фиксирует более новую revision orders.state (владелец, сумма, контакты могли измениться).
func (s *Store) BumpRevision(ctx context.Context, db DBTX, orderID string, rev int, d domain.Delivery) (bool, error) {
	tag, err := db.Exec(ctx, `UPDATE deliveries SET revision = $2, amount = $3, currency = $4, contact_name = $5, contact_phone = $6,
			updated_at = GREATEST(updated_at, $7)
		WHERE order_id = $1 AND revision < $2`, orderID, rev, d.Amount, d.Currency, d.ContactName, d.ContactPhone, d.UpdatedAt)
	return tag.RowsAffected() == 1, err
}

// Update записывает изменяемые поля доставки (вызывается под FOR UPDATE).
func (s *Store) Update(ctx context.Context, db DBTX, d domain.Delivery) error {
	_, err := db.Exec(ctx, `UPDATE deliveries SET status = $2, fail_reason = $3, payment_url = $4, link_pushed = $5,
			courier_ref = $6, courier_id = $7, courier_name = $8, courier_phone = $9, eta_at = $10, paid_reported = $11,
			cancelled = $12, next_poll_at = $13, poll_attempts = $14, updated_at = $15
		WHERE id = $1`, d.ID, string(d.Status), string(d.FailReason), d.PaymentURL, d.LinkPushed, d.CourierRef, d.CourierID, d.CourierName,
		d.CourierPhone, d.EtaAt, d.PaidReported, d.Cancelled, d.NextPollAt, d.PollAttempts, d.UpdatedAt)
	return err
}

// AddHistory пишет строку журнала.
func (s *Store) AddHistory(ctx context.Context, db DBTX, deliveryID string, st domain.Status, reason, source string, at time.Time) error {
	_, err := db.Exec(ctx, `INSERT INTO delivery_history (delivery_id, status, reason, source, at) VALUES ($1,$2,$3,$4,$5)`,
		deliveryID, string(st), reason, source, at)
	return err
}

// History читает журнал (для тестов и разбора инцидентов).
func (s *Store) History(ctx context.Context, db DBTX, deliveryID string) ([]string, error) {
	rows, err := db.Query(ctx, `SELECT status || CASE WHEN reason = '' THEN '' ELSE ':' || reason END || '@' || source
		FROM delivery_history WHERE delivery_id = $1 ORDER BY id`, deliveryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// ClaimDue арендует доставки, которым пора обращаться к курьерской системе (диспетчеризация и опрос).
// Аренда — сдвиг next_poll_at: вызовы идут без блокировок БД, а несколько экземпляров не берут одно и то же.
func (s *Store) ClaimDue(ctx context.Context, db DBTX, now time.Time, lease time.Duration, limit int) ([]domain.Delivery, error) {
	rows, err := db.Query(ctx, `WITH due AS (
			SELECT id FROM deliveries WHERE next_poll_at IS NOT NULL AND next_poll_at <= $1
			ORDER BY next_poll_at LIMIT $3 FOR UPDATE SKIP LOCKED)
		UPDATE deliveries SET next_poll_at = $1 + make_interval(secs => $2)
		WHERE id IN (SELECT id FROM due) RETURNING `+cols, now, lease.Seconds(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Delivery
	for rows.Next() {
		d, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// ScheduleNext назначает следующий опрос (только для нетерминальной доставки).
func (s *Store) ScheduleNext(ctx context.Context, db DBTX, id string, at time.Time, attempts int) error {
	_, err := db.Exec(ctx, `UPDATE deliveries SET next_poll_at = $2, poll_attempts = $3
		WHERE id = $1 AND status NOT IN ('delivered','failed')`, id, at, attempts)
	return err
}

// TryRecordWebhook запоминает eventId; false — уже принимали (дубликат).
func (s *Store) TryRecordWebhook(ctx context.Context, db DBTX, eventID, deliveryID string) (bool, error) {
	tag, err := db.Exec(ctx, `INSERT INTO webhook_events (event_id, delivery_id) VALUES ($1,$2) ON CONFLICT DO NOTHING`, eventID, deliveryID)
	return tag.RowsAffected() == 1, err
}

// SavePaymentLink запоминает ссылку (она могла прийти раньше orders.state) и возвращает true, если доставка уже есть
// и ссылка записана в неё.
func (s *Store) SavePaymentLink(ctx context.Context, db DBTX, orderID, url string) (bool, error) {
	if _, err := db.Exec(ctx, `INSERT INTO payment_links (order_id, url) VALUES ($1,$2)
		ON CONFLICT (order_id) DO UPDATE SET url = EXCLUDED.url, received_at = now()`, orderID, url); err != nil {
		return false, err
	}
	tag, err := db.Exec(ctx, `UPDATE deliveries SET payment_url = $2, link_pushed = false WHERE order_id = $1 AND payment_url IS DISTINCT FROM $2`, orderID, url)
	return tag.RowsAffected() == 1, err
}

// PaymentLink читает сохранённую ссылку (” — нет).
func (s *Store) PaymentLink(ctx context.Context, db DBTX, orderID string) (string, error) {
	var u string
	err := db.QueryRow(ctx, `SELECT url FROM payment_links WHERE order_id = $1`, orderID).Scan(&u)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return u, err
}

// MarkOrderCancelled запоминает отмену заказа (до прихода orders.state).
func (s *Store) MarkOrderCancelled(ctx context.Context, db DBTX, orderID string) error {
	_, err := db.Exec(ctx, `INSERT INTO cancelled_orders (order_id) VALUES ($1) ON CONFLICT DO NOTHING`, orderID)
	return err
}

// IsOrderCancelled — заказ уже отменён.
func (s *Store) IsOrderCancelled(ctx context.Context, db DBTX, orderID string) (bool, error) {
	var ok bool
	err := db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM cancelled_orders WHERE order_id = $1)`, orderID).Scan(&ok)
	return ok, err
}

// Stats — метрики: активные доставки и возраст самой старой в searching.
func (s *Store) Stats(ctx context.Context, db DBTX) (active int64, oldestSearching float64, err error) {
	err = db.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status NOT IN ('delivered','failed')),
			COALESCE(EXTRACT(EPOCH FROM now() - min(search_started_at) FILTER (WHERE status = 'searching')), 0)
		FROM deliveries`).Scan(&active, &oldestSearching)
	return
}

// Expedite назначает ближайший опрос нетерминальной доставки заказа.
func (s *Store) Expedite(ctx context.Context, db DBTX, orderID string, at time.Time) error {
	_, err := db.Exec(ctx, `UPDATE deliveries SET next_poll_at = LEAST(COALESCE(next_poll_at, $2), $2)
		WHERE order_id = $1 AND status NOT IN ('delivered','failed')`, orderID, at)
	return err
}
