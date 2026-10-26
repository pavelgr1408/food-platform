// Package httpapi — HTTP-слой notification-service (contracts/openapi/notification.yaml).
package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/pavelgr1408/food-platform/pkg/authclient"
	"github.com/pavelgr1408/food-platform/pkg/httpx"
	"github.com/pavelgr1408/food-platform/pkg/metrics"
	"github.com/pavelgr1408/food-platform/services/notification/internal/domain"
	"github.com/pavelgr1408/food-platform/services/notification/internal/service"
)

// Tokens — сценарии, нужные HTTP-слою (реализация — service.Service).
type Tokens interface {
	Register(ctx context.Context, in service.RegisterInput) (domain.DeviceToken, bool, error)
	List(ctx context.Context, userID string) ([]domain.DeviceToken, error)
	Delete(ctx context.Context, userID, tokenID string) error
}

// Deps — зависимости HTTP-слоя.
type Deps struct {
	Log     *slog.Logger
	Metrics *metrics.Registry
	Auth    authclient.Authenticator
	Checks  []httpx.Check
	Tokens  Tokens
}

type handlers struct {
	log *slog.Logger
	svc Tokens
}

// New собирает маршруты и стандартную цепочку middleware.
func New(d Deps) http.Handler {
	h := &handlers{log: d.Log, svc: d.Tokens}
	mux := http.NewServeMux()
	httpx.RegisterHealth(mux, d.Checks...)
	mux.Handle("GET /metrics", d.Metrics.Handler())

	// Операции доступны любому аутентифицированному пользователю (гость → 401) и видят только его токены.
	user := requireUser
	mux.Handle("POST /notifications/device-tokens", user(http.HandlerFunc(h.register)))
	mux.Handle("GET /notifications/device-tokens", user(http.HandlerFunc(h.list)))
	mux.Handle("DELETE /notifications/device-tokens/{tokenId}", user(http.HandlerFunc(h.remove)))

	return httpx.Chain(httpx.TrackRoute(mux),
		httpx.RequestContext(), httpx.Recover(d.Log), httpx.AccessLog(d.Log),
		d.Metrics.Middleware(), authclient.Middleware(d.Auth))
}

// requireUser: гость → 401 (роль не важна).
func requireUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p := authclient.FromContext(r.Context()); p.Guest || p.UserID == "" {
			httpx.WriteProblem(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func userID(r *http.Request) string { return authclient.FromContext(r.Context()).UserID }

type registerRequest struct {
	Platform           string `json:"platform"`
	Token              string `json:"token"`
	PermissionsGranted *bool  `json:"permissionsGranted"`
}

type tokenResponse struct {
	ID                 string    `json:"id"`
	Platform           string    `json:"platform"`
	Token              string    `json:"token"`
	PermissionsGranted bool      `json:"permissionsGranted"`
	Locale             string    `json:"locale"`
	RegisteredAt       time.Time `json:"registeredAt"`
}

func toResponse(t domain.DeviceToken) tokenResponse {
	return tokenResponse{ID: t.ID, Platform: string(t.Platform), Token: t.Token, PermissionsGranted: t.PermissionsGranted,
		Locale: string(t.Locale), RegisteredAt: t.RegisteredAt.UTC()}
}

func (h *handlers) register(w http.ResponseWriter, r *http.Request) {
	var in registerRequest
	if !httpx.DecodeJSON(w, r, &in) {
		return
	}
	perm := true
	if in.PermissionsGranted != nil {
		perm = *in.PermissionsGranted
	}
	t, created, err := h.svc.Register(r.Context(), service.RegisterInput{UserID: userID(r), Platform: domain.Platform(in.Platform),
		Token: in.Token, Permissions: perm, Locale: domain.ParseAcceptLanguage(r.Header.Get("Accept-Language"))})
	if err != nil {
		httpx.HandleError(w, r, h.log, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
		w.Header().Set("Location", "/notifications/device-tokens/"+t.ID)
	}
	httpx.WriteJSON(w, status, toResponse(t))
}

func (h *handlers) list(w http.ResponseWriter, r *http.Request) {
	ts, err := h.svc.List(r.Context(), userID(r))
	if err != nil {
		httpx.HandleError(w, r, h.log, err)
		return
	}
	out := make([]tokenResponse, 0, len(ts))
	for _, t := range ts {
		out = append(out, toResponse(t))
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

func (h *handlers) remove(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.Delete(r.Context(), userID(r), r.PathValue("tokenId")); err != nil {
		httpx.HandleError(w, r, h.log, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
