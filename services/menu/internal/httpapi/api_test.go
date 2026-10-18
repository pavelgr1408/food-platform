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

	"github.com/pavelgr1408/food-platform/pkg/authclient"
	"github.com/pavelgr1408/food-platform/pkg/httpx"
	"github.com/pavelgr1408/food-platform/pkg/metrics"
	"github.com/pavelgr1408/food-platform/services/menu/internal/domain"
	"github.com/pavelgr1408/food-platform/services/menu/internal/service"
	"github.com/pavelgr1408/food-platform/services/menu/internal/store"
)

const (
	rid  = "a1000000-0000-4000-8000-000000000001"
	item = "c1000000-0000-4000-8000-000000000001"
	mod  = "d1000000-0000-4000-8000-000000000001"
)

var errNotFound = store.ErrNotFound

// memRepo — in-memory репозиторий: HTTP-слой тестируется вместе с настоящим service.Service, без БД.
type memRepo struct{ items map[string]domain.Item }

func (m memRepo) GetMenu(_ context.Context, id string) (domain.Menu, error) {
	if id != rid {
		return domain.Menu{}, errNotFound
	}
	it := m.items[item]
	return domain.Menu{RestaurantID: rid, Version: 7, Currency: "RUB",
		Categories: []domain.Category{{ID: "e1000000-0000-4000-8000-000000000001", Name: "Бургеры", Items: []domain.Item{it}}}}, nil
}

func (m memRepo) LoadItems(_ context.Context, id string, _ []string) (int64, string, map[string]domain.Item, error) {
	if id != rid {
		return 0, "", nil, errNotFound
	}
	return 7, "RUB", m.items, nil
}

func newHandler() http.Handler {
	repo := memRepo{items: map[string]domain.Item{
		item: {ID: item, Name: "BLT", Price: 34900, Available: true, Modifiers: []domain.Modifier{{ID: mod, Name: "Бекон", Price: 8900, Available: true}}},
	}}
	return New(Deps{Log: slog.New(slog.NewJSONHandler(io.Discard, nil)), Metrics: metrics.New("menu"),
		Auth: authclient.DevAuthenticator{}, Catalog: service.New(repo)})
}

func do(h http.Handler, method, path, body string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestHooksPresent(t *testing.T) {
	h := newHandler()
	if r := do(h, "GET", "/health/", "", nil); r.Code != 200 || strings.TrimSpace(r.Body.String()) != `{"status":"OK"}` {
		t.Fatalf("liveness: %d %s", r.Code, r.Body)
	}
	if r := do(h, "GET", "/actuator/health", "", nil); r.Code != 200 {
		t.Fatalf("readiness: %d", r.Code)
	}
	if r := do(h, "GET", "/metrics", "", nil); r.Code != 200 || !strings.Contains(r.Body.String(), "http_requests_total") {
		t.Fatalf("metrics: %d", r.Code)
	}
}

func TestGetMenuShapeAndMoneyAreIntegers(t *testing.T) {
	r := do(newHandler(), "GET", "/restaurants/"+rid+"/menu", "", nil)
	if r.Code != 200 || r.Header().Get("ETag") != `"v7"` || r.Header().Get("Cache-Control") != "public, max-age=30" {
		t.Fatalf("%d %v %s", r.Code, r.Header(), r.Body)
	}
	body := r.Body.String()
	// целые копейки, без десятичной точки (ADR-13)
	for _, want := range []string{`"amount":34900`, `"amount":8900`, `"currency":"RUB"`, `"menuVersion":7`} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %s in %s", want, body)
		}
	}
	if strings.Contains(body, "34900.") {
		t.Error("money must not be a float")
	}
}

func TestConditionalGet(t *testing.T) {
	h := newHandler()
	for _, inm := range []string{`"v7"`, `W/"v7"`, `"v1", "v7"`, `*`} {
		r := do(h, "GET", "/restaurants/"+rid+"/menu", "", map[string]string{"If-None-Match": inm})
		if r.Code != 304 || r.Body.Len() != 0 || r.Header().Get("ETag") != `"v7"` {
			t.Errorf("If-None-Match %s: %d body=%q", inm, r.Code, r.Body)
		}
	}
	if r := do(h, "GET", "/restaurants/"+rid+"/menu", "", map[string]string{"If-None-Match": `"v6"`}); r.Code != 200 {
		t.Errorf("stale etag must give 200, got %d", r.Code)
	}
}

