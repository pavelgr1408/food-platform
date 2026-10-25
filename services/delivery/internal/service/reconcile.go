package service

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pavelgr1408/food-platform/services/delivery/internal/courier"
	"github.com/pavelgr1408/food-platform/services/delivery/internal/domain"
	"github.com/pavelgr1408/food-platform/services/delivery/internal/location"
	"github.com/pavelgr1408/food-platform/services/delivery/internal/store"
)

// RunReconciler крутит Reconcile, пока жив ctx.
func (s *Service) RunReconciler(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, err := s.Reconcile(ctx); err != nil && ctx.Err() == nil {
				s.log.ErrorContext(ctx, "delivery reconcile failed", "error", err)
			}
		}
	}
}

// Reconcile обрабатывает доставки, которым пора обращаться к курьерской системе: заявка на курьера,
// опрос статуса и координат, снятие заявки при отмене, таймаут поиска. Возвращает число обработанных.
//
// Доставка «арендуется» (next_poll_at сдвигается): вызовы идут без блокировок БД и несколько
// экземпляров не берут одно и то же. Результат применяется тем же applyTx, что и вебхук, поэтому
// гонка «вебхук ↔ опрос» безопасна: проигравший получает no-op.
func (s *Service) Reconcile(ctx context.Context) (int, error) {
	due, err := s.st.ClaimDue(ctx, s.pool, s.now(), s.cfg.Lease, s.cfg.ReconcileBatch)
	if err != nil {
		return 0, err
	}
	for _, d := range due {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		s.process(ctx, d)
	}
	return len(due), nil
}

// backoff — пауза после сбоя: PollEvery·2^attempts, не больше PollMax.
func (s *Service) backoff(attempts int) time.Duration {
	d := s.cfg.PollEvery
	for i := 0; i < attempts && d < s.cfg.PollMax; i++ {
		d *= 2
	}
	if d > s.cfg.PollMax {
		d = s.cfg.PollMax
	}
	return d
}

func (s *Service) retryLater(ctx context.Context, d domain.Delivery, result string) {
	s.m.Reconciled.WithLabelValues(result).Inc()
	if err := s.st.ScheduleNext(ctx, s.pool, d.ID, s.now().Add(s.backoff(d.PollAttempts)), d.PollAttempts+1); err != nil {
		s.log.ErrorContext(ctx, "cannot schedule next poll", "delivery_id", d.ID, "error", err)
	}
}

func (s *Service) scheduleAfter(ctx context.Context, id string, after time.Duration) {
	if err := s.st.ScheduleNext(ctx, s.pool, id, s.now().Add(after), 0); err != nil {
		s.log.ErrorContext(ctx, "cannot schedule next poll", "delivery_id", id, "error", err)
	}
}

func (s *Service) observeCourier(op string, start time.Time, err error) {
	res := "ok"
	switch {
	case err == nil:
	case errors.Is(err, courier.ErrNotFound):
		res = "not_found"
	case courier.IsRejected(err):
		res = "rejected"
	default:
		res = "error"
	}
	s.m.CourierCalls.WithLabelValues(op, res).Inc()
	s.m.CourierDuration.WithLabelValues(op).Observe(time.Since(start).Seconds())
}

func (s *Service) searchExpired(d domain.Delivery) bool {
	return d.Status == domain.StatusSearching && s.now().Sub(d.SearchStartedAt) >= s.cfg.SearchTimeout
}

func (s *Service) process(ctx context.Context, d domain.Delivery) {
	switch {
	case d.Cancelled:
		s.processCancel(ctx, d)
	case d.CourierRef == "":
		s.processDispatch(ctx, d)
	default:
		s.processPoll(ctx, d)
	}
}

// finish применяет наблюдение под блокировкой и выполняет побочные эффекты (Redis).
func (s *Service) finish(ctx context.Context, id string, o Observation, source string, loc *courier.Point, locAt time.Time, pre func(*domain.Delivery)) (domain.Delivery, error) {
	var changed bool
	out, err := s.mutate(ctx, id, func(tx pgx.Tx, d *domain.Delivery) (bool, error) {
		if pre != nil {
			pre(d)
		}
		var err error
		changed, err = s.applyTx(ctx, tx, d, o, source)
		if err != nil {
			return false, err
		}
		// pre мог изменить служебные поля без смены статуса — их надо сохранить
		if !changed && pre != nil {
			if err := s.st.Update(ctx, tx, *d); err != nil {
				return false, err
			}
		}
		return changed, nil
	})
	if err != nil {
		return out, err
	}
	s.afterApply(ctx, out, loc, locAt)
	return out, nil
}

