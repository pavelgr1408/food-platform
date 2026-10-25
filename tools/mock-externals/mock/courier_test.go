package mock

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type cr struct {
	t    *testing.T
	srv  *httptest.Server
	hook *received
	pays *atomic.Int32 // вызовы /_admin/gateway/pay
	paid chan string
}

func newCourier(t *testing.T) *cr {
	t.Helper()
	hook := &received{}
	hs := httptest.NewServer(hook.handler([]byte("dev-courier-webhook-secret")))
	t.Cleanup(hs.Close)
	c := &cr{t: t, hook: hook, pays: &atomic.Int32{}, paid: make(chan string, 4)}
	gws := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.pays.Add(1)
		c.paid <- r.URL.Query().Get("id")
		w.WriteHeader(200)
	}))
	t.Cleanup(gws.Close)
	t.Setenv("MOCK_COURIER_WEBHOOK_URL", hs.URL)
	t.Setenv("MOCK_COURIER_PAY_ADMIN_URL", gws.URL+"/_admin/gateway/pay")
	m := &courierMock{}
	env := newEnv() // шаг D = 20 мс
	mux := http.NewServeMux()
	m.Register(mux, env)
	c.srv = httptest.NewServer(mux)
	t.Cleanup(c.srv.Close)
	return c
}

func (c *cr) call(method, path, body string, hdr map[string]string) (int, map[string]any) {
	c.t.Helper()
	req, _ := http.NewRequest(method, c.srv.URL+path, strings.NewReader(body))
	if !strings.HasPrefix(path, "/_") {
		req.Header.Set("Authorization", "Bearer dev-courier-api-key")
	}
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

const crBody = `{"reference":"d1","orderId":"o1","pickup":{"lat":55.75,"lon":37.61},"dropoff":{"lat":55.76,"lon":37.63},"contact":{"name":"Иван","phone":"+7999"}%s}`

func (c *cr) create(key, extra string) (int, map[string]any) {
	return c.call("POST", "/v1/deliveries", strings.Replace(crBody, "%s", extra, 1), map[string]string{"Idempotency-Key": key})
}

func (c *cr) statuses() []string {
	c.hook.mu.Lock()
	defer c.hook.mu.Unlock()
	var out []string
	for _, e := range c.hook.list {
		out = append(out, e["status"].(string))
	}
	return out
}

func TestCourierHappyPathSendsSignedWebhooks(t *testing.T) {
	c := newCourier(t)
	code, j := c.create("k1", "")
	if code != 201 || j["status"] != "searching" || j["id"] == "" {
		t.Fatalf("%d %v", code, j)
	}
	c.hook.waitFor(t, 4)
	got := c.statuses()
	want := []string{"assigned", "picked_up", "en_route", "delivered"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("webhook statuses = %v", got)
		}
	}
	first := c.hook.list[0]
	if first["orderId"] != "o1" || first["deliveryId"] != "k1" || first["courierId"] == nil || first["eventId"] == "" || first["etaAt"] == nil {
		t.Fatalf("%v", first)
	}
	if c.hook.bad != 0 {
		t.Fatal("signatures must verify")
	}
	_, st := c.call("GET", "/v1/deliveries/"+j["id"].(string), "", nil)
	if st["status"] != "delivered" || st["location"] != nil || st["etaAt"] != nil {
		t.Fatalf("%v", st)
	}
	if c.pays.Load() != 0 {
		t.Fatal("no payment collection without a payment link")
	}
}

func TestCourierWalksFromPickupToDropoff(t *testing.T) {
	c := newCourier(t)
	_, j := c.create("walk", "")
	id := j["id"].(string)
	c.call("POST", "/_admin/courier/advance?id="+id+"&status=picked_up", "", nil)
	_, st := c.call("GET", "/v1/deliveries/"+id, "", nil)
	loc, _ := st["location"].(map[string]any)
	if st["status"] != "picked_up" || loc == nil {
		t.Fatalf("%v", st)
	}
	lat, lon := loc["lat"].(float64), loc["lon"].(float64)
	if lat < 55.75 || lat > 55.76 || lon < 37.61 || lon > 37.63 {
		t.Fatalf("location outside the pickup→dropoff segment: %v", loc)
	}
}

func TestCourierCreateIsIdempotentAndValidated(t *testing.T) {
	c := newCourier(t)
	_, a := c.create("same", "")
	code, b := c.create("same", "")
	if code != 200 || a["id"] != b["id"] {
		t.Fatalf("replay must return the same delivery: %d %v %v", code, a, b)
	}
	if code, _ := c.call("POST", "/v1/deliveries", strings.Replace(crBody, "%s", "", 1), nil); code != 400 {
		t.Fatalf("no key: %d", code)
	}
	if code, _ := c.call("POST", "/v1/deliveries", `{"pickup":{"lat":0,"lon":0}}`, map[string]string{"Idempotency-Key": "x"}); code != 400 {
		t.Fatalf("no dropoff: %d", code)
	}
	req, _ := http.NewRequest("GET", c.srv.URL+"/v1/deliveries/x", nil)
	if resp, _ := http.DefaultClient.Do(req); resp == nil || resp.StatusCode != 401 {
		t.Fatalf("auth required")
	}
	if code, _ := c.call("GET", "/v1/deliveries/nope", "", nil); code != 404 {
		t.Fatalf("unknown: %d", code)
	}
}

