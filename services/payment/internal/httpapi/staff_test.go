package httpapi_test

import (
	"bytes"
	"fmt"
	"testing"
	"time"

	"github.com/pavelgr1408/food-platform/pkg/authclient"
	"github.com/pavelgr1408/food-platform/pkg/idempotency"
	"github.com/pavelgr1408/food-platform/pkg/metrics"
	"github.com/pavelgr1408/food-platform/services/payment/internal/httpapi"
	"github.com/pavelgr1408/food-platform/services/payment/internal/service"
	"github.com/pavelgr1408/food-platform/services/payment/internal/testkit"

	"net/http/httptest"
)

type subject struct {
	name string
	hdr  map[string]string
}

func subjects() []subject {
	h := func(id, roles, rests string) map[string]string {
		m := map[string]string{"X-User-Id": id, "X-User-Roles": roles}
		if rests != "" {
			m["X-Restaurant-Ids"] = rests
		}
		return m
	}
	return []subject{
		{"guest", nil},
		{"customer", h("u-1", "CUSTOMER", "")},
		{"staff-own", h("s-1", "STAFF", "r")},
		{"staff-other", h("s-2", "STAFF", "other")},
		{"owner-own", h("o-1", "FRANCHISE_OWNER", "r")},
		{"owner-other", h("o-2", "FRANCHISE_OWNER", "other")},
		{"hq", h("h-1", "HQ_STAFF", "")},
	}
}

func authz(name string) (int, string) {
	switch name {
	case "guest":
		return 401, "UNAUTHORIZED"
	case "customer":
		return 403, "FORBIDDEN"
	case "staff-other", "owner-other":
		return 404, "ORDER_NOT_FOUND"
	}
	return 201, ""
}

// posFixture — HTTP-слой с отдельной кассой.
func posFixture(t *testing.T) (*fixture, *testkit.FakeGateway) {
	t.Helper()
	pool := testkit.Pool(t)
	gw, pos := testkit.NewGateway(), testkit.NewGateway()
	svc := service.New(service.Deps{Pool: pool, Gateway: gw, POS: pos, Log: testkit.Log()})
	h := httpapi.New(httpapi.Deps{Log: testkit.Log(), Metrics: metrics.New("payment"), Auth: authclient.DevAuthenticator{},
		Idempotency: idempotency.NewStore(pool), Payments: svc, GatewayWebhookSecret: gwSecret, POSWebhookSecret: posSecret})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &fixture{srv: srv, pool: pool, gw: gw, svc: svc}, pos
}

func (f *fixture) posPay(t *testing.T, order, k string, hdr map[string]string, body any) resp {
	t.Helper()
	h := map[string]string{"Idempotency-Key": k}
	for a, b := range hdr {
		h[a] = b
	}
	return f.do(t, "POST", "/orders/"+order+"/payments/pos", body, h)
}

func TestInitiatePosRightsMatrixOverHTTP(t *testing.T) {
	for _, s := range subjects() {
		t.Run(s.name, func(t *testing.T) {
			f, pos := posFixture(t)
			o := f.order(t, "restaurant", "", 25000)
			r := f.posPay(t, o, "k-"+s.name, s.hdr, map[string]any{"tender": "card"})
			wantStatus, wantCode := authz(s.name)
			if r.Status != wantStatus {
				t.Fatalf("%d %s", r.Status, r.Body)
			}
			if wantCode != "" {
				if r.json(t)["code"] != wantCode {
					t.Fatalf("%s", r.Body)
				}
				if pos.Operations() != 0 {
					t.Fatal("a rejected request must not reach the POS")
				}
				return
			}
			p := r.json(t)
			if p["method"] != "pos" || p["tender"] != "card" || p["status"] != "pending" ||
				r.Header.Get("Location") != "/orders/"+o+"/payments/"+p["id"].(string) {
				t.Fatalf("%v", p)
			}
			if pos.Operations() != 1 {
				t.Fatalf("pos operations=%d", pos.Operations())
			}
		})
	}
}

