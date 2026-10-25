package service

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/pavelgr1408/food-platform/services/delivery/internal/domain"
	"github.com/pavelgr1408/food-platform/services/delivery/internal/location"
	"github.com/pavelgr1408/food-platform/services/delivery/internal/store"
)

// Actor — вызывающий (из токена). Нулевое значение — гость.
type Actor struct {
	UserID      string
	Staff       bool // STAFF | FRANCHISE_OWNER | HQ_STAFF
	HQ          bool // HQ_STAFF: видит все рестораны
	Restaurants []string
}

// canStaff — сотрудник, которому разрешён ресторан.
func (a Actor) canStaff(restaurantID string) bool {
	if !a.Staff {
		return false
	}
	if a.HQ {
		return true
	}
	for _, r := range a.Restaurants {
		if r == restaurantID {
			return true
		}
	}
	return false
}

// visible — видит ли актор доставку: гостевой заказ — по знанию UUID (как в orders и payment), заказ
// пользователя — только владелец и сотрудники его ресторана. Иначе — как будто доставки нет.
func (a Actor) visible(d domain.Delivery) bool {
	if d.OwnerUserID == "" {
		return true
	}
	return (a.UserID != "" && a.UserID == d.OwnerUserID) || a.canStaff(d.RestaurantID)
}

func (s *Service) load(ctx context.Context, a Actor, orderID string) (domain.Delivery, error) {
	if _, err := uuid.Parse(orderID); err != nil {
		return domain.Delivery{}, errNotFound
	}
	d, err := s.st.GetByOrder(ctx, s.pool, orderID, false)
	if errors.Is(err, store.ErrNotFound) || (err == nil && !a.visible(d)) {
		return domain.Delivery{}, errNotFound
	}
	return d, err
}

// Get — getDeliveryInfo. Контакты курьера отдаются только пока доставка активна (минимизация PII).
func (s *Service) Get(ctx context.Context, a Actor, orderID string) (domain.Delivery, error) {
	d, err := s.load(ctx, a, orderID)
	if err != nil {
		return d, err
	}
	if !d.Status.CourierVisible() {
		d.CourierID, d.CourierName, d.CourierPhone = "", "", ""
	}
	return d, nil
}

// Location — getCourierLocation: последняя точка из Redis, пока курьер в пути.
func (s *Service) Location(ctx context.Context, a Actor, orderID string) (location.Point, error) {
	d, err := s.load(ctx, a, orderID)
	if err != nil {
		return location.Point{}, err
	}
	if !d.Status.Active() || s.locs == nil {
		return location.Point{}, errNoLocation
	}
	p, err := s.locs.Get(ctx, orderID)
	switch {
	case errors.Is(err, location.ErrNotFound):
		return location.Point{}, errNoLocation
	case err != nil:
		s.log.WarnContext(ctx, "location store read failed", "order_id", orderID, "error", err)
		return location.Point{}, errLocationsDown
	}
	return p, nil
}
