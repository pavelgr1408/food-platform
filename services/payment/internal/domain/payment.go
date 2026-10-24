// Package domain — модель платежа: статусы, автомат переходов, сопоставление способов оплаты.
// Не зависит от HTTP, БД и Kafka. Деньги — int64 копейки (ADR-13).
package domain

import (
	"encoding/json"
	"time"
)

// Status — статус платежа (OpenAPI PaymentStatus).
type Status string

// Статусы платежа.
const (
	StatusPending    Status = "pending"
	StatusAuthorized Status = "authorized" // зарезервирован (двухстадийная оплата), в S3 не используется
	StatusPaid       Status = "paid"
	StatusDeclined   Status = "declined"
	StatusUnknown    Status = "unknown"
)

// transitions — разрешённые переходы. paid и declined финальны.
var transitions = map[Status][]Status{
	StatusPending:    {StatusAuthorized, StatusPaid, StatusDeclined, StatusUnknown},
	StatusAuthorized: {StatusPaid, StatusDeclined, StatusUnknown},
	StatusUnknown:    {StatusPending, StatusAuthorized, StatusPaid, StatusDeclined},
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

// Final — статус не меняется.
func (s Status) Final() bool { return s == StatusPaid || s == StatusDeclined }

// Live — «живая» попытка: блокирует создание новой (см. уникальный индекс payments_one_live).
func (s Status) Live() bool {
	return s == StatusPending || s == StatusAuthorized || s == StatusUnknown
}

// Method — способ оплаты (OpenAPI PaymentMethodType).
type Method string

// Способы оплаты.
const (
	MethodOnlineCard  Method = "online_card"
	MethodCourierCard Method = "courier_card"
	MethodPOS         Method = "pos"
)

// EventForm — значение method в Kafka-событиях (AsyncAPI): online|courier|restaurant.
func (m Method) EventForm() string {
	switch m {
	case MethodCourierCard:
		return "courier"
	case MethodPOS:
		return "restaurant"
	}
	return "online"
}

// MethodFromEvent — обратное сопоставление; ok=false для неизвестного значения.
func MethodFromEvent(s string) (Method, bool) {
	switch s {
	case "online":
		return MethodOnlineCard, true
	case "courier":
		return MethodCourierCard, true
	case "restaurant":
		return MethodPOS, true
	}
	return "", false
}

// Valid — известный способ.
func (m Method) Valid() bool {
	return m == MethodOnlineCard || m == MethodCourierCard || m == MethodPOS
}

// Tender — способ оплаты на кассе (pos).
type Tender string

// Способы на кассе. Наличные тоже проводятся через терминал (Д6).
const (
	TenderCard Tender = "card"
	TenderCash Tender = "cash"
)

// Valid — известный способ на кассе.
func (t Tender) Valid() bool { return t == TenderCard || t == TenderCash }

// RefundStatus — статус возврата по оплаченному платежу (OpenAPI RefundStatus).
type RefundStatus string

// Статусы возврата. Пустая строка — возврат не запрашивался.
const (
	RefundPending  RefundStatus = "pending"
	RefundRefunded RefundStatus = "refunded"
	RefundFailed   RefundStatus = "failed"
)

// Order — реплика заказа, нужная для оплаты (из orders.state).
type Order struct {
	ID          string
	Revision    int
	Number      string
	Restaurant  string
	Fulfillment string // delivery | pickup
	Method      Method
	Amount      int64
	Currency    string
	OwnerUserID string // пусто — гость
	UpdatedAt   time.Time
}

// Payment — попытка оплаты заказа.
type Payment struct {
	ID            string
	OrderID       string
	Attempt       int
	Method        Method
	Status        Status
	Amount        int64
	Currency      string
	GatewayRef    string
	PaymentURL    string
	ReturnURL     string
	DeclineReason string
	RefundNeeded  bool
	Tender        Tender          // только pos
	FiscalData    json.RawMessage // фискальный документ кассы; пусто — нет
	PollAttempts  int
	NextPollAt    *time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
	PaidAt        *time.Time

	// Возврат (S6).
	RefundStatus      RefundStatus
	RefundRef         string
	RefundReason      string
	RefundRequestedAt *time.Time
	RefundedAt        *time.Time
	RefundAttempts    int
}

// Статусы заказа, влияющие на оплату (orders.status).
const (
	OrderPendingPayment = "pending_payment"
	OrderCancelled      = "cancelled"
)

// PosPayable — на каком статусе заказа можно инициировать оплату на кассе ("" — orders.status ещё не дошёл).
func PosPayable(orderStatus string) bool {
	switch orderStatus {
	case "", "accepted", "cooking", "ready":
		return true
	}
	return false
}

// CanBeAccessedBy: ресурс гостя (владелец пуст) доступен знающему id; ресурс пользователя — только ему.
func CanBeAccessedBy(owner, userID string) bool { return owner == "" || owner == userID }

// MethodView — способ оплаты для getPaymentMethods.
type MethodView struct {
	Type    Method
	Title   string
	Enabled bool
}

// MethodsFor — способы оплаты для способа получения (UC-PAY-01): самовывоз — online_card|pos,
// доставка — online_card|courier_card. enabled — только выбранный при оформлении, если заказ можно оплатить.
func MethodsFor(fulfillment string, chosen Method, payable bool) []MethodView {
	titles := map[Method]string{
		MethodOnlineCard:  "Картой онлайн",
		MethodCourierCard: "Картой курьеру",
		MethodPOS:         "На кассе в ресторане",
	}
	list := []Method{MethodOnlineCard, MethodPOS}
	if fulfillment == "delivery" {
		list = []Method{MethodOnlineCard, MethodCourierCard}
	}
	out := make([]MethodView, 0, len(list))
	for _, m := range list {
		out = append(out, MethodView{Type: m, Title: titles[m], Enabled: payable && m == chosen})
	}
	return out
}
