// Package httpapi — HTTP-слой auth-service (contracts/openapi/auth.yaml). Ошибки — problem+json (pkg/httpx).
package httpapi

import (
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/pavelgr1408/food-platform/pkg/authclient"
	"github.com/pavelgr1408/food-platform/pkg/httpx"
	"github.com/pavelgr1408/food-platform/pkg/metrics"
	"github.com/pavelgr1408/food-platform/services/auth/internal/domain"
	"github.com/pavelgr1408/food-platform/services/auth/internal/service"
)

// Коды ошибок (auth.yaml, Problem.code).
const (
	codeValidation    = "VALIDATION_FAILED"
	codeUnknownAction = "UNKNOWN_ACTION"
	codeInvalidToken  = "INVALID_TOKEN"
)

var phonePattern = regexp.MustCompile(`^\+[1-9][0-9]{7,14}$`)

// Deps — зависимости HTTP-слоя.
type Deps struct {
	Log     *slog.Logger
	Metrics *metrics.Registry
	Checks  []httpx.Check
	Service *service.Service
}

type API struct {
	service *service.Service
	log     *slog.Logger
}

// New собирает маршруты и стандартную цепочку middleware.
func New(d Deps) http.Handler {
	a := &API{service: d.Service, log: d.Log}
	mux := http.NewServeMux()
	httpx.RegisterHealth(mux, d.Checks...)
	mux.Handle("GET /metrics", d.Metrics.Handler())

	mux.HandleFunc("POST /auth/register", a.register)
	mux.HandleFunc("POST /auth/login", a.login)
	mux.HandleFunc("POST /auth/refresh", a.refresh)
	mux.HandleFunc("POST /auth/logout", a.logout)
	mux.HandleFunc("GET /auth/me", a.me)
	mux.HandleFunc("POST /auth/introspect", a.introspect)
	mux.HandleFunc("POST /auth/authorize", a.authorize)
	mux.HandleFunc("GET /auth/.well-known/jwks.json", a.jwks)

	return httpx.Chain(httpx.TrackRoute(mux),
		httpx.RequestContext(), httpx.Recover(d.Log), httpx.AccessLog(d.Log), d.Metrics.Middleware())
}

type credentials struct {
	Phone    string `json:"phone"`
	Password string `json:"password"`
}
type refreshRequest struct {
	RefreshToken string `json:"refreshToken"`
}
type introspectRequest struct {
	Token string `json:"token"`
}
type authorizeRequest struct {
	Action       string `json:"action"`
	Scope        string `json:"scope"`
	RestaurantID string `json:"restaurantId"`
}

func (a *API) register(w http.ResponseWriter, r *http.Request) {
	var req credentials
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	if msg := validateCredentials(req); msg != "" {
		httpx.WriteProblem(w, r, http.StatusBadRequest, codeValidation, msg)
		return
	}
	u, err := a.service.Register(r.Context(), strings.TrimSpace(req.Phone), req.Password)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{"userId": u.ID, "phone": u.Phone, "status": u.Status})
}

func (a *API) login(w http.ResponseWriter, r *http.Request) {
	var req credentials
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	if msg := validateCredentials(req); msg != "" {
		httpx.WriteProblem(w, r, http.StatusBadRequest, codeValidation, msg)
		return
	}
	pair, err := a.service.Login(r.Context(), strings.TrimSpace(req.Phone), req.Password)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, pair)
}

func (a *API) refresh(w http.ResponseWriter, r *http.Request) {
	var req refreshRequest
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.RefreshToken) == "" {
		httpx.WriteProblem(w, r, http.StatusBadRequest, codeValidation, "Validation failed: refreshToken must not be blank")
		return
	}
	pair, err := a.service.Refresh(r.Context(), req.RefreshToken)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, pair)
}

func (a *API) logout(w http.ResponseWriter, r *http.Request) {
	var req refreshRequest
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.RefreshToken) == "" {
		httpx.WriteProblem(w, r, http.StatusBadRequest, codeValidation, "Validation failed: refreshToken must not be blank")
		return
	}
	if err := a.service.Logout(r.Context(), req.RefreshToken); err != nil {
		a.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"message": "Logged out successfully"})
}

func (a *API) me(w http.ResponseWriter, r *http.Request) {
	raw, ok := bearer(w, r)
	if !ok {
		return
	}
	id, err := a.service.ValidateAccess(raw)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	u, err := a.service.Me(r.Context(), id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"userId": u.ID, "phone": u.Phone, "roles": u.Roles, "status": u.Status, "restaurants": ids(u.Restaurants)})
}

