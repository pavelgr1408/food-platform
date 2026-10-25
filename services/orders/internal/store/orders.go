package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pavelgr1408/food-platform/pkg/pgxutil"
	"github.com/pavelgr1408/food-platform/services/orders/internal/domain"
)

const orderCols = `id, number, cart_id, restaurant_id, owner_user_id, channel, status, status_reason, payment_method,
	payment_status, fulfillment_type, address_raw, address_lat, address_lon, contact_name, contact_phone, contact_email,
	pickup_from, pickup_to, currency, subtotal, discount_total, total, revision, created_at, updated_at, payment_updated_at,
	accepted_at, production_updated_at, ready_at, created_by, refund_status,
	delivery_status, delivery_fail_reason, delivery_eta_at, delivery_updated_at, applied_promotions`

func scanOrder(row pgx.Row) (domain.Order, error) {
	var (
		o                domain.Order
		owner, createdBy *string
		refund           *string
		dStatus, dFail   *string
		dEta, dAt        *time.Time
		lat, lon         *float64
		applied          []byte
		st, pm           string
		ps, ft           string
	)
	err := row.Scan(&o.ID, &o.Number, &o.CartID, &o.RestaurantID, &owner, &o.Channel, &st, &o.StatusReason, &pm,
		&ps, &ft, &o.Address.Raw, &lat, &lon, &o.Contact.Name, &o.Contact.Phone, &o.Contact.Email,
		&o.PickupFrom, &o.PickupTo, &o.Currency, &o.Subtotal, &o.DiscountTotal, &o.Total, &o.Revision, &o.CreatedAt, &o.UpdatedAt, &o.PaymentUpdatedAt,
		&o.AcceptedAt, &o.ProductionUpdatedAt, &o.ReadyAt, &createdBy, &refund, &dStatus, &dFail, &dEta, &dAt, &applied)
	if errors.Is(err, pgx.ErrNoRows) {
		return o, ErrNotFound
	}
	if err != nil {
		return o, err
	}
	if o.Applied, err = parseApplied(applied); err != nil {
		return o, err
	}
	o.OwnerUserID, o.CreatedBy, o.RefundStatus = deref(owner), deref(createdBy), domain.RefundStatus(deref(refund))
	o.Status, o.PaymentMethod, o.PaymentStatus, o.Fulfillment = domain.Status(st), domain.PaymentMethod(pm), domain.PaymentStatus(ps), domain.FulfillmentType(ft)
	o.Address.Lat, o.Address.Lon = lat, lon
	if dStatus != nil && dAt != nil {
		snap := domain.DeliverySnapshot{Status: domain.DeliveryStatus(*dStatus), FailReason: deref(dFail), UpdatedAt: dAt.UTC()}
		if dEta != nil {
			t := dEta.UTC()
			snap.EtaAt = &t
		}
		o.Delivery = &snap
	}
	o.CreatedAt, o.UpdatedAt = o.CreatedAt.UTC(), o.UpdatedAt.UTC()
	for _, p := range []**time.Time{&o.PaymentUpdatedAt, &o.AcceptedAt, &o.ProductionUpdatedAt, &o.ReadyAt} {
		if *p != nil {
			t := (*p).UTC()
			*p = &t
		}
	}
	if o.PickupFrom != nil {
		f, t := o.PickupFrom.UTC(), o.PickupTo.UTC()
		o.PickupFrom, o.PickupTo = &f, &t
	}
	return o, nil
}

