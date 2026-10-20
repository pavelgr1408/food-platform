// Package authclient — интерфейс аутентификации/авторизации для сервисов.
//
// Контракт (Principal, Authenticator, middleware, RequireRole), dev-реализация из заголовков (S0)
// и локальная проверка JWT по JWKS (S5, ADR-11, jwks.go). Код обработчиков сервисов не зависит от
// режима: меняется только конструктор (New / AUTH_MODE).
package authclient

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/pavelgr1408/food-platform/pkg/config"
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
// Только для разработки срезов без auth. Создаётся только явно и громко предупреждает в лог;
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

// Options — настройки аутентификации сервиса (AUTH_MODE и параметры JWKS).
type Options struct {
	Mode     string // dev | jwks
	Env      string // APP_ENV: в prod режим dev запрещён
	JWKSURL  string
	Issuer   string
	Audience string
}

// OptionsFromEnv читает AUTH_MODE, AUTH_JWKS_URL, AUTH_ISSUER, AUTH_AUDIENCE.
// Режим по умолчанию — dev (срезы разработки без auth); в проде обязателен jwks.
func OptionsFromEnv(r *config.Reader, env string) Options {
	return Options{
		Mode:     r.String("AUTH_MODE", "dev"),
		Env:      env,
		JWKSURL:  r.String("AUTH_JWKS_URL", ""),
		Issuer:   r.String("AUTH_ISSUER", "restaurant-auth-service"),
		Audience: r.String("AUTH_AUDIENCE", "restaurant-api"),
	}
}

// New строит Authenticator по настройкам: "dev" — заголовки (запрещено в prod), "jwks" — локальная
// проверка JWT по JWKS auth-service (ADR-11). В режиме jwks ключи читаются ленивой: если auth ещё не
// поднят, сервис стартует, а запросы с токеном получают 401, пока набор ключей не будет прочитан.
func New(o Options, log *slog.Logger) (Authenticator, error) {
	switch o.Mode {
	case "dev":
		if o.Env == "prod" {
			return nil, errors.New("AUTH_MODE=dev is forbidden when APP_ENV=prod")
		}
		log.Warn("AUTH_MODE=dev: identity is taken from X-User-* headers — NOT SAFE outside local development")
		return DevAuthenticator{}, nil
	case "jwks":
		v, err := NewJWKSVerifier(JWKSOptions{URL: o.JWKSURL, Issuer: o.Issuer, Audience: o.Audience, Log: log})
		if err != nil {
			return nil, fmt.Errorf("AUTH_MODE=jwks: %w (AUTH_JWKS_URL, AUTH_ISSUER, AUTH_AUDIENCE)", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := v.Warmup(ctx); err != nil {
			log.Warn("jwks is not available yet; keys will be loaded on first token", "error", err, "url", o.JWKSURL)
		}
		return v.Authenticator(), nil
	case "":
		return nil, errors.New("AUTH_MODE is required (dev | jwks)")
	default:
		return nil, errors.New("unsupported AUTH_MODE " + o.Mode + " (dev | jwks)")
	}
}

// NewFromMode — краткая форма New для режима dev (сохранена для совместимости и тестов).
// Для jwks используйте New с Options.
func NewFromMode(mode, env string, log *slog.Logger) (Authenticator, error) {
	return New(Options{Mode: mode, Env: env}, log)
}
