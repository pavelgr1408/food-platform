package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/pavelgr1408/food-platform/pkg/pgxutil"
	"github.com/pavelgr1408/food-platform/pkg/pgxutil/pgtest"
	"github.com/pavelgr1408/food-platform/services/restaurant/internal/domain"
	"github.com/pavelgr1408/food-platform/services/restaurant/internal/seed"
	"github.com/pavelgr1408/food-platform/services/restaurant/migrations"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	pool := pgtest.New(t)
	if err := pgxutil.Migrate(context.Background(), pool, nil, pgxutil.Source{Module: "restaurant", FS: migrations.FS}); err != nil {
		t.Fatal(err)
	}
	s := New(pool)
	rs, err := seed.Restaurants()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Upsert(context.Background(), rs); err != nil {
		t.Fatal(err)
	}
	return s
}

var (
	ctx    = context.Background()
	now    = time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	tverSq = domain.Point{Lat: 55.7558, Lon: 37.6173} // центр Москвы
)

func TestCandidatesSortedAndDistanceMatchesHaversine(t *testing.T) {
	s := newStore(t)
	got, err := s.Candidates(ctx, &tverSq, 50000, now.AddDate(0, 0, -2), now.AddDate(0, 0, 2))
	if err != nil {
		t.Fatal(err)
	}
	// Москва: Тверская, Арбат, Парк Горького (неактивный не должен попасть); СПб и Казань далеко
	if len(got) != 3 {
		t.Fatalf("want 3 Moscow restaurants, got %d", len(got))
	}
	prev := -1
	for _, r := range got {
		if r.DistanceM == nil || *r.DistanceM < prev {
			t.Fatalf("not sorted by distance: %+v", got)
		}
		prev = *r.DistanceM
		want := domain.DistanceMeters(tverSq, r.Location)
		if diff := *r.DistanceM - want; diff < -1 || diff > 1 { // SQL и Go могут отличаться на метр при округлении
			t.Errorf("%s: SQL %d vs Go %d", r.Name, *r.DistanceM, want)
		}
		if !r.Active {
			t.Errorf("inactive restaurant leaked: %s", r.Name)
		}
		if len(r.Schedule.Hours) == 0 {
			t.Errorf("%s: schedule not loaded", r.Name)
		}
	}
}

func TestCandidatesRadiusBoundary(t *testing.T) {
	s := newStore(t)
	// Тверская (BLT) от центра ≈ 1.2 км: радиус 1000 → нет, 2000 → есть
	small, _ := s.Candidates(ctx, &tverSq, 1000, now, now)
	for _, r := range small {
		if r.Name == "BLT Тверская" {
			t.Error("Тверская вне радиуса 1000")
		}
	}
	big, _ := s.Candidates(ctx, &tverSq, 2000, now, now)
	found := false
	for _, r := range big {
		found = found || r.Name == "BLT Тверская"
	}
	if !found {
		t.Error("Тверская должна попасть в радиус 2000")
	}
}

func TestCandidatesWithoutCenter(t *testing.T) {
	s := newStore(t)
	got, err := s.Candidates(ctx, nil, 0, now, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 { // 6 в seed, один неактивен
		t.Fatalf("want 5 active, got %d", len(got))
	}
	for _, r := range got {
		if r.DistanceM != nil {
			t.Error("distance must be nil without center")
		}
	}
	for i := 1; i < len(got); i++ {
		if got[i-1].Name > got[i].Name {
			t.Error("must be sorted by name")
		}
	}
}

func TestGetLoadsScheduleAndHidesInactive(t *testing.T) {
	s := newStore(t)
	r, err := s.Get(ctx, "a1000000-0000-4000-8000-000000000001", time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Schedule.Hours) != 7 || len(r.Schedule.Exceptions) != 2 {
		t.Fatalf("hours=%d exceptions=%d", len(r.Schedule.Hours), len(r.Schedule.Exceptions))
	}
	// короткий день 31 декабря: 10:00–18:00, 1 января закрыто
	loc := r.Schedule.Location
	if r.Schedule.IsOpen(time.Date(2026, 12, 31, 19, 0, 0, 0, loc)) || !r.Schedule.IsOpen(time.Date(2026, 12, 31, 12, 0, 0, 0, loc)) {
		t.Error("exception hours not applied")
	}
	if r.Schedule.IsOpen(time.Date(2027, 1, 1, 12, 0, 0, 0, loc)) {
		t.Error("closed day not applied")
	}

	if _, err := s.Get(ctx, "a1000000-0000-4000-8000-000000000006", now); !errors.Is(err, ErrNotFound) {
		t.Errorf("inactive must be ErrNotFound, got %v", err)
	}
	if _, err := s.Get(ctx, "a1000000-0000-4000-8000-0000000000ff", now); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown must be ErrNotFound, got %v", err)
	}
}

func TestExceptionWindowFiltersByDate(t *testing.T) {
	s := newStore(t)
	got, _ := s.Candidates(ctx, nil, 0, now.AddDate(0, 0, -2), now.AddDate(0, 0, 2)) // октябрь: исключений нет
	for _, r := range got {
		if len(r.Schedule.Exceptions) != 0 {
			t.Errorf("%s: unexpected exceptions in window: %+v", r.Name, r.Schedule.Exceptions)
		}
	}
}

func TestUpsertIsIdempotentAndReplacesSchedule(t *testing.T) {
	s := newStore(t)
	rs, _ := seed.Restaurants()
	rs[0].Name = "BLT Тверская (обновлено)"
	rs[0].Schedule.Hours = rs[0].Schedule.Hours[:3]
	rs[0].Schedule.Exceptions = nil
	for i := 0; i < 2; i++ { // повторный запуск не плодит строки
		if err := s.Upsert(ctx, rs); err != nil {
			t.Fatal(err)
		}
	}
	r, err := s.Get(ctx, rs[0].ID, time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if r.Name != "BLT Тверская (обновлено)" || len(r.Schedule.Hours) != 3 || len(r.Schedule.Exceptions) != 0 {
		t.Fatalf("not replaced: %+v", r)
	}
}

func TestAntimeridianCandidates(t *testing.T) {
	s := newStore(t)
	mk := func(id string, lat, lon float64) domain.Restaurant {
		return domain.Restaurant{ID: id, Name: id, Location: domain.Point{Lat: lat, Lon: lon}, Timezone: "UTC", Active: true,
			Schedule: domain.Schedule{Hours: []domain.WorkingHours{{DayOfWeek: 1, Interval: domain.Interval{Opens: 60, Closes: 120}}}}}
	}
	east := mk("b1000000-0000-4000-8000-000000000001", 64.7, 179.9)
	west := mk("b1000000-0000-4000-8000-000000000002", 64.7, -179.9)
	if err := s.Upsert(ctx, []domain.Restaurant{east, west}); err != nil {
		t.Fatal(err)
	}
	c := domain.Point{Lat: 64.7, Lon: 180}
	got, err := s.Candidates(ctx, &c, 20000, now, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want both sides of antimeridian, got %d", len(got))
	}
}
