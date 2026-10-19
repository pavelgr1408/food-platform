package idempotency_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/pavelgr1408/food-platform/pkg/idempotency"
	"github.com/pavelgr1408/food-platform/pkg/pgxutil"
	"github.com/pavelgr1408/food-platform/pkg/pgxutil/pgtest"
)

func newStore(t *testing.T) (*idempotency.Store, context.Context) {
	t.Helper()
	pool := pgtest.New(t)
	ctx := context.Background()
	if err := pgxutil.Migrate(ctx, pool, nil, idempotency.Migrations()); err != nil {
		t.Fatal(err)
	}
	return idempotency.NewStore(pool), ctx
}

func post(h http.Handler, body, key string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/orders", strings.NewReader(body))
	if key != "" {
		req.Header.Set(idempotency.Header, key)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// handler имитирует createOrder: каждый реальный вызов создаёт «новый заказ».
func orderHandler(calls *atomic.Int32, delay time.Duration) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		time.Sleep(delay)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Location", fmt.Sprintf("/orders/%d", n))
		w.WriteHeader(201)
		fmt.Fprintf(w, `{"orderId":"order-%d"}`, n)
	})
}

func TestReplaySameResponseNoSecondEffect(t *testing.T) {
	s, _ := newStore(t)
	var calls atomic.Int32
	h := s.Middleware(idempotency.Options{Required: true})(orderHandler(&calls, 0))

	a := post(h, `{"x":1}`, "key-1")
	b := post(h, `{"x":1}`, "key-1")
	if a.Code != 201 || b.Code != 201 || a.Body.String() != b.Body.String() || calls.Load() != 1 {
		t.Fatalf("a=%d %q b=%d %q calls=%d", a.Code, a.Body, b.Code, b.Body, calls.Load())
	}
	if b.Header().Get("Idempotent-Replay") != "true" || b.Header().Get("Location") != a.Header().Get("Location") || a.Header().Get("Idempotent-Replay") != "" {
		t.Fatalf("headers: %v / %v", a.Header(), b.Header())
	}
	// новый ключ — новый заказ
	if c := post(h, `{"x":1}`, "key-2"); c.Body.String() == a.Body.String() || calls.Load() != 2 {
		t.Fatal("different key must execute again")
	}
}

func TestKeyReuseWithDifferentBodyAndValidation(t *testing.T) {
	s, _ := newStore(t)
	var calls atomic.Int32
	h := s.Middleware(idempotency.Options{Required: true})(orderHandler(&calls, 0))
	post(h, `{"x":1}`, "k")
	if r := post(h, `{"x":2}`, "k"); r.Code != 422 || !strings.Contains(r.Body.String(), "IDEMPOTENCY_KEY_REUSED") || calls.Load() != 1 {
		t.Fatalf("%d %s", r.Code, r.Body)
	}
	if r := post(h, `{}`, ""); r.Code != 400 || !strings.Contains(r.Body.String(), "IDEMPOTENCY_KEY_REQUIRED") {
		t.Fatalf("%d %s", r.Code, r.Body)
	}
	if r := post(h, `{}`, strings.Repeat("a", 129)); r.Code != 400 {
		t.Fatalf("long key: %d", r.Code)
	}
	opt := s.Middleware(idempotency.Options{Required: false})(orderHandler(&calls, 0))
	before := calls.Load()
	post(opt, `{}`, "")
	post(opt, `{}`, "")
	if calls.Load() != before+2 {
		t.Fatal("optional mode must pass through without key")
	}
}

