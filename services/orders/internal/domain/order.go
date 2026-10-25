package domain

import (
	"fmt"
	"strings"
	"time"
)

// FulfillmentType — способ получения.
type FulfillmentType string

// Способы получения.
const (
	FulfillmentDelivery FulfillmentType = "delivery"
	FulfillmentPickup   FulfillmentType = "pickup"
)

// Valid — известный способ получения.
func (f FulfillmentType) Valid() bool { return f == FulfillmentDelivery || f == FulfillmentPickup }

// PaymentMethod — способ оплаты в терминах REST (PaymentMethodType).
type PaymentMethod string

// Способы оплаты.
const (
	PayOnlineCard  PaymentMethod = "online_card"
	PayCourierCard PaymentMethod = "courier_card"
	PayPOS         PaymentMethod = "pos"
)

// Allowed — допустим ли способ оплаты для способа получения:
// самовывоз — online_card|pos, доставка — online_card|courier_card.
func (m PaymentMethod) Allowed(f FulfillmentType) bool {
	switch f {
	case FulfillmentPickup:
		return m == PayOnlineCard || m == PayPOS
	case FulfillmentDelivery:
		return m == PayOnlineCard || m == PayCourierCard
	}
	return false
}

// Prepaid — оплата до производства (ADR-10): только онлайн-карта.
func (m PaymentMethod) Prepaid() bool { return m == PayOnlineCard }

// EventForm — значение в событиях (AsyncAPI: online|courier|restaurant).
func (m PaymentMethod) EventForm() string {
	switch m {
	case PayCourierCard:
		return "courier"
	case PayPOS:
		return "restaurant"
	default:
		return "online"
	}
}

// PaymentStatus — статус оплаты.
type PaymentStatus string

// Статусы оплаты (PaymentStatus в _common.yaml).
const (
	PaymentPending  PaymentStatus = "pending"
	PaymentPaid     PaymentStatus = "paid"
	PaymentDeclined PaymentStatus = "declined"
	PaymentUnknown  PaymentStatus = "unknown"
)

// RefundStatus — ход возврата денег по оплаченному, но не исполняемому заказу (из orders.payments.status).
type RefundStatus string

// Статусы возврата.
const (
	RefundPending  RefundStatus = "pending"
	RefundRefunded RefundStatus = "refunded"
	RefundFailed   RefundStatus = "failed"
)

// Valid — известный статус возврата.
func (r RefundStatus) Valid() bool {
	return r == RefundPending || r == RefundRefunded || r == RefundFailed
}

// ChannelStaffFax — заказ, принятый сотрудником по факсу/телефону (UC-ORD-02).
const ChannelStaffFax = "staff_fax"

// CancelReasons — коды причин отмены персоналом (contracts: CancelOrderRequest.reason).
var CancelReasons = []string{"customer_request", "out_of_stock", "restaurant_closed", "other"}

// InitialStatus — начальный статус заказа: онлайн-оплата ждёт платёж, остальное принимается сразу.
func InitialStatus(m PaymentMethod) Status {
	if m.Prepaid() {
		return StatusPendingPayment
	}
	return StatusAccepted
}

// Contact — контакты клиента.
type Contact struct {
	Name, Phone, Email string
}

// Merge накладывает непустые поля o поверх c.
func (c Contact) Merge(o Contact) Contact {
	if o.Name != "" {
		c.Name = o.Name
	}
	if o.Phone != "" {
		c.Phone = o.Phone
	}
	if o.Email != "" {
		c.Email = o.Email
	}
	return c
}

// Address — адрес доставки.
type Address struct {
	Raw      string
	Lat, Lon *float64
}

// Line — строка корзины/заказа со снапшотом названия и цен (копейки).
type Line struct {
	MenuItemID  string
	Name        string
	Quantity    int
	ModifierIDs []string
	UnitPrice   int64
	LineTotal   int64
}

// DeliveryStatus — статус доставки (orders.delivery.status, S7).
type DeliveryStatus string

// Статусы доставки.
const (
	DeliverySearching DeliveryStatus = "searching"
	DeliveryAssigned  DeliveryStatus = "assigned"
	DeliveryPickedUp  DeliveryStatus = "picked_up"
	DeliveryEnRoute   DeliveryStatus = "en_route"
	DeliveryDelivered DeliveryStatus = "delivered"
	DeliveryFailed    DeliveryStatus = "failed"
)

// Valid — известный статус доставки.
func (d DeliveryStatus) Valid() bool {
	switch d {
	case DeliverySearching, DeliveryAssigned, DeliveryPickedUp, DeliveryEnRoute, DeliveryDelivered, DeliveryFailed:
		return true
	}
	return false
}

