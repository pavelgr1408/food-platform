package service

import (
	"context"
	"time"

	"github.com/pavelgr1408/food-platform/pkg/kafka"
	"github.com/pavelgr1408/food-platform/services/payment/internal/domain"
)

// Топики и типы событий (contracts/asyncapi/kafka-events.yaml).
const (
	TopicState         = "orders.state"
	TopicStatus        = "orders.status"
	TopicPaymentStatus = "orders.payments.status"
	TypePaymentStatus  = "orders.payments.status.v1"
	TopicPaymentLink   = "orders.payments.link"
	TypePaymentLink    = "orders.payments.link.v1"
)

type moneyEvent struct {
	Amount   int64  `json:"amount"`
	Currency string `json:"currency"`
}

// PaymentStatusEvent — payload orders.payments.status (AsyncAPI PaymentStatus).
type PaymentStatusEvent struct {
	OrderID          string     `json:"orderId"`
	PaymentID        string     `json:"paymentId"`
	Attempt          int        `json:"attempt"`
	Status           string     `json:"status"`
	Method           string     `json:"method"`
	Amount           moneyEvent `json:"amount"`
	GatewayReference string     `json:"gatewayReference,omitempty"`
	Reason           string     `json:"reason,omitempty"`
	PaidAt           *time.Time `json:"paidAt,omitempty"`
	RefundStatus     string     `json:"refundStatus,omitempty"` // pending | refunded | failed (S6)
	RefundedAt       *time.Time `json:"refundedAt,omitempty"`
	UpdatedAt        time.Time  `json:"updatedAt"`
}

func statusEvent(p domain.Payment) PaymentStatusEvent {
	ev := PaymentStatusEvent{OrderID: p.OrderID, PaymentID: p.ID, Attempt: p.Attempt, Status: string(p.Status),
		Method: p.Method.EventForm(), Amount: moneyEvent{p.Amount, p.Currency}, GatewayReference: p.GatewayRef,
		Reason: p.DeclineReason, UpdatedAt: p.UpdatedAt.UTC()}
	if p.PaidAt != nil {
		t := p.PaidAt.UTC()
		ev.PaidAt = &t
	}
	ev.RefundStatus = string(p.RefundStatus)
	if p.RefundedAt != nil {
		t := p.RefundedAt.UTC()
		ev.RefundedAt = &t
	}
	return ev
}

// enqueueStatus кладёт orders.payments.status в outbox (в той же транзакции, что и смена статуса).
func enqueueStatus(ctx context.Context, tx kafka.Execer, p domain.Payment) error {
	ev, err := kafka.NewEvent(ctx, TopicPaymentStatus, TypePaymentStatus, p.OrderID, statusEvent(p))
	if err != nil {
		return err
	}
	return kafka.Enqueue(ctx, tx, ev)
}

// PaymentLinkEvent — payload orders.payments.link (AsyncAPI PaymentLink): ссылка на оплату курьеру.
type PaymentLinkEvent struct {
	OrderID    string     `json:"orderId"`
	PaymentID  string     `json:"paymentId"`
	PaymentURL string     `json:"paymentUrl"`
	Amount     moneyEvent `json:"amount"`
	ExpiresAt  *time.Time `json:"expiresAt,omitempty"`
}

// enqueueLink кладёт orders.payments.link в outbox (в той же транзакции, что и запись операции шлюза).
func enqueueLink(ctx context.Context, tx kafka.Execer, p domain.Payment, expiresAt time.Time) error {
	t := expiresAt.UTC()
	ev, err := kafka.NewEvent(ctx, TopicPaymentLink, TypePaymentLink, p.OrderID, PaymentLinkEvent{
		OrderID: p.OrderID, PaymentID: p.ID, PaymentURL: p.PaymentURL, Amount: moneyEvent{p.Amount, p.Currency}, ExpiresAt: &t})
	if err != nil {
		return err
	}
	return kafka.Enqueue(ctx, tx, ev)
}