func TestMenuErrors(t *testing.T) {
	h := newHandler()
	for path, want := range map[string]struct {
		status int
		code   string
	}{
		"/restaurants/a1000000-0000-4000-8000-0000000000ff/menu": {404, "MENU_NOT_FOUND"},
		"/restaurants/not-uuid/menu":                             {400, "INVALID_PARAMETER"},
	} {
		r := do(h, "GET", path, "", nil)
		var p httpx.Problem
		_ = json.Unmarshal(r.Body.Bytes(), &p)
		if r.Code != want.status || p.Code != want.code || r.Header().Get("Content-Type") != httpx.ContentTypeProblem {
			t.Errorf("%s: %d %s", path, r.Code, r.Body)
		}
	}
}

func TestValidateOK(t *testing.T) {
	body := `{"items":[{"menuItemId":"` + item + `","quantity":2,"modifierIds":["` + mod + `"]}]}`
	r := do(newHandler(), "POST", "/restaurants/"+rid+"/menu/validation", body, nil)
	if r.Code != 200 {
		t.Fatalf("%d %s", r.Code, r.Body)
	}
	var out struct {
		Valid bool `json:"valid"`
		Lines []struct {
			CurrentPrice struct{ Amount int64 } `json:"currentPrice"`
			LineTotal    struct{ Amount int64 } `json:"lineTotal"`
		} `json:"lines"`
		Total       struct{ Amount int64 } `json:"total"`
		MenuVersion int64                  `json:"menuVersion"`
	}
	if err := json.Unmarshal(r.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !out.Valid || out.MenuVersion != 7 || out.Lines[0].CurrentPrice.Amount != 43800 || out.Lines[0].LineTotal.Amount != 87600 || out.Total.Amount != 87600 {
		t.Fatalf("%+v", out)
	}
}

func TestValidateUnavailableIs200WithReason(t *testing.T) {
	ghost := "c1000000-0000-4000-8000-0000000000aa"
	body := `{"items":[{"menuItemId":"` + ghost + `","quantity":1}]}`
	r := do(newHandler(), "POST", "/restaurants/"+rid+"/menu/validation", body, nil)
	if r.Code != 200 || !strings.Contains(r.Body.String(), `"valid":false`) || !strings.Contains(r.Body.String(), `"reasonCode":"ITEM_NOT_FOUND"`) {
		t.Fatalf("%d %s", r.Code, r.Body)
	}
	if strings.Contains(r.Body.String(), "currentPrice") {
		t.Error("not found item must not have a price")
	}
}

func TestValidateRequestLimits(t *testing.T) {
	h := newHandler()
	many := `{"items":[` + strings.Repeat(`{"menuItemId":"`+item+`","quantity":1},`, 100) + `{"menuItemId":"` + item + `","quantity":1}]}`
	mods := make([]string, 21)
	for i := range mods {
		mods[i] = `"d1000000-0000-4000-8000-0000000000` + string(rune('a'+i/10)) + string(rune('0'+i%10)) + `"`
	}
	cases := map[string]string{
		"empty list":         `{"items":[]}`,
		"101 lines":          many,
		"quantity 0":         `{"items":[{"menuItemId":"` + item + `","quantity":0}]}`,
		"quantity 100":       `{"items":[{"menuItemId":"` + item + `","quantity":100}]}`,
		"bad item uuid":      `{"items":[{"menuItemId":"x","quantity":1}]}`,
		"bad modifier uuid":  `{"items":[{"menuItemId":"` + item + `","quantity":1,"modifierIds":["x"]}]}`,
		"duplicate modifier": `{"items":[{"menuItemId":"` + item + `","quantity":1,"modifierIds":["` + mod + `","` + mod + `"]}]}`,
		"too many modifiers": `{"items":[{"menuItemId":"` + item + `","quantity":1,"modifierIds":[` + strings.Join(mods, ",") + `]}]}`,
		"float quantity":     `{"items":[{"menuItemId":"` + item + `","quantity":1.5}]}`,
		"unknown field":      `{"items":[{"menuItemId":"` + item + `","quantity":1}],"extra":1}`,
		"not json":           `{`,
	}
	for name, body := range cases {
		r := do(h, "POST", "/restaurants/"+rid+"/menu/validation", body, nil)
		if r.Code != 400 {
			t.Errorf("%s: want 400, got %d %s", name, r.Code, r.Body)
		}
	}
	// 100 строк — ровно на границе — допустимо
	ok := `{"items":[` + strings.TrimSuffix(strings.Repeat(`{"menuItemId":"`+item+`","quantity":1},`, 100), ",") + `]}`
	if r := do(h, "POST", "/restaurants/"+rid+"/menu/validation", ok, nil); r.Code != 200 {
		t.Errorf("100 lines: %d %s", r.Code, r.Body)
	}
	if r := do(h, "POST", "/restaurants/a1000000-0000-4000-8000-0000000000ff/menu/validation",
		`{"items":[{"menuItemId":"`+item+`","quantity":1}]}`, nil); r.Code != 404 {
		t.Errorf("unknown restaurant: %d", r.Code)
	}
}
