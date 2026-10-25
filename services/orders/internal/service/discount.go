package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pavelgr1408/food-platform/pkg/kafka"
	"github.com/pavelgr1408/food-platform/services/orders/internal/domain"
	"github.com/pavelgr1408/food-platform/services/orders/internal/store"
)

// Топик реплики акций (contracts/asyncapi/kafka-events.yaml, PromotionDiscount).
const TopicPromotions = "promotions.discount"

// ReplicaDiscounter считает скидку по реплике акций в БД orders (S8). Реплика читается целиком
// (действующих акций немного) и кэшируется в памяти на TTL: корзина пересчитывается при каждом чтении,
// а запрос к БД на каждое чтение не нужен. Реплика недоступна — используется последний снимок
// (stale-if-error), а если его не было вовсе — скидки нет: оформление заказа не блокируется
// (E1 UC-PRM-01); клиента от неожиданной суммы защищает expectedTotal при createOrder.
type ReplicaDiscounter struct {
	pool *pgxpool.Pool
	st   *store.Store
	ttl  time.Duration
	now  func() time.Time
	log  *slog.Logger
	m    *Metrics

	mu       sync.Mutex
	promos   []domain.Promotion
	loadedAt time.Time
	loaded   bool
}

// NewReplicaDiscounter создаёт расчёт скидок; ttl = 0 — читать БД при каждом расчёте.
func NewReplicaDiscounter(pool *pgxpool.Pool, st *store.Store, ttl time.Duration, now func() time.Time, log *slog.Logger, m *Metrics) *ReplicaDiscounter {
	return &ReplicaDiscounter{pool: pool, st: st, ttl: ttl, now: now, log: log, m: m}
}

func (d *ReplicaDiscounter) snapshot(ctx context.Context, now time.Time) []domain.Promotion {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.loaded && d.ttl > 0 && now.Sub(d.loadedAt) < d.ttl && !now.Before(d.loadedAt) {
		return d.promos
	}
	ps, err := d.st.ActivePromotions(ctx, d.pool, now)
	if err != nil {
		if d.m != nil {
			d.m.PromotionLoads.WithLabelValues("error").Inc()
		}
		d.log.WarnContext(ctx, "promotions replica is unavailable, using the last snapshot", "error", err, "have_snapshot", d.loaded)
		return d.promos // nil, если снимка ещё не было
	}
	if d.m != nil {
		d.m.PromotionLoads.WithLabelValues("ok").Inc()
	}
	d.promos, d.loadedAt, d.loaded = ps, now, true
	return ps
}

// Apply считает скидку по текущему снимку реплики.
func (d *ReplicaDiscounter) Apply(ctx context.Context, in domain.DiscountInput) domain.Discount {
	if in.Now.IsZero() {
		in.Now = d.now()
	}
	return domain.Calculate(d.snapshot(ctx, in.Now), in)
}

// Invalidate сбрасывает кэш (после применения события на этом экземпляре).
func (d *ReplicaDiscounter) Invalidate() {
	d.mu.Lock()
	d.loaded = false
	d.mu.Unlock()
}

// promotionPayload — promotions.discount (AsyncAPI PromotionDiscount).
type promotionPayload struct {
	PromotionID  string `json:"promotionId"`
	Version      int    `json:"version"`
	Scope        string `json:"scope"`
	RestaurantID string `json:"restaurantId"`
	Title        string `json:"title"`
	Currency     string `json:"currency"`
	Conditions   struct {
		MenuItemIDs []string `json:"menuItemIds"`
		MinSubtotal int64    `json:"minSubtotal"`
		Fulfillment string   `json:"fulfillment"`
		Stackable   bool     `json:"stackable"`
	} `json:"conditions"`
	Discount struct {
		Type  string `json:"type"`
		Value *int64 `json:"value"`
	} `json:"discount"`
	Priority  int        `json:"priority"`
	ValidFrom time.Time  `json:"validFrom"`
	ValidTo   *time.Time `json:"validTo"`
	Status    string     `json:"status"`
}