func (a *API) introspect(w http.ResponseWriter, r *http.Request) {
	var req introspectRequest
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Token) == "" {
		httpx.WriteProblem(w, r, http.StatusBadRequest, codeValidation, "Validation failed: token must not be blank")
		return
	}
	u, active := a.service.Introspect(r.Context(), req.Token)
	if !active {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"active": false})
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"active": true, "userId": u.ID, "phone": u.Phone, "roles": u.Roles, "restaurants": ids(u.Restaurants)})
}

func (a *API) authorize(w http.ResponseWriter, r *http.Request) {
	raw, ok := bearer(w, r)
	if !ok {
		return
	}
	var req authorizeRequest
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	ar := domain.AuthzRequest{Action: req.Action, Scope: req.Scope}
	if req.RestaurantID != "" {
		id, err := uuid.Parse(req.RestaurantID)
		if err != nil {
			httpx.WriteProblem(w, r, http.StatusBadRequest, codeValidation, "Validation failed: restaurantId must be a UUID")
			return
		}
		ar.RestaurantID = &id
	}
	d, err := a.service.Authorize(r.Context(), raw, ar)
	switch {
	case errors.Is(err, domain.ErrUnknownAction):
		httpx.WriteProblem(w, r, http.StatusBadRequest, codeUnknownAction, "Unknown action: "+req.Action)
	case errors.Is(err, domain.ErrInvalidAuthz):
		httpx.WriteProblem(w, r, http.StatusBadRequest, codeValidation, "Validation failed: "+err.Error())
	case err != nil:
		a.fail(w, r, err)
	default:
		resp := map[string]any{"allowed": d.Allowed}
		if !d.Allowed {
			resp["reason"] = d.Reason
		}
		httpx.WriteJSON(w, http.StatusOK, resp)
	}
}

func (a *API) jwks(w http.ResponseWriter, r *http.Request) {
	// Потребители кэшируют сами (по kid); минута — чтобы не бить в auth при каждом неизвестном kid.
	w.Header().Set("Cache-Control", "public, max-age=60")
	httpx.WriteJSON(w, http.StatusOK, a.service.JWKS())
}

func bearer(w http.ResponseWriter, r *http.Request) (string, bool) {
	raw, ok := authclient.BearerToken(r.Header.Get("Authorization"))
	if !ok {
		w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
		httpx.WriteProblem(w, r, http.StatusUnauthorized, codeInvalidToken, domain.ErrInvalidAccessToken.Error())
		return "", false
	}
	return raw, true
}

func ids(in []uuid.UUID) []uuid.UUID {
	if in == nil {
		return []uuid.UUID{}
	}
	return in
}

func validateCredentials(req credentials) string {
	if !phonePattern.MatchString(req.Phone) {
		return "Validation failed: phone must be a valid E.164 number"
	}
	n := utf8.RuneCountInString(req.Password)
	if strings.TrimSpace(req.Password) == "" || n < 6 || n > 72 {
		return "Validation failed: password size must be between 6 and 72"
	}
	return ""
}

// fail превращает доменные ошибки в problem с кодом из контракта; остальное — 500 без утечки деталей.
func (a *API) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, domain.ErrDuplicatePhone):
		httpx.WriteProblem(w, r, http.StatusConflict, "PHONE_ALREADY_REGISTERED", err.Error())
	case errors.Is(err, domain.ErrInvalidCredentials):
		httpx.WriteProblem(w, r, http.StatusUnauthorized, "INVALID_CREDENTIALS", err.Error())
	case errors.Is(err, domain.ErrInvalidAccessToken):
		w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
		httpx.WriteProblem(w, r, http.StatusUnauthorized, codeInvalidToken, err.Error())
	case errors.Is(err, domain.ErrInvalidRefreshToken):
		httpx.WriteProblem(w, r, http.StatusUnauthorized, "INVALID_REFRESH_TOKEN", err.Error())
	case errors.Is(err, domain.ErrExpiredRefreshToken):
		httpx.WriteProblem(w, r, http.StatusUnauthorized, "REFRESH_TOKEN_EXPIRED", err.Error())
	case errors.Is(err, domain.ErrUserNotActive):
		httpx.WriteProblem(w, r, http.StatusForbidden, "USER_NOT_ACTIVE", err.Error())
	default:
		httpx.HandleError(w, r, a.log, err)
	}
}
