package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/pavelgr1408/food-platform/services/orders/internal/domain"
)

// UpsertPromotion сохраняет снимок акции, если его версия больше сохранённой.
// applied=false — снимок устарел или повторён (событие доставлено повторно или не по порядку).
func (s *Store) UpsertPromotion(ctx context.Context, db DBTX, p domain.Promotion, receivedAt time.Time) (applied bool, err error) {
	ids := p.MenuItemIDs
	if ids == nil {
		ids = []string{}
	}
	tag, err := db.Exec(ctx, `INSERT INTO promotions_replica (promotion_id, version, scope, restaurant_id, title, currency, discount_type,
		discount_value, priority, valid_from, valid_to, status, menu_item_ids, min_subtotal, fulfillment, stackable, received_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)
		ON CONFLICT (promotion_id) DO UPDATE SET version = EXCLUDED.version, scope = EXCLUDED.scope, restaurant_id = EXCLUDED.restaurant_id,
			title = EXCLUDED.title, currency = EXCLUDED.currency, discount_type = EXCLUDED.discount_type, discount_value = EXCLUDED.discount_value,
			priority = EXCLUDED.priority, valid_from = EXCLUDED.valid_from, valid_to = EXCLUDED.valid_to, status = EXCLUDED.status,
			menu_item_ids = EXCLUDED.menu_item_ids, min_subtotal = EXCLUDED.min_subtotal, fulfillment = EXCLUDED.fulfillment,
			stackable = EXCLUDED.stackable, received_at = EXCLUDED.received_at
		WHERE promotions_replica.version < EXCLUDED.version`,
		p.ID, p.Version, p.Scope, nilIfEmpty(p.RestaurantID), p.Title, p.Currency, p.DiscountType, p.DiscountVal, p.Priority,
		p.ValidFrom, p.ValidTo, p.Status, ids, p.MinSubtotal, p.Fulfillment, p.Stackable, receivedAt)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// ActivePromotions — акции со статусом active, срок которых не вышел на момент now (остальные расчёту не нужны).
func (s *Store) ActivePromotions(ctx context.Context, db DBTX, now time.Time) ([]domain.Promotion, error) {
	rows, err := db.Query(ctx, `SELECT promotion_id::text, version, scope, coalesce(restaurant_id::text, ''), title, currency, discount_type,
		discount_value, priority, valid_from, valid_to, status, menu_item_ids::text[], min_subtotal, fulfillment, stackable
		FROM promotions_replica WHERE status = 'active' AND (valid_to IS NULL OR valid_to > $1)`, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Promotion
	for rows.Next() {
		var p domain.Promotion
		if err := rows.Scan(&p.ID, &p.Version, &p.Scope, &p.RestaurantID, &p.Title, &p.Currency, &p.DiscountType, &p.DiscountVal,
			&p.Priority, &p.ValidFrom, &p.ValidTo, &p.Status, &p.MenuItemIDs, &p.MinSubtotal, &p.Fulfillment, &p.Stackable); err != nil {
			return nil, err
		}
		p.ValidFrom = p.ValidFrom.UTC()
		if p.ValidTo != nil {
			t := p.ValidTo.UTC()
			p.ValidTo = &t
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

type appliedJSON struct {
	PromotionID string `json:"promotionId"`
	Title       string `json:"title"`
	Discount    int64  `json:"discount"`
}

func appliedBytes(a []domain.AppliedPromotion) ([]byte, error) {
	out := make([]appliedJSON, 0, len(a))
	for _, p := range a {
		out = append(out, appliedJSON{p.PromotionID, p.Title, p.Discount})
	}
	return json.Marshal(out)
}

func parseApplied(b []byte) ([]domain.AppliedPromotion, error) {
	var in []appliedJSON
	if err := json.Unmarshal(b, &in); err != nil {
		return nil, err
	}
	out := make([]domain.AppliedPromotion, 0, len(in))
	for _, p := range in {
		out = append(out, domain.AppliedPromotion{PromotionID: p.PromotionID, Title: p.Title, Discount: p.Discount})
	}
	return out, nil
}
