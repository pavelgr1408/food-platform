package httpapi

import (
	"time"

	"github.com/pavelgr1408/food-platform/services/orders/internal/domain"
	"github.com/pavelgr1408/food-platform/services/orders/internal/service"
)

// money — Money из _common.yaml: целые копейки (ADR-13).
type money struct {
	Amount   int64  `json:"amount"`
	Currency string `json:"currency"`
}

type geoDTO struct {
	Lat *float64 `json:"lat"`
	Lon *float64 `json:"lon"`
}

type addressDTO struct {
	Raw      string  `json:"raw,omitempty"`
	Location *geoDTO `json:"location,omitempty"`
}

type contactDTO struct {
	Name  string `json:"name,omitempty"`
	Phone string `json:"phone,omitempty"`
	Email string `json:"email,omitempty"`
}

func (c *contactDTO) domain() domain.Contact {
	if c == nil {
		return domain.Contact{}
	}
	return domain.Contact{Name: c.Name, Phone: c.Phone, Email: c.Email}
}

type cartItemInput struct {
	MenuItemID  string   `json:"menuItemId"`
	Quantity    int      `json:"quantity"`
	ModifierIDs []string `json:"modifierIds"`
}

type cartCreateRequest struct {
	RestaurantID string      `json:"restaurantId"`
	Guest        bool        `json:"guest"`
	Contact      *contactDTO `json:"contact"`
}

type cartItemsUpdate struct {
	Items []cartItemInput `json:"items"`
}

type fulfillmentRequest struct {
	Type    string      `json:"type"`
	Address *addressDTO `json:"address"`
}

type orderCreateRequest struct {
	CartID        string      `json:"cartId"`
	PaymentMethod string      `json:"paymentMethod"`
	Contact       *contactDTO `json:"contact"`
	Channel       string      `json:"channel"`
	ExpectedTotal *moneyIn    `json:"expectedTotal"`
}

// moneyIn — Money из запроса (expectedTotal).
type moneyIn struct {
	Amount   *int64 `json:"amount"`
	Currency string `json:"currency"`
}

type pickupSlotRequest struct {
	From *time.Time `json:"from"`
	To   *time.Time `json:"to"`
}

type itemDTO struct {
	MenuItemID  string   `json:"menuItemId"`
	Quantity    int      `json:"quantity"`
	ModifierIDs []string `json:"modifierIds"`
	Name        string   `json:"name"`
	UnitPrice   money    `json:"unitPrice"`
	LineTotal   money    `json:"lineTotal"`
	Available   bool     `json:"available"`
}

type fulfillmentDTO struct {
	Type    string      `json:"type"`
	Address *addressDTO `json:"address,omitempty"`
}

type appliedDTO struct {
	PromotionID string `json:"promotionId"`
	Title       string `json:"title"`
	Discount    money  `json:"discount"`
}

type cartDTO struct {
	ID                string          `json:"id"`
	RestaurantID      string          `json:"restaurantId"`
	Status            string          `json:"status"`
	OrderID           string          `json:"orderId,omitempty"`
	Items             []itemDTO       `json:"items"`
	AppliedPromotions []appliedDTO    `json:"appliedPromotions"`
	Fulfillment       *fulfillmentDTO `json:"fulfillment,omitempty"`
	Subtotal          money           `json:"subtotal"`
	DiscountTotal     money           `json:"discountTotal"`
	Total             money           `json:"total"`
}

type slotDTO struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
}

type orderDTO struct {
	ID            string         `json:"id"`
	Number        string         `json:"number"`
	RestaurantID  string         `json:"restaurantId"`
	Status        string         `json:"status"`
	StatusReason  string         `json:"statusReason,omitempty"`
	Channel       string         `json:"channel"`
	PaymentStatus string         `json:"paymentStatus"`
	RefundStatus  string         `json:"refundStatus,omitempty"`
	PaymentMethod string         `json:"paymentMethod"`
	Items         []itemDTO      `json:"items"`
	Fulfillment   fulfillmentDTO `json:"fulfillment"`
	PickupSlot    *slotDTO       `json:"pickupSlot,omitempty"`
	Subtotal      money          `json:"subtotal"`
	DiscountTotal money          `json:"discountTotal"`
	Applied       []appliedDTO   `json:"appliedPromotions"`
	Total         money          `json:"total"`
	Delivery      *deliveryDTO   `json:"delivery,omitempty"`
	CreatedAt     time.Time      `json:"createdAt"`
}

// deliveryDTO — OrderDelivery из orders.yaml: снимок доставки.
type deliveryDTO struct {
	Status     string     `json:"status"`
	FailReason string     `json:"failReason,omitempty"`
	Hint       string     `json:"hint,omitempty"`
	EtaAt      *time.Time `json:"etaAt,omitempty"`
	UpdatedAt  time.Time  `json:"updatedAt"`
}

