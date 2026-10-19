package service

import (
	"context"
	"errors"
	"time"

	"github.com/pavelgr1408/food-platform/services/payment/internal/domain"
	"github.com/pavelgr1408/food-platform/services/payment/internal/gateway"
	"github.com/pavelgr1408/food-platform/services/payment/internal/store"
)

// Reconcile сверяет со шлюзом платежи, которым пора (pending без вебхука, unknown). Возвращает число обработанных.
//
// Платёж «арендуется» (next_poll_at сдвигается), поэтому вызовы шлюза идут без блокировок БД и несколько
// экземпляров сервиса не опрашивают одно и то же. Результат применяется тем же автоматом, что и вебхук,
// поэтому гонка «вебхук ↔ опрос» безопасна: проигравший получает no-op.
func (s *Service) Reconcile(ctx context.Context) (int, error) {
	due, err := s.st.ClaimDue(ctx, s.pool, s.now(), s.cfg.PollLease, s.cfg.ReconcileBatch)
	if err != nil {
		return 0, err
	}
	for _, p := range due {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		s.reconcileOne(ctx, p)
	}
	return len(due), nil
}

func (s *Service) reconcileOne(ctx context.Context, p domain.Payment) {
	res := s.reconcile(ctx, p)
	s.m.Reconciled.WithLabelValues(res).Inc()
	if res == "paid" || res == "declined" {
		return
	}
	// остался pending/unknown: следующий опрос с нарастающей паузой
	next := s.now().Add(s.backoff(p.PollAttempts))
	if err := s.st.ScheduleNextPoll(ctx, s.pool, p.ID, next); err != nil {
		s.log.ErrorContext(ctx, "cannot schedule next poll", "payment_id", p.ID, "error", err)
	}
}

// reconcile возвращает результат: paid | declined | pending | unknown | error.
func (s *Service) reconcile(ctx context.Context, p domain.Payment) string {
	if p.GatewayRef == "" {
		return s.reconcileInit(ctx, p)
	}
	start := time.Now()
	st, err := s.gw.Status(ctx, p.GatewayRef)
	s.observeGateway("status", start, err)
	if err != nil {
		s.log.WarnContext(ctx, "gateway status failed", "payment_id", p.ID, "gateway_ref", p.GatewayRef, "error", err)
		if errors.Is(err, gateway.ErrNotFound) {
			return "error"
		}
		if p.Status == domain.StatusPending {
			return s.settle(ctx, p, domain.StatusUnknown, "", "")
		}
		return "unknown"
	}
	if st.Status == gateway.StatePaid && (st.Amount != p.Amount || (st.Currency != "" && st.Currency != p.Currency)) {
		s.m.AmountMismatch.Inc()
		s.log.ErrorContext(ctx, "gateway reports a different amount: not applied", "payment_id", p.ID,
			"expected", p.Amount, "got", st.Amount)
		return "error"
	}
	switch st.Status {
	case gateway.StatePaid:
		return s.settle(ctx, p, domain.StatusPaid, "", st.Ref)
	case gateway.StateDeclined:
		return s.settle(ctx, p, domain.StatusDeclined, nonEmpty(st.Reason, "declined"), st.Ref)
	}
	// у шлюза всё ещё pending
	if p.Status == domain.StatusUnknown {
		return s.settle(ctx, p, domain.StatusPending, "", "")
	}
	if s.now().Sub(p.CreatedAt) > s.cfg.PendingExpiry {
		return s.expire(ctx, p)
	}
	return "pending"
}

// reconcileInit: вызов шлюза не завершился (процесс упал или таймаут) — повторяем Create с тем же ключом;
// шлюз вернёт уже созданную операцию, а не заведёт вторую.
func (s *Service) reconcileInit(ctx context.Context, p domain.Payment) string {
	o, err := s.st.GetOrder(ctx, s.pool, p.OrderID, false)
	if err != nil {
		s.log.ErrorContext(ctx, "reconcile: order replica is missing", "payment_id", p.ID, "error", err)
		return "error"
	}
	res, err := s.gwCreate(ctx, p, o.Number)
	switch {
	case err == nil:
		if err := s.st.SetGatewayOperation(ctx, s.pool, p.ID, res.Ref, res.URL, s.now()); err != nil {
			s.log.ErrorContext(ctx, "cannot store gateway operation", "payment_id", p.ID, "error", err)
			return "error"
		}
		if p.Status == domain.StatusUnknown {
			return s.settle(ctx, p, domain.StatusPending, "", res.Ref)
		}
		return "pending"
	case gateway.IsRejected(err):
		return s.settle(ctx, p, domain.StatusDeclined, "gateway_rejected", "")
	default:
		s.log.WarnContext(ctx, "gateway create failed again", "payment_id", p.ID, "error", err)
		if p.Status == domain.StatusPending {
			return s.settle(ctx, p, domain.StatusUnknown, "", "")
		}
		return "unknown"
	}
}

// expire отменяет у шлюза неоплаченную попытку (пользователь ушёл со страницы оплаты). Если за это время
// платёж успели провести, шлюз вернёт paid — применяем его: деньги важнее.
func (s *Service) expire(ctx context.Context, p domain.Payment) string {
	start := time.Now()
	st, err := s.gw.Cancel(ctx, p.GatewayRef)
	s.observeGateway("cancel", start, err)
	if err != nil {
		s.log.WarnContext(ctx, "gateway cancel failed", "payment_id", p.ID, "error", err)
		return "pending"
	}
	switch st.Status {
	case gateway.StatePaid:
		return s.settle(ctx, p, domain.StatusPaid, "", st.Ref)
	case gateway.StateDeclined:
		return s.settle(ctx, p, domain.StatusDeclined, "expired", st.Ref)
	}
	return "pending"
}

// settle применяет статус и возвращает метку результата.
func (s *Service) settle(ctx context.Context, p domain.Payment, to domain.Status, reason, ref string) string {
	cur, _, err := s.applyByID(ctx, p.ID, to, reason, ref)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			s.log.ErrorContext(ctx, "reconcile: cannot apply status", "payment_id", p.ID, "to", to, "error", err)
		}
		return "error"
	}
	return string(cur.Status)
}

func nonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// RunReconciler периодически сверяется со шлюзом, пока ctx не отменён.
func (s *Service) RunReconciler(ctx context.Context, interval time.Duration) {
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
				n, err := s.Reconcile(ctx)
				if err != nil {
					if ctx.Err() == nil {
						s.log.ErrorContext(ctx, "reconcile failed", "error", err)
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
