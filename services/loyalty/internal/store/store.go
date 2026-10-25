// Package store — PostgreSQL-хранилище loyalty-service. Методы принимают DBTX: работают и с пулом,
// и внутри транзакции (изменение акции, история и событие outbox пишутся атомарно).
package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/pavelgr1408/food-platform/services/loyalty/internal/domain"
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

const cols = `id, title, description, scope, coalesce(restaurant_id::text, ''), status, priority, starts_at, ends_at,
	currency, discount_type, discount_value, conditions, version, created_by, created_at, updated_at`

type condJSON struct {
	MenuItemIDs []string `json:"menuItemIds,omitempty"`
	MinSubtotal int64    `json:"minSubtotal,omitempty"`
	Fulfillment string   `json:"fulfillment,omitempty"`
	Stackable   bool     `json:"stackable,omitempty"`
}

func scan(row pgx.Row) (domain.Promotion, error) {
	var p domain.Promotion
	var scope, status, dt string
	var cond []byte
	err := row.Scan(&p.ID, &p.Title, &p.Description, &scope, &p.RestaurantID, &status, &p.Priority, &p.StartsAt, &p.EndsAt,
		&p.Currency, &dt, &p.Discount.Value, &cond, &p.Version, &p.CreatedBy, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, ErrNotFound
	}
	if err != nil {
		return p, err
	}
	p.Scope, p.Status, p.Discount.Type = domain.Scope(scope), domain.Status(status), domain.DiscountType(dt)
	p.StartsAt, p.EndsAt, p.CreatedAt, p.UpdatedAt = p.StartsAt.UTC(), p.EndsAt.UTC(), p.CreatedAt.UTC(), p.UpdatedAt.UTC()
	var c condJSON
	if err := json.Unmarshal(cond, &c); err != nil {
		return p, err
	}
	p.Conditions = domain.Conditions{MenuItemIDs: c.MenuItemIDs, MinSubtotal: c.MinSubtotal, Fulfillment: c.Fulfillment, Stackable: c.Stackable}
	return p, nil
}

func condBytes(c domain.Conditions) ([]byte, error) {
	return json.Marshal(condJSON{MenuItemIDs: c.MenuItemIDs, MinSubtotal: c.MinSubtotal, Fulfillment: c.Fulfillment, Stackable: c.Stackable})
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// Insert создаёт акцию.
func (s *Store) Insert(ctx context.Context, db DBTX, p domain.Promotion) error {
	cond, err := condBytes(p.Conditions)
	if err != nil {
		return err
	}
	_, err = db.Exec(ctx, `INSERT INTO promotions (id, title, description, scope, restaurant_id, status, priority, starts_at, ends_at,
		currency, discount_type, discount_value, conditions, version, created_by, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)`,
		p.ID, p.Title, p.Description, string(p.Scope), nilIfEmpty(p.RestaurantID), string(p.Status), p.Priority, p.StartsAt, p.EndsAt,
		p.Currency, string(p.Discount.Type), p.Discount.Value, cond, p.Version, p.CreatedBy, p.CreatedAt, p.UpdatedAt)
	return err
}

// Update записывает изменяемые поля и статус (scope/restaurant_id неизменны).
func (s *Store) Update(ctx context.Context, db DBTX, p domain.Promotion) error {
	cond, err := condBytes(p.Conditions)
	if err != nil {
		return err
	}
	_, err = db.Exec(ctx, `UPDATE promotions SET title=$2, description=$3, status=$4, priority=$5, starts_at=$6, ends_at=$7,
		currency=$8, discount_type=$9, discount_value=$10, conditions=$11, version=$12, updated_at=$13 WHERE id=$1`,
		p.ID, p.Title, p.Description, string(p.Status), p.Priority, p.StartsAt, p.EndsAt,
		p.Currency, string(p.Discount.Type), p.Discount.Value, cond, p.Version, p.UpdatedAt)
	return err
}

// Get читает акцию; forUpdate блокирует строку на время транзакции.
func (s *Store) Get(ctx context.Context, db DBTX, id string, forUpdate bool) (domain.Promotion, error) {
	q := `SELECT ` + cols + ` FROM promotions WHERE id = $1`
	if forUpdate {
		q += ` FOR UPDATE`
	}
	return scan(db.QueryRow(ctx, q, id))
}

// ListActive — акции со статусом active и не истёкшим сроком (для проверки конфликтов).
func (s *Store) ListActive(ctx context.Context, db DBTX, now time.Time) ([]domain.Promotion, error) {
	return s.list(ctx, db, `SELECT `+cols+` FROM promotions WHERE status = 'active' AND ends_at > $1 ORDER BY id`, now)
}

// ListCurrent — акции, действующие в момент now; restaurantID пуст — только национальные.
// Порядок: приоритет по убыванию, затем начало и id.
func (s *Store) ListCurrent(ctx context.Context, db DBTX, now time.Time, restaurantID string) ([]domain.Promotion, error) {
	return s.list(ctx, db, `SELECT `+cols+` FROM promotions
		WHERE status = 'active' AND starts_at <= $1 AND ends_at > $1
		  AND (scope = 'national' OR ($2::uuid IS NOT NULL AND restaurant_id = $2::uuid))
		ORDER BY priority DESC, starts_at, id`, now, nilIfEmpty(restaurantID))
}

func (s *Store) list(ctx context.Context, db DBTX, q string, args ...any) ([]domain.Promotion, error) {
	rows, err := db.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Promotion{}
	for rows.Next() {
		p, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Revision — запись истории.
type Revision struct {
	Version    int
	Action     string
	ActorID    string
	OccurredAt time.Time
	Snapshot   json.RawMessage
}

// AddHistory добавляет запись истории (снимок — сериализованная акция).
func (s *Store) AddHistory(ctx context.Context, db DBTX, id string, version int, action, actor string, at time.Time, snapshot []byte) error {
	_, err := db.Exec(ctx, `INSERT INTO promotion_history (promotion_id, version, action, actor_id, occurred_at, snapshot)
		VALUES ($1,$2,$3,$4,$5,$6)`, id, version, action, actor, at, snapshot)
	return err
}

// History — записи истории по возрастанию версии (в рамках одной версии — по времени).
func (s *Store) History(ctx context.Context, db DBTX, id string) ([]Revision, error) {
	rows, err := db.Query(ctx, `SELECT version, action, actor_id, occurred_at, snapshot FROM promotion_history
		WHERE promotion_id = $1 ORDER BY version, occurred_at`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Revision{}
	for rows.Next() {
		var r Revision
		if err := rows.Scan(&r.Version, &r.Action, &r.ActorID, &r.OccurredAt, &r.Snapshot); err != nil {
			return nil, err
		}
		r.OccurredAt = r.OccurredAt.UTC()
		out = append(out, r)
	}
	return out, rows.Err()
}

// LockActivation берёт транзакционный advisory-lock: проверка конфликтов и активация
// выполняются строго по очереди (иначе две конфликтующие акции могли бы активироваться одновременно).
func (s *Store) LockActivation(ctx context.Context, db DBTX) error {
	_, err := db.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('loyalty.activation'))`)
	return err
}
