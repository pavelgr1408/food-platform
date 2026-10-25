// Package httpapi — HTTP-слой loyalty-service (contracts/openapi/loyalty.yaml).
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/pavelgr1408/food-platform/pkg/authclient"
	"github.com/pavelgr1408/food-platform/pkg/httpx"
	"github.com/pavelgr1408/food-platform/pkg/idempotency"
	"github.com/pavelgr1408/food-platform/pkg/metrics"
	"github.com/pavelgr1408/food-platform/services/loyalty/internal/domain"
	"github.com/pavelgr1408/food-platform/services/loyalty/internal/dto"
	"github.com/pavelgr1408/food-platform/services/loyalty/internal/service"
)

// Promotions — сценарии, нужные HTTP-слою (реализация — service.Service).
type Promotions interface {
	Create(ctx context.Context, a service.Actor, in domain.Promotion) (domain.Promotion, error)
	Get(ctx context.Context, a service.Actor, id string) (domain.Promotion, error)
	Update(ctx context.Context, a service.Actor, id string, in domain.Promotion) (domain.Promotion, error)
	Activate(ctx context.Context, a service.Actor, id string) (domain.Promotion, error)
	Complete(ctx context.Context, a service.Actor, id string) (domain.Promotion, error)
	History(ctx context.Context, a service.Actor, id string) ([]service.HistoryEntry, error)
	ListActive(ctx context.Context, restaurantID string) ([]domain.Promotion, error)
}

// Deps — зависимости HTTP-слоя.
type Deps struct {
	Log         *slog.Logger
	Metrics     *metrics.Registry
	Auth        authclient.Authenticator
	Checks      []httpx.Check
	Promotions  Promotions
	Idempotency *idempotency.Store // nil в unit-тестах: createPromotion без защиты от повторов
}

type handlers struct {
	log *slog.Logger
	svc Promotions
}

// New собирает маршруты и стандартную цепочку middleware.
func New(d Deps) http.Handler {
	h := &handlers{log: d.Log, svc: d.Promotions}
	mux := http.NewServeMux()
	httpx.RegisterHealth(mux, d.Checks...)
	mux.Handle("GET /metrics", d.Metrics.Handler())

	// Просмотр действующих акций доступен гостю (UC-PRM-01, A1).
	mux.HandleFunc("GET /promotions", h.list)

	// Управление: роль проверяется здесь, охват (свой ресторан / национальные) — в сервисе через auth /authorize.
	manage := authclient.RequireRole(authclient.RoleFranchiseOwner, authclient.RoleHQStaff)
	var create http.Handler = http.HandlerFunc(h.create)
	if d.Idempotency != nil {
		create = d.Idempotency.Middleware(idempotency.Options{Required: true, Log: d.Log,
			Scope: func(r *http.Request) string { return authclient.FromContext(r.Context()).UserID }})(create)
	}
	mux.Handle("POST /promotions", manage(create))
	mux.Handle("GET /promotions/{promotionId}", manage(http.HandlerFunc(h.get)))
	mux.Handle("PUT /promotions/{promotionId}", manage(http.HandlerFunc(h.update)))
	mux.Handle("GET /promotions/{promotionId}/history", manage(http.HandlerFunc(h.history)))
	mux.Handle("POST /promotions/{promotionId}/activation", manage(http.HandlerFunc(h.activate)))
	mux.Handle("POST /promotions/{promotionId}/completion", manage(http.HandlerFunc(h.complete)))

	return httpx.Chain(httpx.TrackRoute(mux),
		httpx.RequestContext(), httpx.Recover(d.Log), httpx.AccessLog(d.Log),
		d.Metrics.Middleware(), authclient.Middleware(d.Auth))
}

func actor(r *http.Request) service.Actor {
	return service.Actor{Principal: authclient.FromContext(r.Context()), Authorization: r.Header.Get("Authorization")}
}

type conflictProblem struct {
	httpx.Problem
	ConflictingPromotionIDs []string `json:"conflictingPromotionIds"`
}

