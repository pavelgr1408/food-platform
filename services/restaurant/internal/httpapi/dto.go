package httpapi

import (
	"github.com/pavelgr1408/food-platform/services/restaurant/internal/domain"
	"github.com/pavelgr1408/food-platform/services/restaurant/internal/service"
)

type geoPoint struct {
	Lat float64 `json:"lat"`
	Lon float64 `json:"lon"`
}

type restaurantSummary struct {
	ID                string   `json:"id"`
	Name              string   `json:"name"`
	Address           string   `json:"address"`
	City              string   `json:"city"`
	Location          geoPoint `json:"location"`
	DistanceMeters    *int     `json:"distanceMeters,omitempty"`
	DeliveryAvailable bool     `json:"deliveryAvailable"`
	OpenNow           bool     `json:"openNow"`
}

type workingHours struct {
	DayOfWeek int    `json:"dayOfWeek"`
	OpensAt   string `json:"opensAt"`
	ClosesAt  string `json:"closesAt"`
}

type scheduleException struct {
	Date     string `json:"date"`
	Closed   bool   `json:"closed"`
	OpensAt  string `json:"opensAt,omitempty"`
	ClosesAt string `json:"closesAt,omitempty"`
	Note     string `json:"note,omitempty"`
}

type restaurantDetail struct {
	restaurantSummary
	Timezone             string              `json:"timezone"`
	DeliveryEnabled      bool                `json:"deliveryEnabled"`
	DeliveryRadiusMeters int                 `json:"deliveryRadiusMeters"`
	WorkingHours         []workingHours      `json:"workingHours"`
	ScheduleExceptions   []scheduleException `json:"scheduleExceptions"`
}

type restaurantPage struct {
	Items  []restaurantSummary `json:"items"`
	Total  int                 `json:"total"`
	Limit  int                 `json:"limit"`
	Offset int                 `json:"offset"`
	Center *geoPoint           `json:"center,omitempty"`
	Hint   string              `json:"hint,omitempty"`
}

func summary(it service.Item) restaurantSummary {
	return restaurantSummary{
		ID: it.ID, Name: it.Name, Address: it.Address, City: it.City,
		Location:       geoPoint{Lat: it.Location.Lat, Lon: it.Location.Lon},
		DistanceMeters: it.DistanceM, DeliveryAvailable: it.DeliveryAvailable, OpenNow: it.OpenNow,
	}
}

func detail(it service.Item) restaurantDetail {
	d := restaurantDetail{
		restaurantSummary: summary(it), Timezone: it.Timezone,
		DeliveryEnabled: it.DeliveryEnabled, DeliveryRadiusMeters: it.DeliveryRadiusM,
		WorkingHours: []workingHours{}, ScheduleExceptions: []scheduleException{},
	}
	for _, h := range it.Schedule.Hours {
		d.WorkingHours = append(d.WorkingHours, workingHours{h.DayOfWeek, domain.FormatMinutes(h.Opens), domain.FormatMinutes(h.Closes)})
	}
	for _, e := range it.Schedule.Exceptions {
		x := scheduleException{Date: e.Date, Closed: e.Closed, Note: e.Note}
		if !e.Closed {
			x.OpensAt, x.ClosesAt = domain.FormatMinutes(e.Opens), domain.FormatMinutes(e.Closes)
		}
		d.ScheduleExceptions = append(d.ScheduleExceptions, x)
	}
	return d
}
