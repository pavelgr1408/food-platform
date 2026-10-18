// Package authclient — интерфейс аутентификации/авторизации для сервисов.
//
// S0: контракт (Principal, Authenticator, middleware, RequireRole) и dev-реализация
// из заголовков. S5: добавится реализация с локальной проверкой JWT по JWKS
// (ADR-11) — код сервисов не изменится, поменяется только конструктор.
package authclient

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/pavelgr1408/food-platform/pkg/httpx"
)

// Роли платформы (S5 приводит auth-service к этому списку).
const (
	RoleCustomer       = "CUSTOMER"
	RoleStaff          = "STAFF"
	RoleFranchiseOwner = "FRANCHISE_OWNER"
	RoleHQStaff        = "HQ_STAFF"
)

// Principal — субъект запроса.
type Principal struct {
	UserID      string // пусто для гостя
	Roles       []string
	Restaurants []string // рестораны, к которым привязан сотрудник/владелец
	Guest       bool
}

// HasRole — есть ли у субъекта хотя бы одна из ролей.
func (p Principal) HasRole(roles ...string) bool {
	for _, r := range roles {
		if slices.Contains(p.Roles, r) {
			return true
		}
	}
	return false
}

// CanAccessRestaurant: HQ видит всё, остальные — только свои рестораны.
func (p Principal) CanAccessRestaurant(id string) bool {
	return p.HasRole(RoleHQStaff) || slices.Contains(p.Restaurants, id)
}

// ErrUnauthenticated — токен отсутствует/недействителен.
var ErrUnauthenticated = errors.New("unauthenticated")

// Authenticator извлекает Principal из запроса. Нет учётных данных → (Guest, nil);
// есть, но недействительные → ErrUnauthenticated.
type Authenticator interface {
	Authenticate(r *http.Request) (Principal, error)
}

type ctxKey struct{}

// FromContext возвращает Principal запроса (гость, если middleware не отработал).
func FromContext(ctx context.Context) Principal {
	if p, ok := ctx.Value(ctxKey{}).(Principal); ok {
		return p
	}
	return Principal{Guest: true}
}

// WithPrincipal кладёт Principal в контекст (для тестов и сервисных вызовов).
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

// Middleware аутентифицирует запрос; гостей пропускает (ограничивайте RequireRole).
func Middleware(a Authenticator) httpx.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, err := a.Authenticate(r)
			if err != nil {
				w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
				httpx.WriteProblem(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "invalid or missing credentials")
				return
			}
			next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), p)))
		})
	}
}

// RequireRole пропускает только субъектов с одной из ролей: гость → 401, нет роли → 403.
func RequireRole(roles ...string) httpx.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p := FromContext(r.Context())
			if p.Guest || p.UserID == "" {
				httpx.WriteProblem(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
				return
			}
			if !p.HasRole(roles...) {
				httpx.WriteProblem(w, r, http.StatusForbidden, "FORBIDDEN", "insufficient permissions")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// Dev-заголовки (НЕ для продакшена: доверяют клиенту!).
const (
	HeaderUserID      = "X-User-Id"
	HeaderUserRoles   = "X-User-Roles"
	HeaderRestaurants = "X-Restaurant-Ids"
)

// DevAuthenticator читает Principal из X-User-Id / X-User-Roles / X-Restaurant-Ids.
// Нужен, пока нет gateway (до S5). Создаётся только явно и громко предупреждает в лог;
// в проде (APP_ENV=prod) сервис обязан отказаться стартовать — см. NewFromMode.
type DevAuthenticator struct{}

// Authenticate реализует Authenticator.
func (DevAuthenticator) Authenticate(r *http.Request) (Principal, error) {
	id := strings.TrimSpace(r.Header.Get(HeaderUserID))
	if id == "" {
		return Principal{Guest: true}, nil
	}
	return Principal{UserID: id, Roles: split(r.Header.Get(HeaderUserRoles)), Restaurants: split(r.Header.Get(HeaderRestaurants))}, nil
}

func split(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// NewFromMode строит Authenticator по AUTH_MODE: "dev" — заголовки (запрещено в prod).
// Режим "jwks" появится в S5.
func NewFromMode(mode, env string, log *slog.Logger) (Authenticator, error) {
	switch mode {
	case "dev":
		if env == "prod" {
			return nil, errors.New("AUTH_MODE=dev is forbidden when APP_ENV=prod")
		}
		log.Warn("AUTH_MODE=dev: identity is taken from X-User-* headers — NOT SAFE outside local development")
		return DevAuthenticator{}, nil
	case "":
		return nil, errors.New("AUTH_MODE is required (dev | jwks)")
	default:
		return nil, errors.New("unsupported AUTH_MODE " + mode + " (jwks is implemented in S5)")
	}
}
