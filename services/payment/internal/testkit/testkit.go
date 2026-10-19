// Package testkit — помощники тестов payment-service: БД с миграциями, управляемый шлюз, часы.
// Используется только из _test.go.
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

	"github.com/pavelgr1408/food-platform/pkg/idempotency"
	"github.com/pavelgr1408/food-platform/pkg/kafka"
	"github.com/pavelgr1408/food-platform/pkg/pgxutil"
	"github.com/pavelgr1408/food-platform/pkg/pgxutil/pgtest"
	"github.com/pavelgr1408/food-platform/services/payment/internal/gateway"
	"github.com/pavelgr1408/food-platform/services/payment/migrations"
)

// Log — молчаливый логгер тестов.
func Log() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// Pool — временная БД с миграциями payment + kafka + idempotency.
func Pool(t testing.TB) *pgxpool.Pool {
	t.Helper()
	pool := pgtest.New(t)
	if err := pgxutil.Migrate(context.Background(), pool, Log(),
		pgxutil.Source{Module: "payment", FS: migrations.FS}, kafka.Migrations(), idempotency.Migrations()); err != nil {
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
func NewClock() *Clock { return &Clock{t: time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)} }

// Now — текущее время часов.
func (c *Clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }

// Advance сдвигает часы.
func (c *Clock) Advance(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

// Op — операция «у шлюза».
type Op struct {
	Ref    string
	Key    string
	Amount int64
	Status string // pending | paid | declined
	Reason string
}

// FakeGateway — управляемая подделка шлюза: идемпотентное Create по ключу, настраиваемые сбои.
type FakeGateway struct {
	mu        sync.Mutex
	ops       map[string]*Op // по ключу
	byRef     map[string]*Op
	CreateErr error // что вернуть из Create (до создания операции)
	LostReply error // операция СОЗДАЁТСЯ, но вызывающий получает эту ошибку (таймаут «после» записи)
	StatusErr error
	// PaidOnCancel: к моменту Cancel операцию успели оплатить (гонка «истечение ↔ оплата»).
	PaidOnCancel bool
	Calls        struct{ Create, Status, Cancel int }
}

// NewGateway создаёт подделку.
func NewGateway() *FakeGateway {
	return &FakeGateway{ops: map[string]*Op{}, byRef: map[string]*Op{}}
}

// Create — идемпотентно по Key.
func (g *FakeGateway) Create(_ context.Context, r gateway.CreateRequest) (gateway.Created, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.Calls.Create++
	if g.CreateErr != nil {
		return gateway.Created{}, g.CreateErr
	}
	op, ok := g.ops[r.Key]
	if !ok {
		op = &Op{Ref: "gw_" + strconv.Itoa(len(g.ops)+1), Key: r.Key, Amount: r.Amount, Status: gateway.StatePending}
		g.ops[r.Key], g.byRef[op.Ref] = op, op
	}
	if g.LostReply != nil {
		return gateway.Created{}, g.LostReply
	}
	return gateway.Created{Ref: op.Ref, URL: "http://gateway.test/pay/" + op.Ref}, nil
}

// Status — состояние операции.
func (g *FakeGateway) Status(_ context.Context, ref string) (gateway.State, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.Calls.Status++
	if g.StatusErr != nil {
		return gateway.State{}, g.StatusErr
	}
	op, ok := g.byRef[ref]
	if !ok {
		return gateway.State{}, gateway.ErrNotFound
	}
	return gateway.State{Ref: op.Ref, Status: op.Status, Amount: op.Amount, Currency: "RUB", Reason: op.Reason}, nil
}

// Cancel отменяет pending; оплаченную возвращает как есть.
func (g *FakeGateway) Cancel(_ context.Context, ref string) (gateway.State, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.Calls.Cancel++
	op, ok := g.byRef[ref]
	if !ok {
		return gateway.State{}, gateway.ErrNotFound
	}
	if op.Status == gateway.StatePending {
		if g.PaidOnCancel {
			op.Status = gateway.StatePaid
		} else {
			op.Status, op.Reason = gateway.StateDeclined, "canceled"
		}
	}
	return gateway.State{Ref: op.Ref, Status: op.Status, Amount: op.Amount, Currency: "RUB", Reason: op.Reason}, nil
}

// Finish задаёт итог операции (как будто пользователь заплатил / банк отказал).
func (g *FakeGateway) Finish(key, status, reason string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if op := g.ops[key]; op != nil {
		op.Status, op.Reason = status, reason
	}
}

// Operations — число операций, созданных у шлюза (не вызовов Create).
func (g *FakeGateway) Operations() int { g.mu.Lock(); defer g.mu.Unlock(); return len(g.ops) }

// ErrTimeout — «сеть оборвалась»: не отказ, а неизвестность.
var ErrTimeout = errors.New("gateway timeout")
