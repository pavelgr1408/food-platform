// Package store — PostgreSQL-хранилище notification-service. Методы принимают DBTX: работают и с пулом,
// и внутри транзакции (консьюмер пишет уведомления в той же транзакции, что и запись inbox).
package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/pavelgr1408/food-platform/services/notification/internal/domain"
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

const tokenCols = `id, user_id, platform, token, permissions_granted, locale, registered_at, updated_at`

func scanToken(row pgx.Row) (domain.DeviceToken, error) {
	var t domain.DeviceToken
	var pl, loc string
	err := row.Scan(&t.ID, &t.UserID, &pl, &t.Token, &t.PermissionsGranted, &loc, &t.RegisteredAt, &t.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return t, ErrNotFound
	}
	t.Platform, t.Locale = domain.Platform(pl), domain.Locale(loc)
	return t, err
}

// TokenByValue читает токен по (платформа, значение); forUpdate блокирует строку.
func (s *Store) TokenByValue(ctx context.Context, db DBTX, p domain.Platform, token string, forUpdate bool) (domain.DeviceToken, error) {
	q := `SELECT ` + tokenCols + ` FROM device_tokens WHERE platform = $1 AND token = $2`
	if forUpdate {
		q += ` FOR UPDATE`
	}
	return scanToken(db.QueryRow(ctx, q, string(p), token))
}

// InsertToken создаёт токен; false — такой (платформа, токен) уже есть (гонка регистраций).
func (s *Store) InsertToken(ctx context.Context, db DBTX, t domain.DeviceToken) (bool, error) {
	tag, err := db.Exec(ctx, `INSERT INTO device_tokens (id, user_id, platform, token, permissions_granted, locale, registered_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT (platform, token) DO NOTHING`,
		t.ID, t.UserID, string(t.Platform), t.Token, t.PermissionsGranted, string(t.Locale), t.RegisteredAt, t.UpdatedAt)
	return tag.RowsAffected() == 1, err
}

// UpdateToken обновляет владельца, разрешения, язык и (при смене владельца) время регистрации.
func (s *Store) UpdateToken(ctx context.Context, db DBTX, t domain.DeviceToken) error {
	_, err := db.Exec(ctx, `UPDATE device_tokens SET user_id=$2, permissions_granted=$3, locale=$4, registered_at=$5, updated_at=$6 WHERE id=$1`,
		t.ID, t.UserID, t.PermissionsGranted, string(t.Locale), t.RegisteredAt, t.UpdatedAt)
	return err
}

