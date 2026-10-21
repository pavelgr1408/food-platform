package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pavelgr1408/food-platform/pkg/authclient"
	"github.com/pavelgr1408/food-platform/pkg/idempotency"
	"github.com/pavelgr1408/food-platform/pkg/kafka"
	"github.com/pavelgr1408/food-platform/pkg/metrics"
	"github.com/pavelgr1408/food-platform/pkg/webhook"
	"github.com/pavelgr1408/food-platform/services/payment/internal/gateway"
	"github.com/pavelgr1408/food-platform/services/payment/internal/httpapi"
	"github.com/pavelgr1408/food-platform/services/payment/internal/service"
	"github.com/pavelgr1408/food-platform/services/payment/internal/testkit"
)

var (
	gwSecret  = []byte("gateway-secret-for-tests")
	posSecret = []byte("pos-secret-for-tests")
	ctx       = context.Background()
)

type fixture struct {
	srv  *httptest.Server
	pool *pgxpool.Pool
	gw   *testkit.FakeGateway
	svc  *service.Service
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	pool := testkit.Pool(t)
	gw := testkit.NewGateway()
	svc := service.New(service.Deps{Pool: pool, Gateway: gw, Log: testkit.Log()})
	h := httpapi.New(httpapi.Deps{Log: testkit.Log(), Metrics: metrics.New("payment"), Auth: authclient.DevAuthenticator{},
		Idempotency: idempotency.NewStore(pool), Payments: svc, GatewayWebhookSecret: gwSecret, POSWebhookSecret: posSecret})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &fixture{srv: srv, pool: pool, gw: gw, svc: svc}
}

func (f *fixture) order(t *testing.T, method, owner string, amount int64) string {
	t.Helper()
	id := uuid.NewString()
	b, _ := json.Marshal(map[string]any{"orderId": id, "revision": 1, "orderNumber": "R1-261019-0001", "restaurantId": "r",
		"fulfillmentType": "pickup", "customer": map[string]any{"userId": owner}, "paymentMethod": method,
		"totalAmount": map[string]any{"amount": amount, "currency": "RUB"}})
	ev := kafka.Event{ID: uuid.New(), Topic: service.TopicState, Key: id, Payload: b}
	if _, err := kafka.ProcessOnce(ctx, f.pool, "payment-service", ev, f.svc.HandleEvent); err != nil {
		t.Fatal(err)
	}
	return id
}

// consumeStatus проводит событие orders.status через настоящий обработчик консьюмера.
func (f *fixture) consumeStatus(t *testing.T, payload string) {
	t.Helper()
	ev := kafka.Event{ID: uuid.New(), Topic: service.TopicStatus, Key: "k", Payload: []byte(payload)}
	if _, err := kafka.ProcessOnce(ctx, f.pool, "payment-service", ev, f.svc.HandleEvent); err != nil {
		t.Fatal(err)
	}
}

type resp struct {
	Status int
	Header http.Header
	Body   []byte
}

func (r resp) json(t *testing.T) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(r.Body, &m); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, r.Body)
	}
	return m
}

func (f *fixture) do(t *testing.T, method, path string, body any, hdr map[string]string) resp {
	t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case []byte:
		rd = bytes.NewReader(b)
	case string:
		rd = strings.NewReader(b)
	default:
		raw, _ := json.Marshal(b)
		rd = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest(method, f.srv.URL+path, rd)
	if rd != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	return resp{res.StatusCode, res.Header, raw}
}

func key(k string) map[string]string { return map[string]string{"Idempotency-Key": k} }

func (f *fixture) pay(t *testing.T, order, k string, hdr map[string]string) resp {
	h := key(k)
	for a, b := range hdr {
		h[a] = b
	}
	return f.do(t, "POST", "/orders/"+order+"/payments", map[string]any{"method": "online_card"}, h)
}

func signedWebhook(t *testing.T, f *fixture, path string, secret []byte, body any, at time.Time) resp {
	t.Helper()
	raw, _ := json.Marshal(body)
	return f.do(t, "POST", path, raw, map[string]string{webhook.Header: webhook.Sign(secret, raw, at)})
}

func cb(order, payment, status string, amount int64) map[string]any {
	return map[string]any{"eventId": uuid.NewString(), "orderId": order, "paymentId": payment, "status": status,
		"externalRef": "gw_1", "amount": map[string]any{"amount": amount, "currency": "RUB"}}
}

