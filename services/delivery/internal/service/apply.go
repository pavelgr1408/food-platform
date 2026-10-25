package service

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pavelgr1408/food-platform/pkg/pgxutil"
	"github.com/pavelgr1408/food-platform/services/delivery/internal/courier"
	"github.com/pavelgr1408/food-platform/services/delivery/internal/domain"
)

// Observation — что стало известно о доставке (вебхук, опрос, таймаут, отмена).
type Observation struct {
	Status       domain.Status // "" — статус не сообщён
	Reason       domain.FailReason
	CourierID    string
	CourierName  string
	CourierPhone string
	EtaAt        *time.Time
	Paid         bool
}

func obsFromState(st courier.State) Observation {
	o := Observation{CourierID: st.CourierID, CourierName: st.CourierName, CourierPhone: st.CourierPhone, EtaAt: st.EtaAt, Paid: st.Paid,
		Reason: domain.FailReason(st.Reason)}
	if s := domain.Status(st.Status); s.Valid() {
		o.Status = s
	}
	return o
}

// etaStep — на сколько должна измениться оценка времени, чтобы её стоило публиковать.
const etaStep = time.Minute

// applyTx — ЕДИНСТВЕННОЕ место смены статуса доставки: ответ курьерской системы, вебхук, таймаут поиска и
// отмена заказа проходят здесь. Вызывается внутри транзакции над строкой, взятой FOR UPDATE.
// Статусы идут только вперёд, терминальный неизменен, повтор и устаревшее — no-op. Событие
// orders.delivery.status публикуется при смене статуса и при заметном изменении оценки времени.
func (s *Service) applyTx(ctx context.Context, tx pgx.Tx, d *domain.Delivery, o Observation, source string) (bool, error) {
	if d.Status.Terminal() {
		return false, nil
	}
	changed, publish := false, false
	if o.Status != "" && o.Status != d.Status {
		if !domain.CanAdvance(d.Status, o.Status) {
			s.log.WarnContext(ctx, "delivery status not applied (backwards)", "delivery_id", d.ID, "from", d.Status, "to", o.Status, "source", source)
			return false, nil
		}
		if o.Status == domain.StatusFailed {
			r := o.Reason
			if !r.Valid() {
				r = domain.ReasonNoCourier
				if d.Status.Active() {
					r = domain.ReasonUndelivered
				}
			}
			d.FailReason = r
		}
		d.Status = o.Status
		changed, publish = true, true
	}
	if o.CourierID != "" && o.CourierID != d.CourierID {
		d.CourierID, changed = o.CourierID, true
	}
	if o.CourierName != "" && o.CourierName != d.CourierName {
		d.CourierName, changed = o.CourierName, true
	}
	if o.CourierPhone != "" && o.CourierPhone != d.CourierPhone {
		d.CourierPhone, changed = o.CourierPhone, true
	}
	if o.EtaAt != nil {
		diff := time.Duration(0)
		if d.EtaAt != nil {
			diff = o.EtaAt.Sub(*d.EtaAt)
			if diff < 0 {
				diff = -diff
			}
		}
		if d.EtaAt == nil || diff >= etaStep {
			t := o.EtaAt.UTC()
			d.EtaAt, changed = &t, true
			publish = true
		}
	}
	if o.Paid && !d.PaidReported {
		d.PaidReported, changed = true, true
	}
	if !changed {
		return false, nil
	}
	now := s.now()
	if min := d.UpdatedAt.Add(time.Millisecond); now.Before(min) {
		now = min // updatedAt строго растёт: потребитель отсекает устаревшие события по нему
	}
	d.UpdatedAt = now
	if d.Status.Terminal() {
		d.NextPollAt = nil
	}
	if err := s.st.Update(ctx, tx, *d); err != nil {
		return false, err
	}
	if publish {
		if o.Status != "" && o.Status == d.Status {
			if err := s.st.AddHistory(ctx, tx, d.ID, d.Status, string(d.FailReason), source, now); err != nil {
				return false, err
			}
			s.m.Transitions.WithLabelValues(string(d.Status), source).Inc()
		}
		if err := enqueueStatus(ctx, tx, *d); err != nil {
			return false, err
		}
	}
	return true, nil
}

// mutate берёт доставку под FOR UPDATE, даёт fn изменить её и сохраняет. Если fn вернула changed=true —
// будит relay после коммита. Возвращает итоговое состояние.
func (s *Service) mutate(ctx context.Context, id string, fn func(tx pgx.Tx, d *domain.Delivery) (bool, error)) (domain.Delivery, error) {
	var out domain.Delivery
	changed := false
	err := pgxutil.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		d, err := s.st.GetByID(ctx, tx, id, true)
		if err != nil {
			return err
		}
		changed, err = fn(tx, &d)
		out = d
		return err
	})
	if err == nil && changed {
		s.wake()
	}
	return out, err
}

// afterApply — побочные эффекты вне транзакции: координаты курьера в Redis (пока в пути) и их очистка
// при завершении. Сбой Redis не ломает доставку — координаты вторичны, следующий опрос запишет заново.
func (s *Service) afterApply(ctx context.Context, d domain.Delivery, loc *courier.Point, at time.Time) {
	if s.locs == nil {
		return
	}
	switch {
	case d.Status.Terminal():
		if err := s.locs.Delete(ctx, d.OrderID); err != nil {
			s.log.WarnContext(ctx, "location cleanup failed", "order_id", d.OrderID, "error", err)
		}
	case d.Status.Active() && loc != nil:
		if at.IsZero() {
			at = s.now()
		}
		err := s.putLocation(ctx, d.OrderID, loc, at)
		res := "ok"
		if err != nil {
			res = "error"
			s.log.WarnContext(ctx, "location store write failed", "order_id", d.OrderID, "error", err)
		}
		s.m.LocationWrites.WithLabelValues(res).Inc()
	}
}
