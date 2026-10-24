package service

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pavelgr1408/food-platform/pkg/pgxutil"
	"github.com/pavelgr1408/food-platform/services/payment/internal/domain"
	"github.com/pavelgr1408/food-platform/services/payment/internal/gateway"
	"github.com/pavelgr1408/food-platform/services/payment/internal/store"
)

// Возврат — отдельный процесс поверх оплаченного платежа (статус платежа остаётся paid):
//
//	(нет) → pending → refunded | failed
//
// Запрос на возврат создаёт «свип» (RequestRefunds): оплаченные платежи отменённых заказов. Свип, а не
// реакция на одно событие, потому что заказ можно отменить в любой момент относительно оплаты (в том числе
// пока платёж проводят), а Kafka не упорядочивает топики между собой; повторы безопасны.
// Вызов шлюза идемпотентен (ключ = paymentId): повтор после таймаута не заведёт второй возврат.
// Временные сбои повторяются с нарастающей паузой; определённый отказ (4xx) → failed (ручная обработка).

// ProcessRefunds один проход: запрашивает возвраты и выполняет те, которым пора. Возвращает число обработанных.
func (s *Service) ProcessRefunds(ctx context.Context) (int, error) {
	var requested []domain.Payment
	if err := pgxutil.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		if requested, err = s.st.RequestRefunds(ctx, tx, s.now(), s.cfg.ReconcileBatch); err != nil {
			return err
		}
		for _, p := range requested {
			if err := enqueueStatus(ctx, tx, p); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return 0, err
	}
	if len(requested) > 0 {
		s.wake()
		for _, p := range requested {
			s.m.Refunds.WithLabelValues("requested").Inc()
			s.log.WarnContext(ctx, "refund requested: the order is cancelled", "payment_id", p.ID, "order_id", p.OrderID, "amount", p.Amount)
		}
	}
	due, err := s.st.ClaimRefunds(ctx, s.pool, s.now(), s.cfg.PollLease, s.cfg.ReconcileBatch)
	if err != nil {
		return 0, err
	}
	for _, p := range due {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		s.refundOne(ctx, p)
	}
	return len(due), nil
}

func (s *Service) refundOne(ctx context.Context, p domain.Payment) {
	gw := s.gwFor(p.Method)
	if gw == nil || p.GatewayRef == "" {
		// без шлюза или ссылки на операцию вернуть нечего автоматически — ручная обработка
		s.finishRefund(ctx, p, domain.RefundFailed, "", "no_gateway_operation")
		return
	}
	start := time.Now()
	res, err := gw.Refund(ctx, p.GatewayRef, p.ID, p.Amount, p.Currency)
	s.observeGateway("refund", start, err)
	var rej *gateway.RejectedError
	switch {
	case err == nil && res.Status == "refunded":
		s.finishRefund(ctx, p, domain.RefundRefunded, res.Ref, "")
	case err == nil: // у шлюза возврат ещё в обработке: спросим позже тем же ключом
		s.retryRefund(ctx, p, "gateway_pending")
	case errors.As(err, &rej):
		s.finishRefund(ctx, p, domain.RefundFailed, "", nonEmpty(rej.Code, "rejected"))
	case errors.Is(err, gateway.ErrNotFound):
		s.finishRefund(ctx, p, domain.RefundFailed, "", "gateway_operation_not_found")
	default:
		s.log.WarnContext(ctx, "refund call failed: will retry", "payment_id", p.ID, "attempt", p.RefundAttempts+1, "error", err)
		s.retryRefund(ctx, p, "error")
	}
}

func (s *Service) retryRefund(ctx context.Context, p domain.Payment, why string) {
	s.m.Refunds.WithLabelValues("retry").Inc()
	if err := s.st.RetryRefund(ctx, s.pool, p.ID, s.now().Add(s.backoff(p.RefundAttempts))); err != nil {
		s.log.ErrorContext(ctx, "cannot schedule refund retry", "payment_id", p.ID, "why", why, "error", err)
	}
}

// finishRefund фиксирует итог возврата и публикует статус платежа — атомарно.
func (s *Service) finishRefund(ctx context.Context, p domain.Payment, st domain.RefundStatus, ref, reason string) {
	var changed bool
	err := pgxutil.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		ok, err := s.st.FinishRefund(ctx, tx, p.ID, st, ref, reason, s.now())
		if err != nil || !ok {
			return err
		}
		cur, err := s.st.GetPayment(ctx, tx, p.ID, false)
		if err != nil {
			return err
		}
		changed = true
		return enqueueStatus(ctx, tx, cur)
	})
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			s.log.ErrorContext(ctx, "cannot finish refund", "payment_id", p.ID, "error", err)
		}
		return
	}
	if !changed {
		return
	}
	s.wake()
	s.m.Refunds.WithLabelValues(string(st)).Inc()
	if st == domain.RefundFailed {
		s.log.ErrorContext(ctx, "refund failed: manual handling required", "payment_id", p.ID, "order_id", p.OrderID,
			"amount", p.Amount, "reason", reason)
		return
	}
	s.log.InfoContext(ctx, "refund completed", "payment_id", p.ID, "order_id", p.OrderID)
}

// RunRefunds периодически выполняет возвраты, пока ctx не отменён.
func (s *Service) RunRefunds(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			for {
				n, err := s.ProcessRefunds(ctx)
				if err != nil {
					if ctx.Err() == nil {
						s.log.ErrorContext(ctx, "refund pass failed", "error", err)
					}
					break
				}
				if n < s.cfg.ReconcileBatch {
					break
				}
			}
		}
	}
}
