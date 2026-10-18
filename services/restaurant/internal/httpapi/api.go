// Package httpapi — HTTP-слой restaurant-service (contracts/openapi/restaurant.yaml).
package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/google/uuid"

	"github.com/pavelgr1408/food-platform/pkg/authclient"
	"github.com/pavelgr1408/food-platform/pkg/httpx"
	"github.com/pavelgr1408/food-platform/pkg/metrics"
	"github.com/pavelgr1408/food-platform/services/restaurant/internal/domain"
	"github.com/pavelgr1408/food-platform/services/restaurant/internal/service"
)

// Catalog — сценарии, нужные HTTP-слою.
type Catalog interface {
	Search(ctx context.Context, p service.Params) (service.Result, error)
	Get(ctx context.Context, id string) (service.Item, error)
}

// Deps — зависимости HTTP-слоя.
type Deps struct {
	Log     *slog.Logger
	Metrics *metrics.Registry
	Auth    authclient.Authenticator
	Checks  []httpx.Check
	Catalog Catalog
}

// New собирает маршруты и стандартную цепочку middleware.
func New(d Deps) http.Handler {
	mux := http.NewServeMux()
	httpx.RegisterHealth(mux, d.Checks...)
	mux.Handle("GET /metrics", d.Metrics.Handler())

	h := &handlers{log: d.Log, cat: d.Catalog}
	mux.HandleFunc("GET /restaurants", h.search)
	mux.HandleFunc("GET /restaurants/{restaurantId}", h.get)

	return httpx.Chain(httpx.TrackRoute(mux),
		httpx.RequestContext(), httpx.Recover(d.Log), httpx.AccessLog(d.Log),
		d.Metrics.Middleware(), authclient.Middleware(d.Auth))
}

type handlers struct {
	log *slog.Logger
	cat Catalog
}

func badParam(w http.ResponseWriter, r *http.Request, detail string) {
	httpx.WriteProblem(w, r, http.StatusBadRequest, "INVALID_PARAMETER", detail)
}

func (h *handlers) search(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	p := service.Params{RadiusM: service.DefaultRadiusM, Limit: 20, Address: q.Get("address")}

	latS, lonS := q.Get("lat"), q.Get("lon")
	if (latS == "") != (lonS == "") {
		httpx.WriteProblem(w, r, http.StatusBadRequest, "COORDINATES_INCOMPLETE", "lat and lon must be provided together")
		return
	}
	if latS != "" {
		lat, err1 := strconv.ParseFloat(latS, 64)
		lon, err2 := strconv.ParseFloat(lonS, 64)
		if err1 != nil || err2 != nil || lat < -90 || lat > 90 || lon < -180 || lon > 180 {
			badParam(w, r, "lat must be in [-90,90] and lon in [-180,180]")
			return
		}
		p.Center = &domain.Point{Lat: lat, Lon: lon}
	}
	var ok bool
	if p.DeliveryOnly, ok = boolParam(w, r, "deliveryOnly"); !ok {
		return
	}
	if p.OpenNow, ok = boolParam(w, r, "openNow"); !ok {
		return
	}
	if p.RadiusM, ok = intParam(w, r, "radiusMeters", p.RadiusM, service.MinRadiusM, service.MaxRadiusM); !ok {
		return
	}
	if p.Limit, ok = intParam(w, r, "limit", p.Limit, 1, 100); !ok {
		return
	}
	if p.Offset, ok = intParam(w, r, "offset", 0, 0, 1<<30); !ok {
		return
	}

	res, err := h.cat.Search(r.Context(), p)
	if err != nil {
		httpx.HandleError(w, r, h.log, err)
		return
	}
	page := restaurantPage{Items: make([]restaurantSummary, 0, len(res.Items)), Total: res.Total,
		Limit: p.Limit, Offset: p.Offset, Hint: res.Hint}
	if res.Center != nil {
		page.Center = &geoPoint{Lat: res.Center.Lat, Lon: res.Center.Lon}
	}
	for _, it := range res.Items {
		page.Items = append(page.Items, summary(it))
	}
	httpx.WriteJSON(w, http.StatusOK, page)
}

func (h *handlers) get(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("restaurantId")
	if _, err := uuid.Parse(id); err != nil {
		badParam(w, r, "restaurantId must be a UUID")
		return
	}
	it, err := h.cat.Get(r.Context(), id)
	if err != nil {
		httpx.HandleError(w, r, h.log, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, detail(it))
}

func boolParam(w http.ResponseWriter, r *http.Request, name string) (bool, bool) {
	s := r.URL.Query().Get(name)
	if s == "" {
		return false, true
	}
	v, err := strconv.ParseBool(s)
	if err != nil {
		badParam(w, r, name+" must be a boolean")
		return false, false
	}
	return v, true
}

func intParam(w http.ResponseWriter, r *http.Request, name string, def, lo, hi int) (int, bool) {
	s := r.URL.Query().Get(name)
	if s == "" {
		return def, true
	}
	v, err := strconv.Atoi(s)
	if err != nil || v < lo || v > hi {
		badParam(w, r, name+" must be an integer in ["+strconv.Itoa(lo)+","+strconv.Itoa(hi)+"]")
		return 0, false
	}
	return v, true
}
