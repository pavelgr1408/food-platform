package httpapi

import (
	"time"

	"github.com/pavelgr1408/food-platform/services/delivery/internal/domain"
	"github.com/pavelgr1408/food-platform/services/delivery/internal/location"
	"github.com/pavelgr1408/food-platform/services/delivery/internal/service"
)

type courierDTO struct {
	Name  string `json:"name,omitempty"`
	Phone string `json:"phone,omitempty"`
}

type infoDTO struct {
	OrderID       string      `json:"orderId"`
	DeliveryID    string      `json:"deliveryId,omitempty"`
	Status        string      `json:"status"`
	FailReason    string      `json:"failReason,omitempty"`
	Hint          string      `json:"hint,omitempty"`
	Courier       *courierDTO `json:"courier,omitempty"`
	EtaAt         *time.Time  `json:"etaAt,omitempty"`
	PayOnDelivery bool        `json:"payOnDelivery,omitempty"`
	UpdatedAt     time.Time   `json:"updatedAt"`
}

func toInfo(d domain.Delivery) infoDTO {
	out := infoDTO{OrderID: d.OrderID, DeliveryID: d.ID, Status: string(d.Status), FailReason: string(d.FailReason),
		Hint: d.Hint(), EtaAt: d.EtaAt, PayOnDelivery: d.PayOnDelivery, UpdatedAt: d.UpdatedAt}
	if d.CourierName != "" || d.CourierPhone != "" {
		out.Courier = &courierDTO{Name: d.CourierName, Phone: d.CourierPhone}
	}
	if d.Status.Terminal() { // после завершения ETA не нужен
		out.EtaAt = nil
	}
	return out
}

type geoDTO struct {
	Lat float64 `json:"lat"`
	Lon float64 `json:"lon"`
}

type locationDTO struct {
	Location  geoDTO    `json:"location"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func toLocation(p location.Point) locationDTO {
	return locationDTO{Location: geoDTO{Lat: p.Lat, Lon: p.Lon}, UpdatedAt: p.UpdatedAt}
}

// callbackRequest — DeliveryStatusCallback. Неизвестные поля не отвергаются: курьерская система может их добавлять.
type callbackRequest struct {
	EventID    string      `json:"eventId"`
	OrderID    string      `json:"orderId"`
	DeliveryID string      `json:"deliveryId"`
	CourierID  string      `json:"courierId"`
	Courier    *courierDTO `json:"courier"`
	Status     string      `json:"status"`
	Reason     string      `json:"reason"`
	EtaAt      *time.Time  `json:"etaAt"`
	Paid       bool        `json:"paid"`
	Timestamp  time.Time   `json:"timestamp"`
}

func (c callbackRequest) domain() service.Callback {
	out := service.Callback{EventID: c.EventID, OrderID: c.OrderID, Status: c.Status, Reason: c.Reason,
		CourierID: c.CourierID, EtaAt: c.EtaAt, Paid: c.Paid, Timestamp: c.Timestamp}
	if c.Courier != nil {
		out.CourierName, out.CourierPhone = c.Courier.Name, c.Courier.Phone
	}
	return out
}
