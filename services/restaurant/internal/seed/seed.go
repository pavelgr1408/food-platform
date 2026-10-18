// Package seed — стартовые данные ресторанов (dev/demo). CRUD-API у сервиса нет (контракт S1),
// поэтому справочник загружается при старте идемпотентным upsert'ом (SEED_ON_START=true).
package seed

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/pavelgr1408/food-platform/services/restaurant/internal/domain"
)

//go:embed restaurants.json
var restaurantsJSON []byte

type fileRestaurant struct {
	ID                   string  `json:"id"`
	Name                 string  `json:"name"`
	City                 string  `json:"city"`
	Address              string  `json:"address"`
	Lat                  float64 `json:"lat"`
	Lon                  float64 `json:"lon"`
	Timezone             string  `json:"timezone"`
	DeliveryEnabled      bool    `json:"deliveryEnabled"`
	DeliveryRadiusMeters int     `json:"deliveryRadiusMeters"`
	Active               bool    `json:"active"`
	Hours                []struct {
		Days     []int  `json:"days"`
		OpensAt  string `json:"opensAt"`
		ClosesAt string `json:"closesAt"`
	} `json:"hours"`
	Exceptions []struct {
		Date     string `json:"date"`
		Closed   bool   `json:"closed"`
		OpensAt  string `json:"opensAt"`
		ClosesAt string `json:"closesAt"`
		Note     string `json:"note"`
	} `json:"exceptions"`
}

// Restaurants разбирает и валидирует встроенный справочник.
func Restaurants() ([]domain.Restaurant, error) { return Parse(restaurantsJSON) }

// Parse разбирает JSON справочника (экспортирована для тестов).
func Parse(data []byte) ([]domain.Restaurant, error) {
	var in []fileRestaurant
	if err := json.Unmarshal(data, &in); err != nil {
		return nil, fmt.Errorf("seed: %w", err)
	}
	out := make([]domain.Restaurant, 0, len(in))
	for _, f := range in {
		if _, err := uuid.Parse(f.ID); err != nil {
			return nil, fmt.Errorf("seed %q: bad id: %w", f.Name, err)
		}
		loc, err := time.LoadLocation(f.Timezone)
		if err != nil {
			return nil, fmt.Errorf("seed %q: timezone: %w", f.Name, err)
		}
		if f.DeliveryEnabled == (f.DeliveryRadiusMeters == 0) {
			return nil, fmt.Errorf("seed %q: deliveryRadiusMeters must be >0 exactly when deliveryEnabled", f.Name)
		}
		r := domain.Restaurant{
			ID: f.ID, Name: f.Name, City: f.City, Address: f.Address,
			Location: domain.Point{Lat: f.Lat, Lon: f.Lon}, Timezone: f.Timezone,
			DeliveryEnabled: f.DeliveryEnabled, DeliveryRadiusM: f.DeliveryRadiusMeters, Active: f.Active,
			Schedule: domain.Schedule{Location: loc},
		}
		for _, h := range f.Hours {
			iv, err := interval(h.OpensAt, h.ClosesAt)
			if err != nil {
				return nil, fmt.Errorf("seed %q: %w", f.Name, err)
			}
			for _, d := range h.Days {
				if d < 1 || d > 7 {
					return nil, fmt.Errorf("seed %q: day %d out of 1..7", f.Name, d)
				}
				r.Schedule.Hours = append(r.Schedule.Hours, domain.WorkingHours{DayOfWeek: d, Interval: iv})
			}
		}
		for _, e := range f.Exceptions {
			if _, err := time.Parse("2006-01-02", e.Date); err != nil {
				return nil, fmt.Errorf("seed %q: exception date %q: %w", f.Name, e.Date, err)
			}
			ex := domain.Exception{Date: e.Date, Closed: e.Closed, Note: e.Note}
			if !e.Closed {
				if ex.Interval, err = interval(e.OpensAt, e.ClosesAt); err != nil {
					return nil, fmt.Errorf("seed %q: %w", f.Name, err)
				}
			}
			r.Schedule.Exceptions = append(r.Schedule.Exceptions, ex)
		}
		out = append(out, r)
	}
	return out, nil
}

func interval(opens, closes string) (domain.Interval, error) {
	o, err := domain.ParseClock(opens, false)
	if err != nil {
		return domain.Interval{}, err
	}
	c, err := domain.ParseClock(closes, true)
	if err != nil {
		return domain.Interval{}, err
	}
	if c == 0 || o == c {
		return domain.Interval{}, fmt.Errorf("interval %s–%s is empty or invalid", opens, closes)
	}
	return domain.Interval{Opens: o, Closes: c}, nil
}
