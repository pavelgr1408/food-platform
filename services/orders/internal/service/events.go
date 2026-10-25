package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/pavelgr1408/food-platform/pkg/kafka"
	"github.com/pavelgr1408/food-platform/services/orders/internal/domain"
)

// Топики и типы событий (contracts/asyncapi/kafka-events.yaml).
const (
	TopicState    = "orders.state"
	TopicStatus   = "orders.status"
	TypeState     = "orders.state.v1"
	TypeStatus    = "orders.status.v1"
	actorSystem   = "system"
	actorCustomer = "customer"
)

type moneyEvent struct {
	Amount   int64  `json:"amount"`
	Currency string `json:"currency"`
}

type geoEvent struct {
	Lat float64 `json:"lat"`
	Lon float64 `json:"lon"`
}

type stateItem struct {
	DishID    string     `json:"dishId"`
	Name      string     `json:"name"`
	Quantity  int        `json:"quantity"`
	Modifiers []string   `json:"modifiers"`
	Price     moneyEvent `json:"price"`
}

type stateCustomer struct {
	UserID string `json:"userId,omitempty"`
	Guest  bool   `json:"guest"`
	Phone  string `json:"phone,omitempty"`
	Name   string `json:"name,omitempty"`
}

// OrderStateEvent — payload orders.state (AsyncAPI OrderState).
type OrderStateEvent struct {
	OrderID                 string        `json:"orderId"`
	Revision                int           `json:"revision"`
	OrderNumber             string        `json:"orderNumber"`
	RestaurantID            string        `json:"restaurantId"`
	Source                  string        `json:"source"`
	FulfillmentType         string        `json:"fulfillmentType"`
	DeliveryAddress         string        `json:"deliveryAddress,omitempty"`
	DeliveryLocation        *geoEvent     `json:"deliveryLocation,omitempty"`
	Customer                stateCustomer `json:"customer"`
	Items                   []stateItem   `json:"items"`
	TotalAmount             moneyEvent    `json:"totalAmount"`
	PaymentMethod           string        `json:"paymentMethod"`
	PaymentStatus           string        `json:"paymentStatus"`
	AppliedPromotions       []string      `json:"appliedPromotions"`
	RequestedPickupInterval string        `json:"requestedPickupInterval,omitempty"`
	CreatedAt               time.Time     `json:"createdAt"`
}

// OrderStatusEvent — payload orders.status (AsyncAPI OrderStatus).
type OrderStatusEvent struct {
	OrderID     string     `json:"orderId"`
	OrderNumber string     `json:"orderNumber"`
	Status      string     `json:"status"`
	Reason      string     `json:"reason,omitempty"`
	EtaReadyAt  *time.Time `json:"etaReadyAt,omitempty"` // для accepted/cooking (S4)
	UpdatedAt   time.Time  `json:"updatedAt"`
}

// StateEvent собирает payload orders.state по заказу (revision — из заказа).
func StateEvent(o domain.Order) OrderStateEvent {
	ev := OrderStateEvent{
		OrderID: o.ID, Revision: o.Revision, OrderNumber: o.Number, RestaurantID: o.RestaurantID,
		Source: "online", FulfillmentType: string(o.Fulfillment), DeliveryAddress: o.Address.Raw,
		Customer:    stateCustomer{UserID: o.OwnerUserID, Guest: o.OwnerUserID == "", Phone: o.Contact.Phone, Name: o.Contact.Name},
		TotalAmount: moneyEvent{o.Total, o.Currency}, PaymentMethod: o.PaymentMethod.EventForm(),
		PaymentStatus: "pending", AppliedPromotions: appliedIDs(o.Applied), CreatedAt: o.CreatedAt,
	}
	if o.Fulfillment == domain.FulfillmentDelivery && o.Address.Lat != nil && o.Address.Lon != nil {
		ev.DeliveryLocation = &geoEvent{Lat: *o.Address.Lat, Lon: *o.Address.Lon}
	}
	if o.Channel == "staff_fax" {
		ev.Source = "fax"
	}
	if o.PaymentStatus == domain.PaymentPaid {
		ev.PaymentStatus = "paid"
	}
	if o.PickupFrom != nil && o.PickupTo != nil {
		ev.RequestedPickupInterval = o.PickupFrom.UTC().Format(time.RFC3339) + "/" + o.PickupTo.UTC().Format(time.RFC3339)
	}
	ev.Items = make([]stateItem, 0, len(o.Items))
	for _, l := range o.Items {
		mods := l.ModifierIDs
		if mods == nil {
			mods = []string{}
		}
		ev.Items = append(ev.Items, stateItem{DishID: l.MenuItemID, Name: l.Name, Quantity: l.Quantity,
			Modifiers: mods, Price: moneyEvent{l.UnitPrice, o.Currency}})
	}
	return ev
}

// enqueueState кладёт orders.state в outbox (в той же транзакции, что и изменение заказа).
func enqueueState(ctx context.Context, tx kafka.Execer, o domain.Order) error {
	ev, err := kafka.NewEvent(ctx, TopicState, TypeState, o.ID, StateEvent(o))
	if err != nil {
		return err
	}
	return kafka.Enqueue(ctx, tx, ev)
}

// enqueueStatus кладёт orders.status в outbox.
func enqueueStatus(ctx context.Context, tx kafka.Execer, o domain.Order, status domain.Status, reason string, eta *time.Time, at time.Time) error {
	ev, err := kafka.NewEvent(ctx, TopicStatus, TypeStatus, o.ID, OrderStatusEvent{
		OrderID: o.ID, OrderNumber: o.Number, Status: string(status), Reason: reason, EtaReadyAt: eta, UpdatedAt: at.UTC()})
	if err != nil {
		return err
	}
	return kafka.Enqueue(ctx, tx, ev)
}

func (s *Service) wake() {
	if s.waker != nil {
		s.waker.Wake()
	}
}

func shortList(items []Unavailable) string {
	ids := make([]string, 0, len(items))
	for _, u := range items {
		ids = append(ids, fmt.Sprintf("%s(%s)", u.MenuItemID, u.ReasonCode))
	}
	return strings.Join(ids, ", ")
}

func appliedIDs(a []domain.AppliedPromotion) []string {
	out := make([]string, 0, len(a))
	for _, p := range a {
		out = append(out, p.PromotionID)
	}
	return out
}
