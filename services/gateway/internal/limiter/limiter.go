// Package limiter — token bucket в памяти реплики с вытеснением неактивных ключей.
package limiter

import (
	"math"
	"sync"
	"time"
)

type bucket struct {
	tokens float64
	last   time.Time
}

// Limiter — набор бакетов по ключу. rate — токенов в секунду, burst — ёмкость.
type Limiter struct {
	rate, burst float64
	maxKeys     int
	now         func() time.Time

	mu      sync.Mutex
	buckets map[string]*bucket
	sweptAt time.Time
}

// New создаёт лимитер. maxKeys ограничивает память: при переполнении неактивные ключи вытесняются,
// а если и это не помогло (атака с множеством источников), новые ключи пропускаются без учёта (fail-open
// только для памяти — лимит на уже известные ключи продолжает действовать).
func New(ratePerSec float64, burst, maxKeys int) *Limiter {
	return &Limiter{rate: ratePerSec, burst: float64(burst), maxKeys: maxKeys, now: time.Now, buckets: map[string]*bucket{}}
}

// Allow списывает один токен. При отказе возвращает, через сколько появится следующий.
func (l *Limiter) Allow(key string) (ok bool, retryAfter time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	b, found := l.buckets[key]
	if !found {
		if len(l.buckets) >= l.maxKeys {
			l.sweep(now)
			if len(l.buckets) >= l.maxKeys {
				return true, 0
			}
		}
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}
	if elapsed := now.Sub(b.last).Seconds(); elapsed > 0 {
		b.tokens = math.Min(l.burst, b.tokens+elapsed*l.rate)
		b.last = now
	}
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	need := (1 - b.tokens) / l.rate
	return false, time.Duration(math.Ceil(need*1000)) * time.Millisecond
}

// sweep удаляет ключи, чей бакет уже полностью восстановился (клиент давно молчит). Не чаще раза в секунду.
func (l *Limiter) sweep(now time.Time) {
	if now.Sub(l.sweptAt) < time.Second {
		return
	}
	l.sweptAt = now
	for k, b := range l.buckets {
		if b.tokens+now.Sub(b.last).Seconds()*l.rate >= l.burst {
			delete(l.buckets, k)
		}
	}
}

// Len — число отслеживаемых ключей (метрика/тесты).
func (l *Limiter) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.buckets)
}
