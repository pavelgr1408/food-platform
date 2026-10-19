package service

import (
	"context"
	"errors"
	"time"

	"github.com/pavelgr1408/food-platform/services/orders/internal/domain"
)

// ReasonPaymentTimeout — причина автоматической отмены неоплаченного заказа.
const ReasonPaymentTimeout = "payment_timeout"

// ExpireUnpaid отменяет заказы в pending_payment старше PaymentTimeout. Возвращает число отменённых.
// Гонка с оплатой безопасна: Transition перечитывает статус под блокировкой строки, и если
// заказ успели принять (accepted), переход cancelled запрещён автоматом — заказ пропускается.
func (s *Service) ExpireUnpaid(ctx context.Context) (int, error) {
	ids, err := s.st.UnpaidBefore(ctx, s.pool, s.now().Add(-s.cfg.PaymentTimeout), 100)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, id := range ids {
		_, changed, err := s.Transition(ctx, id, domain.StatusCancelled, ReasonPaymentTimeout, actorSystem)
		switch {
		case errors.Is(err, ErrInvalidTransition):
			continue
		case err != nil:
			return n, err
		case changed:
			n++
		}
	}
	return n, nil
}

// PurgeCarts удаляет заброшенные корзины.
func (s *Service) PurgeCarts(ctx context.Context) (int64, error) {
	n, err := s.st.PurgeStaleCarts(ctx, s.pool, s.now().Add(-s.cfg.CartTTL))
	if err == nil && n > 0 && s.m != nil {
		s.m.CartsPurged.Add(float64(n))
	}
	return n, err
}

// RunJanitor периодически отменяет неоплаченные заказы и чистит корзины, пока ctx не отменён.
func (s *Service) RunJanitor(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	lastPurge := time.Time{}
	for {
		if n, err := s.ExpireUnpaid(ctx); err != nil && ctx.Err() == nil {
			s.log.Error("expire unpaid orders failed", "error", err)
		} else if n > 0 {
			s.log.Info("unpaid orders cancelled", "count", n, "reason", ReasonPaymentTimeout)
		}
		if time.Since(lastPurge) > 10*time.Minute {
			lastPurge = time.Now()
			if _, err := s.PurgeCarts(ctx); err != nil && ctx.Err() == nil {
				s.log.Error("purge carts failed", "error", err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
