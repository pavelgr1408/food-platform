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
	"github.com/pavelgr1408/food-platform/services/restaurant/internal/domain"
	"github.com/pavelgr1408/food-platform/services/restaurant/internal/service"
)

type fakeCatalog struct{ last service.Params }

func (f *fakeCatalog) Search(_ context.Context, p service.Params) (service.Result, error) {
	f.last = p
	d := 120
	return service.Result{Total: 1, Center: p.Center, Items: []service.Item{{
		Restaurant: domain.Restaurant{ID: "a1000000-0000-4000-8000-000000000001", Name: "N", DistanceM: &d}, OpenNow: true, DeliveryAvailable: true}}}, nil
}

func (f *fakeCatalog) Get(_ context.Context, id string) (service.Item, error) {
	if id != "a1000000-0000-4000-8000-000000000001" {
		return service.Item{}, httpx.NewError(404, "RESTAURANT_NOT_FOUND", "restaurant not found")
	}
	r := domain.Restaurant{ID: id, Name: "N", Timezone: "Europe/Moscow"}
	r.Schedule.Hours = []domain.WorkingHours{{DayOfWeek: 5, Interval: domain.Interval{Opens: 600, Closes: 120}}}
	r.Schedule.Exceptions = []domain.Exception{{Date: "2027-01-01", Closed: true}}
	return service.Item{Restaurant: r}, nil
}

func newHandler(c Catalog) http.Handler {
	return New(Deps{Log: slog.New(slog.NewJSONHandler(io.Discard, nil)), Metrics: metrics.New("restaurant"),
		Auth: authclient.DevAuthenticator{}, Catalog: c})
}

func get(h http.Handler, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
	return rec
}

func TestHooksPresent(t *testing.T) {
	h := newHandler(&fakeCatalog{})
	if r := get(h, "/health/"); r.Code != 200 || strings.TrimSpace(r.Body.String()) != `{"status":"OK"}` {
		t.Fatalf("liveness: %d %s", r.Code, r.Body)
	}
	if r := get(h, "/actuator/health"); r.Code != 200 {
		t.Fatalf("readiness: %d", r.Code)
	}
	if r := get(h, "/metrics"); r.Code != 200 || !strings.Contains(r.Body.String(), "http_requests_total") {
		t.Fatalf("metrics: %d", r.Code)
	}
}

func TestSearchParams(t *testing.T) {
	fc := &fakeCatalog{}
	h := newHandler(fc)
	r := get(h, "/restaurants?lat=55.75&lon=37.61&deliveryOnly=true&openNow=false&radiusMeters=1500&limit=5&offset=2")
	if r.Code != 200 {
		t.Fatalf("%d %s", r.Code, r.Body)
	}
	p := fc.last
	if p.Center == nil || p.Center.Lat != 55.75 || !p.DeliveryOnly || p.OpenNow || p.RadiusM != 1500 || p.Limit != 5 || p.Offset != 2 {
		t.Fatalf("params: %+v", p)
	}
	var page map[string]any
	_ = json.Unmarshal(r.Body.Bytes(), &page)
	items := page["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["distanceMeters"].(float64) != 120 {
		t.Fatalf("body: %s", r.Body)
	}
	if _, has := page["hint"]; has {
		t.Fatal("hint must be absent when items are present")
	}

	// значения по умолчанию
	get(h, "/restaurants")
	if fc.last.RadiusM != 5000 || fc.last.Limit != 20 || fc.last.Center != nil {
		t.Fatalf("defaults: %+v", fc.last)
	}
}

func TestSearchValidation(t *testing.T) {
	h := newHandler(&fakeCatalog{})
	cases := map[string]string{
		"/restaurants?lat=55.7":           "COORDINATES_INCOMPLETE",
		"/restaurants?lon=37.6":           "COORDINATES_INCOMPLETE",
		"/restaurants?lat=95&lon=37":      "INVALID_PARAMETER",
		"/restaurants?lat=abc&lon=37":     "INVALID_PARAMETER",
		"/restaurants?lat=55&lon=181":     "INVALID_PARAMETER",
		"/restaurants?radiusMeters=99":    "INVALID_PARAMETER",
		"/restaurants?radiusMeters=50001": "INVALID_PARAMETER",
		"/restaurants?limit=0":            "INVALID_PARAMETER",
		"/restaurants?limit=101":          "INVALID_PARAMETER",
		"/restaurants?offset=-1":          "INVALID_PARAMETER",
		"/restaurants?deliveryOnly=maybe": "INVALID_PARAMETER",
		"/restaurants/not-a-uuid":         "INVALID_PARAMETER",
	}
	for path, code := range cases {
		r := get(h, path)
		var pr httpx.Problem
		_ = json.Unmarshal(r.Body.Bytes(), &pr)
		if r.Code != 400 || pr.Code != code || r.Header().Get("Content-Type") != httpx.ContentTypeProblem {
			t.Errorf("%s: %d %s", path, r.Code, r.Body)
		}
	}
}

func TestGetRestaurant(t *testing.T) {
	h := newHandler(&fakeCatalog{})
	r := get(h, "/restaurants/a1000000-0000-4000-8000-000000000001")
	if r.Code != 200 {
		t.Fatalf("%d %s", r.Code, r.Body)
	}
	body := r.Body.String()
	for _, want := range []string{`"opensAt":"10:00"`, `"closesAt":"02:00"`, `"closed":true`, `"timezone":"Europe/Moscow"`, `"deliveryRadiusMeters":0`} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %s in %s", want, body)
		}
	}
	nf := get(h, "/restaurants/a1000000-0000-4000-8000-0000000000ff")
	if nf.Code != 404 || !strings.Contains(nf.Body.String(), "RESTAURANT_NOT_FOUND") {
		t.Fatalf("404: %d %s", nf.Code, nf.Body)
	}
}
