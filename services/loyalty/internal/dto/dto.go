// Package dto — JSON-представление акции (loyalty.yaml: Promotion). Общий для HTTP-ответов
// и снимков истории, чтобы в журнале лежало ровно то, что видел клиент API.
package dto

import (
	"time"

	"github.com/pavelgr1408/food-platform/services/loyalty/internal/domain"
)

// Discount — loyalty.yaml PromotionDiscountValue.
type Discount struct {
	Type  string `json:"type"`
	Value int64  `json:"value"`
}

// Conditions — loyalty.yaml PromotionConditions.
type Conditions struct {
	MenuItemIDs []string `json:"menuItemIds,omitempty"`
	MinSubtotal *int64   `json:"minSubtotal,omitempty"`
	Fulfillment string   `json:"fulfillment,omitempty"`
	Stackable   bool     `json:"stackable"`
}

// Promotion — loyalty.yaml Promotion.
type Promotion struct {
	ID           string     `json:"id"`
	Title        string     `json:"title"`
	Description  string     `json:"description,omitempty"`
	Scope        string     `json:"scope"`
	RestaurantID string     `json:"restaurantId,omitempty"`
	Status       string     `json:"status"`
	Priority     int        `json:"priority"`
	StartsAt     time.Time  `json:"startsAt"`
	EndsAt       time.Time  `json:"endsAt"`
	Currency     string     `json:"currency"`
	Discount     Discount   `json:"discount"`
	Conditions   Conditions `json:"conditions"`
	Version      int        `json:"version"`
	CreatedBy    string     `json:"createdBy,omitempty"`
	CreatedAt    time.Time  `json:"createdAt"`
	UpdatedAt    time.Time  `json:"updatedAt"`
}

// Public — loyalty.yaml PromotionPublic (то, что видит клиент).
type Public struct {
	ID           string     `json:"id"`
	Title        string     `json:"title"`
	Description  string     `json:"description,omitempty"`
	Scope        string     `json:"scope"`
	RestaurantID string     `json:"restaurantId,omitempty"`
	Priority     int        `json:"priority"`
	StartsAt     time.Time  `json:"startsAt"`
	EndsAt       time.Time  `json:"endsAt"`
	Currency     string     `json:"currency"`
	Discount     Discount   `json:"discount"`
	Conditions   Conditions `json:"conditions"`
}

func conditions(c domain.Conditions) Conditions {
	out := Conditions{MenuItemIDs: c.MenuItemIDs, Fulfillment: c.Fulfillment, Stackable: c.Stackable}
	if c.MinSubtotal > 0 {
		v := c.MinSubtotal
		out.MinSubtotal = &v
	}
	return out
}

// From преобразует акцию в полное представление.
func From(p domain.Promotion) Promotion {
	return Promotion{ID: p.ID, Title: p.Title, Description: p.Description, Scope: string(p.Scope), RestaurantID: p.RestaurantID,
		Status: string(p.Status), Priority: p.Priority, StartsAt: p.StartsAt, EndsAt: p.EndsAt, Currency: p.Currency,
		Discount: Discount{Type: string(p.Discount.Type), Value: p.Discount.Value}, Conditions: conditions(p.Conditions),
		Version: p.Version, CreatedBy: p.CreatedBy, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt}
}

// ToPublic — клиентское представление.
func ToPublic(p domain.Promotion) Public {
	return Public{ID: p.ID, Title: p.Title, Description: p.Description, Scope: string(p.Scope), RestaurantID: p.RestaurantID,
		Priority: p.Priority, StartsAt: p.StartsAt, EndsAt: p.EndsAt, Currency: p.Currency,
		Discount: Discount{Type: string(p.Discount.Type), Value: p.Discount.Value}, Conditions: conditions(p.Conditions)}
}
