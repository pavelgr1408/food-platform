// Package mock — каркас единого бинаря внешних систем (S0). Конкретные моки
// (платёжный шлюз, POS, производство, курьерка, push) добавляются как Module
// на этапах S3…S9 и включаются переменной MOCKS.
//
// Соглашения:
//   - у каждого модуля свой listener (порт), чтобы адреса в конфигах сервисов
//     совпадали с настоящими внешними системами;
//   - /_admin/* управляет сценарием (задержка, режимы отказа), /_inspect/* показывает
//     принятые запросы — Postman/newman проверяют эффекты без доступа к БД;
//   - асинхронные эффекты (вебхуки, события) выполняются через Env.After с задержкой Env.Delay.
package mock

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// Env — общее окружение модулей.
type Env struct {
	Log     *slog.Logger
	Brokers []string
	delay   atomic.Int64 // наносекунды
	wg      sync.WaitGroup
}

// NewEnv создаёт окружение с начальной задержкой асинхронных эффектов.
func NewEnv(log *slog.Logger, brokers []string, delay time.Duration) *Env {
	e := &Env{Log: log, Brokers: brokers}
	e.delay.Store(int64(delay))
	return e
}

// Delay — текущая задержка асинхронных эффектов (меняется через /_admin/delay).
func (e *Env) Delay() time.Duration { return time.Duration(e.delay.Load()) }

// SetDelay меняет задержку.
func (e *Env) SetDelay(d time.Duration) { e.delay.Store(int64(d)) }

// After выполняет fn через Env.Delay(), если ctx не отменён. Ждётся при остановке (Wait).
func (e *Env) After(ctx context.Context, fn func()) { e.AfterD(ctx, e.Delay(), fn) }

// AfterD — то же с явной задержкой.
func (e *Env) AfterD(ctx context.Context, d time.Duration, fn func()) {
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-t.C:
			fn()
		case <-ctx.Done():
		}
	}()
}

// Wait дожидается отложенных эффектов (после отмены контекста).
func (e *Env) Wait() { e.wg.Wait() }

// Module — один мок внешней системы.
type Module interface {
	Name() string
	DefaultAddr() string
	// Register вешает HTTP-ручки модуля на mux его listener'а.
	Register(mux *http.ServeMux, env *Env)
	// Start запускает фоновые воркеры (консьюмеры Kafka, таймеры); должен вернуться быстро.
	Start(ctx context.Context, env *Env) error
}

// Factory создаёт модуль.
type Factory func() Module

var registry = map[string]Factory{}

// Register добавляет фабрику модуля (вызывается из init()/main).
func Register(name string, f Factory) { registry[name] = f }

// Names — зарегистрированные модули.
func Names() []string {
	n := make([]string, 0, len(registry))
	for k := range registry {
		n = append(n, k)
	}
	sort.Strings(n)
	return n
}

// Build создаёт запрошенные модули; неизвестное имя — ошибка.
func Build(names []string) ([]Module, error) {
	var out []Module
	for _, n := range names {
		f, ok := registry[n]
		if !ok {
			return nil, fmt.Errorf("unknown mock %q (available: %v)", n, Names())
		}
		out = append(out, f())
	}
	return out, nil
}
