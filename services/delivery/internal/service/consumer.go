package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/pavelgr1408/food-platform/pkg/geo"
	"github.com/pavelgr1408/food-platform/pkg/kafka"
	"github.com/pavelgr1408/food-platform/services/delivery/internal/domain"
	"github.com/pavelgr1408/food-platform/services/delivery/internal/store"
)

// orderStatePayload — нужное нам подмножество orders.state (AsyncAPI OrderState).
type orderStatePayload struct {
	OrderID          string `json:"orderId"`
	Revision         int    `json:"revision"`
	OrderNumber      string `json:"orderNumber"`
	RestaurantID     string `json:"restaurantId"`
	FulfillmentType  string `json:"fulfillmentType"`
	DeliveryAddress  string `json:"deliveryAddress"`
	DeliveryLocation *struct {
		Lat float64 `json:"lat"`
		Lon float64 `json:"lon"`
	} `json:"deliveryLocation"`
	Customer struct {
		UserID string `json:"userId"`
		Phone  string `json:"phone"`
		Name   string `json:"name"`
	} `json:"customer"`
	TotalAmount struct {
		Amount   int64  `json:"amount"`
		Currency string `json:"currency"`
	} `json:"totalAmount"`
	PaymentMethod string    `json:"paymentMethod"`
	PaymentStatus string    `json:"paymentStatus"`
	CreatedAt     time.Time `json:"createdAt"`
}