func (s *Service) processCancel(ctx context.Context, d domain.Delivery) {
	if d.CourierRef != "" {
		start := time.Now()
		_, err := s.cr.Cancel(ctx, d.CourierRef)
		s.observeCourier("cancel", start, err)
		if err != nil && !errors.Is(err, courier.ErrNotFound) && !courier.IsRejected(err) {
			s.log.WarnContext(ctx, "courier cancel failed, will retry", "delivery_id", d.ID, "error", err)
			s.retryLater(ctx, d, "retry")
			return
		}
	}
	if _, err := s.finish(ctx, d.ID, Observation{Status: domain.StatusFailed, Reason: domain.ReasonCancelled}, sourceCancel, nil, time.Time{}, nil); err != nil {
		s.log.ErrorContext(ctx, "cannot apply cancellation", "delivery_id", d.ID, "error", err)
		s.retryLater(ctx, d, "retry")
		return
	}
	s.m.Reconciled.WithLabelValues("cancelled").Inc()
}

func (s *Service) failNoCourier(ctx context.Context, d domain.Delivery, source, result string) {
	if _, err := s.finish(ctx, d.ID, Observation{Status: domain.StatusFailed, Reason: domain.ReasonNoCourier}, source, nil, time.Time{}, nil); err != nil {
		s.log.ErrorContext(ctx, "cannot fail delivery", "delivery_id", d.ID, "error", err)
		s.retryLater(ctx, d, "retry")
		return
	}
	s.m.Reconciled.WithLabelValues(result).Inc()
}

// processDispatch — заявка курьерской системе. Идемпотентна по ключу = deliveryId: повтор после таймаута
// или падения между вызовом и записью courier_ref находит ту же заявку.
func (s *Service) processDispatch(ctx context.Context, d domain.Delivery) {
	if s.searchExpired(d) {
		s.failNoCourier(ctx, d, sourceTimout, "timeout")
		return
	}
	if d.PayOnDelivery && d.PaymentURL == "" && s.now().Sub(d.CreatedAt) < s.cfg.LinkWait {
		s.scheduleAfter(ctx, d.ID, time.Second) // ссылка на оплату вот-вот придёт из payment
		return
	}
	pl, err := s.rest.Place(ctx, d.RestaurantID)
	if err != nil {
		s.log.WarnContext(ctx, "restaurant location unavailable", "delivery_id", d.ID, "error", err)
		s.retryLater(ctx, d, "retry")
		return
	}
	req := courier.CreateRequest{Key: d.ID, OrderID: d.OrderID, OrderNumber: d.OrderNumber,
		Pickup:      courier.Place{Point: courier.Point{Lat: pl.Lat, Lon: pl.Lon}, Address: pl.Address},
		Dropoff:     courier.Place{Point: courier.Point{Lat: d.DestLat, Lon: d.DestLon}, Address: d.Address},
		ContactName: d.ContactName, ContactPhone: d.ContactPhone}
	if d.PayOnDelivery {
		req.Payment = &courier.Payment{URL: d.PaymentURL, Amount: d.Amount, Currency: d.Currency}
	}
	start := time.Now()
	st, err := s.cr.Create(ctx, req)
	s.observeCourier("create", start, err)
	if courier.IsRejected(err) {
		s.log.ErrorContext(ctx, "courier system rejected the delivery request", "delivery_id", d.ID, "error", err)
		s.failNoCourier(ctx, d, sourceTimout, "rejected")
		return
	}
	if err != nil {
		s.log.WarnContext(ctx, "courier create failed, will retry", "delivery_id", d.ID, "error", err)
		s.retryLater(ctx, d, "retry")
		return
	}
	pushed := d.PayOnDelivery && d.PaymentURL != ""
	out, err := s.finish(ctx, d.ID, obsFromState(st), sourcePoll, st.Location, st.LocationAt, func(cur *domain.Delivery) {
		cur.CourierRef = st.Ref
		cur.LinkPushed = cur.LinkPushed || (pushed && cur.PaymentURL == d.PaymentURL)
		cur.PollAttempts = 0
	})
	if err != nil {
		s.log.ErrorContext(ctx, "cannot store courier reference", "delivery_id", d.ID, "courier_ref", st.Ref, "error", err)
		s.retryLater(ctx, d, "retry")
		return
	}
	s.m.Reconciled.WithLabelValues("dispatched").Inc()
	if !out.Status.Terminal() {
		s.scheduleAfter(ctx, d.ID, s.cfg.PollEvery)
	}
}