// Причины неудачи доставки.
const (
	FailNoCourier   = "no_courier"
	FailUndelivered = "undelivered"
	FailCancelled   = "cancelled"
)

// DeliverySnapshot — последний известный снимок доставки заказа.
type DeliverySnapshot struct {
	Status     DeliveryStatus
	FailReason string
	EtaAt      *time.Time
	UpdatedAt  time.Time
}

// Hint — подсказка клиенту: при failed/no_courier заказ можно забрать в ресторане (E2).
func (d DeliverySnapshot) Hint() string {
	if d.Status == DeliveryFailed && d.FailReason == FailNoCourier {
		return "pickup"
	}
	return ""
}

// PickupFallback — заказ с доставкой, который клиенту предложено забрать самому (E2): курьер не найден.
func (o Order) PickupFallback() bool {
	return o.Fulfillment == FulfillmentDelivery && o.Delivery != nil && o.Delivery.Hint() == "pickup"
}

// Order — заказ.
type Order struct {
	ID, Number, CartID, RestaurantID string
	OwnerUserID                      string // пусто — гость
	Channel                          string
	Status                           Status
	StatusReason                     string
	PaymentMethod                    PaymentMethod
	PaymentStatus                    PaymentStatus
	PaymentUpdatedAt                 *time.Time   // время последнего применённого события оплаты
	AcceptedAt                       *time.Time   // момент принятия заказа (S4: от него считается ETA)
	ReadyAt                          *time.Time   // момент перехода в ready (S6: от него считается неполучение)
	CreatedBy                        string       // сотрудник, оформивший заказ по факсу (S6)
	RefundStatus                     RefundStatus // ход возврата денег (S6), пусто — возврата нет
	ProductionUpdatedAt              *time.Time   // updatedAt последнего применённого снимка блюд (S4)
	Fulfillment                      FulfillmentType
	Address                          Address
	Contact                          Contact
	PickupFrom, PickupTo             *time.Time
	Currency                         string
	Items                            []Line
	Subtotal, DiscountTotal, Total   int64
	Revision                         int
	Delivery                         *DeliverySnapshot // снимок доставки (S7), nil — событий доставки не было
	CreatedAt, UpdatedAt             time.Time
}

// Cart — серверная корзина.
type Cart struct {
	ID, RestaurantID string
	OwnerUserID      string // пусто — гость
	Status           string // open | ordered
	OrderID          string
	Revision         int64
	Currency         string
	Items            []Line
	Fulfillment      FulfillmentType // пусто — не выбран
	Address          Address
	Contact          Contact
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// Корзина в статусах.
const (
	CartOpen    = "open"
	CartOrdered = "ordered"
)

// Subtotal — сумма строк.
func Subtotal(lines []Line) int64 {
	var s int64
	for _, l := range lines {
		s += l.LineTotal
	}
	return s
}

// Subtotal корзины.
func (c Cart) Subtotal() int64 { return Subtotal(c.Items) }

// CanBeAccessedBy: ресурс гостя (owner пуст) доступен знающему id; ресурс пользователя — только ему.
func CanBeAccessedBy(owner, userID string) bool { return owner == "" || owner == userID }

// FormatNumber строит номер заказа: R<код ресторана>-<YYMMDD>-<NNNN>.
func FormatNumber(restaurantCode int, day time.Time, seq int) string {
	return fmt.Sprintf("R%d-%s-%04d", restaurantCode, day.Format("060102"), seq)
}

// SlotRules — ограничения интервала получения.
const (
	MaxSlotLength = 4 * time.Hour
	MaxSlotAhead  = 7 * 24 * time.Hour
	slotTolerance = time.Minute // допуск на рассинхрон часов клиента
)

// ValidateSlot проверяет интервал получения; пустая строка — корректен.
func ValidateSlot(from, to, now time.Time) string {
	switch {
	case !from.Before(to):
		return "from must be before to"
	case from.Before(now.Add(-slotTolerance)):
		return "from must not be in the past"
	case to.Sub(from) > MaxSlotLength:
		return "slot must not be longer than 4 hours"
	case from.After(now.Add(MaxSlotAhead)):
		return "slot must not be more than 7 days ahead"
	}
	return ""
}

// NormalizePhone: пробелы/дефисы/скобки убираются; допустим только E.164.
func NormalizePhone(s string) (string, bool) {
	r := strings.NewReplacer(" ", "", "-", "", "(", "", ")", "")
	p := r.Replace(strings.TrimSpace(s))
	if len(p) < 8 || len(p) > 16 || p[0] != '+' || p[1] == '0' {
		return "", false
	}
	for _, c := range p[1:] {
		if c < '0' || c > '9' {
			return "", false
		}
	}
	return p, true
}