type orderStatusPayload struct {
	OrderID   string    `json:"orderId"`
	Status    string    `json:"status"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type paymentLinkPayload struct {
	OrderID    string `json:"orderId"`
	PaymentID  string `json:"paymentId"`
	PaymentURL string `json:"paymentUrl"`
}

// HandleEvent — kafka.TxHandler консьюмера (внутри inbox-транзакции). Ошибка разбора ведёт к повторам и
// затем в <topic>.dlq; события, не относящиеся к доставке, пропускаются молча.
func (s *Service) HandleEvent(ctx context.Context, tx pgx.Tx, ev kafka.Event) error {
	switch ev.Topic {
	case TopicState:
		return s.handleState(ctx, tx, ev)
	case TopicStatus:
		return s.handleStatus(ctx, tx, ev)
	case TopicLink:
		return s.handleLink(ctx, tx, ev)
	}
	return fmt.Errorf("unexpected topic %q", ev.Topic)
}

func (s *Service) handleState(ctx context.Context, tx pgx.Tx, ev kafka.Event) error {
	var p orderStatePayload
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		return fmt.Errorf("orders.state: %w", err)
	}
	if _, err := uuid.Parse(p.OrderID); err != nil || p.Revision < 1 || p.TotalAmount.Amount < 0 || p.TotalAmount.Currency == "" {
		return fmt.Errorf("orders.state: invalid payload (orderId=%q revision=%d)", p.OrderID, p.Revision)
	}
	if p.FulfillmentType != "delivery" {
		s.m.StateEvents.WithLabelValues("skipped").Inc()
		return nil
	}
	now := s.now()
	existing, err := s.st.GetByOrder(ctx, tx, p.OrderID, true)
	switch {
	case err == nil:
		// доставка уже есть: фиксируем более новую revision (сумма/контакты), статус не трогаем
		upd := domain.Delivery{Amount: p.TotalAmount.Amount, Currency: p.TotalAmount.Currency,
			ContactName: p.Customer.Name, ContactPhone: p.Customer.Phone, UpdatedAt: now}
		if ok, err := s.st.BumpRevision(ctx, tx, p.OrderID, p.Revision, upd); err != nil {
			return err
		} else if ok {
			s.m.StateEvents.WithLabelValues("updated").Inc()
		} else {
			s.m.StateEvents.WithLabelValues("ignored").Inc()
		}
		_ = existing
		return nil
	case !errors.Is(err, store.ErrNotFound):
		return err
	}
	if !domain.ShouldDeliver(p.FulfillmentType, p.PaymentMethod, p.PaymentStatus) {
		s.m.StateEvents.WithLabelValues("skipped").Inc() // предоплата ещё не подтверждена (ADR-10)
		return nil
	}
	if cancelled, err := s.st.IsOrderCancelled(ctx, tx, p.OrderID); err != nil || cancelled {
		s.m.StateEvents.WithLabelValues("cancelled").Inc()
		return err
	}
	if p.DeliveryLocation == nil || !geo.Valid(p.DeliveryLocation.Lat, p.DeliveryLocation.Lon) {
		// без координат курьера не вызвать; событие не ядовитое — заказ создан старой версией orders
		s.log.ErrorContext(ctx, "delivery order without coordinates: courier is not requested", "order_id", p.OrderID)
		s.m.StateEvents.WithLabelValues("no_location").Inc()
		return nil
	}
	link, err := s.st.PaymentLink(ctx, tx, p.OrderID)
	if err != nil {
		return err
	}
	d := domain.Delivery{ID: uuid.NewString(), OrderID: p.OrderID, OrderNumber: p.OrderNumber, RestaurantID: p.RestaurantID,
		OwnerUserID: p.Customer.UserID, Revision: p.Revision, Status: domain.StatusSearching, Address: p.DeliveryAddress,
		DestLat: p.DeliveryLocation.Lat, DestLon: p.DeliveryLocation.Lon, ContactName: p.Customer.Name, ContactPhone: p.Customer.Phone,
		Amount: p.TotalAmount.Amount, Currency: p.TotalAmount.Currency, PayOnDelivery: p.PaymentMethod == "courier", PaymentURL: link,
		SearchStartedAt: now, NextPollAt: &now, CreatedAt: now, UpdatedAt: now}
	if _, err := uuid.Parse(d.RestaurantID); err != nil {
		return fmt.Errorf("orders.state: invalid restaurantId %q", d.RestaurantID)
	}
	inserted, err := s.st.Insert(ctx, tx, d)
	if err != nil || !inserted {
		return err
	}
	if err := s.st.AddHistory(ctx, tx, d.ID, d.Status, "", sourceState, now); err != nil {
		return err
	}
	if err := enqueueStatus(ctx, tx, d); err != nil {
		return err
	}
	s.m.Transitions.WithLabelValues(string(d.Status), sourceState).Inc()
	s.m.StateEvents.WithLabelValues("created").Inc()
	s.wake()
	return nil
}

func (s *Service) handleStatus(ctx context.Context, tx pgx.Tx, ev kafka.Event) error {
	var p orderStatusPayload
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		return fmt.Errorf("orders.status: %w", err)
	}
	if _, err := uuid.Parse(p.OrderID); err != nil || p.Status == "" {
		return fmt.Errorf("orders.status: invalid payload (orderId=%q status=%q)", p.OrderID, p.Status)
	}
	if p.Status != "cancelled" {
		return nil
	}
	d, err := s.st.GetByOrder(ctx, tx, p.OrderID, true)
	if errors.Is(err, store.ErrNotFound) {
		return s.st.MarkOrderCancelled(ctx, tx, p.OrderID) // orders.state ещё не дошёл: доставку не создадим
	}
	if err != nil {
		return err
	}
	if d.Status.Terminal() || d.Cancelled {
		return nil
	}
	// снятие заявки у курьерской системы — вызов вне транзакции, его сделает цикл опроса
	d.Cancelled = true
	now := s.now()
	d.NextPollAt = &now
	d.UpdatedAt = now
	if err := s.st.Update(ctx, tx, d); err != nil {
		return err
	}
	s.m.StateEvents.WithLabelValues("cancelled").Inc()
	return nil
}

func (s *Service) handleLink(ctx context.Context, tx pgx.Tx, ev kafka.Event) error {
	var p paymentLinkPayload
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		return fmt.Errorf("orders.payments.link: %w", err)
	}
	if _, err := uuid.Parse(p.OrderID); err != nil || p.PaymentURL == "" {
		return fmt.Errorf("orders.payments.link: invalid payload (orderId=%q)", p.OrderID)
	}
	updated, err := s.st.SavePaymentLink(ctx, tx, p.OrderID, p.PaymentURL)
	if err != nil {
		return err
	}
	if updated {
		// ссылка у уже созданной доставки: передать курьерской системе при ближайшем опросе
		return s.st.Expedite(ctx, tx, p.OrderID, s.now())
	}
	return nil
}
