// Package domain — правила акций loyalty-service: модель, валидация, конфликты (E2).
// Чистые функции без ввода-вывода; деньги — только int64 копейки (ADR-13).
package domain

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Scope — область действия акции.
type Scope string

// Области действия.
const (
	ScopeLocal    Scope = "local"
	ScopeNational Scope = "national"
)

// Status — статус акции. «Запланирована» — это Active до StartsAt, отдельного статуса нет.
type Status string

// Статусы.
const (
	StatusDraft     Status = "draft"
	StatusActive    Status = "active"
	StatusCompleted Status = "completed"
)

// DiscountType — тип скидки (gift из AsyncAPI не поддерживается).
type DiscountType string

// Типы скидки.
const (
	DiscountPercent DiscountType = "percent"
	DiscountFixed   DiscountType = "fixed"
)

// Пределы.
const (
	MaxPercent      = 10000 // 100,00 % в сотых долях процента
	MaxPriority     = 1000
	MaxTitleLen     = 120
	MaxDescription  = 1000
	MaxItemIDs      = 100
	MaxMoney        = int64(10_000_000_000) // как CHECK цены в menu: 10^10 копеек
	DefaultCurrency = "RUB"
)

// Discount — размер скидки: percent — сотые доли процента, fixed — копейки.
type Discount struct {
	Type  DiscountType
	Value int64
}

// Conditions — условия применения.
type Conditions struct {
	MenuItemIDs []string // пусто — весь заказ
	MinSubtotal int64    // копейки
	Fulfillment string   // "", pickup, delivery
	Stackable   bool
}

// Promotion — акция.
type Promotion struct {
	ID           string
	Title        string
	Description  string
	Scope        Scope
	RestaurantID string // только для local
	Status       Status
	Priority     int
	StartsAt     time.Time
	EndsAt       time.Time
	Currency     string
	Discount     Discount
	Conditions   Conditions
	Version      int
	CreatedBy    string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// ValidationError — семантическая ошибка параметров (422 PROMOTION_INVALID).
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

func invalid(format string, a ...any) error { return &ValidationError{Msg: fmt.Sprintf(format, a...)} }

// Normalize приводит параметры к каноническому виду и проверяет их. Вызывается для входа
// createPromotion/updatePromotion; возвращает копию (срезы не разделяются с входом).
func Normalize(p Promotion) (Promotion, error) {
	p.Title = strings.TrimSpace(p.Title)
	if p.Title == "" || len([]rune(p.Title)) > MaxTitleLen {
		return p, invalid("title must be 1..%d characters", MaxTitleLen)
	}
	if len([]rune(p.Description)) > MaxDescription {
		return p, invalid("description must be at most %d characters", MaxDescription)
	}
	switch p.Scope {
	case ScopeLocal:
		if _, err := uuid.Parse(p.RestaurantID); err != nil {
			return p, invalid("restaurantId (uuid) is required for a local promotion")
		}
		p.RestaurantID = strings.ToLower(p.RestaurantID)
	case ScopeNational:
		if p.RestaurantID != "" {
			return p, invalid("restaurantId must be empty for a national promotion")
		}
	default:
		return p, invalid("scope must be local or national")
	}
	if p.Priority < 0 || p.Priority > MaxPriority {
		return p, invalid("priority must be 0..%d", MaxPriority)
	}
	p.StartsAt, p.EndsAt = p.StartsAt.UTC(), p.EndsAt.UTC()
	if p.StartsAt.IsZero() || p.EndsAt.IsZero() || !p.EndsAt.After(p.StartsAt) {
		return p, invalid("endsAt must be later than startsAt")
	}
	if p.Currency == "" {
		p.Currency = DefaultCurrency
	}
	p.Currency = strings.ToUpper(p.Currency)
	if len(p.Currency) != 3 || strings.Trim(p.Currency, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") != "" {
		return p, invalid("currency must be a 3-letter ISO 4217 code")
	}
	switch p.Discount.Type {
	case DiscountPercent:
		if p.Discount.Value < 1 || p.Discount.Value > MaxPercent {
			return p, invalid("percent discount must be 1..%d (hundredths of a percent)", MaxPercent)
		}
	case DiscountFixed:
		if p.Discount.Value < 1 || p.Discount.Value > MaxMoney {
			return p, invalid("fixed discount must be a positive amount in kopecks")
		}
	default:
		return p, invalid("discount.type must be percent or fixed")
	}
	c := p.Conditions
	if c.MinSubtotal < 0 || c.MinSubtotal > MaxMoney {
		return p, invalid("conditions.minSubtotal must be a non-negative amount in kopecks")
	}
	switch c.Fulfillment {
	case "", "pickup", "delivery":
	default:
		return p, invalid("conditions.fulfillment must be pickup or delivery")
	}
	if len(c.MenuItemIDs) > MaxItemIDs {
		return p, invalid("conditions.menuItemIds has more than %d items", MaxItemIDs)
	}
	ids := make([]string, 0, len(c.MenuItemIDs))
	for _, id := range c.MenuItemIDs {
		if _, err := uuid.Parse(id); err != nil {
			return p, invalid("conditions.menuItemIds must contain UUIDs")
		}
		id = strings.ToLower(id)
		if slices.Contains(ids, id) {
			return p, invalid("conditions.menuItemIds must be unique")
		}
		ids = append(ids, id)
	}
	slices.Sort(ids)
	p.Conditions.MenuItemIDs = ids
	return p, nil
}

// InWindow — действует ли акция в момент now по сроку (статус не проверяется).
func (p Promotion) InWindow(now time.Time) bool {
	return !now.Before(p.StartsAt) && now.Before(p.EndsAt)
}

// Expired — срок вышел.
func (p Promotion) Expired(now time.Time) bool { return !now.Before(p.EndsAt) }

// Covers — пересекаются ли охваты по ресторанам: национальная пересекается с любой,
// две локальные — только в одном ресторане.
func Covers(a, b Promotion) bool {
	if a.Scope == ScopeNational || b.Scope == ScopeNational {
		return true
	}
	return a.RestaurantID == b.RestaurantID
}

func overlapPeriod(a, b Promotion) bool {
	return a.StartsAt.Before(b.EndsAt) && b.StartsAt.Before(a.EndsAt)
}

// overlapItems: пустой список = весь заказ, то есть пересекается со всем.
func overlapItems(a, b Conditions) bool {
	if len(a.MenuItemIDs) == 0 || len(b.MenuItemIDs) == 0 {
		return true
	}
	for _, id := range a.MenuItemIDs {
		if slices.Contains(b.MenuItemIDs, id) {
			return true
		}
	}
	return false
}

// Conflicts — E2: обе акции не stackable, приоритет равен, пересекаются период, охват и позиции.
// Неоднозначность порядка применения возможна только при равном приоритете; разный — не конфликт.
func Conflicts(a, b Promotion) bool {
	if a.ID == b.ID || a.Conditions.Stackable || b.Conditions.Stackable || a.Priority != b.Priority {
		return false
	}
	return Covers(a, b) && overlapPeriod(a, b) && overlapItems(a.Conditions, b.Conditions)
}

// FindConflicts возвращает идентификаторы действующих акций, конфликтующих с кандидатом.
func FindConflicts(cand Promotion, active []Promotion) []string {
	var ids []string
	for _, o := range active {
		if o.Status == StatusActive && Conflicts(cand, o) {
			ids = append(ids, o.ID)
		}
	}
	slices.Sort(ids)
	return ids
}
