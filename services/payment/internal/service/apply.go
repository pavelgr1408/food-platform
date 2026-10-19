package service

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/pavelgr1408/food-platform/pkg/pgxutil"
	"github.com/pavelgr1408/food-platform/services/payment/internal/domain"
	"github.com/pavelgr1408/food-platform/services/payment/internal/store"
)

// backoff — пауза до следующего опроса шлюза: PollMin·2^attempts, но не больше PollMax.
func (s *Service) backoff(attempts int) time.Duration {
	d := s.cfg.PollMin
	for i := 0; i < attempts && d < s.cfg.PollMax; i++ {
		d *= 2
	}
	if d > s.cfg.PollMax {
		d = s.cfg.PollMax
	}
	return d
}

// applyTx переводит платёж p (прочитанный с блокировкой в tx) в статус to. Единственное место смены статуса.
//   - тот же статус — ничего не делает (идемпотентность вебхука/опроса);
//   - устаревшее наблюдение (платёж уже финален, а пришёл pending/unknown) — тихо пропускается;
//   - противоречие (paid ↔ declined) — не применяется, учитывается метрикой и логом;
//   - paid по отменённому заказу — применяется (деньги получены!) с пометкой refund_required.
//
// Статус и событие orders.payments.status пишутся атомарно.
func (s *Service) applyTx(ctx context.Context, tx pgx.Tx, p domain.Payment, to domain.Status, reason, ref string) (domain.Payment, bool, error) {
	if p.Status == to {
		return p, false, nil
	}
	if p.Status.Final() && !to.Final() {
		return p, false, nil
	}
	if !domain.CanTransition(p.Status, to) {
		s.m.Conflicts.WithLabelValues(string(p.Status), string(to)).Inc()
		s.log.WarnContext(ctx, "conflicting payment update ignored", "payment_id", p.ID, "order_id", p.OrderID,
			"from", p.Status, "to", to, "reason", reason)
		return p, false, nil
	}
	at := s.now()
	u := store.Update{Status: to, Reason: reason, GatewayRef: ref, At: at}
	switch to {
	case domain.StatusPaid:
		u.PaidAt = &at
		st, err := s.st.OrderStatus(ctx, tx, p.OrderID)
		if err != nil {
			return p, false, err
		}
		if st == domain.OrderCancelled {
			u.RefundNeeded = true
			s.m.PaidAfterCancel.Inc()
			s.log.ErrorContext(ctx, "payment received for a cancelled order: refund required",
				"payment_id", p.ID, "order_id", p.OrderID, "amount", p.Amount)
		}
	case domain.StatusPending, domain.StatusUnknown, domain.StatusAuthorized:
		next := at.Add(s.cfg.PollMin)
		u.NextPollAt = &next
	}
	if err := s.st.ApplyUpdate(ctx, tx, p.ID, u); err != nil {
		return p, false, err
	}
	p.Status, p.UpdatedAt, p.NextPollAt = to, at, u.NextPollAt
	if reason != "" {
		p.DeclineReason = reason
	}
	if p.GatewayRef == "" {
		p.GatewayRef = ref
	}
	if u.PaidAt != nil {
		p.PaidAt = u.PaidAt
	}
	p.RefundNeeded = p.RefundNeeded || u.RefundNeeded
	if err := enqueueStatus(ctx, tx, p); err != nil {
		return p, false, err
	}
	s.m.Transitions.WithLabelValues(string(to)).Inc()
	s.log.InfoContext(ctx, "payment status changed", "payment_id", p.ID, "order_id", p.OrderID, "status", to, "reason", reason)
	return p, true, nil
}

// applyByID читает платёж с блокировкой, применяет статус и будит relay после коммита.
func (s *Service) applyByID(ctx context.Context, id string, to domain.Status, reason, ref string) (p domain.Payment, changed bool, err error) {
	err = pgxutil.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := s.st.GetPayment(ctx, tx, id, true)
		if err != nil {
			return err
		}
		p, changed, err = s.applyTx(ctx, tx, cur, to, reason, ref)
		return err
	})
	if err == nil && changed {
		s.wake()
	}
	return p, changed, err
}

// Callback — разобранное уведомление шлюза/кассы (PaymentStatusCallback).
type Callback struct {
	EventID     string
	OrderID     string
	PaymentID   string
	Status      string // paid | declined
	ExternalRef string
	Reason      string
	Amount      *int64 // nil — сумму не передали
	Currency    string
}

// HandleCallback применяет вебхук. source — способ оплаты, чьим каналом он пришёл (online_card — шлюз, pos — касса).
// Повтор того же eventId — nil без эффекта. Подпись проверяет HTTP-слой.
func (s *Service) HandleCallback(ctx context.Context, source domain.Method, cb Callback) error {
	label := string(source)
	for _, id := range []string{cb.EventID, cb.OrderID, cb.PaymentID} {
		if _, err := uuid.Parse(id); err != nil {
			return errBadParam("eventId, orderId and paymentId must be UUIDs")
		}
	}
	var to domain.Status
	switch cb.Status {
	case "paid":
		to = domain.StatusPaid
	case "declined":
		to = domain.StatusDeclined
	default:
		return errBadParam("status must be paid or declined")
	}
	var changed bool
	err := pgxutil.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		p, err := s.st.GetPayment(ctx, tx, cb.PaymentID, true)
		if errors.Is(err, store.ErrNotFound) || (err == nil && (p.OrderID != cb.OrderID || p.Method != source)) {
			s.m.Webhooks.WithLabelValues(label, "not_found").Inc()
			return errPaymentNotFound
		}
		if err != nil {
			return err
		}
		if cb.Amount != nil && (*cb.Amount != p.Amount || (cb.Currency != "" && cb.Currency != p.Currency)) {
			s.m.Webhooks.WithLabelValues(label, "amount_mismatch").Inc()
			s.m.AmountMismatch.Inc()
			s.log.ErrorContext(ctx, "webhook amount mismatch: notification ignored", "payment_id", p.ID,
				"order_id", p.OrderID, "expected", p.Amount, "got", *cb.Amount)
			return errAmountMismatch
		}
		first, err := s.st.TryRecordWebhook(ctx, tx, cb.EventID, label, p.ID)
		if err != nil {
			return err
		}
		if !first {
			s.m.Webhooks.WithLabelValues(label, "duplicate").Inc()
			return nil
		}
		reason := ""
		if to == domain.StatusDeclined {
			reason = cb.Reason
			if reason == "" {
				reason = "declined"
			}
		}
		_, changed, err = s.applyTx(ctx, tx, p, to, reason, cb.ExternalRef)
		if err == nil {
			s.m.Webhooks.WithLabelValues(label, "applied").Inc()
		}
		return err
	})
	if err == nil && changed {
		s.wake()
	}
	return err
}
