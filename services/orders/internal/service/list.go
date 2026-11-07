package service

import (
	"context"
	"fmt"

	"github.com/pavelgr1408/food-platform/services/orders/internal/domain"
)

// Границы страницы списка заказов (orders.yaml: Limit/Offset).
const (
	DefaultListLimit = 20
	MaxListLimit     = 50
	maxListStatuses  = 10
)

// ListOrdersInput — запрос списка своих заказов. Limit/Offset == nil — значение по умолчанию.
type ListOrdersInput struct {
	Statuses []string // сырые значения ?status=…; пусто — любые
	Limit    *int
	Offset   *int
}

// OrderPage — страница заказов пользователя.
type OrderPage struct {
	Items         []domain.Order
	Total         int
	Limit, Offset int
}

// ListOrders возвращает заказы пользователя (долг №80): только его собственные, от новых к старым.
// Гостевые заказы и заказы по факсу владельца не имеют, поэтому в выдачу попасть не могут. Пустой userID
// (гость) сюда доходить не должен — это отсекает HTTP-слой; здесь он тоже даёт 401, а не «все заказы без владельца».
func (s *Service) ListOrders(ctx context.Context, userID string, in ListOrdersInput) (OrderPage, error) {
	if userID == "" {
		return OrderPage{}, errUnauthorized
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
	var statuses []domain.Status
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
	items, total, err := s.st.ListOwnerOrders(ctx, s.pool, userID, statuses, limit, offset)
	if err != nil {
		return OrderPage{}, err
	}
	return OrderPage{Items: items, Total: total, Limit: limit, Offset: offset}, nil
}