// RestaurantCode возвращает короткий числовой код ресторана для номеров заказов, создавая его
// при первом обращении. Вызывать ВНЕ транзакции заказа (с пулом): код фиксируется сразу и не
// «сгорает» при откате оформления (identity-последовательности не откатываются).
//
// Создание идёт в короткой транзакции под advisory-lock: иначе параллельные первые заказы
// делали бы INSERT … ON CONFLICT DO NOTHING, а такой INSERT расходует значение identity даже
// при конфликте — код ресторана получался бы случайным (R2 вместо R1). Быстрый путь (код уже
// есть) — один SELECT без блокировок.
func (s *Store) RestaurantCode(ctx context.Context, pool *pgxpool.Pool, restaurantID string) (int, error) {
	const sel = `SELECT code FROM restaurant_codes WHERE restaurant_id = $1`
	var code int
	err := pool.QueryRow(ctx, sel, restaurantID).Scan(&code)
	if !errors.Is(err, pgx.ErrNoRows) {
		return code, err
	}
	err = pgxutil.InTx(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('orders.restaurant_codes'))`); err != nil {
			return err
		}
		err := tx.QueryRow(ctx, sel, restaurantID).Scan(&code) // мог создать победитель гонки
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		return tx.QueryRow(ctx, `INSERT INTO restaurant_codes (restaurant_id) VALUES ($1) RETURNING code`, restaurantID).Scan(&code)
	})
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
	applied, err := appliedBytes(o.Applied)
	if err != nil {
		return err
	}
	_, err = db.Exec(ctx, `INSERT INTO orders (id, number, cart_id, restaurant_id, owner_user_id, channel, status, status_reason, payment_method,
		payment_status, fulfillment_type, address_raw, address_lat, address_lon, contact_name, contact_phone, contact_email,
		currency, subtotal, discount_total, total, revision, created_at, updated_at, accepted_at, created_by, applied_promotions)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$23,
			CASE WHEN $7 = 'accepted' THEN $23::timestamptz END, $24, $25)`,
		o.ID, o.Number, o.CartID, o.RestaurantID, nilIfEmpty(o.OwnerUserID), o.Channel, string(o.Status), o.StatusReason, string(o.PaymentMethod),
		string(o.PaymentStatus), string(o.Fulfillment), o.Address.Raw, o.Address.Lat, o.Address.Lon, o.Contact.Name, o.Contact.Phone, o.Contact.Email,
		o.Currency, o.Subtotal, o.DiscountTotal, o.Total, o.Revision, o.CreatedAt, nilIfEmpty(o.CreatedBy), applied)
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
	if _, err := db.Exec(ctx, `UPDATE orders SET status = $2, status_reason = $3, updated_at = $4,
			accepted_at = CASE WHEN $2 = 'accepted' THEN $4::timestamptz ELSE accepted_at END,
			ready_at = CASE WHEN $2 = 'ready' THEN $4::timestamptz ELSE ready_at END WHERE id = $1`,
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
// Заказы с paymentStatus=unknown (деньги могли уйти, итог неизвестен) отменяются только по истечении
// более длинного срока unknownBefore: решение за payment-service, а не за таймером.
func (s *Store) UnpaidBefore(ctx context.Context, db DBTX, before, unknownBefore time.Time, limit int) ([]string, error) {
	rows, err := db.Query(ctx, `SELECT id FROM orders WHERE status = 'pending_payment' AND created_at < $1
		AND (payment_status <> 'unknown' OR created_at < $2) ORDER BY created_at LIMIT $3`, before, unknownBefore, limit)
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

// SetPaymentStatus фиксирует статус оплаты, пришедший от payment-service. paid не перезаписывается другим статусом.
// Для paid revision увеличивается (orders.state публикуется повторно, ADR-10) — bumpRevision=true; новая ревизия возвращается.
func (s *Store) SetPaymentStatus(ctx context.Context, db DBTX, id string, ps domain.PaymentStatus, at time.Time, bumpRevision bool) (int, error) {
	var rev int
	err := db.QueryRow(ctx, `UPDATE orders SET payment_status = $2, payment_updated_at = $3,
			revision = revision + CASE WHEN $4 THEN 1 ELSE 0 END, updated_at = $3
		WHERE id = $1 AND payment_status <> 'paid' RETURNING revision`, id, string(ps), at, bumpRevision).Scan(&rev)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	return rev, err
}

// SetProductionUpdatedAt запоминает updatedAt применённого снимка блюд.
func (s *Store) SetProductionUpdatedAt(ctx context.Context, db DBTX, id string, at time.Time) error {
	_, err := db.Exec(ctx, `UPDATE orders SET production_updated_at = $2 WHERE id = $1`, id, at)
	return err
}

// StatusTime возвращает время первого перехода заказа в статус (по журналу); found=false — не было.
func (s *Store) StatusTime(ctx context.Context, db DBTX, orderID string, st domain.Status) (t time.Time, found bool, err error) {
	var p *time.Time
	if err = db.QueryRow(ctx, `SELECT min(at) FROM order_status_history WHERE order_id = $1 AND to_status = $2`,
		orderID, string(st)).Scan(&p); err != nil || p == nil {
		return time.Time{}, false, err
	}
	return p.UTC(), true, nil
}

// GetOrderByNumber читает заказ по человекочитаемому номеру (UNIQUE number); ErrNotFound, если нет.
func (s *Store) GetOrderByNumber(ctx context.Context, db DBTX, number string) (domain.Order, error) {
	o, err := scanOrder(db.QueryRow(ctx, `SELECT `+orderCols+` FROM orders WHERE number = $1`, number))
	if err != nil {
		return o, err
	}
	o.Items, err = s.items(ctx, db, "order_items", "order_id", o.ID)
	return o, err
}

// SetRefundStatus фиксирует ход возврата (из orders.payments.status). Допустимо: ничего→любой,
// pending→refunded|failed, failed→refunded; refunded окончателен, назад не идёт. Возвращает true, если статус изменился.
func (s *Store) SetRefundStatus(ctx context.Context, db DBTX, id string, rs domain.RefundStatus, at time.Time) (bool, error) {
	tag, err := db.Exec(ctx, `UPDATE orders SET refund_status = $2, updated_at = $3
		WHERE id = $1 AND refund_status IS DISTINCT FROM $2
		  AND (refund_status IS NULL OR refund_status = 'pending' OR (refund_status = 'failed' AND $2 = 'refunded'))`,
		id, string(rs), at)
	return tag.RowsAffected() == 1, err
}

// ReadyPickupBefore возвращает id заказов самовывоза, готовых раньше before и не выданных (старые первыми).
func (s *Store) ReadyPickupBefore(ctx context.Context, db DBTX, before time.Time, limit int) ([]string, error) {
	rows, err := db.Query(ctx, `SELECT id FROM orders WHERE status = 'ready' AND fulfillment_type = 'pickup' AND ready_at < $1
		ORDER BY ready_at LIMIT $2`, before, limit)
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

// SetDelivery сохраняет снимок доставки. Событие не новее сохранённого не применяется (false).
func (s *Store) SetDelivery(ctx context.Context, db DBTX, id string, d domain.DeliverySnapshot) (bool, error) {
	var reason *string
	if d.FailReason != "" {
		reason = &d.FailReason
	}
	tag, err := db.Exec(ctx, `UPDATE orders SET delivery_status = $2, delivery_fail_reason = $3, delivery_eta_at = $4, delivery_updated_at = $5
		WHERE id = $1 AND (delivery_updated_at IS NULL OR delivery_updated_at < $5)`,
		id, string(d.Status), reason, d.EtaAt, d.UpdatedAt)
	return tag.RowsAffected() == 1, err
}

// ReadyFallbackBefore возвращает id готовых заказов, которые клиенту предложено забрать самому (доставка
// failed/no_courier): ждём с момента, когда стали и готовы, и без курьера (старые первыми).
func (s *Store) ReadyFallbackBefore(ctx context.Context, db DBTX, before time.Time, limit int) ([]string, error) {
	rows, err := db.Query(ctx, `SELECT id FROM orders WHERE status = 'ready' AND fulfillment_type = 'delivery'
		AND delivery_status = 'failed' AND delivery_fail_reason = 'no_courier'
		AND GREATEST(ready_at, delivery_updated_at) < $1 ORDER BY ready_at LIMIT $2`, before, limit)
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
