// Package testkit — помощники тестов notification-service: БД с миграциями, управляемый провайдер push,
// часы. Используется только из _test.go.
package testkit

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pavelgr1408/food-platform/pkg/kafka"
	"github.com/pavelgr1408/food-platform/pkg/pgxutil"
	"github.com/pavelgr1408/food-platform/pkg/pgxutil/pgtest"
	"github.com/pavelgr1408/food-platform/services/notification/internal/push"
	"github.com/pavelgr1408/food-platform/services/notification/migrations"
)

// Log — молчаливый логгер тестов.
func Log() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// Pool — временная БД с миграциями notification + kafka.
func Pool(t testing.TB) *pgxpool.Pool {
	t.Helper()
	pool := pgtest.New(t)
	if err := pgxutil.Migrate(context.Background(), pool, Log(),
		pgxutil.Source{Module: "notification", FS: migrations.FS}, kafka.Migrations()); err != nil {
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

// Provider — управляемый «провайдер push»: запоминает сообщения, исход задаётся по токену или по умолчанию.
type Provider struct {
	mu      sync.Mutex
	Sent    []push.Message
	Calls   int
	Default push.Result
	ByToken map[string]push.Result
	Err     error // ошибка, возвращаемая вместе с не-успешным исходом
	Delay   time.Duration
}

// NewProvider — провайдер, принимающий всё.
func NewProvider() *Provider { return &Provider{ByToken: map[string]push.Result{}} }

// Send реализует push.Sender.
func (p *Provider) Send(ctx context.Context, m push.Message) (push.Result, error) {
	if p.Delay > 0 {
		select {
		case <-time.After(p.Delay):
		case <-ctx.Done():
			return push.Retry, ctx.Err()
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.Calls++
	res, ok := p.ByToken[m.Token]
	if !ok {
		res = p.Default
	}
	if res == push.Sent {
		p.Sent = append(p.Sent, m)
		return res, nil
	}
	err := p.Err
	if err == nil {
		err = errors.New("provider says " + res.String())
	}
	return res, err
}

// Set меняет исход для токена.
func (p *Provider) Set(token string, r push.Result) { p.mu.Lock(); p.ByToken[token] = r; p.mu.Unlock() }

// SetDefault меняет исход по умолчанию.
func (p *Provider) SetDefault(r push.Result) { p.mu.Lock(); p.Default = r; p.mu.Unlock() }

// Messages — копия отправленных сообщений.
func (p *Provider) Messages() []push.Message {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]push.Message(nil), p.Sent...)
}

// CallCount — число обращений к провайдеру.
func (p *Provider) CallCount() int { p.mu.Lock(); defer p.mu.Unlock(); return p.Calls }
