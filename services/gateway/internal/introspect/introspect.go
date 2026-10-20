// Package introspect — онлайн-проверка токена через auth /auth/introspect с коротким кэшем.
// Нужна для чувствительных маршрутов (деньги): заблокированного пользователя отсекаем сразу,
// не дожидаясь истечения access-токена. Кэш ограничивает нагрузку на auth и срок «слепоты».
package introspect

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/pavelgr1408/food-platform/pkg/httpx"
)

// ErrUnavailable — auth не ответил корректно (сеть, таймаут, 5xx, разомкнутый выключатель).
// Вызывающий обязан отказать в запросе (fail-closed).
var ErrUnavailable = errors.New("introspection unavailable")

type entry struct {
	active  bool
	expires time.Time
}

// Client спрашивает auth и кэширует ответ по идентификатору токена.
type Client struct {
	baseURL string
	http    *httpx.Client
	ttl     time.Duration
	now     func() time.Time

	mu    sync.Mutex
	cache map[string]entry
}

// New создаёт клиент. authBaseURL — адрес auth без пути, например http://auth:8081.
func New(authBaseURL string, ttl, timeout time.Duration) *Client {
	return &Client{
		baseURL: authBaseURL,
		// MaxRetries -1: повторять POST нельзя без Idempotency-Key, а выключатель оберегает auth от лавины.
		http:  httpx.NewClient(httpx.ClientConfig{Timeout: timeout, MaxRetries: -1}),
		ttl:   ttl,
		now:   time.Now,
		cache: map[string]entry{},
	}
}

const maxEntries = 10000

// Active сообщает, активен ли токен у auth. cacheKey — стабильный идентификатор токена (jti);
// notAfter — срок действия токена: в кэше запись не живёт дольше него.
func (c *Client) Active(ctx context.Context, cacheKey, rawToken string, notAfter time.Time) (bool, error) {
	now := c.now()
	c.mu.Lock()
	if e, ok := c.cache[cacheKey]; ok && now.Before(e.expires) {
		c.mu.Unlock()
		return e.active, nil
	}
	c.mu.Unlock()

	body, _ := json.Marshal(map[string]string{"token": rawToken})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/auth/introspect", bytes.NewReader(body))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return false, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return false, fmt.Errorf("%w: status %d", ErrUnavailable, resp.StatusCode)
	}
	var out struct {
		Active bool `json:"active"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&out); err != nil {
		return false, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}

	exp := now.Add(c.ttl)
	if !notAfter.IsZero() && notAfter.Before(exp) {
		exp = notAfter
	}
	c.mu.Lock()
	if len(c.cache) >= maxEntries {
		for k, e := range c.cache {
			if !now.Before(e.expires) {
				delete(c.cache, k)
			}
		}
		if len(c.cache) >= maxEntries {
			c.cache = map[string]entry{}
		}
	}
	c.cache[cacheKey] = entry{active: out.Active, expires: exp}
	c.mu.Unlock()
	return out.Active, nil
}
