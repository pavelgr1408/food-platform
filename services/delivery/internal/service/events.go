package service

import (
	"context"
	"time"

	"github.com/pavelgr1408/food-platform/pkg/kafka"
	"github.com/pavelgr1408/food-platform/services/delivery/internal/domain"
)

// StatusEvent — payload orders.delivery.status (AsyncAPI DeliveryStatus): снимок статуса доставки.
type StatusEvent struct {
	OrderID    string     `json:"orderId"`
	DeliveryID string     `json:"deliveryId"`
	CourierID  string     `json:"courierId,omitempty"`
	Status     string     `json:"status"`
	Reason     string     `json:"reason,omitempty"`
	EtaAt      *time.Time `json:"etaAt,omitempty"`
	UpdatedAt  time.Time  `json:"updatedAt"`
}

func statusEvent(d domain.Delivery) StatusEvent {
	ev := StatusEvent{OrderID: d.OrderID, DeliveryID: d.ID, CourierID: d.CourierID, Status: string(d.Status),
		Reason: string(d.FailReason), UpdatedAt: d.UpdatedAt.UTC()}
	if d.EtaAt != nil && !d.Status.Terminal() {
		t := d.EtaAt.UTC()
		ev.EtaAt = &t
	}
	return ev
}

// enqueueStatus кладёт orders.delivery.status в outbox (в той же транзакции, что и смена статуса).
func enqueueStatus(ctx context.Context, tx kafka.Execer, d domain.Delivery) error {
	ev, err := kafka.NewEvent(ctx, TopicOut, TypeOut, d.OrderID, statusEvent(d))
	if err != nil {
		return err
	}
	return kafka.Enqueue(ctx, tx, ev)
}
