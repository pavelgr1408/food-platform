package service

import (
	"fmt"
	"math"

	"github.com/pavelgr1408/food-platform/pkg/geo"
	"github.com/pavelgr1408/food-platform/services/orders/internal/clients"
	"github.com/pavelgr1408/food-platform/services/orders/internal/domain"
)

// OutOfZoneError — адрес доставки дальше радиуса доставки ресторана: 422 ADDRESS_OUT_OF_ZONE с расстоянием,
// радиусом и подсказкой pickup (OutOfZoneProblem в orders.yaml).
type OutOfZoneError struct {
	DistanceM int
	RadiusM   int
}

func (e *OutOfZoneError) Error() string {
	return fmt.Sprintf("ADDRESS_OUT_OF_ZONE: the address is %d m away, delivery radius is %d m", e.DistanceM, e.RadiusM)
}

// checkZone проверяет координаты адреса доставки и попадание в радиус доставки ресторана (S7, долг №10).
//
//	нет координат                      → 422 ADDRESS_LOCATION_REQUIRED
//	радиус не задан (<= 0) или у ресторана нет координат → проверка радиуса пропускается (сравнивать не с чем)
//	расстояние по прямой > радиуса     → 422 ADDRESS_OUT_OF_ZONE (distanceMeters, radiusMeters, hint=pickup)
func checkZone(info clients.RestaurantInfo, addr domain.Address) error {
	if addr.Lat == nil || addr.Lon == nil || !geo.Valid(*addr.Lat, *addr.Lon) {
		return unprocessable("ADDRESS_LOCATION_REQUIRED", "address.location (lat, lon) is required for delivery")
	}
	if info.DeliveryRadiusM <= 0 || !info.HasLocation {
		return nil
	}
	d := geo.DistanceMeters(info.Lat, info.Lon, *addr.Lat, *addr.Lon)
	if d > float64(info.DeliveryRadiusM) {
		return &OutOfZoneError{DistanceM: int(math.Round(d)), RadiusM: info.DeliveryRadiusM}
	}
	return nil
}
