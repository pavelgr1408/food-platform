// Package testkit — общие помощники тестов orders-service: БД с миграциями и
// управляемые подделки menu/restaurant. Используется только из _test.go.
package testkit

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pavelgr1408/food-platform/pkg/idempotency"
	"github.com/pavelgr1408/food-platform/pkg/kafka"
	"github.com/pavelgr1408/food-platform/pkg/pgxutil"
	"github.com/pavelgr1408/food-platform/pkg/pgxutil/pgtest"
	"github.com/pavelgr1408/food-platform/services/orders/internal/clients"
	"github.com/pavelgr1408/food-platform/services/orders/migrations"
)

// Тестовые идентификаторы.
const (
	Restaurant1 = "a1000000-0000-4000-8000-000000000001"
	Restaurant2 = "a1000000-0000-4000-8000-000000000002"
	Burger      = "b1000000-0000-4000-8000-000000000001"
	Cola        = "b1000000-0000-4000-8000-000000000002"
	Cake        = "b1000000-0000-4000-8000-000000000003" // в стоп-листе
	Bacon       = "c1000000-0000-4000-8000-000000000001"
	Cheese      = "c1000000-0000-4000-8000-000000000002" // модификатор в стоп-листе
)

// Log — молчаливый логгер тестов.
func Log() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// Pool — временная БД с применёнными миграциями orders + kafka + idempotency.
func Pool(t testing.TB) *pgxpool.Pool {
	t.Helper()
	pool := pgtest.New(t)
	if err := pgxutil.Migrate(context.Background(), pool, Log(),
		pgxutil.Source{Module: "orders", FS: migrations.FS}, kafka.Migrations(), idempotency.Migrations()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return pool
}

// MenuItem — позиция подделки меню.
type MenuItem struct {
	Name      string
	Price     int64
	Available bool
	Mods      map[string]Mod
}

// Mod — модификатор.
type Mod struct {
	Price     int64
	Available bool
}

// FakeMenu реализует service.MenuValidator.
type FakeMenu struct {
	mu       sync.Mutex
	items    map[string]MenuItem
	err      error
	calls    int
	Currency string
}

// NewMenu возвращает меню с бургером (349 ₽, бекон +89 ₽, сыр в стоп-листе), колой (129 ₽) и тортом в стоп-листе.
func NewMenu() *FakeMenu {
	return &FakeMenu{Currency: "RUB", items: map[string]MenuItem{
		Burger: {Name: "Бургер", Price: 34900, Available: true, Mods: map[string]Mod{Bacon: {8900, true}, Cheese: {5000, false}}},
		Cola:   {Name: "Кола", Price: 12900, Available: true},
		Cake:   {Name: "Торт", Price: 17900, Available: false},
	}}
}

// SetPrice меняет цену позиции.
func (m *FakeMenu) SetPrice(id string, price int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	it := m.items[id]
	it.Price = price
	m.items[id] = it
}

// SetAvailable включает/выключает позицию (стоп-лист).
func (m *FakeMenu) SetAvailable(id string, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	it := m.items[id]
	it.Available = ok
	m.items[id] = it
}

// Fail заставляет Validate возвращать ошибку (nil — снять).
func (m *FakeMenu) Fail(err error) { m.mu.Lock(); m.err = err; m.mu.Unlock() }

// Calls — число вызовов Validate.
func (m *FakeMenu) Calls() int { m.mu.Lock(); defer m.mu.Unlock(); return m.calls }

// Validate повторяет семантику menu-service.validateMenuItems.
func (m *FakeMenu) Validate(_ context.Context, _ string, lines []clients.MenuLineInput) (clients.MenuResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	if m.err != nil {
		return clients.MenuResult{}, m.err
	}
	res := clients.MenuResult{Valid: true, MenuVersion: 1, Currency: m.Currency}
	for _, l := range lines {
		ml := clients.MenuLine{MenuItemID: l.MenuItemID, Quantity: l.Quantity, Available: true}
		it, ok := m.items[l.MenuItemID]
		if !ok {
			ml.Available, ml.ReasonCode, ml.Reason = false, "ITEM_NOT_FOUND", "Позиции нет в меню ресторана"
		} else {
			ml.Name, ml.UnitPrice = it.Name, it.Price
			if !it.Available {
				ml.Available, ml.ReasonCode, ml.Reason = false, "ITEM_UNAVAILABLE", "Позиция недоступна"
			}
			for _, id := range l.ModifierIDs {
				md, found := it.Mods[id]
				switch {
				case !found:
					ml.Available, ml.ReasonCode, ml.Reason = false, "MODIFIER_NOT_FOUND", "Модификатор не относится к позиции"
				default:
					ml.UnitPrice += md.Price
					if !md.Available && ml.ReasonCode == "" {
						ml.Available, ml.ReasonCode, ml.Reason = false, "MODIFIER_UNAVAILABLE", "Модификатор недоступен"
					}
				}
			}
			ml.LineTotal = ml.UnitPrice * int64(l.Quantity)
		}
		if ml.Available {
			res.Total += ml.LineTotal
		} else {
			res.Valid = false
		}
		res.Lines = append(res.Lines, ml)
	}
	return res, nil
}

// FakeRestaurants реализует service.RestaurantGetter.
type FakeRestaurants struct {
	mu   sync.Mutex
	info map[string]clients.RestaurantInfo
	err  error
}

// NewRestaurants: ресторан 1 открыт, с доставкой в радиусе 5 км от Красной площади, Москва; ресторан 2 — открыт, без доставки, Владивосток.
func NewRestaurants() *FakeRestaurants {
	return &FakeRestaurants{info: map[string]clients.RestaurantInfo{
		Restaurant1: {ID: Restaurant1, Timezone: "Europe/Moscow", DeliveryEnabled: true, OpenNow: true,
			Lat: 55.7512, Lon: 37.6184, HasLocation: true, DeliveryRadiusM: 5000},
		Restaurant2: {ID: Restaurant2, Timezone: "Asia/Vladivostok", DeliveryEnabled: false, OpenNow: true},
	}}
}

// Update изменяет карточку ресторана.
func (r *FakeRestaurants) Update(id string, f func(*clients.RestaurantInfo)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	i := r.info[id]
	f(&i)
	r.info[id] = i
}

// Fail заставляет Get возвращать ошибку (nil — снять).
func (r *FakeRestaurants) Fail(err error) { r.mu.Lock(); r.err = err; r.mu.Unlock() }

// Get возвращает карточку или clients.ErrNotFound.
func (r *FakeRestaurants) Get(_ context.Context, id string) (clients.RestaurantInfo, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return clients.RestaurantInfo{}, r.err
	}
	i, ok := r.info[id]
	if !ok {
		return clients.RestaurantInfo{}, clients.ErrNotFound
	}
	return i, nil
}

// Clock — управляемые часы.
type Clock struct {
	mu sync.Mutex
	t  time.Time
}

// NewClock стартует с фиксированного момента (2026-10-18 09:00 UTC = 12:00 MSK).
func NewClock() *Clock { return &Clock{t: time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)} }

// Now возвращает текущее время часов.
func (c *Clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }

// Advance сдвигает часы.
func (c *Clock) Advance(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }
