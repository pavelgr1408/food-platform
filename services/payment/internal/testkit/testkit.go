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
	Tender string
	Method string // metadata.method, переданный при создании
	Fiscal []byte
	// Refunded — число созданных возвратов (идемпотентность: повтор с тем же ключом не увеличивает).
	Refunded int
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
	// RefundErr — что вернуть из Refund (nil — успех); RefundPendingOnce — первый ответ «pending».
	RefundErr         error
	RefundPendingOnce bool
	Calls             struct{ Create, Status, Cancel, Refund int }
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
		op = &Op{Ref: "gw_" + strconv.Itoa(len(g.ops)+1), Key: r.Key, Amount: r.Amount, Status: gateway.StatePending, Tender: r.Tender, Method: r.Method}
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
	return gateway.State{Ref: op.Ref, Status: op.Status, Amount: op.Amount, Currency: "RUB", Reason: op.Reason, Fiscal: op.Fiscal}, nil
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

// SetFiscal задаёт фискальный документ операции (его отдаёт Status).
func (g *FakeGateway) SetFiscal(key string, doc []byte) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if op := g.ops[key]; op != nil {
		op.Fiscal = doc
	}
}

// Refund — идемпотентно по ключу; возвращать можно только оплаченное.
func (g *FakeGateway) Refund(_ context.Context, ref, key string, _ int64, _ string) (gateway.Refunded, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.Calls.Refund++
	if g.RefundErr != nil {
		return gateway.Refunded{}, g.RefundErr
	}
	op, ok := g.byRef[ref]
	if !ok {
		return gateway.Refunded{}, gateway.ErrNotFound
	}
	if op.Status != gateway.StatePaid {
		return gateway.Refunded{}, &gateway.RejectedError{HTTPStatus: 422, Code: "not_paid", Message: "only paid operations can be refunded"}
	}
	if g.RefundPendingOnce {
		g.RefundPendingOnce = false
		return gateway.Refunded{Ref: "rf_" + key[:8], Status: "pending"}, nil
	}
	if op.Refunded == 0 {
		op.Refunded = 1
	}
	return gateway.Refunded{Ref: "rf_" + key[:8], Status: "refunded"}, nil
}

// Operations — число операций, созданных у шлюза (не вызовов Create).
func (g *FakeGateway) Operations() int { g.mu.Lock(); defer g.mu.Unlock(); return len(g.ops) }

// ErrTimeout — «сеть оборвалась»: не отказ, а неизвестность.
var ErrTimeout = errors.New("gateway timeout")

// Ref возвращает идентификатор операции у шлюза по ключу (paymentId); "" — операции нет.
func (g *FakeGateway) Ref(key string) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	if op := g.ops[key]; op != nil {
		return op.Ref
	}
	return ""
}

// Method возвращает metadata.method операции по ключу (идентификатору платежа).
func (g *FakeGateway) Method(key string) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	if op := g.ops[key]; op != nil {
		return op.Method
	}
	return ""
}