// processPoll — опрос заявки: передать ссылку на оплату, снять заявку по таймауту поиска, обновить статус
// и координаты.
func (s *Service) processPoll(ctx context.Context, d domain.Delivery) {
	if d.PayOnDelivery && d.PaymentURL != "" && !d.LinkPushed {
		start := time.Now()
		err := s.cr.SetPayment(ctx, d.CourierRef, courier.Payment{URL: d.PaymentURL, Amount: d.Amount, Currency: d.Currency})
		s.observeCourier("set_payment", start, err)
		if err == nil {
			url := d.PaymentURL
			_, _ = s.mutate(ctx, d.ID, func(tx pgx.Tx, cur *domain.Delivery) (bool, error) {
				cur.LinkPushed = cur.PaymentURL == url
				return false, s.st.Update(ctx, tx, *cur)
			})
		} else if !courier.IsRejected(err) {
			s.log.WarnContext(ctx, "cannot pass the payment link to the courier system", "delivery_id", d.ID, "error", err)
		}
	}
	if s.searchExpired(d) {
		start := time.Now()
		st, err := s.cr.Cancel(ctx, d.CourierRef)
		s.observeCourier("cancel", start, err)
		switch {
		case err == nil && domain.Status(st.Status) != domain.StatusSearching && domain.Status(st.Status) != domain.StatusFailed && domain.Status(st.Status).Valid():
			// курьера успели назначить, пока мы снимали заявку: заявка в силе
			s.apply(ctx, d, st, sourcePoll)
			return
		case err == nil || errors.Is(err, courier.ErrNotFound) || courier.IsRejected(err):
			s.failNoCourier(ctx, d, sourceTimout, "timeout")
			return
		}
		s.log.WarnContext(ctx, "courier cancel on search timeout failed, will retry", "delivery_id", d.ID, "error", err)
		s.retryLater(ctx, d, "retry")
		return
	}
	start := time.Now()
	st, err := s.cr.Status(ctx, d.CourierRef)
	s.observeCourier("status", start, err)
	if errors.Is(err, courier.ErrNotFound) {
		// курьерская система потеряла заявку: заново (идемпотентно по ключу) на ближайшем проходе
		s.log.WarnContext(ctx, "courier system does not know the delivery: re-dispatching", "delivery_id", d.ID)
		_, _ = s.mutate(ctx, d.ID, func(tx pgx.Tx, cur *domain.Delivery) (bool, error) {
			cur.CourierRef, cur.LinkPushed = "", false
			now := s.now()
			cur.NextPollAt = &now
			return false, s.st.Update(ctx, tx, *cur)
		})
		return
	}
	if err != nil {
		s.log.WarnContext(ctx, "courier status failed", "delivery_id", d.ID, "error", err)
		s.retryLater(ctx, d, "retry")
		return
	}
	s.apply(ctx, d, st, sourcePoll)
}

func (s *Service) apply(ctx context.Context, d domain.Delivery, st courier.State, source string) {
	out, err := s.finish(ctx, d.ID, obsFromState(st), source, st.Location, st.LocationAt, func(cur *domain.Delivery) { cur.PollAttempts = 0 })
	if err != nil {
		s.log.ErrorContext(ctx, "cannot apply courier state", "delivery_id", d.ID, "error", err)
		s.retryLater(ctx, d, "retry")
		return
	}
	s.m.Reconciled.WithLabelValues("polled").Inc()
	if !out.Status.Terminal() {
		s.scheduleAfter(ctx, d.ID, s.cfg.PollEvery)
	}
}

func (s *Service) putLocation(ctx context.Context, orderID string, p *courier.Point, at time.Time) error {
	return s.locs.Put(ctx, orderID, location.Point{Lat: p.Lat, Lon: p.Lon, UpdatedAt: at.UTC()}, s.cfg.LocationTTL)
}

var _ = store.ErrNotFound
