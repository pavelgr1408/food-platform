package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/example/restaurant-auth-service/internal/domain"
	"github.com/example/restaurant-auth-service/internal/service"
	"github.com/example/restaurant-auth-service/internal/store"
	"github.com/google/uuid"
)

type API struct {
	service *service.Service
	store   *store.Postgres
	log     *slog.Logger
}
type ctxKey string

const userIDKey ctxKey = "userID"

var phonePattern = regexp.MustCompile(`^\+[1-9][0-9]{7,14}$`)

func New(svc *service.Service, st *store.Postgres, log *slog.Logger) http.Handler {
	a := &API{service: svc, store: st, log: log}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /auth/register", a.register)
	mux.HandleFunc("POST /auth/login", a.login)
	mux.HandleFunc("POST /auth/refresh", a.refresh)
	mux.HandleFunc("POST /auth/logout", a.logout)
	mux.Handle("GET /auth/me", a.authenticate(http.HandlerFunc(a.me)))
	mux.HandleFunc("POST /auth/introspect", a.introspect)
	mux.HandleFunc("GET /auth/.well-known/jwks.json", a.jwks)
	mux.HandleFunc("GET /actuator/health", a.health)
	return a.recover(a.jsonContent(mux))
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
type errorResponse struct {
	Timestamp time.Time `json:"timestamp"`
	Status    int       `json:"status"`
	Error     string    `json:"error"`
	Message   string    `json:"message"`
	Path      string    `json:"path"`
}

func (a *API) register(w http.ResponseWriter, r *http.Request) {
	var req credentials
	if !a.decode(w, r, &req) {
		return
	}
	if msg := validateCredentials(req); msg != "" {
		a.problem(w, r, 400, msg)
		return
	}
	u, err := a.service.Register(r.Context(), strings.TrimSpace(req.Phone), req.Password)
	if err != nil {
		a.handle(w, r, err)
		return
	}
	a.write(w, 201, map[string]any{"userId": u.ID, "phone": u.Phone, "status": u.Status})
}
func (a *API) login(w http.ResponseWriter, r *http.Request) {
	var req credentials
	if !a.decode(w, r, &req) {
		return
	}
	if msg := validateCredentials(req); msg != "" {
		a.problem(w, r, 400, msg)
		return
	}
	pair, err := a.service.Login(r.Context(), strings.TrimSpace(req.Phone), req.Password)
	if err != nil {
		a.handle(w, r, err)
		return
	}
	a.write(w, 200, pair)
}
func (a *API) refresh(w http.ResponseWriter, r *http.Request) {
	var req refreshRequest
	if !a.decode(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.RefreshToken) == "" {
		a.problem(w, r, 400, "Validation failed: refreshToken must not be blank")
		return
	}
	pair, err := a.service.Refresh(r.Context(), req.RefreshToken)
	if err != nil {
		a.handle(w, r, err)
		return
	}
	a.write(w, 200, pair)
}
func (a *API) logout(w http.ResponseWriter, r *http.Request) {
	var req refreshRequest
	if !a.decode(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.RefreshToken) == "" {
		a.problem(w, r, 400, "Validation failed: refreshToken must not be blank")
		return
	}
	if err := a.service.Logout(r.Context(), req.RefreshToken); err != nil {
		a.handle(w, r, err)
		return
	}
	a.write(w, 200, map[string]string{"message": "Logged out successfully"})
}
func (a *API) me(w http.ResponseWriter, r *http.Request) {
	id, _ := r.Context().Value(userIDKey).(uuid.UUID)
	u, err := a.service.Me(r.Context(), id)
	if err != nil {
		a.handle(w, r, err)
		return
	}
	a.write(w, 200, map[string]any{"userId": u.ID, "phone": u.Phone, "roles": u.Roles, "status": u.Status})
}
func (a *API) introspect(w http.ResponseWriter, r *http.Request) {
	var req introspectRequest
	if !a.decode(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Token) == "" {
		a.problem(w, r, 400, "Validation failed: token must not be blank")
		return
	}
	u, active := a.service.Introspect(r.Context(), req.Token)
	if !active {
		a.write(w, 200, map[string]any{"active": false})
		return
	}
	a.write(w, 200, map[string]any{"active": true, "userId": u.ID, "phone": u.Phone, "roles": u.Roles})
}
func (a *API) jwks(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	a.write(w, 200, a.service.JWKS())
}
func (a *API) health(w http.ResponseWriter, r *http.Request) {
	if err := a.store.Ping(r.Context()); err != nil {
		a.write(w, 503, map[string]string{"status": "DOWN"})
		return
	}
	a.write(w, 200, map[string]string{"status": "UP"})
}

func (a *API) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		if !strings.HasPrefix(header, "Bearer ") || strings.TrimSpace(strings.TrimPrefix(header, "Bearer ")) == "" {
			a.problem(w, r, 401, "Invalid access token")
			return
		}
		id, err := a.service.ValidateAccess(strings.TrimSpace(strings.TrimPrefix(header, "Bearer ")))
		if err != nil {
			a.problem(w, r, 401, "Invalid access token")
			return
		}
		u, err := a.service.Me(r.Context(), id)
		if err != nil {
			a.handle(w, r, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userIDKey, u.ID)))
	})
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

func (a *API) decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := dec.Decode(dst); err != nil {
		a.problem(w, r, 400, "Malformed JSON request")
		return false
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		a.problem(w, r, 400, "Malformed JSON request")
		return false
	}
	return true
}
func (a *API) write(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json;charset=UTF-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func (a *API) problem(w http.ResponseWriter, r *http.Request, status int, message string) {
	a.write(w, status, errorResponse{Timestamp: time.Now().UTC(), Status: status, Error: http.StatusText(status), Message: message, Path: r.URL.Path})
}
func (a *API) handle(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, domain.ErrDuplicatePhone):
		a.problem(w, r, 409, err.Error())
	case errors.Is(err, domain.ErrInvalidCredentials), errors.Is(err, domain.ErrInvalidAccessToken), errors.Is(err, domain.ErrInvalidRefreshToken), errors.Is(err, domain.ErrExpiredRefreshToken):
		a.problem(w, r, 401, err.Error())
	case errors.Is(err, domain.ErrUserNotActive):
		a.problem(w, r, 403, err.Error())
	default:
		a.log.Error("request failed", "method", r.Method, "path", r.URL.Path, "error", err)
		a.problem(w, r, 500, "Internal server error")
	}
}
func (a *API) recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if value := recover(); value != nil {
				a.log.Error("panic", "value", value)
				a.problem(w, r, 500, "Internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}
func (a *API) jsonContent(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { next.ServeHTTP(w, r) })
}
