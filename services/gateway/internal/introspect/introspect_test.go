package introspect

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func fakeAuth(t *testing.T, status int, body string, hits *atomic.Int32) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path != "/auth/introspect" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestActiveIsCachedAndInactiveToo(t *testing.T) {
	var hits atomic.Int32
	srv := fakeAuth(t, 200, `{"active":false}`, &hits)
	c := New(srv.URL, 10*time.Second, time.Second)
	clock := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return clock }
	exp := clock.Add(15 * time.Minute)

	for i := 0; i < 3; i++ {
		active, err := c.Active(context.Background(), "jti-1", "tok", exp)
		if err != nil || active {
			t.Fatalf("active=%v err=%v, want inactive", active, err)
		}
	}
	if hits.Load() != 1 {
		t.Fatalf("auth called %d times, want 1 (cache)", hits.Load())
	}
	clock = clock.Add(11 * time.Second) // TTL истёк → повторный вопрос
	_, _ = c.Active(context.Background(), "jti-1", "tok", exp)
	if hits.Load() != 2 {
		t.Fatalf("auth called %d times after TTL, want 2", hits.Load())
	}
}

func TestCacheNeverOutlivesTheToken(t *testing.T) {
	var hits atomic.Int32
	srv := fakeAuth(t, 200, `{"active":true}`, &hits)
	c := New(srv.URL, time.Hour, time.Second)
	clock := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return clock }
	_, _ = c.Active(context.Background(), "j", "tok", clock.Add(2*time.Second))
	clock = clock.Add(3 * time.Second)
	_, _ = c.Active(context.Background(), "j", "tok", clock.Add(time.Minute))
	if hits.Load() != 2 {
		t.Fatalf("hits=%d: entry must expire with the token", hits.Load())
	}
}

func TestFailClosed(t *testing.T) {
	var hits atomic.Int32
	for name, srv := range map[string]*httptest.Server{
		"5xx":      fakeAuth(t, 503, `down`, &hits),
		"not json": fakeAuth(t, 200, `<html>`, &hits),
		"404":      fakeAuth(t, 404, ``, &hits),
	} {
		c := New(srv.URL, time.Second, time.Second)
		if _, err := c.Active(context.Background(), "j", "tok", time.Time{}); !errors.Is(err, ErrUnavailable) {
			t.Errorf("%s: want ErrUnavailable, got %v", name, err)
		}
	}
	dead := httptest.NewServer(http.NotFoundHandler())
	url := dead.URL
	dead.Close()
	c := New(url, time.Second, 200*time.Millisecond)
	if _, err := c.Active(context.Background(), "j", "tok", time.Time{}); !errors.Is(err, ErrUnavailable) {
		t.Errorf("connection refused: want ErrUnavailable, got %v", err)
	}
}