func (h *handlers) fail(w http.ResponseWriter, r *http.Request, err error) {
	var ce *service.ConflictError
	if errors.As(err, &ce) {
		w.Header().Set("Content-Type", httpx.ContentTypeProblem)
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(conflictProblem{
			Problem: httpx.Problem{Type: "about:blank", Title: http.StatusText(http.StatusConflict), Status: http.StatusConflict,
				Detail: "the promotion conflicts with active promotions of the same priority", Instance: r.URL.Path, Code: "PROMOTION_CONFLICT"},
			ConflictingPromotionIDs: ce.IDs})
		return
	}
	httpx.HandleError(w, r, h.log, err)
}

func (h *handlers) list(w http.ResponseWriter, r *http.Request) {
	ps, err := h.svc.ListActive(r.Context(), r.URL.Query().Get("restaurantId"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := make([]dto.Public, 0, len(ps))
	for _, p := range ps {
		out = append(out, dto.ToPublic(p))
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// input — тело createPromotion/updatePromotion (loyalty.yaml PromotionInput).
type input struct {
	Title        string    `json:"title"`
	Description  string    `json:"description"`
	Scope        string    `json:"scope"`
	RestaurantID string    `json:"restaurantId"`
	Priority     int       `json:"priority"`
	StartsAt     time.Time `json:"startsAt"`
	EndsAt       time.Time `json:"endsAt"`
	Currency     string    `json:"currency"`
	Discount     *struct {
		Type  string `json:"type"`
		Value int64  `json:"value"`
	} `json:"discount"`
	Conditions *struct {
		MenuItemIDs []string `json:"menuItemIds"`
		MinSubtotal int64    `json:"minSubtotal"`
		Fulfillment string   `json:"fulfillment"`
		Stackable   bool     `json:"stackable"`
	} `json:"conditions"`
}

// decode читает тело; false — ответ уже записан. Отсутствие обязательных полей — 400.
func decode(w http.ResponseWriter, r *http.Request) (domain.Promotion, bool) {
	var in input
	if !httpx.DecodeJSON(w, r, &in) {
		return domain.Promotion{}, false
	}
	if in.Title == "" || in.Scope == "" || in.Discount == nil || in.StartsAt.IsZero() || in.EndsAt.IsZero() {
		httpx.WriteProblem(w, r, http.StatusBadRequest, "VALIDATION_FAILED", "title, scope, discount, startsAt and endsAt are required")
		return domain.Promotion{}, false
	}
	p := domain.Promotion{Title: in.Title, Description: in.Description, Scope: domain.Scope(in.Scope), RestaurantID: in.RestaurantID,
		Priority: in.Priority, StartsAt: in.StartsAt, EndsAt: in.EndsAt, Currency: in.Currency,
		Discount: domain.Discount{Type: domain.DiscountType(in.Discount.Type), Value: in.Discount.Value}}
	if c := in.Conditions; c != nil {
		p.Conditions = domain.Conditions{MenuItemIDs: c.MenuItemIDs, MinSubtotal: c.MinSubtotal, Fulfillment: c.Fulfillment, Stackable: c.Stackable}
	}
	return p, true
}

func (h *handlers) create(w http.ResponseWriter, r *http.Request) {
	in, ok := decode(w, r)
	if !ok {
		return
	}
	p, err := h.svc.Create(r.Context(), actor(r), in)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	w.Header().Set("Location", "/promotions/"+p.ID)
	httpx.WriteJSON(w, http.StatusCreated, dto.From(p))
}

func (h *handlers) get(w http.ResponseWriter, r *http.Request) {
	p, err := h.svc.Get(r.Context(), actor(r), r.PathValue("promotionId"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, dto.From(p))
}

func (h *handlers) update(w http.ResponseWriter, r *http.Request) {
	in, ok := decode(w, r)
	if !ok {
		return
	}
	p, err := h.svc.Update(r.Context(), actor(r), r.PathValue("promotionId"), in)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, dto.From(p))
}

func (h *handlers) history(w http.ResponseWriter, r *http.Request) {
	es, err := h.svc.History(r.Context(), actor(r), r.PathValue("promotionId"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, es)
}

func (h *handlers) activate(w http.ResponseWriter, r *http.Request) {
	p, err := h.svc.Activate(r.Context(), actor(r), r.PathValue("promotionId"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, dto.From(p))
}

func (h *handlers) complete(w http.ResponseWriter, r *http.Request) {
	p, err := h.svc.Complete(r.Context(), actor(r), r.PathValue("promotionId"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, dto.From(p))
}
