package service

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/pavelgr1408/food-platform/pkg/kafka"
	"github.com/pavelgr1408/food-platform/services/orders/internal/domain"
)

// TopicPaymentStatus — orders.payments.status (продюсер — payment-service).
const TopicPaymentStatus = "orders.payments.status"

// ReasonPaid — основание перехода pending_payment → accepted.
const ReasonPaid = "paid"

type paymentStatusPayload struct {
	OrderID   string    `json:"orderId"`
	PaymentID string    `json:"paymentId"`
	Status    string    `json:"status"`
	UpdatedAt time.Time `json:"updatedAt"`
	// Ход возврата (S6): payment публикует то же событие со статусом paid и refundStatus.
	RefundStatus string `json:"refundStatus,omitempty"`
}

// HandlePaymentStatus — kafka.TxHandler консьюмера orders.payments.status (внутри inbox-транзакции).
//
//	paid     → заказ pending_payment → accepted, revision+1, orders.state (paymentStatus=paid) и orders.status — ADR-10;
//	declined → paymentStatus=declined, заказ остаётся pending_payment (оплатить можно заново);
//	unknown  → paymentStatus=unknown (автоотмена по таймауту откладывается), pending → paymentStatus=pending.
//
// Защиты: событие старше уже применённого (по updatedAt) игнорируется; paid не перезаписывается;
// paid по отменённому заказу НЕ возвращает его в работу и не публикует orders.state (производство не стартует) —
// фиксируется paymentStatus=paid при status=cancelled, метрика и ошибка в лог — возврат запускает сам payment
// (S6) и сообщает ход в refundStatus: он применяется независимо от защиты выше (pending → refunded|failed).
func (s *Service) HandlePaymentStatus(ctx context.Context, tx pgx.Tx, ev kafka.Event) error {
	var p paymentStatusPayload
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		return fmt.Errorf("orders.payments.status: %w", err)
	}
	ps := domain.PaymentStatus(p.Status)
	switch ps {
	case domain.PaymentPaid, domain.PaymentDeclined, domain.PaymentUnknown, domain.PaymentPending:
	default:
		return fmt.Errorf("orders.payments.status: unknown status %q", p.Status)
	}
	if _, err := uuid.Parse(p.OrderID); err != nil || p.UpdatedAt.IsZero() {
		return fmt.Errorf("orders.payments.status: invalid payload (orderId=%q)", p.OrderID)
	}

	o, err := s.st.GetOrder(ctx, tx, p.OrderID, true)
	if err != nil {
		return fmt.Errorf("orders.payments.status: order %s: %w", p.OrderID, err) // невозможно без заказа → повторы и DLQ
	}
	if rs := domain.RefundStatus(p.RefundStatus); rs != "" {
		if !rs.Valid() {
			return fmt.Errorf("orders.payments.status: unknown refundStatus %q", p.RefundStatus)
		}
		if _, err := s.st.SetRefundStatus(ctx, tx, o.ID, rs, p.UpdatedAt.UTC()); err != nil {
			return err
		}
		if s.m != nil {
			s.m.RefundEvents.WithLabelValues(string(rs)).Inc()
		}
	}
	at := p.UpdatedAt.UTC()
	if o.PaymentStatus == domain.PaymentPaid || (o.PaymentUpdatedAt != nil && at.Before(*o.PaymentUpdatedAt)) {
		s.log.DebugContext(ctx, "stale or repeated payment event ignored", "order_id", o.ID, "status", p.Status)
		return nil
	}

	if ps != domain.PaymentPaid {
		_, err := s.st.SetPaymentStatus(ctx, tx, o.ID, ps, at, false)
		return err
	}

	if o.Status == domain.StatusCancelled {
		if _, err := s.st.SetPaymentStatus(ctx, tx, o.ID, ps, at, false); err != nil {
			return err
		}
		if s.m != nil {
			s.m.PaidAfterCancel.Inc()
		}
		if p.RefundStatus == "" { // payment ещё не сообщил о возврате: он будет, пока считаем pending
			if _, err := s.st.SetRefundStatus(ctx, tx, o.ID, domain.RefundPending, at); err != nil {
				return err
			}
		}
		s.log.ErrorContext(ctx, "payment received for a cancelled order: refund required",
			"order_id", o.ID, "payment_id", p.PaymentID, "order_status_reason", o.StatusReason)
		return nil
	}

	rev, err := s.st.SetPaymentStatus(ctx, tx, o.ID, ps, at, true)
	if err != nil {
		return err
	}
	o.PaymentStatus, o.Revision = ps, rev
	if o.Status == domain.StatusPendingPayment {
		accepted, changed, err := s.transitionTx(ctx, tx, o.ID, domain.StatusAccepted, ReasonPaid, actorSystem)
		if err != nil {
			return err
		}
		if changed {
			o.Status, o.UpdatedAt = accepted.Status, accepted.UpdatedAt
			s.noteTransition(ctx, accepted, domain.StatusAccepted, ReasonPaid, actorSystem)
		}
	}
	if err := enqueueState(ctx, tx, o); err != nil {
		return err
	}
	// оплата курьеру подтверждается после вручения: доставленный заказ закрывается (S7)
	return s.closeIfSettled(ctx, tx, o.ID)
}
