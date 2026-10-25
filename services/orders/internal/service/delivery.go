package service

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/pavelgr1408/food-platform/pkg/kafka"
	"github.com/pavelgr1408/food-platform/services/orders/internal/domain"
)

// TopicDeliveryStatus — orders.delivery.status (продюсер — delivery-service).
const TopicDeliveryStatus = "orders.delivery.status"

// Основания переходов, вызванных доставкой (OrderStatus.reason в AsyncAPI) и актёр.
const (
	ReasonCourierPickedUp = "courier_picked_up"
	ReasonDelivered       = "delivered"
	ReasonUndelivered     = "undelivered"
	actorDelivery         = "delivery"
)

type deliveryStatusPayload struct {
	OrderID    string     `json:"orderId"`
	DeliveryID string     `json:"deliveryId"`
	Status     string     `json:"status"`
	Reason     string     `json:"reason"`
	EtaAt      *time.Time `json:"etaAt"`
	UpdatedAt  time.Time  `json:"updatedAt"`
}

// HandleDeliveryStatus — kafka.TxHandler консьюмера orders.delivery.status (внутри inbox-транзакции).
//
// Событие — снимок статуса доставки. Снимок сохраняется у заказа, если он новее сохранённого
// (delivery_updated_at); затем advanceDelivery двигает заказ:
//
//	picked_up | en_route → ready → on_delivery   (courier_picked_up)
//	delivered            → on_delivery → delivered (+ closed, если заказ уже оплачен)
//	failed/undelivered   → on_delivery → not_received
//	failed/no_courier|cancelled, searching, assigned → заказ не меняется (E2: клиенту предлагается самовывоз)
//
// Курьер ищется параллельно с приготовлением, поэтому заказ может ещё не быть ready: тогда переход откладывается
// до готовности (HandleDishStatus вызывает advanceDelivery после ready).
func (s *Service) HandleDeliveryStatus(ctx context.Context, tx pgx.Tx, ev kafka.Event) error {
	var p deliveryStatusPayload
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		return fmt.Errorf("orders.delivery.status: %w", err)
	}
	st := domain.DeliveryStatus(p.Status)
	if _, err := uuid.Parse(p.OrderID); err != nil || !st.Valid() || p.UpdatedAt.IsZero() {
		return fmt.Errorf("orders.delivery.status: invalid payload (orderId=%q status=%q)", p.OrderID, p.Status)
	}
	switch p.Reason {
	case "", domain.FailNoCourier, domain.FailUndelivered, domain.FailCancelled:
	default:
		return fmt.Errorf("orders.delivery.status: unknown reason %q", p.Reason)
	}
	o, err := s.st.GetOrder(ctx, tx, p.OrderID, true)
	if err != nil {
		return fmt.Errorf("orders.delivery.status: order %s: %w", p.OrderID, err) // заказ создаётся раньше, чем delivery о нём узнаёт
	}
	if o.Fulfillment != domain.FulfillmentDelivery {
		s.deliveryEvent("ignored_fulfillment")
		s.log.WarnContext(ctx, "delivery status for a pickup order", "order_id", o.ID)
		return nil
	}
	snap := domain.DeliverySnapshot{Status: st, FailReason: p.Reason, EtaAt: p.EtaAt, UpdatedAt: p.UpdatedAt.UTC()}
	if snap.EtaAt != nil {
		t := snap.EtaAt.UTC()
		snap.EtaAt = &t
	}
	applied, err := s.st.SetDelivery(ctx, tx, o.ID, snap)
	if err != nil {
		return err
	}
	if !applied {
		s.deliveryEvent("stale")
		return nil
	}
	s.deliveryEvent("applied")
	return s.advanceDelivery(ctx, tx, o.ID)
}

// advanceDelivery приводит статус заказа в соответствие со снимком доставки (см. HandleDeliveryStatus).
// Идемпотентна: вызывается и при событии доставки, и при готовности заказа. Вызывать внутри транзакции.
func (s *Service) advanceDelivery(ctx context.Context, tx pgx.Tx, orderID string) error {
	o, err := s.st.GetOrder(ctx, tx, orderID, true)
	if err != nil {
		return err
	}
	if o.Delivery == nil || o.Fulfillment != domain.FulfillmentDelivery {
		return nil
	}
	var final domain.Status // куда должен прийти заказ; пусто — только on_delivery или ничего
	reason := ReasonCourierPickedUp
	switch o.Delivery.Status {
	case domain.DeliveryPickedUp, domain.DeliveryEnRoute:
		final = domain.StatusOnDelivery
	case domain.DeliveryDelivered:
		final, reason = domain.StatusDelivered, ReasonDelivered
	case domain.DeliveryFailed:
		if o.Delivery.FailReason != domain.FailUndelivered {
			return nil // no_courier — заказ остаётся ready (E2), cancelled — заказ уже отменён
		}
		final, reason = domain.StatusNotReceived, ReasonUndelivered
	default:
		return nil // searching, assigned
	}
	if o.Status != domain.StatusReady && o.Status != domain.StatusOnDelivery {
		return nil // ещё готовится (отложено до ready) либо заказ уже вне доставки
	}
	step := func(to domain.Status, why string) error {
		next, changed, err := s.transitionTx(ctx, tx, o.ID, to, why, actorDelivery)
		if err != nil {
			return err
		}
		if changed {
			s.noteTransition(ctx, next, to, why, actorDelivery)
			o = next
		}
		return nil
	}
	if o.Status == domain.StatusReady {
		if err := step(domain.StatusOnDelivery, ReasonCourierPickedUp); err != nil {
			return err
		}
	}
	if final != domain.StatusOnDelivery {
		if err := step(final, reason); err != nil {
			return err
		}
	}
	return s.closeIfSettled(ctx, tx, o.ID)
}

// closeIfSettled закрывает доставленный заказ, как только он оплачен (delivered → closed). При оплате курьеру
// деньги подтверждает payment-service позже вручения, поэтому вызывается и из HandlePaymentStatus.
func (s *Service) closeIfSettled(ctx context.Context, tx pgx.Tx, orderID string) error {
	o, err := s.st.GetOrder(ctx, tx, orderID, true)
	if err != nil {
		return err
	}
	if o.Status != domain.StatusDelivered || o.PaymentStatus != domain.PaymentPaid {
		return nil
	}
	next, changed, err := s.transitionTx(ctx, tx, o.ID, domain.StatusClosed, ReasonDelivered, actorDelivery)
	if err != nil {
		return err
	}
	if changed {
		s.noteTransition(ctx, next, domain.StatusClosed, ReasonDelivered, actorDelivery)
	}
	return nil
}

func (s *Service) deliveryEvent(result string) {
	if s.m != nil {
		s.m.DeliveryEvents.WithLabelValues(result).Inc()
	}
}
