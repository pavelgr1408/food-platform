// Package restaurant — клиент restaurant-service: координаты ресторана (точка забора заказа курьером).
package restaurant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/pavelgr1408/food-platform/pkg/geo"
	"github.com/pavelgr1408/food-platform/services/delivery/internal/service"
)

// Doer — исходящий HTTP-клиент (httpx.Client: ретраи идемпотентных GET, circuit breaker).
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

// Client — клиент с небольшим кэшем: адрес и координаты ресторана меняются крайне редко.
type Client struct {
	BaseURL string
	HTTP    Doer
	TTL     time.Duration    // срок жизни записи кэша, по умолчанию 10 минут
	Now     func() time.Time // nil → time.Now

	mu    sync.Mutex
	cache map[string]entry
}

type entry struct {
	p   service.Place
	exp time.Time
}

// ErrNotFound — ресторана нет.
var ErrNotFound = errors.New("restaurant: not found")

// Place возвращает координаты и адрес ресторана.
func (c *Client) Place(ctx context.Context, id string) (service.Place, error) {
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	c.mu.Lock()
	if e, ok := c.cache[id]; ok && now().Before(e.exp) {
		c.mu.Unlock()
		return e.p, nil
	}
	c.mu.Unlock()

	u := strings.TrimRight(c.BaseURL, "/") + "/restaurants/" + url.PathEscape(id)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return service.Place{}, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return service.Place{}, fmt.Errorf("restaurant: %w", err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return service.Place{}, ErrNotFound
	case resp.StatusCode != http.StatusOK:
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return service.Place{}, fmt.Errorf("restaurant: status %d", resp.StatusCode)
	}
	var r struct {
		Address  string `json:"address"`
		Location *struct {
			Lat float64 `json:"lat"`
			Lon float64 `json:"lon"`
		} `json:"location"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&r); err != nil {
		return service.Place{}, fmt.Errorf("restaurant: decode: %w", err)
	}
	if r.Location == nil || !geo.Valid(r.Location.Lat, r.Location.Lon) {
		return service.Place{}, errors.New("restaurant: response without valid location")
	}
	p := service.Place{Lat: r.Location.Lat, Lon: r.Location.Lon, Address: r.Address}
	ttl := c.TTL
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	c.mu.Lock()
	if c.cache == nil {
		c.cache = map[string]entry{}
	}
	c.cache[id] = entry{p: p, exp: now().Add(ttl)}
	c.mu.Unlock()
	return p, nil
}