// staffOrderDTO — ответ searchOrder: заказ с контактами клиента (StaffOrder в orders.yaml).
type staffOrderDTO struct {
	orderDTO
	Contact   *contactDTO `json:"contact,omitempty"`
	CreatedBy string      `json:"createdBy,omitempty"`
}

func toStaffOrder(o domain.Order) staffOrderDTO {
	d := staffOrderDTO{orderDTO: toOrder(o), CreatedBy: o.CreatedBy}
	if c := o.Contact; c.Name != "" || c.Phone != "" || c.Email != "" {
		d.Contact = &contactDTO{Name: c.Name, Phone: c.Phone, Email: c.Email}
	}
	return d
}

type cancelRequest struct {
	Reason string `json:"reason"`
}

type etaDTO struct {
	ReadyAt         time.Time  `json:"readyAt"`
	DeliveryAt      *time.Time `json:"deliveryAt,omitempty"`
	EstimateMinutes int        `json:"estimateMinutes"`
	Approximate     bool       `json:"approximate"`
}

type unavailableDTO struct {
	MenuItemID string `json:"menuItemId"`
	ReasonCode string `json:"reasonCode"`
	Reason     string `json:"reason,omitempty"`
}

func toAddress(a domain.Address) *addressDTO {
	if a.Raw == "" && a.Lat == nil {
		return nil
	}
	d := &addressDTO{Raw: a.Raw}
	if a.Lat != nil && a.Lon != nil {
		d.Location = &geoDTO{Lat: a.Lat, Lon: a.Lon}
	}
	return d
}

func toItems(lines []domain.Line, cur string) []itemDTO {
	out := make([]itemDTO, 0, len(lines))
	for _, l := range lines {
		mods := l.ModifierIDs
		if mods == nil {
			mods = []string{}
		}
		out = append(out, itemDTO{MenuItemID: l.MenuItemID, Quantity: l.Quantity, ModifierIDs: mods, Name: l.Name,
			UnitPrice: money{l.UnitPrice, cur}, LineTotal: money{l.LineTotal, cur}, Available: true})
	}
	return out
}

func toCart(v service.CartView) cartDTO {
	c := v.Cart
	d := cartDTO{ID: c.ID, RestaurantID: c.RestaurantID, Status: c.Status, OrderID: c.OrderID,
		Items: toItems(c.Items, c.Currency), AppliedPromotions: make([]appliedDTO, 0, len(v.Applied)),
		Subtotal: money{v.Subtotal, c.Currency}, DiscountTotal: money{v.DiscountTotal, c.Currency}, Total: money{v.Total, c.Currency}}
	for _, a := range v.Applied {
		d.AppliedPromotions = append(d.AppliedPromotions, appliedDTO{a.PromotionID, a.Title, money{a.Discount, c.Currency}})
	}
	if c.Fulfillment.Valid() {
		d.Fulfillment = &fulfillmentDTO{Type: string(c.Fulfillment), Address: toAddress(c.Address)}
	}
	return d
}

func toOrder(o domain.Order) orderDTO {
	d := orderDTO{ID: o.ID, Number: o.Number, RestaurantID: o.RestaurantID, Status: string(o.Status), StatusReason: o.StatusReason,
		Channel: o.Channel, PaymentStatus: string(o.PaymentStatus), RefundStatus: string(o.RefundStatus), PaymentMethod: string(o.PaymentMethod), Items: toItems(o.Items, o.Currency),
		Fulfillment: fulfillmentDTO{Type: string(o.Fulfillment), Address: toAddress(o.Address)},
		Subtotal:    money{o.Subtotal, o.Currency}, DiscountTotal: money{o.DiscountTotal, o.Currency}, Total: money{o.Total, o.Currency},
		Applied: make([]appliedDTO, 0, len(o.Applied)), CreatedAt: o.CreatedAt}
	for _, a := range o.Applied {
		d.Applied = append(d.Applied, appliedDTO{a.PromotionID, a.Title, money{a.Discount, o.Currency}})
	}
	if o.PickupFrom != nil && o.PickupTo != nil {
		d.PickupSlot = &slotDTO{From: *o.PickupFrom, To: *o.PickupTo}
	}
	if dl := o.Delivery; dl != nil && o.Fulfillment == domain.FulfillmentDelivery {
		d.Delivery = &deliveryDTO{Status: string(dl.Status), FailReason: dl.FailReason, Hint: dl.Hint(), UpdatedAt: dl.UpdatedAt}
		if dl.Status != domain.DeliveryDelivered && dl.Status != domain.DeliveryFailed {
			d.Delivery.EtaAt = dl.EtaAt
		}
	}
	return d
}
