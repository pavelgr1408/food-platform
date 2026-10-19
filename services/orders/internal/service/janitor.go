package service

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pavelgr1408/food-platform/pkg/pgxutil"
	"github.com/pavelgr1408/food-platform/services/orders/internal/domain"
)

// ReasonPaymentTimeout — причина автоматической отмены неоплаченного заказа.
const ReasonPaymentTimeout = "payment_timeout"

// ExpireUnpaid отменяет заказы в pending_payment старше PaymentTimeout (с paymentStatus=unknown — старше
// PaymentTimeout+UnknownGrace). Возвращает число отменённых.
func (s *Service) ExpireUnpaid(ctx context.Context) (int, error) {
	now := s.now()
	ids, err := s.st.UnpaidBefore(ctx, s.pool, now.Add(-s.cfg.PaymentTimeout), now.Add(-s.cfg.PaymentTimeout-s.cfg.UnknownGrace), 100)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, id := range ids {
		changed, err := s.cancelIfUnpaid(ctx, id)
		if err != nil {
			return n, err
		}
		if changed {
			n++
		}
	}
	return n, nil
}

// cancelIfUnpaid отменяет заказ, только если под блокировкой он всё ещё pending_payment. Гонка с оплатой:
// автомат разрешает accepted → cancelled (отмена принятого заказа), поэтому одной таблицы переходов мало —
// без этой проверки таймер мог бы отменить заказ, который payment как раз успел принять.
func (s *Service) cancelIfUnpaid(ctx context.Context, id string) (bool, error) {
	var o domain.Order
	var changed bool
	err := pgxutil.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := s.st.GetOrder(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if cur.Status != domain.StatusPendingPayment || cur.PaymentStatus == domain.PaymentPaid {
			return nil
		}
		o, changed, err = s.transitionTx(ctx, tx, id, domain.StatusCancelled, ReasonPaymentTimeout, actorSystem)
		return err
	})
	if err != nil {
		return false, err
	}
	if changed {
		s.wake()
		s.noteTransition(ctx, o, domain.StatusCancelled, ReasonPaymentTimeout, actorSystem)
	}
	return changed, nil
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
