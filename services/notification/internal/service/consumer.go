package service

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/pavelgr1408/food-platform/pkg/kafka"
	"github.com/pavelgr1408/food-platform/services/notification/internal/domain"
)

// orderStatusPayload — нужное нам подмножество orders.status (AsyncAPI OrderStatus).
type orderStatusPayload struct {
	OrderID     string    `json:"orderId"`
	OrderNumber string    `json:"orderNumber"`
	Status      string    `json:"status"`
	Reason      string    `json:"reason"`
	UserID      string    `json:"userId"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// HandleEvent — kafka.TxHandler консьюмера (внутри inbox-транзакции). Ошибка разбора ведёт к повторам и
// затем в orders.status.dlq; события, которые не порождают уведомлений, пропускаются молча. По сети
// обработчик ничего не вызывает: он только ставит строки в очередь отправки.
func (s *Service) HandleEvent(ctx context.Context, tx pgx.Tx, ev kafka.Event) error {
	if ev.Topic != TopicStatus {
		return fmt.Errorf("unexpected topic %q", ev.Topic)
	}
	var p orderStatusPayload
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		return fmt.Errorf("orders.status: %w", err)
	}
	if _, err := uuid.Parse(p.OrderID); err != nil || p.Status == "" || p.UpdatedAt.IsZero() {
		return fmt.Errorf("orders.status: invalid payload (orderId=%q status=%q)", p.OrderID, p.Status)
	}
	typ, ok := domain.TypeFor(p.Status)
	if !ok {
		s.m.Events.WithLabelValues("ignored").Inc()
		return nil
	}
	if p.UserID == "" {
		// гость или заказ по факсу: токенов нет, push не отправляется (осознанное ограничение)
		s.m.Events.WithLabelValues("no_recipient").Inc()
		return nil
	}
	now := s.now().UTC()
	if now.Sub(p.UpdatedAt) > s.cfg.MaxEventAge {
		s.m.Events.WithLabelValues("stale").Inc()
		return nil
	}
	tokens, err := s.st.DeliverableTokens(ctx, tx, p.UserID)
	if err != nil {
		return err
	}
	if len(tokens) == 0 {
		s.m.Events.WithLabelValues("no_devices").Inc()
		return nil
	}
	queued := 0
	for _, t := range tokens {
		text := domain.Render(typ, p.Reason, t.Locale, p.OrderNumber)
		n := domain.Notification{ID: uuid.NewString(), OrderID: p.OrderID, OrderNumber: p.OrderNumber, Type: typ, Reason: p.Reason,
			Priority: domain.PriorityOf(typ), Title: text.Title, Body: text.Body, TokenID: t.ID, EventAt: p.UpdatedAt.UTC()}
		ins, err := s.st.InsertNotification(ctx, tx, n, now)
		if err != nil {
			return err
		}
		if ins {
			queued++
		}
	}
	if queued == 0 {
		s.m.Events.WithLabelValues("duplicate").Inc()
		return nil
	}
	s.m.Events.WithLabelValues("queued").Inc()
	s.m.Queued.Add(float64(queued))
	s.log.InfoContext(ctx, "notifications queued", "order_id", p.OrderID, "status", p.Status, "type", typ, "devices", queued)
	return nil
}
