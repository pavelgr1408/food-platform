package clients_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pavelgr1408/food-platform/pkg/httpx"
	"github.com/pavelgr1408/food-platform/services/orders/internal/clients"
)

const (
	item1 = "b1000000-0000-4000-8000-000000000001"
	item2 = "b1000000-0000-4000-8000-000000000002"
	mod1  = "c1000000-0000-4000-8000-000000000001"
	rest1 = "a1000000-0000-4000-8000-000000000001"
)

func menuServer(t *testing.T, h http.HandlerFunc) (*clients.Menu, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &clients.Menu{BaseURL: srv.URL, HTTP: httpx.NewClient(httpx.ClientConfig{Timeout: 300 * time.Millisecond, MaxRetries: 1, BackoffBase: time.Millisecond})}, srv
}

func TestMenuValidateParsesContractResponse(t *testing.T) {
	var gotPath, gotKey string
	var gotBody map[string]any
	m, _ := menuServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotKey = r.URL.Path, r.Header.Get("Idempotency-Key")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"valid":false,"menuVersion":7,"total":{"amount":43800,"currency":"RUB"},"lines":[
		 {"menuItemId":"` + item1 + `","name":"Бургер","quantity":1,"available":true,"currentPrice":{"amount":43800,"currency":"RUB"},"lineTotal":{"amount":43800,"currency":"RUB"}},
		 {"menuItemId":"` + item2 + `","quantity":2,"available":false,"reasonCode":"ITEM_NOT_FOUND","reason":"нет"}]}`))
	})
	res, err := m.Validate(context.Background(), rest1, []clients.MenuLineInput{
		{MenuItemID: item1, Quantity: 1, ModifierIDs: []string{mod1}}, {MenuItemID: item2, Quantity: 2}})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/restaurants/"+rest1+"/menu/validation" || gotKey == "" {
		t.Fatalf("request: %s key=%q", gotPath, gotKey)
	}
	items := gotBody["items"].([]any)
	if len(items) != 2 || items[0].(map[string]any)["modifierIds"].([]any)[0] != mod1 {
		t.Fatalf("body: %v", gotBody)
	}
	if res.Valid || res.MenuVersion != 7 || res.Currency != "RUB" || res.Total != 43800 || len(res.Lines) != 2 {
		t.Fatalf("result: %+v", res)
	}
	if l := res.Lines[0]; l.Name != "Бургер" || l.UnitPrice != 43800 || l.LineTotal != 43800 || !l.Available {
		t.Fatalf("line0: %+v", l)
	}
	if l := res.Lines[1]; l.Available || l.ReasonCode != "ITEM_NOT_FOUND" || l.UnitPrice != 0 {
		t.Fatalf("line1: %+v", l)
	}
}

func TestMenuValidateErrorMapping(t *testing.T) {
	in := []clients.MenuLineInput{{MenuItemID: item1, Quantity: 1}}
	cases := map[string]struct {
		h    http.HandlerFunc
		want error
	}{
		"404 → ErrNotFound": {func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) }, clients.ErrNotFound},
		"500 → unavailable": {func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }, clients.ErrUnavailable},
		"400 → unavailable": {func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(400) }, clients.ErrUnavailable},
		"garbage body":      {func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("<html>")) }, clients.ErrUnavailable},
		"line count differs": {func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"valid":true,"menuVersion":1,"lines":[],"total":{"amount":0,"currency":"RUB"}}`))
		}, clients.ErrUnavailable},
		"foreign line": {func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"valid":true,"menuVersion":1,"total":{"amount":1,"currency":"RUB"},"lines":[{"menuItemId":"` + item2 + `","quantity":1,"available":true}]}`))
		}, clients.ErrUnavailable},
	}
	for name, c := range cases {
		m, _ := menuServer(t, c.h)
		_, err := m.Validate(context.Background(), rest1, in)
		if err == nil || !errorsIs(err, c.want) {
			t.Errorf("%s: got %v want %v", name, err, c.want)
		}
	}
}

func errorsIs(err, target error) bool {
	for e := err; e != nil; {
		if e == target {
			return true
		}
		u, ok := e.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		e = u.Unwrap()
	}
	return false
}

func TestMenuValidateRetriesTransient503ThenSucceeds(t *testing.T) {
	var n atomic.Int32
	m, _ := menuServer(t, func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"valid":true,"menuVersion":1,"total":{"amount":100,"currency":"RUB"},"lines":[{"menuItemId":"` + item1 + `","quantity":1,"available":true,"name":"X","currentPrice":{"amount":100,"currency":"RUB"},"lineTotal":{"amount":100,"currency":"RUB"}}]}`))
	})
	res, err := m.Validate(context.Background(), rest1, []clients.MenuLineInput{{MenuItemID: item1, Quantity: 1}})
	if err != nil || res.Total != 100 || n.Load() != 2 {
		t.Fatalf("err=%v calls=%d res=%+v", err, n.Load(), res)
	}
}

func TestMenuValidateTimeoutIsUnavailable(t *testing.T) {
	m, _ := menuServer(t, func(w http.ResponseWriter, r *http.Request) { time.Sleep(900 * time.Millisecond) })
	start := time.Now()
	_, err := m.Validate(context.Background(), rest1, []clients.MenuLineInput{{MenuItemID: item1, Quantity: 1}})
	if !errorsIs(err, clients.ErrUnavailable) {
		t.Fatalf("got %v", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("must not hang: %v", d)
	}
}

func TestRestaurantGet(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/restaurants/"+rest1:
			_, _ = w.Write([]byte(`{"id":"` + rest1 + `","name":"x","timezone":"Europe/Moscow","deliveryEnabled":true,"openNow":false,"location":{"lat":55.75,"lon":37.61},"deliveryRadiusMeters":3000,"workingHours":[]}`))
		case strings.HasSuffix(r.URL.Path, "-err"):
			w.WriteHeader(502)
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	c := &clients.Restaurant{BaseURL: srv.URL + "/", HTTP: httpx.NewClient(httpx.ClientConfig{Timeout: 200 * time.Millisecond, MaxRetries: -1})}
	info, err := c.Get(context.Background(), rest1)
	if err != nil || info.Timezone != "Europe/Moscow" || !info.DeliveryEnabled || info.OpenNow {
		t.Fatalf("%v %+v", err, info)
	}
	if !info.HasLocation || info.Lat != 55.75 || info.Lon != 37.61 || info.DeliveryRadiusM != 3000 {
		t.Fatalf("location and radius must be read: %+v", info)
	}
	if _, err := c.Get(context.Background(), "ghost"); !errorsIs(err, clients.ErrNotFound) {
		t.Fatalf("404: %v", err)
	}
	if _, err := c.Get(context.Background(), "x-err"); !errorsIs(err, clients.ErrUnavailable) {
		t.Fatalf("502: %v", err)
	}
	srv.Close()
	if _, err := c.Get(context.Background(), rest1); !errorsIs(err, clients.ErrUnavailable) {
		t.Fatalf("closed: %v", err)
	}
}
