package service

import (
	"context"
	"sync"
	"time"

	"github.com/pavelgr1408/food-platform/services/notification/internal/domain"
	"github.com/pavelgr1408/food-platform/services/notification/internal/push"
	"github.com/pavelgr1408/food-platform/services/notification/internal/store"
)

// RunSender запускает цикл отправки до отмены ctx: каждые interval берёт созревшие уведомления.
func (s *Service) RunSender(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 500 * time.Millisecond
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if _, err := s.Tick(ctx); err != nil && ctx.Err() == nil {
			s.log.ErrorContext(ctx, "notification sender tick failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Tick выполняет один проход: закрывает устаревшие, берёт созревшие в аренду и отправляет (до Workers
// параллельно, раундами); раз в минуту удаляет старые закрытые. Возвращает число обработанных уведомлений.
func (s *Service) Tick(ctx context.Context) (int, error) {
	now := s.now().UTC()
	if n, err := s.st.ExpirePending(ctx, s.pool, now.Add(-s.cfg.MaxEventAge), now); err != nil {
		return 0, err
	} else if n > 0 {
		s.m.Expired.Add(float64(n))
		s.log.WarnContext(ctx, "stale notifications skipped", "count", n)
	}
	if now.Sub(s.lastPurge) > time.Minute {
		s.lastPurge = now
		if _, err := s.st.PurgeDone(ctx, s.pool, now.Add(-s.cfg.Retention)); err != nil {
			s.log.ErrorContext(ctx, "purge notifications failed", "error", err)
		}
	}
	// Берём по Workers уведомлений за раунд: худший случай раунда — один вызов провайдера (SendTimeout),
	// он короче аренды, поэтому взятое в работу не может «протухнуть» и уйти вторым push.
	total := 0
	for total < s.cfg.Batch && ctx.Err() == nil {
		due, err := s.st.Claim(ctx, s.pool, s.now().UTC(), s.cfg.Lease, s.cfg.Workers)
		if err != nil {
			return total, err
		}
		var wg sync.WaitGroup
		for _, d := range due {
			wg.Add(1)
			go func(d store.Due) {
				defer wg.Done()
				s.deliver(ctx, d)
			}(d)
		}
		wg.Wait()
		total += len(due)
		if len(due) < s.cfg.Workers {
			break
		}
	}
	return total, nil
}

// deliver отправляет одно уведомление и фиксирует исход. Вызов провайдера — вне транзакции БД.
func (s *Service) deliver(ctx context.Context, d store.Due) {
	// исход записываем даже при остановке сервиса: иначе после рестарта получим дубль push
	done, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()

	if !d.PermissionsGranted {
		s.m.Push.WithLabelValues(string(d.Platform), "skipped").Inc()
		s.record(ctx, "mark skipped", s.st.MarkFinal(done, s.pool, d.ID, domain.StatusSkipped, "permissions_revoked", s.now().UTC()))
		return
	}
	callCtx, cancelCall := context.WithTimeout(ctx, s.cfg.SendTimeout)
	started := time.Now()
	res, err := s.sender.Send(callCtx, push.Message{ID: d.ID, Platform: d.Platform, Token: d.Token, Title: d.Title, Body: d.Body,
		Priority: d.Priority, OrderID: d.OrderID, OrderNumber: d.OrderNumber, Type: d.Type})
	cancelCall()
	plat := string(d.Platform)
	s.m.Duration.WithLabelValues(plat).Observe(time.Since(started).Seconds())
	s.m.Push.WithLabelValues(plat, res.String()).Inc()
	reason := ""
	if err != nil {
		reason = err.Error()
	}
	now := s.now().UTC()
	switch res {
	case push.Sent:
		s.record(ctx, "mark sent", s.st.MarkSent(done, s.pool, d.ID, now))
		s.log.InfoContext(ctx, "push sent", "notification_id", d.ID, "order_id", d.OrderID, "type", d.Type, "platform", plat, "attempt", d.Attempts)
	case push.InvalidToken:
		// провайдер: устройства нет. Токен удаляем (уведомления токена уйдут каскадом) — шлём только живым.
		if _, derr := s.st.DeleteTokenByID(done, s.pool, d.TokenID); derr != nil {
			s.record(ctx, "delete invalid token", derr)
			return
		}
		s.m.Revoked.Inc()
		s.log.InfoContext(ctx, "push token revoked by provider", "notification_id", d.ID, "token_id", d.TokenID, "platform", plat)
	case push.Retry:
		if d.Attempts >= s.cfg.MaxAttempts {
			s.record(ctx, "mark failed", s.st.MarkFinal(done, s.pool, d.ID, domain.StatusFailed, "max attempts: "+reason, now))
			s.log.ErrorContext(ctx, "push failed after retries", "notification_id", d.ID, "order_id", d.OrderID, "platform", plat, "attempts", d.Attempts, "error", reason)
			return
		}
		s.record(ctx, "schedule retry", s.st.Retry(done, s.pool, d.ID, now.Add(s.backoff(d.Attempts)), reason, now))
		s.log.WarnContext(ctx, "push attempt failed, will retry", "notification_id", d.ID, "platform", plat, "attempt", d.Attempts, "error", reason)
	default:
		s.record(ctx, "mark failed", s.st.MarkFinal(done, s.pool, d.ID, domain.StatusFailed, reason, now))
		s.log.ErrorContext(ctx, "push rejected by provider", "notification_id", d.ID, "order_id", d.OrderID, "platform", plat, "error", reason)
	}
}

func (s *Service) record(ctx context.Context, what string, err error) {
	if err != nil {
		s.log.ErrorContext(ctx, "notification state update failed", "op", what, "error", err)
	}
}

// backoff — пауза перед следующей попыткой после attempt-й: base, 2·base, 4·base … не больше BackoffMax.
func (s *Service) backoff(attempt int) time.Duration {
	d := s.cfg.BackoffBase
	for i := 1; i < attempt && d < s.cfg.BackoffMax; i++ {
		d *= 2
	}
	if d > s.cfg.BackoffMax {
		d = s.cfg.BackoffMax
	}
	return d
}
