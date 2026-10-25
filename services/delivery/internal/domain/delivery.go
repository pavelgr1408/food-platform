// Package domain — модель доставки: статусы, допустимые переходы, права доступа. Без HTTP, БД и Kafka.
package domain

import (
	"strings"
	"time"
)

// Status — статус доставки (совпадает с orders.delivery.status, AsyncAPI).
type Status string

// Статусы доставки.
const (
	StatusSearching Status = "searching"
	StatusAssigned  Status = "assigned"
	StatusPickedUp  Status = "picked_up"
	StatusEnRoute   Status = "en_route"
	StatusDelivered Status = "delivered"
	StatusFailed    Status = "failed"
)

// FailReason — причина failed.
type FailReason string

// Причины неудачи.
const (
	ReasonNone        FailReason = ""
	ReasonNoCourier   FailReason = "no_courier"
	ReasonUndelivered FailReason = "undelivered"
	ReasonCancelled   FailReason = "cancelled"
)

// Valid — известная причина.
func (r FailReason) Valid() bool {
	switch r {
	case ReasonNoCourier, ReasonUndelivered, ReasonCancelled:
		return true
	}
	return false
}

// Valid — известный статус.
func (s Status) Valid() bool {
	switch s {
	case StatusSearching, StatusAssigned, StatusPickedUp, StatusEnRoute, StatusDelivered, StatusFailed:
		return true
	}
	return false
}

func (s Status) rank() int {
	switch s {
	case StatusSearching:
		return 0
	case StatusAssigned:
		return 1
	case StatusPickedUp:
		return 2
	case StatusEnRoute:
		return 3
	case StatusDelivered, StatusFailed:
		return 4
	}
	return -1
}

// Terminal — из статуса нет переходов.
func (s Status) Terminal() bool { return s == StatusDelivered || s == StatusFailed }

// Active — курьер выехал с заказом: координаты имеют смысл.
func (s Status) Active() bool { return s == StatusPickedUp || s == StatusEnRoute }

// CourierVisible — контакты курьера показываются клиенту (минимизация PII после завершения).
func (s Status) CourierVisible() bool {
	return s == StatusAssigned || s == StatusPickedUp || s == StatusEnRoute
}

// CanAdvance — разрешён ли переход from → to. Статусы идут только вперёд (пропуск промежуточных
// допустим: уведомление «assigned» могло потеряться); failed доступен из любого нетерминального;
// терминальные статусы неизменны; повтор того же статуса — не переход.
func CanAdvance(from, to Status) bool {
	if !from.Valid() || !to.Valid() || from.Terminal() {
		return false
	}
	if to == StatusFailed {
		return true
	}
	return to.rank() > from.rank()
}

// Delivery — доставка заказа.
type Delivery struct {
	ID, OrderID, OrderNumber, RestaurantID string
	OwnerUserID                            string
	Revision                               int
	Status                                 Status
	FailReason                             FailReason
	Address                                string
	DestLat, DestLon                       float64
	ContactName, ContactPhone              string
	Amount                                 int64 // копейки
	Currency                               string
	PayOnDelivery                          bool
	PaymentURL                             string
	LinkPushed                             bool
	CourierRef                             string
	CourierID, CourierName, CourierPhone   string
	EtaAt                                  *time.Time
	PaidReported                           bool
	Cancelled                              bool
	SearchStartedAt                        time.Time
	NextPollAt                             *time.Time
	PollAttempts                           int
	CreatedAt, UpdatedAt                   time.Time
}

// ShouldDeliver — берёт ли delivery-service заказ (ADR-10): доставка и (оплачен или оплата не предоплатная).
func ShouldDeliver(fulfillment, paymentMethod, paymentStatus string) bool {
	if fulfillment != "delivery" {
		return false
	}
	return paymentStatus == "paid" || paymentMethod == "courier" || paymentMethod == "restaurant"
}

// Hint — подсказка клиенту при неудаче (E2): забрать заказ в ресторане.
func (d Delivery) Hint() string {
	if d.Status == StatusFailed && d.FailReason == ReasonNoCourier {
		return "pickup"
	}
	return ""
}

// MaskPhone скрывает середину номера для журналов.
func MaskPhone(p string) string {
	p = strings.TrimSpace(p)
	if len(p) <= 4 {
		return "***"
	}
	return p[:2] + strings.Repeat("*", len(p)-4) + p[len(p)-2:]
}
