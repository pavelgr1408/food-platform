package limiter

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newLimiter(rate float64, burst, maxKeys int) (*Limiter, *clock) {
	c := &clock{t: time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)}
	l := New(rate, burst, maxKeys)
	l.now = c.now
	return l, c
}

func TestBurstThenRefill(t *testing.T) {
	l, c := newLimiter(2, 3, 100) // 2 в секунду, всплеск 3
	for i := 0; i < 3; i++ {
		if ok, _ := l.Allow("a"); !ok {
			t.Fatalf("request %d within burst must pass", i)
		}
	}
	ok, retry := l.Allow("a")
	if ok || retry != 500*time.Millisecond {
		t.Fatalf("4th request: ok=%v retry=%v, want denied with 500ms", ok, retry)
	}
	c.t = c.t.Add(500 * time.Millisecond)
	if ok, _ := l.Allow("a"); !ok {
		t.Fatal("one token must be back after 500ms")
	}
	if ok, _ := l.Allow("a"); ok {
		t.Fatal("only one token was refilled")
	}
	c.t = c.t.Add(time.Hour)
	for i := 0; i < 3; i++ { // ёмкость не превышает burst
		if ok, _ := l.Allow("a"); !ok {
			t.Fatal("refill is capped at burst but burst must pass")
		}
	}
	if ok, _ := l.Allow("a"); ok {
		t.Fatal("refill must not exceed burst")
	}
}

func TestKeysAreIndependent(t *testing.T) {
	l, _ := newLimiter(1, 1, 100)
	if ok, _ := l.Allow("u:1"); !ok {
		t.Fatal()
	}
	if ok, _ := l.Allow("u:1"); ok {
		t.Fatal("second request of the same key must be denied")
	}
	if ok, _ := l.Allow("u:2"); !ok {
		t.Fatal("another key has its own bucket")
	}
}

func TestRetryAfterCoversSlowRates(t *testing.T) {
	l, _ := newLimiter(10.0/60, 1, 10) // 10 в минуту
	l.Allow("ip")
	if ok, retry := l.Allow("ip"); ok || retry < 5*time.Second || retry > 7*time.Second {
		t.Fatalf("ok=%v retry=%v, want ~6s", ok, retry)
	}
}

func TestMemoryIsBounded(t *testing.T) {
	l, c := newLimiter(1, 2, 50)
	for i := 0; i < 50; i++ {
		l.Allow(fmt.Sprintf("k%d", i))
	}
	c.t = c.t.Add(10 * time.Second) // все бакеты восстановились
	l.Allow("fresh")                // переполнение → вытеснение неактивных
	if n := l.Len(); n > 2 {
		t.Fatalf("idle keys were not swept: %d", n)
	}
	// атака: много активных источников — память не растёт сверх maxKeys
	for i := 0; i < 500; i++ {
		l.Allow(fmt.Sprintf("flood%d", i))
		l.Allow(fmt.Sprintf("flood%d", i))
	}
	if n := l.Len(); n > 50 {
		t.Fatalf("limiter grew beyond maxKeys: %d", n)
	}
}

func TestConcurrentUse(t *testing.T) {
	l := New(1000, 1000, 1000)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				l.Allow(fmt.Sprintf("k%d", i%20))
			}
		}()
	}
	wg.Wait()
}
