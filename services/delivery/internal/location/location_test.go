package location

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/pavelgr1408/food-platform/pkg/redisx"
)

func exercise(t *testing.T, s Store) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.Get(ctx, "o-missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
	p := Point{Lat: 59.9371, Lon: 30.3271, UpdatedAt: time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)}
	if err := s.Put(ctx, "o1", p, time.Minute); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, "o1")
	if err != nil || got.Lat != p.Lat || got.Lon != p.Lon || !got.UpdatedAt.Equal(p.UpdatedAt) {
		t.Fatalf("%+v %v", got, err)
	}
	if err := s.Delete(ctx, "o1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, "o1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted: %v", err)
	}
	// точка живёт ограниченное время: устаревшая координата хуже её отсутствия
	if err := s.Put(ctx, "o2", p, 150*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	time.Sleep(400 * time.Millisecond)
	if _, err := s.Get(ctx, "o2"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ttl: %v", err)
	}
}

func TestMemory(t *testing.T) { exercise(t, NewMemory()) }

func TestRedis(t *testing.T) {
	addr := os.Getenv("TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("TEST_REDIS_ADDR is not set")
	}
	c, err := redisx.New(redisx.Options{Addr: addr})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	exercise(t, Redis{C: c})
}