// HandlePromotion — kafka.TxHandler консьюмера promotions.discount: обновляет реплику, если версия снимка
// больше сохранённой. Невалидный снимок — ошибка (ретраи → DLQ), а не молчаливый пропуск: иначе акция
// «тихо» не начнёт действовать. Новая consumer-группа читает топик с начала, поэтому реплика
// восстанавливается из компактного топика.
func (s *Service) HandlePromotion(ctx context.Context, tx pgx.Tx, ev kafka.Event) error {
	var p promotionPayload
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		return fmt.Errorf("promotions.discount: %w", err)
	}
	if err := validatePromotion(p); err != nil {
		return fmt.Errorf("promotions.discount: invalid payload (promotionId=%q): %w", p.PromotionID, err)
	}
	ids := make([]string, 0, len(p.Conditions.MenuItemIDs))
	for _, id := range p.Conditions.MenuItemIDs {
		ids = append(ids, strings.ToLower(id))
	}
	cur := p.Currency
	if cur == "" {
		cur = "RUB"
	}
	pr := domain.Promotion{ID: strings.ToLower(p.PromotionID), Version: p.Version, Scope: p.Scope, RestaurantID: strings.ToLower(p.RestaurantID),
		Title: p.Title, Currency: strings.ToUpper(cur), DiscountType: p.Discount.Type, DiscountVal: *p.Discount.Value, Priority: p.Priority,
		ValidFrom: p.ValidFrom.UTC(), Status: p.Status, MenuItemIDs: ids, MinSubtotal: p.Conditions.MinSubtotal,
		Fulfillment: p.Conditions.Fulfillment, Stackable: p.Conditions.Stackable}
	if p.ValidTo != nil {
		t := p.ValidTo.UTC()
		pr.ValidTo = &t
	}
	applied, err := s.st.UpsertPromotion(ctx, tx, pr, s.now())
	if err != nil {
		return err
	}
	result := "applied"
	switch {
	case !applied:
		result = "stale"
	case pr.DiscountType == "gift":
		result = "applied_ignored" // сохранена, но расчёт подарки не поддерживает
	}
	if s.m != nil {
		s.m.PromotionEvents.WithLabelValues(result).Inc()
	}
	if applied {
		if rd, ok := s.disc.(*ReplicaDiscounter); ok {
			rd.Invalidate()
		}
	}
	return nil
}

func validatePromotion(p promotionPayload) error {
	if _, err := uuid.Parse(p.PromotionID); err != nil {
		return fmt.Errorf("promotionId must be a UUID")
	}
	if p.Version < 1 {
		return fmt.Errorf("version must be >= 1")
	}
	switch p.Scope {
	case "national":
		if p.RestaurantID != "" {
			return fmt.Errorf("restaurantId must be empty for a national promotion")
		}
	case "local":
		if _, err := uuid.Parse(p.RestaurantID); err != nil {
			return fmt.Errorf("restaurantId is required for a local promotion")
		}
	default:
		return fmt.Errorf("unknown scope %q", p.Scope)
	}
	switch p.Status {
	case domain.PromotionActive, domain.PromotionCompleted:
	default:
		return fmt.Errorf("unknown status %q", p.Status)
	}
	switch p.Discount.Type {
	case domain.DiscountPercent, domain.DiscountFixed, "gift":
	default:
		return fmt.Errorf("unknown discount.type %q", p.Discount.Type)
	}
	if p.Discount.Value == nil || *p.Discount.Value < 0 {
		return fmt.Errorf("discount.value must be a non-negative integer")
	}
	if p.Discount.Type == domain.DiscountPercent && *p.Discount.Value > 10000 {
		return fmt.Errorf("percent discount is over 100%%")
	}
	if p.ValidFrom.IsZero() {
		return fmt.Errorf("validFrom is required")
	}
	if p.ValidTo != nil && !p.ValidTo.After(p.ValidFrom) {
		return fmt.Errorf("validTo must be later than validFrom")
	}
	if p.Conditions.MinSubtotal < 0 {
		return fmt.Errorf("conditions.minSubtotal must be non-negative")
	}
	switch p.Conditions.Fulfillment {
	case "", "pickup", "delivery":
	default:
		return fmt.Errorf("unknown conditions.fulfillment %q", p.Conditions.Fulfillment)
	}
	for _, id := range p.Conditions.MenuItemIDs {
		if _, err := uuid.Parse(id); err != nil {
			return fmt.Errorf("conditions.menuItemIds must contain UUIDs")
		}
	}
	return nil
}
