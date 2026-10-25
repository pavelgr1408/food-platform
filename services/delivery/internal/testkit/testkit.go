// Package testkit — помощники тестов delivery-service: БД с миграциями, управляемая курьерская система,
// справочник ресторанов, часы. Используется только из _test.go.
package testkit

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pavelgr1408/food-platform/pkg/kafka"
	"github.com/pavelgr1408/food-platform/pkg/pgxutil"
	"github.com/pavelgr1408/food-platform/pkg/pgxutil/pgtest"
	"github.com/pavelgr1408/food-platform/services/delivery/internal/courier"
	"github.com/pavelgr1408/food-platform/services/delivery/internal/service"
	"github.com/pavelgr1408/food-platform/services/delivery/migrations"
)

// Log — молчаливый логгер тестов.
func Log() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// Pool — временная БД с миграциями delivery + kafka.
func Pool(t testing.TB) *pgxpool.Pool {
	t.Helper()
	pool := pgtest.New(t)
	if err := pgxutil.Migrate(context.Background(), pool, Log(),
		pgxutil.Source{Module: "delivery", FS: migrations.FS}, kafka.Migrations()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return pool
}

// Clock — управляемые часы.
type Clock struct {
	mu sync.Mutex
	t  time.Time
}

// NewClock стартует с фиксированного момента.
func NewClock() *Clock { return &Clock{t: time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)} }

// Now — текущее время часов.
func (c *Clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }

// Advance сдвигает часы.
func (c *Clock) Advance(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

// Job — заявка «у курьерской системы».
type Job struct {
	Ref       string
	Key       string
	Req       courier.CreateRequest
	State     courier.State
	Payment   *courier.Payment
	Cancelled bool
}

// FakeCourier — управляемая подделка курьерской системы: идемпотентное Create по ключу, настраиваемые сбои.
type FakeCourier struct {
	mu        sync.Mutex
	byKey     map[string]*Job
	byRef     map[string]*Job
	CreateErr error // что вернуть из Create (до создания заявки)
	LostReply error // заявка СОЗДАЁТСЯ, но вызывающий получает эту ошибку (таймаут «после» записи)
	StatusErr error
	CancelErr error
	// Calls — число вызовов по операциям.
	Calls struct{ Create, Status, Cancel, SetPayment int }
}

// NewCourier создаёт подделку.
func NewCourier() *FakeCourier {
	return &FakeCourier{byKey: map[string]*Job{}, byRef: map[string]*Job{}}
}

// Create — идемпотентно по Key.
func (c *FakeCourier) Create(_ context.Context, r courier.CreateRequest) (courier.State, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Calls.Create++
	if c.CreateErr != nil {
		return courier.State{}, c.CreateErr
	}
	j, ok := c.byKey[r.Key]
	if !ok {
		j = &Job{Ref: "cr_" + strconv.Itoa(len(c.byKey)+1), Key: r.Key, Req: r, State: courier.State{Status: "searching"}}
		j.State.Ref = j.Ref
		if r.Payment != nil {
			p := *r.Payment
			j.Payment = &p
		}
		c.byKey[r.Key], c.byRef[j.Ref] = j, j
	}
	if c.LostReply != nil {
		return courier.State{}, c.LostReply
	}
	return j.State, nil
}

// Status — состояние заявки.
func (c *FakeCourier) Status(_ context.Context, ref string) (courier.State, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Calls.Status++
	if c.StatusErr != nil {
		return courier.State{}, c.StatusErr
	}
	j, ok := c.byRef[ref]
	if !ok {
		return courier.State{}, courier.ErrNotFound
	}
	return j.State, nil
}

// Cancel снимает заявку, пока курьер не выехал; иначе возвращает текущее состояние.
func (c *FakeCourier) Cancel(_ context.Context, ref string) (courier.State, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Calls.Cancel++
	if c.CancelErr != nil {
		return courier.State{}, c.CancelErr
	}
	j, ok := c.byRef[ref]
	if !ok {
		return courier.State{}, courier.ErrNotFound
	}
	if j.State.Status == "searching" || j.State.Status == "assigned" {
		j.Cancelled = true
		j.State.Status, j.State.Reason = "failed", "cancelled"
	}
	return j.State, nil
}

// SetPayment сохраняет платёжную ссылку заявки.
func (c *FakeCourier) SetPayment(_ context.Context, ref string, p courier.Payment) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Calls.SetPayment++
	j, ok := c.byRef[ref]
	if !ok {
		return courier.ErrNotFound
	}
	j.Payment = &p
	return nil
}

// Job возвращает копию заявки по ключу (deliveryId).
func (c *FakeCourier) Job(key string) (Job, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	j, ok := c.byKey[key]
	if !ok {
		return Job{}, false
	}
	return *j, true
}

// Jobs — число созданных заявок.
func (c *FakeCourier) Jobs() int { c.mu.Lock(); defer c.mu.Unlock(); return len(c.byKey) }

// Set меняет состояние единственной заявки курьерской системы (то, что «сделал курьер»).
func (c *FakeCourier) Set(key string, fn func(*courier.State)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if j, ok := c.byKey[key]; ok {
		fn(&j.State)
	}
}

// FakeRestaurants — справочник ресторанов.
type FakeRestaurants struct {
	mu    sync.Mutex
	At    service.Place
	Err   error
	Calls int
}

// NewRestaurants создаёт справочник с одной точкой.
func NewRestaurants() *FakeRestaurants {
	return &FakeRestaurants{At: service.Place{Lat: 55.7512, Lon: 37.6184, Address: "Москва, Тверская, 1"}}
}

// Place — координаты ресторана.
func (r *FakeRestaurants) Place(_ context.Context, _ string) (service.Place, error) { return r.get() }

func (r *FakeRestaurants) get() (service.Place, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Calls++
	if r.Err != nil {
		return service.Place{}, r.Err
	}
	return r.At, nil
}

// ErrDown — типовая сетевая ошибка для сбоев.
var ErrDown = errors.New("connection refused")
