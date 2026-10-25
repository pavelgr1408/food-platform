package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/pavelgr1408/food-platform/pkg/kafka"
	"github.com/pavelgr1408/food-platform/services/payment/internal/domain"
	"github.com/pavelgr1408/food-platform/services/payment/internal/store"
)

// orderStatePayload — нужное нам подмножество orders.state (AsyncAPI OrderState).
type orderStatePayload struct {
	OrderID         string `json:"orderId"`
	Revision        int    `json:"revision"`
	OrderNumber     string `json:"orderNumber"`
	RestaurantID    string `json:"restaurantId"`
	FulfillmentType string `json:"fulfillmentType"`
	Customer        struct {
		UserID string `json:"userId"`
	} `json:"customer"`
	TotalAmount struct {
		Amount   int64  `json:"amount"`
		Currency string `json:"currency"`
	} `json:"totalAmount"`
	PaymentMethod string    `json:"paymentMethod"`
	CreatedAt     time.Time `json:"createdAt"`
}

type orderStatusPayload struct {
	OrderID   string    `json:"orderId"`
	Status    string    `json:"status"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Topics — топики, которые потребляет payment-service.
func Topics() []string { return []string{TopicState, TopicStatus} }

// HandleEvent — kafka.TxHandler консьюмера (внутри inbox-транзакции): orders.state → реплика заказа,
// orders.status → статус заказа. Ошибка разбора ведёт к повторам и затем в <topic>.dlq.
func (s *Service) HandleEvent(ctx context.Context, tx pgx.Tx, ev kafka.Event) error {
	switch ev.Topic {
	case TopicState:
		var p orderStatePayload
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			return fmt.Errorf("orders.state: %w", err)
		}
		m, ok := domain.MethodFromEvent(p.PaymentMethod)
		if _, err := uuid.Parse(p.OrderID); err != nil || !ok || p.Revision < 1 || p.TotalAmount.Amount < 0 || p.TotalAmount.Currency == "" {
			return fmt.Errorf("orders.state: invalid payload (orderId=%q revision=%d method=%q)", p.OrderID, p.Revision, p.PaymentMethod)
		}
		changed, err := s.st.UpsertOrder(ctx, tx, domain.Order{ID: p.OrderID, Revision: p.Revision, Number: p.OrderNumber,
			Restaurant: p.RestaurantID, Fulfillment: p.FulfillmentType, Method: m, Amount: p.TotalAmount.Amount,
			Currency: p.TotalAmount.Currency, OwnerUserID: p.Customer.UserID, UpdatedAt: s.now()})
		if err != nil {
			return err
		}
		if !changed {
			s.log.DebugContext(ctx, "stale orders.state ignored", "order_id", p.OrderID, "revision", p.Revision)
		}
		return s.startCourierPayment(ctx, tx, p.OrderID)
	case TopicStatus:
		var p orderStatusPayload
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			return fmt.Errorf("orders.status: %w", err)
		}
		if _, err := uuid.Parse(p.OrderID); err != nil || p.Status == "" || p.UpdatedAt.IsZero() {
			return fmt.Errorf("orders.status: invalid payload (orderId=%q status=%q)", p.OrderID, p.Status)
		}
		if err := s.st.SetOrderStatus(ctx, tx, p.OrderID, p.Status, p.UpdatedAt); err != nil {
			return err
		}
		if p.Status == domain.OrderCancelled {
			// Незавершённые операции пора отменить у шлюза (reconciler), оплаченные — вернуть (цикл возвратов).
			return s.st.ExpediteLive(ctx, tx, p.OrderID, s.now())
		}
		return nil
	}
	return fmt.Errorf("unexpected topic %q", ev.Topic)
}

// startCourierPayment создаёт платёж «картой курьеру» (S7, UC-PAY-03): клиент его не инициирует, платёж заводит
// система на orders.state заказа с доставкой и способом courier_card. Операция у шлюза создаётся reconciler'ом
// (вызов вне транзакции, идемпотентно по paymentId), он же публикует orders.payments.link. Уже есть платёж —
// ничего не делает (повторные revision, перепроигрывание событий).
func (s *Service) startCourierPayment(ctx context.Context, tx pgx.Tx, orderID string) error {
	o, err := s.st.GetOrder(ctx, tx, orderID, false)
	if err != nil {
		return err
	}
	if o.Method != domain.MethodCourierCard || o.Fulfillment != "delivery" {
		return nil
	}
	if st, err := s.st.OrderStatus(ctx, tx, orderID); err != nil || st == domain.OrderCancelled {
		return err
	}
	list, err := s.st.OrderPayments(ctx, tx, orderID)
	if err != nil || len(list) > 0 {
		return err
	}
	now := s.now()
	p := domain.Payment{ID: uuid.NewString(), OrderID: orderID, Attempt: 1, Method: domain.MethodCourierCard, Status: domain.StatusPending,
		Amount: o.Amount, Currency: o.Currency, NextPollAt: &now, CreatedAt: now, UpdatedAt: now}
	if err := s.st.InsertPayment(ctx, tx, p); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return nil // параллельная попытка уже есть
		}
		return err
	}
	if err := enqueueStatus(ctx, tx, p); err != nil {
		return err
	}
	s.m.Created.WithLabelValues(string(p.Method)).Inc()
	s.m.Transitions.WithLabelValues(string(domain.StatusPending)).Inc()
	return nil
}
