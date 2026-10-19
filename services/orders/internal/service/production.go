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
	"github.com/pavelgr1408/food-platform/services/orders/internal/store"
)

// Топики системы производства (продюсер — внешняя система, в dev — mock-externals/production).
const (
	TopicDishStatus = "orders.dish.status"
	TopicLeadTime   = "production.lead.time"
)

// Основания и актёр переходов, вызванных производством.
const (
	ReasonProduction = "production"
	actorProduction  = "production"
)

type dishStatusPayload struct {
	OrderID string `json:"orderId"`
	Dishes  []struct {
		DishID string `json:"dishId"`
		Status string `json:"status"`
	} `json:"dishes"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// HandleDishStatus — kafka.TxHandler консьюмера orders.dish.status (внутри inbox-транзакции).
//
// Событие — снимок статусов всех блюд заказа. Статус заказа выводится domain.AggregateDishes:
// все блюда queued → accepted (без изменений), есть cooking/ready → cooking, все ready → ready.
// Из accepted сразу в ready заказ проходит через cooking (две записи журнала и два orders.status).
//
// Защиты: снимок старше уже применённого (production_updated_at) игнорируется; статус не откатывается
// (событие по заказу, который уже ready и дальше, игнорируется); события по pending_payment и cancelled
// игнорируются — производство не должно было получить такой заказ (метрика `ignored`, предупреждение в лог).
// Неизвестный заказ — ошибка (повторы и DLQ): orders.state публикуется после коммита заказа, поэтому
// состояние гонки невозможно, а потеря статуса блюд — инцидент.
func (s *Service) HandleDishStatus(ctx context.Context, tx pgx.Tx, ev kafka.Event) error {
	var p dishStatusPayload
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		return fmt.Errorf("orders.dish.status: %w", err)
	}
	if _, err := uuid.Parse(p.OrderID); err != nil || p.UpdatedAt.IsZero() || len(p.Dishes) == 0 {
		return fmt.Errorf("orders.dish.status: invalid payload (orderId=%q, dishes=%d)", p.OrderID, len(p.Dishes))
	}
	snap := make([]domain.Dish, 0, len(p.Dishes))
	for _, d := range p.Dishes {
		ds := domain.DishStatus(d.Status)
		if d.DishID == "" || !ds.Valid() {
			return fmt.Errorf("orders.dish.status: invalid dish %q status %q", d.DishID, d.Status)
		}
		snap = append(snap, domain.Dish{ID: d.DishID, Status: ds})
	}

	o, err := s.st.GetOrder(ctx, tx, p.OrderID, true)
	if err != nil {
		return fmt.Errorf("orders.dish.status: order %s: %w", p.OrderID, err)
	}
	at := p.UpdatedAt.UTC()
	if o.ProductionUpdatedAt != nil && at.Before(*o.ProductionUpdatedAt) {
		s.productionEvent("dish_status", "stale")
		return nil
	}
	switch o.Status {
	case domain.StatusAccepted, domain.StatusCooking:
	case domain.StatusPendingPayment, domain.StatusCancelled:
		s.productionEvent("dish_status", "ignored_status")
		s.log.WarnContext(ctx, "dish status for an order that must not be in production",
			"order_id", o.ID, "order_status", o.Status)
		return nil
	default: // ready и дальше: статус не откатывается
		s.productionEvent("dish_status", "ignored_status")
		return nil
	}

	ids := make([]string, 0, len(o.Items))
	for _, l := range o.Items {
		ids = append(ids, l.MenuItemID)
	}
	target := domain.AggregateDishes(ids, snap)
	var steps []domain.Status
	switch {
	case o.Status == domain.StatusAccepted && target == domain.StatusCooking:
		steps = []domain.Status{domain.StatusCooking}
	case o.Status == domain.StatusAccepted && target == domain.StatusReady:
		steps = []domain.Status{domain.StatusCooking, domain.StatusReady}
	case o.Status == domain.StatusCooking && target == domain.StatusReady:
		steps = []domain.Status{domain.StatusReady}
	}
	for _, to := range steps {
		next, changed, err := s.transitionTx(ctx, tx, o.ID, to, ReasonProduction, actorProduction)
		if err != nil {
			return err
		}
		if changed {
			s.noteTransition(ctx, next, to, ReasonProduction, actorProduction)
		}
	}
	if err := s.st.SetProductionUpdatedAt(ctx, tx, o.ID, at); err != nil {
		return err
	}
	s.productionEvent("dish_status", "applied")
	return nil
}

type leadTimePayload struct {
	RestaurantID    string    `json:"restaurantId"`
	CurrentLoad     float64   `json:"currentLoad"`
	LeadTimeMinutes *int      `json:"leadTimeMinutes"`
	MeasuredAt      time.Time `json:"measuredAt"`
}

// HandleLeadTime — kafka.TxHandler консьюмера production.lead.time: хранит последнее значение на ресторан
// (измерение старше сохранённого игнорируется). ETA читает реплику локально.
func (s *Service) HandleLeadTime(ctx context.Context, tx pgx.Tx, ev kafka.Event) error {
	var p leadTimePayload
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		return fmt.Errorf("production.lead.time: %w", err)
	}
	if _, err := uuid.Parse(p.RestaurantID); err != nil || p.LeadTimeMinutes == nil || *p.LeadTimeMinutes < 0 ||
		*p.LeadTimeMinutes > 24*60 || p.MeasuredAt.IsZero() {
		return fmt.Errorf("production.lead.time: invalid payload (restaurantId=%q)", p.RestaurantID)
	}
	applied, err := s.st.UpsertLeadTime(ctx, tx, store.LeadTime{RestaurantID: p.RestaurantID, Minutes: *p.LeadTimeMinutes,
		Load: p.CurrentLoad, MeasuredAt: p.MeasuredAt.UTC()}, s.now())
	if err != nil {
		return err
	}
	if applied {
		s.productionEvent("lead_time", "applied")
	} else {
		s.productionEvent("lead_time", "stale")
	}
	return nil
}

func (s *Service) productionEvent(kind, result string) {
	if s.m != nil {
		s.m.ProductionEvents.WithLabelValues(kind, result).Inc()
	}
}

// HandleProduction — обработчик группы консьюмеров производства: разводит события по топику.
func (s *Service) HandleProduction(ctx context.Context, tx pgx.Tx, ev kafka.Event) error {
	switch ev.Topic {
	case TopicDishStatus:
		return s.HandleDishStatus(ctx, tx, ev)
	case TopicLeadTime:
		return s.HandleLeadTime(ctx, tx, ev)
	}
	return fmt.Errorf("orders production consumer: unexpected topic %q", ev.Topic)
}
