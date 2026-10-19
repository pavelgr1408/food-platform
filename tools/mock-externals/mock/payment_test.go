package mock

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pavelgr1408/food-platform/pkg/webhook"
)

type received struct {
	mu   sync.Mutex
	list []map[string]any
	code int // что отвечать получателю
	bad  int // подписей, не прошедших проверку
}

func (r *received) handler(secret []byte) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		if err := webhook.Verify(secret, body, req.Header.Get(webhook.Header), time.Now(), 0); err != nil {
			r.mu.Lock()
			r.bad++
			r.mu.Unlock()
			w.WriteHeader(401)
			return
		}
		var m map[string]any
		_ = json.Unmarshal(body, &m)
		r.mu.Lock()
		r.list = append(r.list, m)
		code := r.code
		r.mu.Unlock()
		if code == 0 {
			code = 200
		}
		w.WriteHeader(code)
	})
}

func (r *received) count() int { r.mu.Lock(); defer r.mu.Unlock(); return len(r.list) }

func (r *received) waitFor(t *testing.T, n int) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if r.count() >= n {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("want %d webhooks, got %d", n, r.count())
}

type gw struct {
	t    *testing.T
	srv  *httptest.Server
	hook *received
}

func newGW(t *testing.T) *gw {
	t.Helper()
	hook := &received{}
	secret := []byte("dev-gateway-webhook-secret")
	hs := httptest.NewServer(hook.handler(secret))
	t.Cleanup(hs.Close)
	t.Setenv("MOCK_PAYMENT_WEBHOOK_URL", hs.URL)
	m := &paymentMock{}
	env := newEnv() // задержка итога 20 мс
	mux := http.NewServeMux()
	m.Register(mux, env)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &gw{t: t, srv: srv, hook: hook}
}

