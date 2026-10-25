// Package location — последние координаты курьера в delivery-Redis (ключ на заказ, TTL).
package location

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/pavelgr1408/food-platform/pkg/redisx"
)

// ErrNotFound — точки нет или она истекла.
var ErrNotFound = errors.New("location: not found")

// Point — последняя известная точка.
type Point struct {
	Lat       float64   `json:"lat"`
	Lon       float64   `json:"lon"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Store — хранилище координат.
type Store interface {
	Put(ctx context.Context, orderID string, p Point, ttl time.Duration) error
	Get(ctx context.Context, orderID string) (Point, error)
	Delete(ctx context.Context, orderID string) error
}

func key(orderID string) string { return "delivery:courier-location:" + orderID }

// Redis — реализация поверх pkg/redisx.
type Redis struct{ C *redisx.Client }

// Put записывает точку с TTL.
func (r Redis) Put(ctx context.Context, orderID string, p Point, ttl time.Duration) error {
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return r.C.Set(ctx, key(orderID), string(b), ttl)
}

// Get читает точку.
func (r Redis) Get(ctx context.Context, orderID string) (Point, error) {
	s, err := r.C.Get(ctx, key(orderID))
	if errors.Is(err, redisx.ErrNil) {
		return Point{}, ErrNotFound
	}
	if err != nil {
		return Point{}, err
	}
	var p Point
	if err := json.Unmarshal([]byte(s), &p); err != nil {
		return Point{}, err
	}
	return p, nil
}

// Delete удаляет точку (доставка завершена).
func (r Redis) Delete(ctx context.Context, orderID string) error {
	_, err := r.C.Del(ctx, key(orderID))
	return err
}

// Memory — реализация в памяти для тестов и работы без Redis (REDIS_ADDR не задан).
type Memory struct {
	mu   sync.Mutex
	data map[string]memVal
	Now  func() time.Time
}

type memVal struct {
	p   Point
	exp time.Time
}

// NewMemory создаёт хранилище в памяти.
func NewMemory() *Memory {
	return &Memory{data: map[string]memVal{}, Now: time.Now}
}

func (m *Memory) lock()   { m.mu.Lock() }
func (m *Memory) unlock() { m.mu.Unlock() }

// Put записывает точку.
func (m *Memory) Put(_ context.Context, orderID string, p Point, ttl time.Duration) error {
	m.lock()
	defer m.unlock()
	m.data[orderID] = memVal{p: p, exp: m.Now().Add(ttl)}
	return nil
}

// Get читает точку.
func (m *Memory) Get(_ context.Context, orderID string) (Point, error) {
	m.lock()
	defer m.unlock()
	v, ok := m.data[orderID]
	if !ok || !m.Now().Before(v.exp) {
		delete(m.data, orderID)
		return Point{}, ErrNotFound
	}
	return v.p, nil
}

// Delete удаляет точку.
func (m *Memory) Delete(_ context.Context, orderID string) error {
	m.lock()
	defer m.unlock()
	delete(m.data, orderID)
	return nil
}
