package service

import (
	"context"
	"time"

	"github.com/pavelgr1408/food-platform/pkg/kafka"
	"github.com/pavelgr1408/food-platform/services/loyalty/internal/domain"
)

// Топик и тип события (contracts/asyncapi/kafka-events.yaml, PromotionDiscount).
const (
	TopicOut = "promotions.discount"
	TypeOut  = "promotions.discount.v1"
)

type eventConditions struct {
	MenuItemIDs []string `json:"menuItemIds"`
	MinSubtotal int64    `json:"minSubtotal"`
	Fulfillment string   `json:"fulfillment,omitempty"`
	Stackable   bool     `json:"stackable"`
}

type eventDiscount struct {
	Type  string `json:"type"`
	Value int64  `json:"value"`
}

// DiscountEvent — payload promotions.discount: снимок акции для реплики в orders.
type DiscountEvent struct {
	PromotionID  string          `json:"promotionId"`
	Version      int             `json:"version"`
	Scope        string          `json:"scope"`
	RestaurantID string          `json:"restaurantId,omitempty"`
	Title        string          `json:"title"`
	Currency     string          `json:"currency"`
	Conditions   eventConditions `json:"conditions"`
	Discount     eventDiscount   `json:"discount"`
	Priority     int             `json:"priority"`
	ValidFrom    time.Time       `json:"validFrom"`
	ValidTo      time.Time       `json:"validTo"`
	Status       string          `json:"status"`
}

func discountEvent(p domain.Promotion) DiscountEvent {
	ids := p.Conditions.MenuItemIDs
	if ids == nil {
		ids = []string{}
	}
	return DiscountEvent{PromotionID: p.ID, Version: p.Version, Scope: string(p.Scope), RestaurantID: p.RestaurantID,
		Title: p.Title, Currency: p.Currency,
		Conditions: eventConditions{MenuItemIDs: ids, MinSubtotal: p.Conditions.MinSubtotal, Fulfillment: p.Conditions.Fulfillment, Stackable: p.Conditions.Stackable},
		Discount:   eventDiscount{Type: string(p.Discount.Type), Value: p.Discount.Value},
		Priority:   p.Priority, ValidFrom: p.StartsAt.UTC(), ValidTo: p.EndsAt.UTC(), Status: string(p.Status)}
}

// enqueue кладёт снимок акции в outbox (в той же транзакции, что и изменение). Ключ — promotionId:
// топик компактный, важен последний снимок.
func enqueue(ctx context.Context, tx kafka.Execer, p domain.Promotion) error {
	ev, err := kafka.NewEvent(ctx, TopicOut, TypeOut, p.ID, discountEvent(p))
	if err != nil {
		return err
	}
	return kafka.Enqueue(ctx, tx, ev)
}