// ListTokens — токены пользователя от новых к старым.
func (s *Store) ListTokens(ctx context.Context, db DBTX, userID string) ([]domain.DeviceToken, error) {
	rows, err := db.Query(ctx, `SELECT `+tokenCols+` FROM device_tokens WHERE user_id = $1 ORDER BY registered_at DESC, id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.DeviceToken{}
	for rows.Next() {
		t, err := scanToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// DeliverableTokens — токены пользователя с разрешёнными уведомлениями.
func (s *Store) DeliverableTokens(ctx context.Context, db DBTX, userID string) ([]domain.DeviceToken, error) {
	rows, err := db.Query(ctx, `SELECT `+tokenCols+` FROM device_tokens WHERE user_id = $1 AND permissions_granted ORDER BY registered_at DESC, id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.DeviceToken
	for rows.Next() {
		t, err := scanToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// DeleteUserToken удаляет токен пользователя; false — такого токена у пользователя нет.
// Неотправленные уведомления для него удаляются каскадом.
func (s *Store) DeleteUserToken(ctx context.Context, db DBTX, userID, tokenID string) (bool, error) {
	tag, err := db.Exec(ctx, `DELETE FROM device_tokens WHERE id = $1 AND user_id = $2`, tokenID, userID)
	return tag.RowsAffected() == 1, err
}

// DeleteTokenByID удаляет токен без проверки владельца (провайдер сообщил, что токен недействителен).
func (s *Store) DeleteTokenByID(ctx context.Context, db DBTX, tokenID string) (bool, error) {
	tag, err := db.Exec(ctx, `DELETE FROM device_tokens WHERE id = $1`, tokenID)
	return tag.RowsAffected() == 1, err
}

// EvictOldTokens оставляет у пользователя не более keep самых новых токенов; возвращает число удалённых.
func (s *Store) EvictOldTokens(ctx context.Context, db DBTX, userID string, keep int) (int64, error) {
	tag, err := db.Exec(ctx, `DELETE FROM device_tokens WHERE id IN (
		SELECT id FROM device_tokens WHERE user_id = $1 ORDER BY registered_at DESC, id OFFSET $2)`, userID, keep)
	return tag.RowsAffected(), err
}

// DropPending удаляет неотправленные уведомления токена (токен сменил владельца).
func (s *Store) DropPending(ctx context.Context, db DBTX, tokenID string) error {
	_, err := db.Exec(ctx, `DELETE FROM notifications WHERE token_id = $1 AND status = 'pending'`, tokenID)
	return err
}

// InsertNotification ставит уведомление в очередь; false — оно уже есть (дубль события).
func (s *Store) InsertNotification(ctx context.Context, db DBTX, n domain.Notification, at time.Time) (bool, error) {
	tag, err := db.Exec(ctx, `INSERT INTO notifications (id, order_id, order_number, type, reason, priority, title, body, token_id,
			status, attempts, next_attempt_at, event_at, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,'pending',0,$10,$11,$10,$10)
		ON CONFLICT (order_id, type, token_id) DO NOTHING`,
		n.ID, n.OrderID, n.OrderNumber, string(n.Type), n.Reason, string(n.Priority), n.Title, n.Body, n.TokenID, at, n.EventAt)
	return tag.RowsAffected() == 1, err
}

// Due — уведомление, взятое в отправку, вместе с данными устройства.
type Due struct {
	domain.Notification
	Platform           domain.Platform
	Token              string
	PermissionsGranted bool
}

// ExpirePending закрывает как skipped неотправленные уведомления, чьё событие старше порога.
func (s *Store) ExpirePending(ctx context.Context, db DBTX, olderThan, now time.Time) (int64, error) {
	tag, err := db.Exec(ctx, `UPDATE notifications SET status='skipped', last_error='expired', updated_at=$2
		WHERE status='pending' AND event_at < $1`, olderThan, now)
	return tag.RowsAffected(), err
}

// Claim берёт до limit созревших уведомлений в аренду (SKIP LOCKED, сдвиг next_attempt_at на lease, attempts+1).
// Если процесс упадёт, уведомление вернётся в работу после окончания аренды.
func (s *Store) Claim(ctx context.Context, db DBTX, now time.Time, lease time.Duration, limit int) ([]Due, error) {
	rows, err := db.Query(ctx, `
		WITH due AS (
			SELECT id FROM notifications WHERE status = 'pending' AND next_attempt_at <= $1
			ORDER BY next_attempt_at LIMIT $3 FOR UPDATE SKIP LOCKED)
		UPDATE notifications n SET attempts = n.attempts + 1, next_attempt_at = $1 + make_interval(secs => $2::float8), updated_at = $1
		FROM due, device_tokens t
		WHERE n.id = due.id AND t.id = n.token_id
		RETURNING n.id, n.order_id, n.order_number, n.type, n.reason, n.priority, n.title, n.body, n.token_id, n.attempts, n.event_at,
			t.platform, t.token, t.permissions_granted`, now, lease.Seconds(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Due
	for rows.Next() {
		var d Due
		var typ, prio, pl string
		if err := rows.Scan(&d.ID, &d.OrderID, &d.OrderNumber, &typ, &d.Reason, &prio, &d.Title, &d.Body, &d.TokenID, &d.Attempts, &d.EventAt,
			&pl, &d.Token, &d.PermissionsGranted); err != nil {
			return nil, err
		}
		d.Type, d.Priority, d.Platform = domain.Type(typ), domain.Priority(prio), domain.Platform(pl)
		out = append(out, d)
	}
	return out, rows.Err()
}

// MarkSent — уведомление доставлено провайдеру.
func (s *Store) MarkSent(ctx context.Context, db DBTX, id string, now time.Time) error {
	_, err := db.Exec(ctx, `UPDATE notifications SET status='sent', sent_at=$2, updated_at=$2, last_error='' WHERE id=$1 AND status='pending'`, id, now)
	return err
}

// MarkFinal закрывает уведомление как failed или skipped с причиной.
func (s *Store) MarkFinal(ctx context.Context, db DBTX, id string, st domain.Status, reason string, now time.Time) error {
	_, err := db.Exec(ctx, `UPDATE notifications SET status=$2, last_error=$3, updated_at=$4 WHERE id=$1 AND status='pending'`, id, string(st), reason, now)
	return err
}

// Retry возвращает уведомление в очередь на момент at.
func (s *Store) Retry(ctx context.Context, db DBTX, id string, at time.Time, reason string, now time.Time) error {
	_, err := db.Exec(ctx, `UPDATE notifications SET next_attempt_at=$2, last_error=$3, updated_at=$4 WHERE id=$1 AND status='pending'`, id, at, reason, now)
	return err
}

// PurgeDone удаляет закрытые уведомления старше порога.
func (s *Store) PurgeDone(ctx context.Context, db DBTX, olderThan time.Time) (int64, error) {
	tag, err := db.Exec(ctx, `DELETE FROM notifications WHERE status <> 'pending' AND updated_at < $1`, olderThan)
	return tag.RowsAffected(), err
}

// ByOrder — уведомления заказа (для тестов и диагностики).
func (s *Store) ByOrder(ctx context.Context, db DBTX, orderID string) ([]Row, error) {
	rows, err := db.Query(ctx, `SELECT id, type, status, attempts, last_error, token_id FROM notifications WHERE order_id = $1 ORDER BY created_at, id`, orderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Row
	for rows.Next() {
		var r Row
		var typ, st string
		if err := rows.Scan(&r.ID, &typ, &st, &r.Attempts, &r.LastError, &r.TokenID); err != nil {
			return nil, err
		}
		r.Type, r.Status = domain.Type(typ), domain.Status(st)
		out = append(out, r)
	}
	return out, rows.Err()
}

// Row — краткая запись очереди.
type Row struct {
	ID, TokenID string
	Type        domain.Type
	Status      domain.Status
	Attempts    int
	LastError   string
}

// Stats — число неотправленных и возраст самого старого (секунды); для алертов.
func (s *Store) Stats(ctx context.Context, db DBTX, now time.Time) (pending int64, oldestAge float64, err error) {
	var oldest *time.Time
	err = db.QueryRow(ctx, `SELECT count(*), min(created_at) FROM notifications WHERE status = 'pending'`).Scan(&pending, &oldest)
	if oldest != nil {
		oldestAge = now.Sub(*oldest).Seconds()
	}
	return
}
