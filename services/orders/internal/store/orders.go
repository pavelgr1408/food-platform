package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pavelgr1408/food-platform/services/orders/internal/domain"
)

const orderCols = `id, number, cart_id, restaurant_id, owner_user_id, channel, status, status_reason, payment_method,
	payment_status, fulfillment_type, address_raw, address_lat, address_lon, contact_name, contact_phone, contact_email,
	pickup_from, pickup_to, currency, subtotal, discount_total, total, revision, created_at, updated_at`

func scanOrder(row pgx.Row) (domain.Order, error) {
	var (
		o        domain.Order
		owner    *string
		lat, lon *float64
		st, pm   string
		ps, ft   string
	)
	err := row.Scan(&o.ID, &o.Number, &o.CartID, &o.RestaurantID, &owner, &o.Channel, &st, &o.StatusReason, &pm,
		&ps, &ft, &o.Address.Raw, &lat, &lon, &o.Contact.Name, &o.Contact.Phone, &o.Contact.Email,
		&o.PickupFrom, &o.PickupTo, &o.Currency, &o.Subtotal, &o.DiscountTotal, &o.Total, &o.Revision, &o.CreatedAt, &o.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return o, ErrNotFound
	}
	if err != nil {
		return o, err
	}
	o.OwnerUserID = deref(owner)
	o.Status, o.PaymentMethod, o.PaymentStatus, o.Fulfillment = domain.Status(st), domain.PaymentMethod(pm), domain.PaymentStatus(ps), domain.FulfillmentType(ft)
	o.Address.Lat, o.Address.Lon = lat, lon
	o.CreatedAt, o.UpdatedAt = o.CreatedAt.UTC(), o.UpdatedAt.UTC()
	if o.PickupFrom != nil {
		f, t := o.PickupFrom.UTC(), o.PickupTo.UTC()
		o.PickupFrom, o.PickupTo = &f, &t
	}
	return o, nil
}

// RestaurantCode возвращает короткий числовой код ресторана для номеров заказов, создавая его
// при первом обращении. Вызывать ВНЕ транзакции заказа (с пулом): код фиксируется сразу и не
// «сгорает» при откате оформления (identity-последовательности не откатываются).
func (s *Store) RestaurantCode(ctx context.Context, db DBTX, restaurantID string) (int, error) {
	var code int
	err := db.QueryRow(ctx, `SELECT code FROM restaurant_codes WHERE restaurant_id = $1`, restaurantID).Scan(&code)
	if errors.Is(err, pgx.ErrNoRows) {
		// ON CONFLICT: параллельный запрос мог создать код одновременно.
		if _, err = db.Exec(ctx, `INSERT INTO restaurant_codes (restaurant_id) VALUES ($1) ON CONFLICT DO NOTHING`, restaurantID); err != nil {
			return 0, err
		}
		err = db.QueryRow(ctx, `SELECT code FROM restaurant_codes WHERE restaurant_id = $1`, restaurantID).Scan(&code)
	}
	return code, err
}

// NextNumber выдаёт следующий номер заказа ресторана за (местный) день. Вызывать в tx создания
// заказа: строка счётчика блокируется до коммита, номера идут без дыр и дублей, откат
// оформления возвращает номер.
func (s *Store) NextNumber(ctx context.Context, db DBTX, restaurantID string, code int, day time.Time) (string, error) {
	var seq int
	err := db.QueryRow(ctx, `INSERT INTO order_counters (restaurant_id, day, last_seq) VALUES ($1, $2, 1)
		ON CONFLICT (restaurant_id, day) DO UPDATE SET last_seq = order_counters.last_seq + 1 RETURNING last_seq`,
		restaurantID, day.Format("2006-01-02")).Scan(&seq)
	if err != nil {
		return "", err
	}
	return domain.FormatNumber(code, day, seq), nil
}

