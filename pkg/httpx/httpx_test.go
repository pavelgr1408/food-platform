package httpx

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pavelgr1408/food-platform/pkg/logging"
)

func discard() *slog.Logger { return slog.New(slog.NewJSONHandler(io.Discard, nil)) }

func TestHealthEndpoints(t *testing.T) {
	mux := http.NewServeMux()
	RegisterHealth(mux, Check{"db", func(context.Context) error { return nil }})
	for _, p := range []string{"/health", "/health/"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", p, nil))
		if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"status":"OK"}` {
			t.Fatalf("%s: %d %q", p, rec.Code, rec.Body.String())
		}
	}
	bad := http.NewServeMux()
	RegisterHealth(bad, Check{"db", func(context.Context) error { return errors.New("down") }})
	rec := httptest.NewRecorder()
	bad.ServeHTTP(rec, httptest.NewRequest("GET", "/actuator/health", nil))
	if rec.Code != 503 || !strings.Contains(rec.Body.String(), `"DOWN"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestStandardMiddlewareRecoverAndRequestID(t *testing.T) {
	var buf bytes.Buffer
	log := logging.New(logging.Options{Writer: &buf, Level: "debug"})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /boom", func(http.ResponseWriter, *http.Request) { panic("x") })
	mux.HandleFunc("GET /ok", func(w http.ResponseWriter, r *http.Request) {
		if logging.RequestID(r.Context()) == "" || len(logging.TraceID(r.Context())) != 32 {
			t.Errorf("ctx ids: %q %q", logging.RequestID(r.Context()), logging.TraceID(r.Context()))
		}
		w.WriteHeader(204)
	})
	h := Standard(log)(mux)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/boom", nil))
	if rec.Code != 500 || rec.Header().Get("Content-Type") != ContentTypeProblem || rec.Header().Get(HeaderRequestID) == "" {
		t.Fatalf("recover: %d %v", rec.Code, rec.Header())
	}
	req := httptest.NewRequest("GET", "/ok", nil)
	req.Header.Set(HeaderRequestID, "abc-1")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 204 || rec.Header().Get(HeaderRequestID) != "abc-1" {
		t.Fatalf("%d", rec.Code)
	}
	if !strings.Contains(buf.String(), `"route":"GET /ok"`) {
		t.Fatalf("access log lacks route: %s", buf.String())
	}
	// небезопасный request id заменяется
	req = httptest.NewRequest("GET", "/ok", nil)
	req.Header.Set(HeaderRequestID, "bad id\r\n")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if got := rec.Header().Get(HeaderRequestID); got == "" || strings.ContainsAny(got, " \r\n") {
		t.Fatalf("unsafe id passed: %q", got)
	}
}

func TestDecodeJSON(t *testing.T) {
	type in struct{ A int }
	cases := []struct {
		name, body, ct string
		want           int
	}{
		{"ok", `{"A":1}`, "application/json", 0},
		{"unknown field", `{"A":1,"B":2}`, "application/json", 400},
		{"trailing", `{"A":1}{"A":2}`, "application/json", 400},
		{"bad ct", `{"A":1}`, "text/plain", 415},
		{"garbage", `{`, "application/json", 400},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/", strings.NewReader(c.body))
		req.Header.Set("Content-Type", c.ct)
		var v in
		ok := DecodeJSON(rec, req, &v)
		if (c.want == 0) != ok || (!ok && rec.Code != c.want) {
			t.Errorf("%s: ok=%v code=%d", c.name, ok, rec.Code)
		}
	}
}

func TestHandleError(t *testing.T) {
	rec := httptest.NewRecorder()
	HandleError(rec, httptest.NewRequest("GET", "/x", nil), discard(), NewError(409, "ITEM_UNAVAILABLE", "gone"))
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), `"code":"ITEM_UNAVAILABLE"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	HandleError(rec, httptest.NewRequest("GET", "/x", nil), discard(), errors.New("secret db failure"))
	if rec.Code != 500 || strings.Contains(rec.Body.String(), "secret") {
		t.Fatalf("leak: %s", rec.Body.String())
	}
}

func fastCfg() ClientConfig {
	return ClientConfig{Timeout: time.Second, MaxRetries: 2, BackoffBase: time.Millisecond, BackoffMax: 2 * time.Millisecond, BreakerThreshold: 3, BreakerOpenFor: time.Hour}
}

func TestClientRetriesIdempotent(t *testing.T) {
	var n atomic.Int32
	var gotReqID, gotTP string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotReqID, gotTP = r.Header.Get(HeaderRequestID), r.Header.Get("traceparent")
		if n.Add(1) < 3 {
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()
	c := NewClient(fastCfg())
	ctx := logging.WithTraceID(logging.WithRequestID(context.Background(), "rid"), strings.Repeat("a", 32))
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL, nil)
	resp, err := c.Do(req)
	if err != nil || resp.StatusCode != 200 || n.Load() != 3 {
		t.Fatalf("err=%v resp=%v calls=%d", err, resp, n.Load())
	}
	resp.Body.Close()
	if gotReqID != "rid" || !strings.HasPrefix(gotTP, "00-"+strings.Repeat("a", 32)+"-") {
		t.Fatalf("propagation: %q %q", gotReqID, gotTP)
	}
}

func TestClientNoRetryForPlainPOST_RetryWithKey(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if string(b) != "payload" {
			t.Errorf("body lost on retry: %q", b)
		}
		n.Add(1)
		w.WriteHeader(503)
	}))
	defer srv.Close()
	cfg := fastCfg()
	cfg.BreakerThreshold = 100
	c := NewClient(cfg)
	req, _ := http.NewRequest("POST", srv.URL, strings.NewReader("payload"))
	resp, _ := c.Do(req)
	resp.Body.Close()
	if n.Load() != 1 {
		t.Fatalf("plain POST retried: %d", n.Load())
	}
	n.Store(0)
	req, _ = http.NewRequest("POST", srv.URL, strings.NewReader("payload"))
	req.Header.Set("Idempotency-Key", "k")
	resp, _ = c.Do(req)
	resp.Body.Close()
	if n.Load() != 3 {
		t.Fatalf("keyed POST calls=%d want 3", n.Load())
	}
}

func TestCircuitBreakerOpensAndRecovers(t *testing.T) {
	var n atomic.Int32
	healthy := atomic.Bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		if healthy.Load() {
			w.WriteHeader(200)
			return
		}
		w.WriteHeader(500)
	}))
	defer srv.Close()
	cfg := fastCfg()
	cfg.MaxRetries = -1 // без ретраев: считаем попытки точно
	c := NewClient(cfg)
	now := time.Now()
	c.br.now = func() time.Time { return now }
	do := func() error {
		req, _ := http.NewRequest("GET", srv.URL, nil)
		resp, err := c.Do(req)
		if resp != nil {
			resp.Body.Close()
		}
		return err
	}
	for i := 0; i < 3; i++ {
		_ = do()
	}
	before := n.Load()
	if err := do(); !errors.Is(err, ErrCircuitOpen) || n.Load() != before {
		t.Fatalf("breaker should short-circuit: err=%v calls %d→%d", err, before, n.Load())
	}
	now = now.Add(2 * time.Hour) // half-open
	healthy.Store(true)
	if err := do(); err != nil {
		t.Fatalf("probe: %v", err)
	}
	if err := do(); err != nil {
		t.Fatalf("closed again: %v", err)
	}
}

func TestRunGracefulShutdown(t *testing.T) {
	srv := NewServer("127.0.0.1:0", http.NewServeMux())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, srv, discard(), time.Second) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown timeout")
	}
}
