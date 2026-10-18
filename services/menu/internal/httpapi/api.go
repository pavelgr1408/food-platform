// Package httpapi — HTTP-слой menu-service (contracts/openapi/menu.yaml).
package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/pavelgr1408/food-platform/pkg/authclient"
	"github.com/pavelgr1408/food-platform/pkg/httpx"
	"github.com/pavelgr1408/food-platform/pkg/metrics"
	"github.com/pavelgr1408/food-platform/services/menu/internal/domain"
	"github.com/pavelgr1408/food-platform/services/menu/internal/service"
)

// Лимиты запроса validateMenuItems (контракт menu.yaml).
const (
	maxLines     = 100
	maxQuantity  = 99
	maxModifiers = 20
	cacheControl = "public, max-age=30"
)

// Catalog — сценарии, нужные HTTP-слою.
type Catalog interface {
	Menu(ctx context.Context, restaurantID string) (domain.Menu, error)
	Validate(ctx context.Context, restaurantID string, lines []domain.Line) (service.Validation, error)
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
	mux.HandleFunc("GET /restaurants/{restaurantId}/menu", h.getMenu)
	mux.HandleFunc("POST /restaurants/{restaurantId}/menu/validation", h.validate)

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

func restaurantID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := r.PathValue("restaurantId")
	if _, err := uuid.Parse(id); err != nil {
		badParam(w, r, "restaurantId must be a UUID")
		return "", false
	}
	return id, true
}

func etag(version int64) string { return `"v` + strconv.FormatInt(version, 10) + `"` }

// matches — If-None-Match содержит текущий ETag (список через запятую, `*`, слабые валидаторы W/).
func matches(header, tag string) bool {
	for _, p := range strings.Split(header, ",") {
		p = strings.TrimSpace(p)
		if p == "*" || strings.TrimPrefix(p, "W/") == tag {
			return true
		}
	}
	return false
}

func (h *handlers) getMenu(w http.ResponseWriter, r *http.Request) {
	id, ok := restaurantID(w, r)
	if !ok {
		return
	}
	m, err := h.cat.Menu(r.Context(), id)
	if err != nil {
		httpx.HandleError(w, r, h.log, err)
		return
	}
	tag := etag(m.Version)
	w.Header().Set("ETag", tag)
	w.Header().Set("Cache-Control", cacheControl)
	if inm := r.Header.Get("If-None-Match"); inm != "" && matches(inm, tag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toMenu(m))
}

func (h *handlers) validate(w http.ResponseWriter, r *http.Request) {
	id, ok := restaurantID(w, r)
	if !ok {
		return
	}
	var req validationRequest
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	lines, detail := req.lines()
	if detail != "" {
		badParam(w, r, detail)
		return
	}
	v, err := h.cat.Validate(r.Context(), id, lines)
	if err != nil {
		httpx.HandleError(w, r, h.log, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toValidation(v))
}