// InsertOrder сохраняет заказ с позициями и первую запись истории статусов.
func (s *Store) InsertOrder(ctx context.Context, db DBTX, o domain.Order, actor string) error {
	_, err := db.Exec(ctx, `INSERT INTO orders (id, number, cart_id, restaurant_id, owner_user_id, channel, status, status_reason, payment_method,
		payment_status, fulfillment_type, address_raw, address_lat, address_lon, contact_name, contact_phone, contact_email,
		currency, subtotal, discount_total, total, revision, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$23)`,
		o.ID, o.Number, o.CartID, o.RestaurantID, nilIfEmpty(o.OwnerUserID), o.Channel, string(o.Status), o.StatusReason, string(o.PaymentMethod),
		string(o.PaymentStatus), string(o.Fulfillment), o.Address.Raw, o.Address.Lat, o.Address.Lon, o.Contact.Name, o.Contact.Phone, o.Contact.Email,
		o.Currency, o.Subtotal, o.DiscountTotal, o.Total, o.Revision, o.CreatedAt)
	if err != nil {
		return err
	}
	for i, l := range o.Items {
		mods := l.ModifierIDs
		if mods == nil {
			mods = []string{}
		}
		if _, err := db.Exec(ctx, `INSERT INTO order_items (order_id, position, menu_item_id, name, quantity, modifier_ids, unit_price, line_total)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, o.ID, i, l.MenuItemID, l.Name, l.Quantity, mods, l.UnitPrice, l.LineTotal); err != nil {
			return err
		}
	}
	return s.appendHistory(ctx, db, o.ID, "", o.Status, "", actor, o.CreatedAt)
}

func (s *Store) appendHistory(ctx context.Context, db DBTX, orderID string, from, to domain.Status, reason, actor string, at time.Time) error {
	_, err := db.Exec(ctx, `INSERT INTO order_status_history (order_id, seq, from_status, to_status, reason, actor, at)
		VALUES ($1, COALESCE((SELECT max(seq) FROM order_status_history WHERE order_id = $1), 0) + 1, $2, $3, $4, $5, $6)`,
		orderID, string(from), string(to), reason, actor, at)
	return err
}

// GetOrder читает заказ с позициями. lock=true — FOR UPDATE (только внутри tx).
func (s *Store) GetOrder(ctx context.Context, db DBTX, id string, lock bool) (domain.Order, error) {
	q := `SELECT ` + orderCols + ` FROM orders WHERE id = $1`
	if lock {
		q += ` FOR UPDATE`
	}
	o, err := scanOrder(db.QueryRow(ctx, q, id))
	if err != nil {
		return o, err
	}
	o.Items, err = s.items(ctx, db, "order_items", "order_id", id)
	return o, err
}

// SetStatus меняет статус и пишет историю. Допустимость перехода проверяет вызывающий
// (domain.CanTransition) под блокировкой GetOrder(lock=true).
func (s *Store) SetStatus(ctx context.Context, db DBTX, o domain.Order, to domain.Status, reason, actor string, at time.Time) error {
	if _, err := db.Exec(ctx, `UPDATE orders SET status = $2, status_reason = $3, updated_at = $4 WHERE id = $1`,
		o.ID, string(to), reason, at); err != nil {
		return err
	}
	return s.appendHistory(ctx, db, o.ID, o.Status, to, reason, actor, at)
}

// SetPickupSlot сохраняет интервал и повышает revision состояния; возвращает новую ревизию.
func (s *Store) SetPickupSlot(ctx context.Context, db DBTX, id string, from, to time.Time, at time.Time) (int, error) {
	var rev int
	err := db.QueryRow(ctx, `UPDATE orders SET pickup_from = $2, pickup_to = $3, revision = revision + 1, updated_at = $4
		WHERE id = $1 RETURNING revision`, id, from, to, at).Scan(&rev)
	return rev, err
}

// StatusHistory — журнал переходов заказа (для тестов и разбора инцидентов).
type HistoryEntry struct {
	Seq      int
	From, To domain.Status
	Reason   string
	Actor    string
}

// History возвращает журнал статусов по порядку.
func (s *Store) History(ctx context.Context, db DBTX, orderID string) ([]HistoryEntry, error) {
	rows, err := db.Query(ctx, `SELECT seq, from_status, to_status, reason, actor FROM order_status_history WHERE order_id = $1 ORDER BY seq`, orderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HistoryEntry
	for rows.Next() {
		var h HistoryEntry
		var f, t string
		if err := rows.Scan(&h.Seq, &f, &t, &h.Reason, &h.Actor); err != nil {
			return nil, err
		}
		h.From, h.To = domain.Status(f), domain.Status(t)
		out = append(out, h)
	}
	return out, rows.Err()
}

// UnpaidBefore возвращает id заказов в pending_payment, созданных раньше before (старые первыми).
func (s *Store) UnpaidBefore(ctx context.Context, db DBTX, before time.Time, limit int) ([]string, error) {
	rows, err := db.Query(ctx, `SELECT id FROM orders WHERE status = 'pending_payment' AND created_at < $1 ORDER BY created_at LIMIT $2`, before, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
