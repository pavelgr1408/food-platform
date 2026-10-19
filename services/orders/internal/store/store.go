// Package store — PostgreSQL-хранилище orders-service. Методы принимают DBTX, поэтому
// работают и с пулом, и внутри транзакции (outbox и ключ идемпотентности пишутся в той же tx).
package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/pavelgr1408/food-platform/services/orders/internal/domain"
)

// Ошибки хранилища.
var (
	ErrNotFound = errors.New("store: not found")
)

// DBTX — общее подмножество *pgxpool.Pool и pgx.Tx.
type DBTX interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Store — доступ к данным. Не хранит состояния: пул/транзакцию передаёт вызывающий.
type Store struct{}

// New создаёт Store.
func New() *Store { return &Store{} }

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

const cartCols = `id, restaurant_id, owner_user_id, status, order_id, revision, currency, fulfillment_type,
	address_raw, address_lat, address_lon, contact_name, contact_phone, contact_email, created_at, updated_at`

func scanCart(row pgx.Row) (domain.Cart, error) {
	var (
		c        domain.Cart
		owner    *string
		orderID  *string
		ft       *string
		lat, lon *float64
	)
	err := row.Scan(&c.ID, &c.RestaurantID, &owner, &c.Status, &orderID, &c.Revision, &c.Currency, &ft,
		&c.Address.Raw, &lat, &lon, &c.Contact.Name, &c.Contact.Phone, &c.Contact.Email, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, ErrNotFound
	}
	if err != nil {
		return c, err
	}
	c.OwnerUserID, c.OrderID = deref(owner), deref(orderID)
	c.Fulfillment = domain.FulfillmentType(deref(ft))
	c.Address.Lat, c.Address.Lon = lat, lon
	c.CreatedAt, c.UpdatedAt = c.CreatedAt.UTC(), c.UpdatedAt.UTC()
	return c, nil
}

// InsertCart создаёт пустую открытую корзину.
func (s *Store) InsertCart(ctx context.Context, db DBTX, c domain.Cart) error {
	_, err := db.Exec(ctx, `INSERT INTO carts (id, restaurant_id, owner_user_id, currency, contact_name, contact_phone, contact_email)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		c.ID, c.RestaurantID, nilIfEmpty(c.OwnerUserID), c.Currency, c.Contact.Name, c.Contact.Phone, c.Contact.Email)
	return err
}

// GetCart читает корзину с позициями. lock=true — SELECT … FOR UPDATE (только внутри tx).
func (s *Store) GetCart(ctx context.Context, db DBTX, id string, lock bool) (domain.Cart, error) {
	q := `SELECT ` + cartCols + ` FROM carts WHERE id = $1`
	if lock {
		q += ` FOR UPDATE`
	}
	c, err := scanCart(db.QueryRow(ctx, q, id))
	if err != nil {
		return c, err
	}
	c.Items, err = s.items(ctx, db, "cart_items", "cart_id", id)
	return c, err
}

func (s *Store) items(ctx context.Context, db DBTX, table, col, id string) ([]domain.Line, error) {
	rows, err := db.Query(ctx, `SELECT menu_item_id, name, quantity, modifier_ids, unit_price, line_total
		FROM `+table+` WHERE `+col+` = $1 ORDER BY position`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Line{}
	for rows.Next() {
		var l domain.Line
		if err := rows.Scan(&l.MenuItemID, &l.Name, &l.Quantity, &l.ModifierIDs, &l.UnitPrice, &l.LineTotal); err != nil {
			return nil, err
		}
		if l.ModifierIDs == nil {
			l.ModifierIDs = []string{}
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// ReplaceCartItems заменяет состав открытой корзины и повышает revision. Вызывать в tx
// после GetCart(lock=true). Возвращает новую ревизию.
func (s *Store) ReplaceCartItems(ctx context.Context, db DBTX, id, currency string, lines []domain.Line) (int64, error) {
	if _, err := db.Exec(ctx, `DELETE FROM cart_items WHERE cart_id = $1`, id); err != nil {
		return 0, err
	}
	for i, l := range lines {
		mods := l.ModifierIDs
		if mods == nil {
			mods = []string{}
		}
		if _, err := db.Exec(ctx, `INSERT INTO cart_items (cart_id, position, menu_item_id, name, quantity, modifier_ids, unit_price, line_total)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, id, i, l.MenuItemID, l.Name, l.Quantity, mods, l.UnitPrice, l.LineTotal); err != nil {
			return 0, err
		}
	}
	var rev int64
	err := db.QueryRow(ctx, `UPDATE carts SET revision = revision + 1, currency = $2, updated_at = now() WHERE id = $1 RETURNING revision`,
		id, currency).Scan(&rev)
	return rev, err
}

// SetCartFulfillment сохраняет способ получения; для самовывоза адрес очищается.
func (s *Store) SetCartFulfillment(ctx context.Context, db DBTX, id string, f domain.FulfillmentType, a domain.Address) error {
	if f == domain.FulfillmentPickup {
		a = domain.Address{}
	}
	_, err := db.Exec(ctx, `UPDATE carts SET fulfillment_type = $2, address_raw = $3, address_lat = $4, address_lon = $5,
		revision = revision + 1, updated_at = now() WHERE id = $1`, id, string(f), a.Raw, a.Lat, a.Lon)
	return err
}

// CloseCart помечает корзину оформленной.
func (s *Store) CloseCart(ctx context.Context, db DBTX, id, orderID string) error {
	_, err := db.Exec(ctx, `UPDATE carts SET status = 'ordered', order_id = $2, revision = revision + 1, updated_at = now() WHERE id = $1`, id, orderID)
	return err
}

// PurgeStaleCarts удаляет открытые корзины, не менявшиеся с before (позиции — каскадом).
func (s *Store) PurgeStaleCarts(ctx context.Context, db DBTX, before time.Time) (int64, error) {
	tag, err := db.Exec(ctx, `DELETE FROM carts WHERE status = 'open' AND updated_at < $1`, before)
	return tag.RowsAffected(), err
}