func withRef(m map[string]any, ref string) map[string]any { m["externalRef"] = ref; return m }

func TestHooks(t *testing.T) {
	f := newFixture(t)
	if r := f.do(t, "GET", "/health/", nil, nil); r.Status != 200 || strings.TrimSpace(string(r.Body)) != `{"status":"OK"}` {
		t.Fatalf("liveness: %d %s", r.Status, r.Body)
	}
	if r := f.do(t, "GET", "/actuator/health", nil, nil); r.Status != 200 {
		t.Fatalf("readiness: %d", r.Status)
	}
	if r := f.do(t, "GET", "/metrics", nil, nil); r.Status != 200 || !strings.Contains(string(r.Body), "http_requests_total") {
		t.Fatalf("metrics: %d", r.Status)
	}
}

func TestInitiateOverHTTPWithIdempotency(t *testing.T) {
	f := newFixture(t)
	o := f.order(t, "online", "", 54990)

	if r := f.do(t, "POST", "/orders/"+o+"/payments", map[string]any{"method": "online_card"}, nil); r.Status != 400 || r.json(t)["code"] != "IDEMPOTENCY_KEY_REQUIRED" {
		t.Fatalf("no key: %d %s", r.Status, r.Body)
	}
	first := f.pay(t, o, "k1", nil)
	if first.Status != 201 {
		t.Fatalf("%d %s", first.Status, first.Body)
	}
	p := first.json(t)
	amount := p["amount"].(map[string]any)
	if p["status"] != "pending" || amount["amount"] != float64(54990) || p["paymentUrl"] == "" || p["attempt"] != float64(1) ||
		first.Header.Get("Location") != "/orders/"+o+"/payments/"+p["id"].(string) {
		t.Fatalf("%v loc=%s", p, first.Header.Get("Location"))
	}
	replay := f.pay(t, o, "k1", nil)
	if replay.Status != 201 || replay.Header.Get("Idempotent-Replay") != "true" || !bytes.Equal(replay.Body, first.Body) {
		t.Fatalf("replay: %d %v\n%s\n%s", replay.Status, replay.Header, replay.Body, first.Body)
	}
	if f.gw.Operations() != 1 {
		t.Fatalf("operations=%d", f.gw.Operations())
	}
	// тот же ключ, другое тело
	r := f.do(t, "POST", "/orders/"+o+"/payments", map[string]any{"method": "online_card", "returnUrl": "https://shop.test/back"}, key("k1"))
	if r.Status != 422 || r.json(t)["code"] != "IDEMPOTENCY_KEY_REUSED" {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	// другой ключ, пока есть живая попытка: 409 с paymentId
	r = f.pay(t, o, "k2", nil)
	if r.Status != 409 || r.json(t)["code"] != "PAYMENT_IN_PROGRESS" || r.json(t)["paymentId"] != p["id"] {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	if r.Header.Get("Content-Type") != "application/problem+json" {
		t.Fatalf("content type %q", r.Header.Get("Content-Type"))
	}
}

func TestInitiateErrorsOverHTTP(t *testing.T) {
	f := newFixture(t)
	r := f.pay(t, uuid.NewString(), "a", nil)
	if r.Status != 404 || r.json(t)["code"] != "ORDER_NOT_FOUND" || r.Header.Get("Retry-After") != "1" {
		t.Fatalf("unknown order: %d %v %s", r.Status, r.Header, r.Body)
	}
	o := f.order(t, "online", "u1", 1000)
	if r := f.pay(t, o, "b", map[string]string{"X-User-Id": "u2"}); r.Status != 404 {
		t.Fatalf("someone else's order: %d", r.Status)
	}
	for name, body := range map[string]string{"bad method": `{"method":"cash"}`, "unknown field": `{"method":"online_card","x":1}`, "not json": `{`} {
		if r := f.do(t, "POST", "/orders/"+o+"/payments", body, map[string]string{"Idempotency-Key": "c-" + name, "X-User-Id": "u1"}); r.Status != 400 {
			t.Errorf("%s: %d %s", name, r.Status, r.Body)
		}
	}
	own := func(k string) map[string]string { return map[string]string{"Idempotency-Key": k, "X-User-Id": "u1"} }
	if r := f.do(t, "POST", "/orders/"+o+"/payments", `{"method":"pos"}`, own("d")); r.Status != 422 || r.json(t)["code"] != "PAYMENT_METHOD_NOT_ALLOWED" {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	if r := f.do(t, "POST", "/orders/"+o+"/payments", `{"method":"online_card","returnUrl":"ftp://x"}`, own("e")); r.Status != 400 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
}

func TestGatewayTimeoutIs202AndSynchronousRefusalIs402(t *testing.T) {
	f := newFixture(t)
	o := f.order(t, "online", "", 1000)
	f.gw.LostReply = testkit.ErrTimeout
	r := f.pay(t, o, "t1", nil)
	if r.Status != 202 || r.json(t)["status"] != "unknown" {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	// повтор оплаты заблокирован
	if r := f.pay(t, o, "t2", nil); r.Status != 409 || r.json(t)["code"] != "PAYMENT_STATUS_UNKNOWN" {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	// 5xx/202 в ответе клиенту, но 202 под ключом запоминается: повтор того же ключа вернёт тот же ответ
	if r := f.pay(t, o, "t1", nil); r.Status != 202 || r.Header.Get("Idempotent-Replay") != "true" {
		t.Fatalf("%d %v", r.Status, r.Header)
	}

	g := newFixture(t)
	o2 := g.order(t, "online", "", 1000)
	g.gw.CreateErr = &gateway.RejectedError{HTTPStatus: 422, Code: "x"}
	if r := g.pay(t, o2, "r1", nil); r.Status != 402 || r.json(t)["code"] != "PAYMENT_DECLINED" {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
}

func TestGetPaymentAndMethodsOverHTTP(t *testing.T) {
	f := newFixture(t)
	o := f.order(t, "online", "u1", 1000)
	h := map[string]string{"X-User-Id": "u1"}
	r := f.do(t, "GET", "/orders/"+o+"/payment-methods", nil, h)
	var ms []map[string]any
	if err := json.Unmarshal(r.Body, &ms); err != nil || r.Status != 200 || len(ms) != 2 || ms[0]["type"] != "online_card" || ms[0]["enabled"] != true {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	created := f.pay(t, o, "g1", h).json(t)
	r = f.do(t, "GET", "/orders/"+o+"/payments/"+created["id"].(string), nil, h)
	if r.Status != 200 || r.json(t)["status"] != "pending" {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	if r := f.do(t, "GET", "/orders/"+o+"/payments/"+created["id"].(string), nil, map[string]string{"X-User-Id": "u2"}); r.Status != 404 {
		t.Fatalf("%d", r.Status)
	}
	if r := f.do(t, "GET", "/orders/"+o+"/payments/"+uuid.NewString(), nil, h); r.Status != 404 || r.json(t)["code"] != "PAYMENT_NOT_FOUND" {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
}

func TestGatewayWebhook(t *testing.T) {
	f := newFixture(t)
	o := f.order(t, "online", "", 54990)
	p := f.pay(t, o, "w1", nil).json(t)["id"].(string)
	path := "/webhooks/payments/gateway"
	now := time.Now()

	// без подписи / чужой секрет / устаревшая метка времени → 401
	raw, _ := json.Marshal(cb(o, p, "paid", 54990))
	if r := f.do(t, "POST", path, raw, nil); r.Status != 401 || r.json(t)["code"] != "INVALID_SIGNATURE" {
		t.Fatalf("no signature: %d %s", r.Status, r.Body)
	}
	if r := signedWebhook(t, f, path, []byte("another-secret-value"), cb(o, p, "paid", 54990), now); r.Status != 401 {
		t.Fatalf("wrong secret: %d", r.Status)
	}
	if r := signedWebhook(t, f, path, gwSecret, cb(o, p, "paid", 54990), now.Add(-time.Hour)); r.Status != 401 {
		t.Fatalf("replay window: %d", r.Status)
	}
	// подпись POS-секретом на канале шлюза не проходит
	if r := signedWebhook(t, f, path, posSecret, cb(o, p, "paid", 54990), now); r.Status != 401 {
		t.Fatalf("cross-channel secret: %d", r.Status)
	}
	if st := f.do(t, "GET", "/orders/"+o+"/payments/"+p, nil, nil).json(t)["status"]; st != "pending" {
		t.Fatalf("rejected webhooks must not change the payment: %v", st)
	}

	// неверная сумма → 422, статус не меняется
	if r := signedWebhook(t, f, path, gwSecret, cb(o, p, "paid", 1), now); r.Status != 422 || r.json(t)["code"] != "AMOUNT_MISMATCH" {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	// дробная сумма (ADR-13) → 400
	if r := signedWebhook(t, f, path, gwSecret, map[string]any{"eventId": uuid.NewString(), "orderId": o, "paymentId": p, "status": "paid",
		"amount": map[string]any{"amount": 549.9, "currency": "RUB"}}, now); r.Status != 400 {
		t.Fatalf("float amount: %d %s", r.Status, r.Body)
	}
	// битый JSON с правильной подписью → 400
	bad := []byte("{")
	if r := f.do(t, "POST", path, bad, map[string]string{webhook.Header: webhook.Sign(gwSecret, bad, now)}); r.Status != 400 {
		t.Fatalf("%d", r.Status)
	}

	good := cb(o, p, "paid", 54990)
	if r := signedWebhook(t, f, path, gwSecret, good, now); r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	if r := signedWebhook(t, f, path, gwSecret, good, now); r.Status != 200 { // дубликат доставки
		t.Fatalf("duplicate: %d %s", r.Status, r.Body)
	}
	got := f.do(t, "GET", "/orders/"+o+"/payments/"+p, nil, nil).json(t)
	if got["status"] != "paid" || got["paidAt"] == nil {
		t.Fatalf("%v", got)
	}
	var events int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE topic = 'orders.payments.status' AND payload->>'status' = 'paid'`).Scan(&events); err != nil {
		// payload — bytea: приводим
		if err2 := f.pool.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE topic = 'orders.payments.status' AND convert_from(payload,'UTF8')::jsonb->>'status' = 'paid'`).Scan(&events); err2 != nil {
			t.Fatal(err2)
		}
	}
	if events != 1 {
		t.Fatalf("exactly one paid event, got %d", events)
	}
	// неизвестный платёж → 404
	if r := signedWebhook(t, f, path, gwSecret, cb(o, uuid.NewString(), "paid", 54990), now); r.Status != 404 {
		t.Fatalf("%d", r.Status)
	}
}

func TestPOSWebhookOnlyForPOSPayments(t *testing.T) {
	f := newFixture(t)
	o := f.order(t, "online", "", 1000)
	p := f.pay(t, o, "p1", nil).json(t)["id"].(string)
	// платёж онлайн-картой не может быть подтверждён по каналу кассы, даже с верной подписью
	if r := signedWebhook(t, f, "/webhooks/payments/pos", posSecret, cb(o, p, "paid", 1000), time.Now()); r.Status != 404 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	// POS-платёж (создаёт initiatePosPayment) принимается: вставляем напрямую
	posOrder := f.order(t, "restaurant", "", 2000)
	pid := uuid.NewString()
	if _, err := f.pool.Exec(ctx, `INSERT INTO payments (id, order_id, attempt, method, status, amount, currency, created_at, updated_at, tender)
		VALUES ($1,$2,1,'pos','pending',2000,'RUB',now(),now(),'card')`, pid, posOrder); err != nil {
		t.Fatal(err)
	}
	if r := signedWebhook(t, f, "/webhooks/payments/pos", posSecret, withRef(cb(posOrder, pid, "paid", 2000), "pos_1"), time.Now()); r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	var st string
	if err := f.pool.QueryRow(ctx, `SELECT status FROM payments WHERE id = $1`, pid).Scan(&st); err != nil || st != "paid" {
		t.Fatalf("%q %v", st, err)
	}
}

func TestWebhookDisabledWithoutSecret(t *testing.T) {
	pool := testkit.Pool(t)
	svc := service.New(service.Deps{Pool: pool, Gateway: testkit.NewGateway(), Log: testkit.Log()})
	h := httpapi.New(httpapi.Deps{Log: testkit.Log(), Metrics: metrics.New("payment"), Auth: authclient.DevAuthenticator{}, Payments: svc})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/webhooks/payments/gateway", strings.NewReader("{}")))
	if rec.Code != 503 {
		t.Fatalf("a webhook without a configured secret must not be accepted, got %d", rec.Code)
	}
}