// DoD S2: параллельные одинаковые Idempotency-Key дают один заказ.
func TestConcurrentSameKeyExecutesOnce(t *testing.T) {
	s, _ := newStore(t)
	var calls atomic.Int32
	h := s.Middleware(idempotency.Options{Required: true})(orderHandler(&calls, 150*time.Millisecond))
	var wg sync.WaitGroup
	var created, conflict, replay atomic.Int32
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := post(h, `{"x":1}`, "race")
			switch {
			case r.Code == 201 && r.Header().Get("Idempotent-Replay") == "":
				created.Add(1)
			case r.Code == 201:
				replay.Add(1)
			case r.Code == 409:
				conflict.Add(1)
			default:
				t.Errorf("unexpected %d %s", r.Code, r.Body)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 || created.Load() != 1 {
		t.Fatalf("handler calls=%d created=%d (conflict=%d replay=%d)", calls.Load(), created.Load(), conflict.Load(), replay.Load())
	}
	if conflict.Load() == 0 {
		t.Log("note: no 409s observed (all duplicates arrived after completion)")
	}
	if r := post(h, `{"x":1}`, "race"); r.Code != 201 || r.Header().Get("Idempotent-Replay") != "true" {
		t.Fatalf("after completion must replay: %d", r.Code)
	}
}

func TestServerErrorReleasesKey(t *testing.T) {
	s, _ := newStore(t)
	var calls atomic.Int32
	h := s.Middleware(idempotency.Options{Required: true})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(201)
	}))
	if r := post(h, `{}`, "k"); r.Code != 503 {
		t.Fatal(r.Code)
	}
	if r := post(h, `{}`, "k"); r.Code != 201 || calls.Load() != 2 {
		t.Fatalf("retry after 5xx must execute: %d calls=%d", r.Code, calls.Load())
	}
}

func TestPanicReleasesKey(t *testing.T) {
	s, _ := newStore(t)
	var calls atomic.Int32
	h := s.Middleware(idempotency.Options{Required: true})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			panic("boom")
		}
		w.WriteHeader(201)
	}))
	func() {
		defer func() { _ = recover() }()
		post(h, `{}`, "k")
	}()
	if r := post(h, `{}`, "k"); r.Code != 201 {
		t.Fatalf("key stuck after panic: %d", r.Code)
	}
}

func TestStaleLockTakeoverAndExpiry(t *testing.T) {
	s, ctx := newStore(t)
	s.LockTimeout = 400 * time.Millisecond // запас: на медленном CI второй Begin может прийти позже 50 мс и «перехватить» замок
	hash := []byte("h")
	if o, _, _ := s.Begin(ctx, "sc", "k", hash); o != idempotency.Acquired {
		t.Fatal("first acquire")
	}
	if o, _, _ := s.Begin(ctx, "sc", "k", hash); o != idempotency.InProgress {
		t.Fatalf("want InProgress, got %v", o)
	}
	time.Sleep(550 * time.Millisecond) // исполнитель «умер»
	if o, _, _ := s.Begin(ctx, "sc", "k", hash); o != idempotency.Acquired {
		t.Fatalf("stale lock must be taken over, got %v", o)
	}
	if o, _, _ := s.Begin(ctx, "sc", "k", []byte("other")); o != idempotency.Mismatch {
		t.Fatalf("want Mismatch, got %v", o)
	}
}

func TestCompleteTxAtomicWithBusinessData(t *testing.T) {
	pool := pgtest.New(t)
	ctx := context.Background()
	if err := pgxutil.Migrate(ctx, pool, nil, idempotency.Migrations()); err != nil {
		t.Fatal(err)
	}
	_, _ = pool.Exec(ctx, `CREATE TABLE orders(id int)`)
	s := idempotency.NewStore(pool)
	h := s.Middleware(idempotency.Options{Required: true})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claim := idempotency.ClaimFrom(r.Context())
		err := pgxutil.InTx(r.Context(), pool, func(tx pgx.Tx) error {
			if _, err := tx.Exec(r.Context(), `INSERT INTO orders VALUES (1)`); err != nil {
				return err
			}
			return claim.CompleteTx(r.Context(), tx, idempotency.Record{Status: 201, Body: []byte(`{"orderId":"o1"}`),
				Headers: map[string]string{"Content-Type": "application/json"}})
		})
		if err != nil {
			w.WriteHeader(500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"orderId":"o1"}`))
	}))
	a := post(h, `{}`, "k")
	b := post(h, `{}`, "k")
	body, _ := io.ReadAll(b.Body)
	var n int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM orders`).Scan(&n)
	if a.Code != 201 || b.Code != 201 || string(body) != `{"orderId":"o1"}` || n != 1 {
		t.Fatalf("a=%d b=%d body=%s orders=%d", a.Code, b.Code, body, n)
	}
}
