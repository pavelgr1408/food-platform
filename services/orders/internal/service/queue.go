package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/pavelgr1408/food-platform/services/orders/internal/domain"
	"github.com/pavelgr1408/food-platform/services/orders/internal/store"
)

// Порядок очереди ресторана (orders.yaml: listRestaurantOrders, параметр sort).
const (
	SortOldest = "oldest"
	SortNewest = "newest"
)

// activeStatuses — «незавершённые» заказы: очередь по умолчанию (все статусы, кроме closed и cancelled).
var activeStatuses = []domain.Status{
	domain.StatusPendingPayment, domain.StatusAccepted, domain.StatusCooking, domain.StatusReady,
	domain.StatusOnDelivery, domain.StatusDelivered, domain.StatusHandedOver, domain.StatusNotReceived,
}

// QueueInput — запрос очереди ресторана. Limit/Offset == nil — значение по умолчанию.
type QueueInput struct {
	RestaurantID string   // "" — все рестораны, доступные сотруднику
	Statuses     []string // сырые значения ?status=…; пусто — незавершённые
	Fulfillment  string   // "", pickup, delivery
	Sort         string   // "", oldest, newest
	Limit        *int
	Offset       *int
}

// StaffOrderPage — страница очереди.
type StaffOrderPage = OrderPage

// ListRestaurantOrders — listRestaurantOrders (долг №89): заказы ресторанов, к которым привязан сотрудник
// (HQ — любых). Гость → 401, не персонал → 403; недоступный restaurantId или сотрудник без привязки → 403.
func (s *Service) ListRestaurantOrders(ctx context.Context, a Actor, in QueueInput) (OrderPage, error) {
	if err := a.requireStaff(); err != nil {
		return OrderPage{}, err
	}
	limit, offset := DefaultListLimit, 0
	if in.Limit != nil {
		limit = *in.Limit
	}
	if in.Offset != nil {
		offset = *in.Offset
	}
	if limit < 1 || limit > MaxListLimit {
		return OrderPage{}, badParam(fmt.Sprintf("limit must be between 1 and %d", MaxListLimit))
	}
	if offset < 0 {
		return OrderPage{}, badParam("offset must be 0 or greater")
	}
	if len(in.Statuses) > maxListStatuses {
		return OrderPage{}, badParam(fmt.Sprintf("status may be repeated at most %d times", maxListStatuses))
	}
	newest := false
	switch in.Sort {
	case "", SortOldest:
	case SortNewest:
		newest = true
	default:
		return OrderPage{}, badParam("sort must be oldest or newest")
	}
	switch in.Fulfillment {
	case "", string(domain.FulfillmentPickup), string(domain.FulfillmentDelivery):
	default:
		return OrderPage{}, badParam("fulfillment must be pickup or delivery")
	}
	statuses := []domain.Status{}
	seen := map[domain.Status]bool{}
	for _, raw := range in.Statuses {
		st := domain.Status(raw)
		if !st.Valid() {
			return OrderPage{}, badParam("unknown status: " + raw)
		}
		if !seen[st] {
			seen[st] = true
			statuses = append(statuses, st)
		}
	}
	if len(statuses) == 0 {
		statuses = activeStatuses
	}

	var restaurants []string
	if rid := strings.ToLower(strings.TrimSpace(in.RestaurantID)); rid != "" {
		if !validUUID(rid) {
			return OrderPage{}, badParam("restaurantId must be a UUID")
		}
		if !a.Can(rid) {
			return OrderPage{}, errForbidden
		}
		restaurants = []string{rid}
	} else if !a.HQ {
		if len(a.Restaurants) == 0 {
			return OrderPage{}, errForbidden
		}
		restaurants = a.Restaurants
	}
	items, total, err := s.st.ListRestaurantOrders(ctx, s.pool, store.QueueFilter{
		RestaurantIDs: restaurants, Statuses: statuses, Fulfillment: in.Fulfillment,
		NewestFirst: newest, Limit: limit, Offset: offset,
	})
	if err != nil {
		return OrderPage{}, err
	}
	return OrderPage{Items: items, Total: total, Limit: limit, Offset: offset}, nil
}
