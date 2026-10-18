// Package domain — модель заказа: статусы и переходы, корзина, нумерация, скидки.
// Пакет не зависит от HTTP, БД и Kafka. Деньги — int64 копейки (ADR-13).
package domain

// Status — статус заказа (совпадает с orders.status в AsyncAPI + стартовый pending_payment).
type Status string

// Статусы заказа.
const (
	StatusPendingPayment Status = "pending_payment"
	StatusAccepted       Status = "accepted"
	StatusCooking        Status = "cooking"
	StatusReady          Status = "ready"
	StatusOnDelivery     Status = "on_delivery"
	StatusDelivered      Status = "delivered"
	StatusHandedOver     Status = "handed_over"
	StatusClosed         Status = "closed"
	StatusNotReceived    Status = "not_received"
	StatusCancelled      Status = "cancelled"
)

// transitions — таблица разрешённых переходов (MG3: всё остальное запрещено).
var transitions = map[Status][]Status{
	StatusPendingPayment: {StatusAccepted, StatusCancelled},
	StatusAccepted:       {StatusCooking, StatusCancelled},
	StatusCooking:        {StatusReady},
	StatusReady:          {StatusHandedOver, StatusOnDelivery, StatusNotReceived},
	StatusOnDelivery:     {StatusDelivered, StatusNotReceived},
	StatusDelivered:      {StatusClosed},
	StatusHandedOver:     {StatusClosed},
	StatusNotReceived:    {StatusClosed},
}

// Valid — известный статус.
func (s Status) Valid() bool {
	if _, ok := transitions[s]; ok {
		return true
	}
	return s == StatusClosed || s == StatusCancelled
}

// CanTransition — разрешён ли переход from → to.
func CanTransition(from, to Status) bool {
	for _, t := range transitions[from] {
		if t == to {
			return true
		}
	}
	return false
}

// Terminal — из статуса нет переходов.
func (s Status) Terminal() bool { return len(transitions[s]) == 0 }

// Next возвращает копию списка допустимых следующих статусов.
func (s Status) Next() []Status { return append([]Status(nil), transitions[s]...) }
