// Package httpapi — HTTP-слой delivery-service (contracts/openapi/delivery.yaml).
package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/pavelgr1408/food-platform/pkg/authclient"
	"github.com/pavelgr1408/food-platform/pkg/httpx"
	"github.com/pavelgr1408/food-platform/pkg/metrics"
	"github.com/pavelgr1408/food-platform/pkg/webhook"
	"github.com/pavelgr1408/food-platform/services/delivery/internal/domain"
	"github.com/pavelgr1408/food-platform/services/delivery/internal/location"
	"github.com/pavelgr1408/food-platform/services/delivery/internal/service"
)

// Deliveries — сценарии, нужные HTTP-слою (реализация — service.Service).
type Deliveries interface {
	Get(ctx context.Context, a service.Actor, orderID string) (domain.Delivery, error)
	Location(ctx context.Context, a service.Actor, orderID string) (location.Point, error)
	HandleCallback(ctx context.Context, c service.Callback) error
}

// Deps — зависимости HTTP-слоя.
type Deps struct {
	Log        *slog.Logger
	Metrics    *metrics.Registry
	Auth       authclient.Authenticator
	Checks     []httpx.Check
	Deliveries Deliveries
	// Секрет подписи вебхука курьерской системы (HMAC-SHA256, pkg/webhook). Пустой — приём отключён (503).
	CourierWebhookSecret []byte
	Clock                func() time.Time // nil → time.Now
}

type handlers struct {
	log *slog.Logger
	svc Deliveries
	sec []byte
	now func() time.Time
}

// maxWebhookBody — предел тела вебхука.
const maxWebhookBody = 64 << 10

// New собирает маршруты и стандартную цепочку middleware.
func New(d Deps) http.Handler {
	h := &handlers{log: d.Log, svc: d.Deliveries, sec: d.CourierWebhookSecret, now: d.Clock}
	if h.now == nil {
		h.now = time.Now
	}
	mux := http.NewServeMux()
	httpx.RegisterHealth(mux, d.Checks...)
	mux.Handle("GET /metrics", d.Metrics.Handler())

	mux.HandleFunc("GET /orders/{orderId}/delivery", h.getDelivery)
	mux.HandleFunc("GET /orders/{orderId}/delivery/courier-location", h.getLocation)
	// Вебхук: мимо gateway, защита — подпись, а не токен.
	mux.HandleFunc("POST /webhooks/delivery/status", h.webhook)

	return httpx.Chain(httpx.TrackRoute(mux),
		httpx.RequestContext(), httpx.Recover(d.Log), httpx.AccessLog(d.Log),
		d.Metrics.Middleware(), authclient.Middleware(d.Auth))
}

func actor(r *http.Request) service.Actor {
	p := authclient.FromContext(r.Context())
	return service.Actor{
		UserID:      p.UserID,
		Staff:       p.HasRole(authclient.RoleStaff, authclient.RoleFranchiseOwner, authclient.RoleHQStaff),
		HQ:          p.HasRole(authclient.RoleHQStaff),
		Restaurants: p.Restaurants,
	}
}

func (h *handlers) getDelivery(w http.ResponseWriter, r *http.Request) {
	d, err := h.svc.Get(r.Context(), actor(r), r.PathValue("orderId"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toInfo(d))
}

func (h *handlers) getLocation(w http.ResponseWriter, r *http.Request) {
	p, err := h.svc.Location(r.Context(), actor(r), r.PathValue("orderId"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toLocation(p))
}

func (h *handlers) fail(w http.ResponseWriter, r *http.Request, err error) {
	if service.IsNotFound(err) {
		w.Header().Set("Retry-After", "1") // событие orders.state могло ещё не дойти
	}
	httpx.HandleError(w, r, h.log, err)
}

// webhook принимает уведомление курьерской системы: подпись → разбор → применение.
func (h *handlers) webhook(w http.ResponseWriter, r *http.Request) {
	if len(h.sec) == 0 {
		httpx.WriteProblem(w, r, http.StatusServiceUnavailable, "WEBHOOK_DISABLED", "webhook secret is not configured")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxWebhookBody))
	if err != nil {
		httpx.WriteProblem(w, r, http.StatusRequestEntityTooLarge, "BODY_TOO_LARGE", "request body too large")
		return
	}
	if err := webhook.Verify(h.sec, body, r.Header.Get(webhook.Header), h.now(), 0); err != nil {
		h.log.WarnContext(r.Context(), "webhook signature rejected", "reason", err.Error())
		httpx.WriteProblem(w, r, http.StatusUnauthorized, "INVALID_SIGNATURE", "signature is missing, invalid or outside the replay window")
		return
	}
	var in callbackRequest
	if err := json.Unmarshal(body, &in); err != nil {
		httpx.WriteProblem(w, r, http.StatusBadRequest, "INVALID_JSON", "malformed JSON body")
		return
	}
	if err := h.svc.HandleCallback(r.Context(), in.domain()); err != nil {
		if service.IsValidation(err) {
			httpx.WriteProblem(w, r, http.StatusBadRequest, "INVALID_PARAMETER", err.Error())
			return
		}
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "accepted"})
}
