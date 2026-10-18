package service

import (
	"context"
	"testing"
	"time"

	"github.com/pavelgr1408/food-platform/pkg/httpx"
	"github.com/pavelgr1408/food-platform/services/restaurant/internal/domain"
	"github.com/pavelgr1408/food-platform/services/restaurant/internal/store"
)

type fakeRepo struct{ list []domain.Restaurant }

func (f fakeRepo) Get(_ context.Context, id string, _ time.Time) (domain.Restaurant, error) {
	for _, r := range f.list {
		if r.ID == id {
			return r, nil
		}
	}
	return domain.Restaurant{}, store.ErrNotFound
}

func (f fakeRepo) Candidates(_ context.Context, c *domain.Point, radius int, _, _ time.Time) ([]domain.Restaurant, error) {
	var out []domain.Restaurant
	for _, r := range f.list {
		if c != nil {
			d := domain.DistanceMeters(*c, r.Location)
			if d > radius {
				continue
			}
			r.DistanceM = &d
		}
		out = append(out, r)
	}
	return out, nil
}

func rest(id string, p domain.Point, delivery int, open bool) domain.Restaurant {
	hours := []domain.WorkingHours{}
	if open {
		for d := 1; d <= 7; d++ {
			hours = append(hours, domain.WorkingHours{DayOfWeek: d, Interval: domain.Interval{Opens: 0, Closes: 1440}})
		}
	}
	return domain.Restaurant{ID: id, Name: id, Location: p, DeliveryEnabled: delivery > 0, DeliveryRadiusM: delivery, Active: true,
		Schedule: domain.Schedule{Location: time.UTC, Hours: hours}}
}

var moscow = domain.Point{Lat: 55.7558, Lon: 37.6173}

func newSvc(rs ...domain.Restaurant) *Service {
	s := New(fakeRepo{rs})
	s.Now = func() time.Time { return time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC) }
	return s
}

func params() Params { return Params{RadiusM: DefaultRadiusM, Limit: 20} }

func TestSearchDeliveryRadiusAndOpenNow(t *testing.T) {
	near := rest("near", domain.Point{Lat: 55.7600, Lon: 37.6200}, 2000, true)        // ~0.5 км, доставка 2 км
	edge := rest("edge", domain.Point{Lat: 55.7558 + 0.03, Lon: 37.6173}, 1000, true) // ~3.3 км, доставка 1 км → вне зоны
	closed := rest("closed", domain.Point{Lat: 55.7570, Lon: 37.6180}, 2000, false)
	s := newSvc(near, edge, closed)

	p := params()
	p.Center = &moscow
	res, _ := s.Search(context.Background(), p)
	if res.Total != 3 || res.Items[0].ID != "closed" && res.Items[0].ID != "near" {
		t.Fatalf("all: %+v", res)
	}
	for _, it := range res.Items {
		if it.ID == "edge" && it.DeliveryAvailable {
			t.Error("edge: центр вне радиуса доставки ресторана")
		}
		if it.ID == "closed" && it.OpenNow {
			t.Error("closed must not be open")
		}
	}

	p.DeliveryOnly, p.OpenNow = true, true
	res, _ = s.Search(context.Background(), p)
	if res.Total != 1 || res.Items[0].ID != "near" {
		t.Fatalf("deliveryOnly+openNow: %+v", res)
	}
}

func TestSearchSortedByDistanceAndPaging(t *testing.T) {
	a := rest("a", domain.Point{Lat: 55.7558 + 0.02, Lon: 37.6173}, 0, true)
	b := rest("b", domain.Point{Lat: 55.7558 + 0.005, Lon: 37.6173}, 0, true)
	c := rest("c", domain.Point{Lat: 55.7558 + 0.01, Lon: 37.6173}, 0, true)
	s := newSvc(a, b, c)
	p := params()
	p.Center, p.Limit = &moscow, 2
	res, _ := s.Search(context.Background(), p)
	// fakeRepo не сортирует: сортировку делает SQL; здесь проверяем только страницу
	if res.Total != 3 || len(res.Items) != 2 {
		t.Fatalf("paging: %+v", res)
	}
	p.Offset = 2
	res, _ = s.Search(context.Background(), p)
	if len(res.Items) != 1 || res.Total != 3 {
		t.Fatalf("second page: %+v", res)
	}
	p.Offset = 10
	res, _ = s.Search(context.Background(), p)
	if res.Items == nil || len(res.Items) != 0 || res.Hint != "" {
		t.Fatalf("offset beyond: %+v", res)
	}
}

func TestSearchHints(t *testing.T) {
	pickupOnly := rest("p", domain.Point{Lat: 55.7560, Lon: 37.6175}, 0, true) // без доставки
	s := newSvc(pickupOnly)

	p := params()
	p.Center, p.DeliveryOnly = &moscow, true
	if res, _ := s.Search(context.Background(), p); res.Hint != HintPickup || len(res.Items) != 0 {
		t.Fatalf("want pickup hint: %+v", res)
	}

	empty := newSvc()
	p = params()
	p.Center = &moscow
	if res, _ := empty.Search(context.Background(), p); res.Hint != HintExpandRadius {
		t.Fatalf("want expand_radius: %+v", res)
	}
	p.RadiusM = MaxRadiusM
	if res, _ := empty.Search(context.Background(), p); res.Hint != HintChangeAddr {
		t.Fatalf("want change_address at max radius: %+v", res)
	}
	p = params()
	p.Address = "Нижний Тагил"
	if res, _ := empty.Search(context.Background(), p); res.Hint != HintChangeAddr || res.Items == nil {
		t.Fatalf("unknown address: %+v", res)
	}
	p = params() // без центра и адреса, каталог пуст — подсказки нет
	if res, _ := empty.Search(context.Background(), p); res.Hint != "" {
		t.Fatalf("no center: %+v", res)
	}
}

func TestSearchByAddressUsesGeocoder(t *testing.T) {
	s := newSvc(rest("m", domain.Point{Lat: 55.7570, Lon: 37.6180}, 3000, true))
	p := params()
	p.Address = "г. Москва, Тверская 12"
	res, _ := s.Search(context.Background(), p)
	if res.Total != 1 || res.Center == nil || res.Items[0].DistanceM == nil {
		t.Fatalf("%+v", res)
	}
}

func TestGetNotFound(t *testing.T) {
	_, err := newSvc().Get(context.Background(), "nope")
	var he *httpx.Error
	if !asHTTP(err, &he) || he.Status != 404 {
		t.Fatalf("want 404, got %v", err)
	}
}

func asHTTP(err error, target **httpx.Error) bool {
	he, ok := err.(*httpx.Error)
	if ok {
		*target = he
	}
	return ok
}