func TestInitiatePosValidationAndIdempotency(t *testing.T) {
	f, pos := posFixture(t)
	staff := subjects()[2].hdr
	o := f.order(t, "restaurant", "", 25000)

	if r := f.do(t, "POST", "/orders/"+o+"/payments/pos", map[string]any{"tender": "card"}, staff); r.Status != 400 || r.json(t)["code"] != "IDEMPOTENCY_KEY_REQUIRED" {
		t.Fatalf("no key: %d %s", r.Status, r.Body)
	}
	for name, body := range map[string]any{"bad tender": map[string]any{"tender": "crypto"}, "no tender": map[string]any{},
		"unknown field": map[string]any{"tender": "card", "x": 1}, "not json": "{"} {
		if r := f.posPay(t, o, "v-"+name, staff, body); r.Status != 400 {
			t.Errorf("%s: %d %s", name, r.Status, r.Body)
		}
	}
	first := f.posPay(t, o, "k1", staff, map[string]any{"tender": "cash"})
	if first.Status != 201 || first.json(t)["tender"] != "cash" {
		t.Fatalf("%d %s", first.Status, first.Body)
	}
	replay := f.posPay(t, o, "k1", staff, map[string]any{"tender": "cash"})
	if replay.Status != 201 || replay.Header.Get("Idempotent-Replay") != "true" || !bytes.Equal(replay.Body, first.Body) {
		t.Fatalf("replay: %d %v", replay.Status, replay.Header)
	}
	if r := f.posPay(t, o, "k1", staff, map[string]any{"tender": "card"}); r.Status != 422 || r.json(t)["code"] != "IDEMPOTENCY_KEY_REUSED" {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	// другой ключ при живой попытке — 409 с paymentId
	r := f.posPay(t, o, "k2", staff, map[string]any{"tender": "cash"})
	if r.Status != 409 || r.json(t)["code"] != "PAYMENT_IN_PROGRESS" || r.json(t)["paymentId"] != first.json(t)["id"] {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	if pos.Operations() != 1 {
		t.Fatalf("exactly one POS operation, got %d", pos.Operations())
	}
	// заказ с онлайн-оплатой — 422
	online := f.order(t, "online", "", 1000)
	if r := f.posPay(t, online, "k3", staff, map[string]any{"tender": "card"}); r.Status != 422 || r.json(t)["code"] != "PAYMENT_METHOD_NOT_ALLOWED" {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
}

func TestPosPaymentOverHTTPFiscalDataAndStaffRead(t *testing.T) {
	f, _ := posFixture(t)
	staff := subjects()[2].hdr
	o := f.order(t, "restaurant", "", 25000)
	p := f.posPay(t, o, "k1", staff, map[string]any{"tender": "card"}).json(t)["id"].(string)

	hook := cb(o, p, "paid", 25000)
	hook["fiscalData"] = map[string]any{"receiptNumber": "000123", "fn": "9960440300123456"}
	if r := signedWebhook(t, f, "/webhooks/payments/pos", posSecret, withRef(hook, "pos_1"), time.Now()); r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	r := f.do(t, "GET", "/orders/"+o+"/payments/"+p, nil, staff)
	got := r.json(t)
	fd, _ := got["fiscalData"].(map[string]any)
	if r.Status != 200 || got["status"] != "paid" || fd["receiptNumber"] != "000123" {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	if _, has := got["refund"]; has {
		t.Fatal("no refund was requested")
	}
	// сотрудник чужого ресторана и гость платёж не читают (заказ без владельца доступен знающему id: гость — исключение)
	if r := f.do(t, "GET", "/orders/"+o+"/payments/"+p, nil, subjects()[3].hdr); r.Status != 200 {
		// заказ гостевой (владельца нет): доступ по знанию id — как у онлайн-оплаты
		t.Fatalf("guest order is readable by id: %d", r.Status)
	}
	own := f.order(t, "restaurant", "client-1", 1000)
	pp := f.posPay(t, own, "k5", staff, map[string]any{"tender": "cash"}).json(t)["id"].(string)
	for name, hdr := range map[string]map[string]string{"another restaurant": subjects()[3].hdr, "another customer": subjects()[1].hdr} {
		if r := f.do(t, "GET", "/orders/"+own+"/payments/"+pp, nil, hdr); r.Status != 404 {
			t.Errorf("%s: %d", name, r.Status)
		}
	}
	if r := f.do(t, "GET", "/orders/"+own+"/payments/"+pp, nil, map[string]string{"X-User-Id": "client-1", "X-User-Roles": "CUSTOMER"}); r.Status != 200 {
		t.Fatalf("owner: %d", r.Status)
	}
}

func TestRefundVisibleInPaymentOverHTTP(t *testing.T) {
	f, _ := posFixture(t)
	o := f.order(t, "online", "", 1000)
	p := f.pay(t, o, "k1", nil).json(t)["id"].(string)
	gwRef := f.gw.Ref(p)
	hook := withRef(cb(o, p, "paid", 1000), gwRef)
	if r := signedWebhook(t, f, "/webhooks/payments/gateway", gwSecret, hook, time.Now()); r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	f.gw.Finish(p, "paid", "")
	status := fmt.Sprintf(`{"orderId":%q,"status":"cancelled","updatedAt":%q}`, o, time.Now().UTC().Format(time.RFC3339Nano))
	f.consumeStatus(t, status)
	if _, err := f.svc.ProcessRefunds(ctx); err != nil {
		t.Fatal(err)
	}
	got := f.do(t, "GET", "/orders/"+o+"/payments/"+p, nil, nil).json(t)
	rf, _ := got["refund"].(map[string]any)
	if got["status"] != "paid" || rf["status"] != "refunded" || rf["refundedAt"] == nil {
		t.Fatalf("%v", got)
	}
}
