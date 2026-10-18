package httpx

import (
	"context"
	"errors"
	"io"
	"math/rand/v2"
	"net/http"
	"sync"
	"time"

	"github.com/pavelgr1408/food-platform/pkg/logging"
)

// ErrCircuitOpen возвращается, пока выключатель разомкнут.
var ErrCircuitOpen = errors.New("httpx: circuit breaker is open")

// ClientConfig — настройки исходящего клиента (межсервисные вызовы, напр. orders→menu).
type ClientConfig struct {
	Timeout          time.Duration     // на одну попытку, включая чтение тела (по умолчанию 800ms)
	MaxRetries       int               // дополнительные попытки (по умолчанию 1)
	BackoffBase      time.Duration     // базовая задержка, экспонента + jitter (по умолчанию 50ms)
	BackoffMax       time.Duration     // потолок задержки (по умолчанию 1s)
	BreakerThreshold int               // подряд неудач до размыкания (по умолчанию 5)
	BreakerOpenFor   time.Duration     // сколько держать разомкнутым (по умолчанию 10s)
	Transport        http.RoundTripper // для тестов
}

func (c *ClientConfig) defaults() {
	if c.Timeout <= 0 {
		c.Timeout = 800 * time.Millisecond
	}
	if c.MaxRetries < 0 {
		c.MaxRetries = 0
	} else if c.MaxRetries == 0 {
		c.MaxRetries = 1
	}
	if c.BackoffBase <= 0 {
		c.BackoffBase = 50 * time.Millisecond
	}
	if c.BackoffMax <= 0 {
		c.BackoffMax = time.Second
	}
	if c.BreakerThreshold <= 0 {
		c.BreakerThreshold = 5
	}
	if c.BreakerOpenFor <= 0 {
		c.BreakerOpenFor = 10 * time.Second
	}
}

// Client — HTTP-клиент с таймаутом, ретраями и circuit breaker.
// Ретраятся сетевые ошибки и 429/502/503/504, но только идемпотентные запросы
// (GET/HEAD/OPTIONS/PUT/DELETE либо с заголовком Idempotency-Key) с повторяемым телом.
type Client struct {
	cfg  ClientConfig
	http *http.Client
	br   *breaker
}

// NewClient создаёт клиент.
func NewClient(cfg ClientConfig) *Client {
	cfg.defaults()
	return &Client{
		cfg:  cfg,
		http: &http.Client{Timeout: cfg.Timeout, Transport: cfg.Transport},
		br:   &breaker{threshold: cfg.BreakerThreshold, openFor: cfg.BreakerOpenFor, now: time.Now},
	}
}

// Do выполняет запрос. 5xx после исчерпания попыток возвращаются как есть (caller решает).
func (c *Client) Do(req *http.Request) (*http.Response, error) {
	ctx := req.Context()
	propagate(ctx, req)
	retryable := idempotent(req) && (req.Body == nil || req.Body == http.NoBody || req.GetBody != nil)
	attempts := 1
	if retryable {
		attempts += c.cfg.MaxRetries
	}
	var lastErr error
	for i := 0; i < attempts; i++ {
		if i > 0 {
			if err := sleep(ctx, c.backoff(i)); err != nil {
				return nil, err
			}
			if req.GetBody != nil {
				body, err := req.GetBody()
				if err != nil {
					return nil, err
				}
				req.Body = body
			}
		}
		if !c.br.allow() {
			return nil, ErrCircuitOpen
		}
		resp, err := c.http.Do(req)
		failed := err != nil || resp.StatusCode >= 500
		c.br.record(!failed)
		if err == nil && !retryStatus(resp.StatusCode) {
			return resp, nil
		}
		if err == nil {
			if i == attempts-1 {
				return resp, nil
			}
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			lastErr = nil
			continue
		}
		lastErr = err
		if ctx.Err() != nil {
			return nil, err
		}
	}
	return nil, lastErr
}

func (c *Client) backoff(attempt int) time.Duration {
	d := c.cfg.BackoffBase << (attempt - 1)
	if d > c.cfg.BackoffMax || d <= 0 {
		d = c.cfg.BackoffMax
	}
	return d/2 + time.Duration(rand.Int64N(int64(d/2)+1))
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func idempotent(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodPut, http.MethodDelete:
		return true
	}
	return r.Header.Get("Idempotency-Key") != ""
}

func retryStatus(code int) bool {
	return code == http.StatusTooManyRequests || code == http.StatusBadGateway ||
		code == http.StatusServiceUnavailable || code == http.StatusGatewayTimeout
}

// propagate пробрасывает X-Request-Id и traceparent (тот же trace id, новый span id).
func propagate(ctx context.Context, req *http.Request) {
	if id := logging.RequestID(ctx); id != "" && req.Header.Get(HeaderRequestID) == "" {
		req.Header.Set(HeaderRequestID, id)
	}
	if t := logging.TraceID(ctx); t != "" && req.Header.Get("traceparent") == "" {
		req.Header.Set("traceparent", "00-"+t+"-"+randHex(8)+"-01")
	}
}

// breaker — простой consecutive-failures circuit breaker (closed → open → half-open).
type breaker struct {
	mu        sync.Mutex
	threshold int
	openFor   time.Duration
	now       func() time.Time

	failures int
	openedAt time.Time
	open     bool
	probing  bool
}

func (b *breaker) allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.open {
		return true
	}
	if b.now().Sub(b.openedAt) < b.openFor || b.probing {
		return false
	}
	b.probing = true // half-open: пропускаем ровно один пробный запрос
	return true
}

func (b *breaker) record(ok bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if ok {
		b.failures, b.open, b.probing = 0, false, false
		return
	}
	b.failures++
	if b.open || b.failures >= b.threshold {
		b.open, b.probing, b.openedAt = true, false, b.now()
	}
}