func TestCourierNoCouriersMode(t *testing.T) {
	c := newCourier(t)
	if code, _ := c.call("POST", "/_admin/courier/mode?couriers=off", "", nil); code != 200 {
		t.Fatal(code)
	}
	_, j := c.create("none", "")
	time.Sleep(150 * time.Millisecond)
	_, st := c.call("GET", "/v1/deliveries/"+j["id"].(string), "", nil)
	if st["status"] != "searching" || c.hook.count() != 0 {
		t.Fatalf("no courier must stay searching: %v hooks=%d", st, c.hook.count())
	}
	// заявку можно снять
	_, st = c.call("POST", "/v1/deliveries/"+j["id"].(string)+"/cancel", "", nil)
	if st["status"] != "failed" || st["reason"] != "cancelled" {
		t.Fatalf("%v", st)
	}
}

func TestCourierUndelivered(t *testing.T) {
	c := newCourier(t)
	c.call("POST", "/_admin/courier/next?outcome=undelivered", "", nil)
	c.create("u", "")
	c.hook.waitFor(t, 4)
	got := c.statuses()
	if got[3] != "failed" || c.hook.list[3]["reason"] != "undelivered" {
		t.Fatalf("%v %v", got, c.hook.list[3])
	}
}

func TestCourierCancelAfterPickupKeepsDelivery(t *testing.T) {
	c := newCourier(t)
	_, j := c.create("c", "")
	id := j["id"].(string)
	c.call("POST", "/_admin/courier/advance?id="+id+"&status=en_route", "", nil)
	_, st := c.call("POST", "/v1/deliveries/"+id+"/cancel", "", nil)
	if st["status"] != "en_route" {
		t.Fatalf("a courier on the way cannot be cancelled: %v", st)
	}
}

func TestCourierCollectsPaymentOnDelivery(t *testing.T) {
	c := newCourier(t)
	extra := `,"payment":{"url":"http://localhost:9091/pay/gw_abc123","amount":{"amount":125000,"currency":"RUB"}}`
	c.create("p", extra)
	c.hook.waitFor(t, 4)
	select {
	case id := <-c.paid:
		if id != "gw_abc123" {
			t.Fatalf("gateway operation id = %q", id)
		}
	case <-time.After(time.Second):
		t.Fatal("the courier mock must complete the payment at the gateway on delivery")
	}
	if c.hook.list[3]["paid"] != true {
		t.Fatalf("delivered webhook must report paid: %v", c.hook.list[3])
	}
}

func TestCourierLatePaymentLink(t *testing.T) {
	c := newCourier(t)
	c.call("POST", "/_admin/courier/mode?couriers=off", "", nil) // заявка стоит: ссылку успеваем передать
	_, j := c.create("late", "")
	id := j["id"].(string)
	if code, _ := c.call("PUT", "/v1/deliveries/"+id+"/payment", `{"url":"http://x/pay/gw_late","amount":{"amount":100,"currency":"RUB"}}`, nil); code != 204 {
		t.Fatalf("set payment: %d", code)
	}
	if code, _ := c.call("PUT", "/v1/deliveries/nope/payment", `{"url":"u"}`, nil); code != 404 {
		t.Fatalf("unknown: %d", code)
	}
	if code, _ := c.call("PUT", "/v1/deliveries/"+id+"/payment", `{}`, nil); code != 400 {
		t.Fatalf("empty url: %d", code)
	}
	c.call("POST", "/_admin/courier/advance?id="+id+"&status=delivered", "", nil)
	select {
	case got := <-c.paid:
		if got != "gw_late" {
			t.Fatalf("got %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("payment must be collected for the late link")
	}
}

func TestCourierRedeliverAndFailureModes(t *testing.T) {
	c := newCourier(t)
	_, j := c.create("r", "")
	c.hook.waitFor(t, 1)
	id := j["id"].(string)
	first := c.hook.list[0]["eventId"]
	if code, _ := c.call("POST", "/_admin/courier/redeliver?id="+id, "", nil); code != 202 {
		t.Fatalf("redeliver: %d", code)
	}
	c.hook.waitFor(t, 3)
	dups := 0
	c.hook.mu.Lock()
	for _, e := range c.hook.list {
		if e["eventId"] == first {
			dups++
		}
	}
	c.hook.mu.Unlock()
	if dups < 2 {
		t.Fatalf("redelivery must repeat the same eventId (%d)", dups)
	}
	c.call("POST", "/_admin/courier/mode?create=error", "", nil)
	if code, _ := c.create("e", ""); code != 503 {
		t.Fatalf("create=error: %d", code)
	}
	c.call("POST", "/_admin/courier/mode?create=reject", "", nil)
	if code, _ := c.create("e2", ""); code != 422 {
		t.Fatalf("create=reject: %d", code)
	}
	c.call("POST", "/_admin/courier/mode?create=ok&status=error", "", nil)
	if code, _ := c.call("GET", "/v1/deliveries/"+id, "", nil); code != 503 {
		t.Fatalf("status=error: %d", code)
	}
	if code, _ := c.call("POST", "/_admin/courier/mode?couriers=maybe", "", nil); code != 400 {
		t.Fatalf("bad mode: %d", code)
	}
	if code, _ := c.call("POST", "/_admin/courier/reset", "", nil); code != 204 {
		t.Fatalf("reset: %d", code)
	}
	if code, _ := c.call("GET", "/v1/deliveries/"+id, "", nil); code != 404 {
		t.Fatalf("after reset: %d", code)
	}
}
