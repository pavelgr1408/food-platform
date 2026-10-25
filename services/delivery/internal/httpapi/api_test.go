package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pavelgr1408/food-platform/pkg/authclient"
	"github.com/pavelgr1408/food-platform/pkg/httpx"
	"github.com/pavelgr1408/food-platform/pkg/metrics"
	"github.com/pavelgr1408/food-platform/pkg/webhook"
	"github.com/pavelgr1408/food-platform/services/delivery/internal/domain"
	"github.com/pavelgr1408/food-platform/services/delivery/internal/location"
	"github.com/pavelgr1408/food-platform/services/delivery/internal/service"
)

var secret = []byte("test-webhook-secret-123")

type stub struct {
	d      domain.Delivery
	err    error
	locErr error
	got    service.Actor
	cb     service.Callback
	cbErr  error
	cbN    int
}

func (s *stub) Get(_ context.Context, a service.Actor, _ string) (domain.Delivery, error) {
	s.got = a
	return s.d, s.err
}
func (s *stub) Location(_ context.Context, a service.Actor, _ string) (location.Point, error) {
	s.got = a
	return location.Point{Lat: 55.75, Lon: 37.61, UpdatedAt: time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)}, s.locErr
}
func (s *stub) HandleCallback(_ context.Context, c service.Callback) error {
	s.cb, s.cbN = c, s.cbN+1
	return s.cbErr
}

func newHandler(s *stub, sec []byte) http.Handler {
	return New(Deps{Log: slog.New(slog.NewJSONHandler(io.Discard, nil)), Metrics: metrics.New("delivery"),
		Auth: authclient.DevAuthenticator{}, Deliveries: s, CourierWebhookSecret: sec})
}

func do(h http.Handler, method, path, body string, hdr map[string]string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	h.ServeHTTP(rec, req)
	return rec
}

const oid = "5b0b4a0e-3f55-4a0c-9b7e-1d0d6f2b7a11"

func sign(body string) map[string]string {
	return map[string]string{webhook.Header: webhook.Sign(secret, []byte(body), time.Now())}
}

func TestHooksPresent(t *testing.T) {
	h := newHandler(&stub{}, secret)
	if r := do(h, "GET", "/health/", "", nil); r.Code != 200 || strings.TrimSpace(r.Body.String()) != `{"status":"OK"}` {
		t.Fatalf("liveness: %d %s", r.Code, r.Body)
	}
	if r := do(h, "GET", "/actuator/health", "", nil); r.Code != 200 {
		t.Fatalf("readiness: %d", r.Code)
	}
	if r := do(h, "GET", "/metrics", "", nil); r.Code != 200 || !strings.Contains(r.Body.String(), "http_requests_total") {
		t.Fatalf("metrics: %d", r.Code)
	}
	if do(h, "GET", "/health/", "", nil).Header().Get("X-Request-Id") == "" {
		t.Fatal("request id header missing")
	}
}

func TestGetDeliveryJSON(t *testing.T) {
	eta := time.Date(2026, 10, 25, 12, 30, 0, 0, time.UTC)
	s := &stub{d: domain.Delivery{ID: "d1", OrderID: oid, Status: domain.StatusEnRoute, CourierName: "Пётр", CourierPhone: "+7999",
		EtaAt: &eta, PayOnDelivery: true, UpdatedAt: eta}}
	h := newHandler(s, secret)
	r := do(h, "GET", "/orders/"+oid+"/delivery", "", map[string]string{"X-User-Id": "u1", "X-User-Roles": "STAFF", "X-Restaurant-Ids": "r1"})
	if r.Code != 200 {
		t.Fatalf("code %d %s", r.Code, r.Body)
	}
	var m map[string]any
	_ = json.Unmarshal(r.Body.Bytes(), &m)
	if m["status"] != "en_route" || m["orderId"] != oid || m["payOnDelivery"] != true || m["etaAt"] == nil {
		t.Fatalf("body: %s", r.Body)
	}
	if c, _ := m["courier"].(map[string]any); c["name"] != "Пётр" {
		t.Fatalf("courier: %s", r.Body)
	}
	if !s.got.Staff || s.got.UserID != "u1" || len(s.got.Restaurants) != 1 || s.got.HQ {
		t.Fatalf("actor: %+v", s.got)
	}
	// завершённая доставка: без ETA; failed/no_courier — с подсказкой
	s.d = domain.Delivery{OrderID: oid, Status: domain.StatusFailed, FailReason: domain.ReasonNoCourier, EtaAt: &eta, UpdatedAt: eta}
	r = do(h, "GET", "/orders/"+oid+"/delivery", "", nil)
	m = map[string]any{}
	_ = json.Unmarshal(r.Body.Bytes(), &m)
	if m["hint"] != "pickup" || m["failReason"] != "no_courier" || m["etaAt"] != nil || m["courier"] != nil {
		t.Fatalf("failed body: %s", r.Body)
	}
	if !strings.Contains(r.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("content-type %q", r.Header().Get("Content-Type"))
	}
}