func (g *gw) call(method, path string, body string, hdr map[string]string) (int, map[string]any) {
	g.t.Helper()
	req, _ := http.NewRequest(method, g.srv.URL+path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer dev-gateway-api-key")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := (&http.Client{Timeout: 2 * time.Second}).Do(req)
	if err != nil {
		return 0, nil
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return resp.StatusCode, m
}

func (g *gw) create(key string, amount int64) (int, map[string]any) {
	body := `{"amount":{"amount":` + itoa(amount) + `,"currency":"RUB"},"reference":"pay-` + key + `","metadata":{"orderId":"ord-1"}}`
	return g.call("POST", "/v1/payments", body, map[string]string{"Idempotency-Key": key})
}

func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }

func TestGatewayPaidSendsSignedWebhook(t *testing.T) {
	g := newGW(t)
	code, op := g.create("k1", 54900)
	if code != 201 || op["status"] != "pending" || !strings.HasSuffix(op["confirmationUrl"].(string), "/pay/"+op["id"].(string)) {
		t.Fatalf("%d %v", code, op)
	}
	g.hook.waitFor(t, 1)
	ev := g.hook.list[0]
	amount := ev["amount"].(map[string]any)
	if ev["status"] != "paid" || ev["paymentId"] != "pay-k1" || ev["orderId"] != "ord-1" || ev["externalRef"] != op["id"] || amount["amount"] != float64(54900) || ev["eventId"] == "" {
		t.Fatalf("%v", ev)
	}
	if _, st := g.call("GET", "/v1/payments/"+op["id"].(string), "", nil); st["status"] != "paid" {
		t.Fatalf("%v", st)
	}
}

func TestGatewayCreateIsIdempotentByKey(t *testing.T) {
	g := newGW(t)
	_, a := g.create("same", 1000)
	code, b := g.create("same", 1000)
	if code != 200 || a["id"] != b["id"] {
		t.Fatalf("replay must return the same operation: %d %v %v", code, a, b)
	}
	if code, _ := g.create("same", 2000); code != 422 {
		t.Fatalf("same key with another amount: %d", code)
	}
	if code, _ := g.call("POST", "/v1/payments", `{"amount":{"amount":1,"currency":"RUB"},"reference":"x"}`, nil); code != 400 {
		t.Fatalf("no key: %d", code)
	}
	if code, _ := g.call("POST", "/v1/payments", `{"amount":{"amount":1.5,"currency":"RUB"},"reference":"x"}`, map[string]string{"Idempotency-Key": "z"}); code != 400 {
		t.Fatalf("fractional amount must be rejected: %d", code)
	}
	if g.hook.count() > 1 {
		t.Fatalf("one operation → at most one webhook, got %d", g.hook.count())
	}
}

func TestGatewayRequiresAPIKey(t *testing.T) {
	g := newGW(t)
	req, _ := http.NewRequest("POST", g.srv.URL+"/v1/payments", strings.NewReader(`{}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 401 {
		t.Fatalf("%v %v", resp, err)
	}
}

func TestMagicAmounts(t *testing.T) {
	g := newGW(t)
	_, declined := g.create("d", 54913)
	g.hook.waitFor(t, 1)
	if g.hook.list[0]["status"] != "declined" || g.hook.list[0]["reason"] != "insufficient_funds" {
		t.Fatalf("%v", g.hook.list[0])
	}
	_, silent := g.create("s", 54999)
	time.Sleep(150 * time.Millisecond)
	if g.hook.count() != 1 {
		t.Fatalf("a silent amount must not produce a webhook, got %d", g.hook.count())
	}
	if _, st := g.call("GET", "/v1/payments/"+silent["id"].(string), "", nil); st["status"] != "paid" {
		t.Fatalf("silent operation is paid, visible only via GET: %v", st)
	}
	if _, st := g.call("GET", "/v1/payments/"+declined["id"].(string), "", nil); st["status"] != "declined" {
		t.Fatalf("%v", st)
	}
}

func TestNextOutcomeAndModes(t *testing.T) {
	g := newGW(t)
	g.call("POST", "/_admin/gateway/next?outcome=declined", "", nil)
	g.create("n1", 1000)
	g.hook.waitFor(t, 1)
	if g.hook.list[0]["status"] != "declined" {
		t.Fatalf("%v", g.hook.list[0])
	}
	if code, _ := g.call("POST", "/_admin/gateway/next?outcome=nope", "", nil); code != 400 {
		t.Fatal("bad outcome must be rejected")
	}
	g.call("POST", "/_admin/gateway/mode?webhooks=off", "", nil)
	_, op := g.create("n2", 1000)
	time.Sleep(100 * time.Millisecond)
	if g.hook.count() != 1 {
		t.Fatal("webhooks=off must drop the webhook")
	}
	if _, st := g.call("GET", "/v1/payments/"+op["id"].(string), "", nil); st["status"] != "paid" {
		t.Fatalf("%v", st)
	}
	g.call("POST", "/_admin/gateway/mode?init=error", "", nil)
	if code, _ := g.create("n3", 1000); code != 503 {
		t.Fatalf("%d", code)
	}
	g.call("POST", "/_admin/gateway/mode?init=reject", "", nil)
	if code, _ := g.create("n4", 1000); code != 422 {
		t.Fatalf("%d", code)
	}
	g.call("POST", "/_admin/gateway/mode?status=error", "", nil)
	if code, _ := g.call("GET", "/v1/payments/"+op["id"].(string), "", nil); code != 503 {
		t.Fatalf("%d", code)
	}
	g.call("POST", "/_admin/gateway/reset", "", nil)
	if code, _ := g.create("n5", 1000); code != 201 {
		t.Fatalf("reset must restore normal mode: %d", code)
	}
}

func TestLostReplyStillCreatesOperation(t *testing.T) {
	g := newGW(t)
	g.call("POST", "/_admin/gateway/mode?init=timeout", "", nil)
	if code, _ := g.create("lost", 1000); code != 0 { // клиент отваливается по таймауту
		t.Fatalf("want a client-side timeout, got %d", code)
	}
	g.call("POST", "/_admin/gateway/mode?init=ok", "", nil)
	code, op := g.create("lost", 1000)
	if code != 200 || op["id"] == nil {
		t.Fatalf("retry with the same key must return the already created operation: %d %v", code, op)
	}
	_, all := g.call("GET", "/_inspect/payments", "", nil)
	if n := len(all["operations"].([]any)); n != 1 {
		t.Fatalf("exactly one operation expected, got %d", n)
	}
}

func TestRedeliverSendsDuplicateWithSameEventID(t *testing.T) {
	g := newGW(t)
	_, op := g.create("r1", 1000)
	g.hook.waitFor(t, 1)
	code, _ := g.call("POST", "/_admin/gateway/redeliver?id="+op["id"].(string), "", nil)
	if code != 202 {
		t.Fatalf("%d", code)
	}
	g.hook.waitFor(t, 2)
	if g.hook.list[0]["eventId"] != g.hook.list[1]["eventId"] {
		t.Fatalf("a duplicate must carry the same eventId: %v %v", g.hook.list[0], g.hook.list[1])
	}
	if code, _ := g.call("POST", "/_admin/gateway/redeliver?id=nope", "", nil); code != 404 {
		t.Fatal("unknown operation")
	}
}

func TestWebhookRetriesUntilReceiverAccepts(t *testing.T) {
	g := newGW(t)
	g.hook.mu.Lock()
	g.hook.code = 503
	g.hook.mu.Unlock()
	g.create("w1", 1000)
	g.hook.waitFor(t, 2) // первая попытка + повтор
	g.hook.mu.Lock()
	g.hook.code = 200
	g.hook.mu.Unlock()
	g.hook.waitFor(t, 3)
}

func TestCancel(t *testing.T) {
	g := newGW(t)
	g.call("POST", "/_admin/gateway/mode?webhooks=off", "", nil)
	// слишком большая задержка итога, чтобы операция оставалась pending
	g.call("POST", "/_admin/gateway/next?outcome=silent", "", nil)
	_, op := g.create("c1", 1000)
	code, st := g.call("POST", "/v1/payments/"+op["id"].(string)+"/cancel", "", nil)
	if code != 200 {
		t.Fatalf("%d", code)
	}
	// итог мог наступить до отмены (задержка 20 мс); оба результата допустимы, но он должен быть финальным
	if st["status"] != "declined" && st["status"] != "paid" {
		t.Fatalf("%v", st)
	}
	_, again := g.call("POST", "/v1/payments/"+op["id"].(string)+"/cancel", "", nil)
	if again["status"] != st["status"] {
		t.Fatalf("a final status does not change: %v → %v", st, again)
	}
	if code, _ := g.call("POST", "/v1/payments/nope/cancel", "", nil); code != 404 {
		t.Fatal("unknown")
	}
}