func TestNotFoundHasRetryAfter(t *testing.T) {
	s := &stub{err: httpx.NewError(404, "DELIVERY_NOT_FOUND", "delivery not found")}
	h := newHandler(s, secret)
	r := do(h, "GET", "/orders/"+oid+"/delivery", "", nil)
	if r.Code != 404 || r.Header().Get("Retry-After") != "1" || !strings.Contains(r.Body.String(), "DELIVERY_NOT_FOUND") {
		t.Fatalf("got %d %q %s", r.Code, r.Header().Get("Retry-After"), r.Body)
	}
}

func TestCourierLocation(t *testing.T) {
	s := &stub{}
	h := newHandler(s, secret)
	r := do(h, "GET", "/orders/"+oid+"/delivery/courier-location", "", nil)
	var m struct {
		Location  struct{ Lat, Lon float64 }
		UpdatedAt string
	}
	if err := json.Unmarshal(r.Body.Bytes(), &m); err != nil || r.Code != 200 || m.Location.Lat != 55.75 || m.UpdatedAt == "" {
		t.Fatalf("got %d %s", r.Code, r.Body)
	}
	s.locErr = httpx.NewError(404, "LOCATION_NOT_AVAILABLE", "x")
	if r := do(h, "GET", "/orders/"+oid+"/delivery/courier-location", "", nil); r.Code != 404 || !strings.Contains(r.Body.String(), "LOCATION_NOT_AVAILABLE") {
		t.Fatalf("got %d %s", r.Code, r.Body)
	}
	s.locErr = httpx.NewError(503, "LOCATION_STORE_UNAVAILABLE", "x")
	if r := do(h, "GET", "/orders/"+oid+"/delivery/courier-location", "", nil); r.Code != 503 {
		t.Fatalf("got %d", r.Code)
	}
}

func TestWebhook(t *testing.T) {
	body := `{"eventId":"e1","orderId":"` + oid + `","status":"assigned","courierId":"c1","courier":{"name":"Пётр","phone":"+7999"},"paid":true,"timestamp":"2026-10-25T12:00:00Z","extra":1}`
	t.Run("отключён без секрета", func(t *testing.T) {
		r := do(newHandler(&stub{}, nil), "POST", "/webhooks/delivery/status", body, sign(body))
		if r.Code != 503 || !strings.Contains(r.Body.String(), "WEBHOOK_DISABLED") {
			t.Fatalf("%d %s", r.Code, r.Body)
		}
	})
	t.Run("без подписи и с неверной", func(t *testing.T) {
		s := &stub{}
		h := newHandler(s, secret)
		for _, hdr := range []map[string]string{nil, {webhook.Header: "t=1,v1=00"}, {webhook.Header: webhook.Sign([]byte("other-secret-value"), []byte(body), time.Now())}} {
			if r := do(h, "POST", "/webhooks/delivery/status", body, hdr); r.Code != 401 || !strings.Contains(r.Body.String(), "INVALID_SIGNATURE") {
				t.Fatalf("%d %s", r.Code, r.Body)
			}
		}
		if s.cbN != 0 {
			t.Fatal("unsigned webhook must not be applied")
		}
	})
	t.Run("просроченная подпись (replay)", func(t *testing.T) {
		h := newHandler(&stub{}, secret)
		old := map[string]string{webhook.Header: webhook.Sign(secret, []byte(body), time.Now().Add(-time.Hour))}
		if r := do(h, "POST", "/webhooks/delivery/status", body, old); r.Code != 401 {
			t.Fatalf("%d", r.Code)
		}
	})
	t.Run("принят", func(t *testing.T) {
		s := &stub{}
		r := do(newHandler(s, secret), "POST", "/webhooks/delivery/status", body, sign(body))
		if r.Code != 200 || s.cbN != 1 {
			t.Fatalf("%d %s", r.Code, r.Body)
		}
		if s.cb.CourierName != "Пётр" || s.cb.CourierID != "c1" || !s.cb.Paid || s.cb.Status != "assigned" {
			t.Fatalf("callback: %+v", s.cb)
		}
	})
	t.Run("битый JSON", func(t *testing.T) {
		if r := do(newHandler(&stub{}, secret), "POST", "/webhooks/delivery/status", "{", sign("{")); r.Code != 400 || !strings.Contains(r.Body.String(), "INVALID_JSON") {
			t.Fatalf("%d %s", r.Code, r.Body)
		}
	})
	t.Run("валидация -> 400", func(t *testing.T) {
		s := &stub{cbErr: &service.ValidationError{Msg: "status must be one of ..."}}
		if r := do(newHandler(s, secret), "POST", "/webhooks/delivery/status", body, sign(body)); r.Code != 400 || !strings.Contains(r.Body.String(), "INVALID_PARAMETER") {
			t.Fatalf("%d %s", r.Code, r.Body)
		}
	})
	t.Run("неизвестная доставка -> 404", func(t *testing.T) {
		s := &stub{cbErr: httpx.NewError(404, "DELIVERY_NOT_FOUND", "delivery not found")}
		if r := do(newHandler(s, secret), "POST", "/webhooks/delivery/status", body, sign(body)); r.Code != 404 {
			t.Fatalf("%d", r.Code)
		}
	})
	t.Run("слишком большое тело", func(t *testing.T) {
		big := `{"eventId":"` + strings.Repeat("a", 70<<10) + `"}`
		if r := do(newHandler(&stub{}, secret), "POST", "/webhooks/delivery/status", big, sign(big)); r.Code != 413 {
			t.Fatalf("%d", r.Code)
		}
	})
}
