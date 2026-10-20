// Package gateway — периметр платформы (contracts/openapi/gateway.yaml): таблица маршрутов, проверка JWT
// по JWKS (ADR-11), онлайн-проверка для чувствительных маршрутов, лимиты запросов, CORS, ошибки периметра.
package gateway

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/pavelgr1408/food-platform/pkg/authclient"
	"github.com/pavelgr1408/food-platform/pkg/httpx"
	"github.com/pavelgr1408/food-platform/pkg/logging"
	"github.com/pavelgr1408/food-platform/pkg/metrics"
	"github.com/pavelgr1408/food-platform/services/gateway/internal/clientip"
	"github.com/pavelgr1408/food-platform/services/gateway/internal/introspect"
	"github.com/pavelgr1408/food-platform/services/gateway/internal/limiter"
	"github.com/pavelgr1408/food-platform/services/gateway/internal/routes"
)

// Коды ошибок периметра (gateway.yaml).
const (
	CodeUnauthorized   = "UNAUTHORIZED"
	CodeForbidden      = "FORBIDDEN"
	CodeRouteNotFound  = "ROUTE_NOT_FOUND"
	CodeRateLimited    = "RATE_LIMITED"
	CodeTooLarge       = "PAYLOAD_TOO_LARGE"
	CodeUpstreamDown   = "UPSTREAM_UNAVAILABLE"
	CodeUpstreamTimout = "UPSTREAM_TIMEOUT"
	CodeAuthDown       = "AUTH_UNAVAILABLE"
)

// Introspector — онлайн-проверка токена у auth (для sensitive-маршрутов).
type Introspector interface {
	Active(ctx context.Context, cacheKey, rawToken string, notAfter time.Time) (bool, error)
}

// Limits — настройки лимита запросов.
type Limits struct {
	RPS            float64 // класс default: токенов в секунду на ключ
	Burst          int
	AuthPerMin     float64 // класс auth: запросов в минуту на IP
	AuthBurst      int
	MaxTrackedKeys int // по умолчанию 100000 на лимитер
}

// Deps — зависимости.
type Deps struct {
	Log          *slog.Logger
	Metrics      *metrics.Registry
	Routes       []routes.Route
	Upstreams    map[string]*url.URL
	Verifier     *authclient.JWKSVerifier
	Introspector Introspector
	ClientIP     clientip.Resolver
	CORSOrigins  []string
	MaxBody      int64
	Timeout      time.Duration // ожидание ответа сервиса
	Limits       Limits
	Transport    http.RoundTripper // тесты; по умолчанию — транспорт с таймаутами
	Checks       []httpx.Check
}

type gw struct {
	d        Deps
	proxies  map[string]*httputil.ReverseProxy
	defLim   *limiter.Limiter
	authLim  *limiter.Limiter
	origins  map[string]bool
	mRate    *prometheus.CounterVec
	mAuthErr *prometheus.CounterVec
	mUpErr   *prometheus.CounterVec
	mIntro   *prometheus.CounterVec
}

// New собирает обработчик gateway. Ошибка — несогласованность таблицы маршрутов и конфигурации.
func New(d Deps) (http.Handler, error) {
	g := &gw{d: d, proxies: map[string]*httputil.ReverseProxy{}, origins: map[string]bool{}}
	if d.Limits.MaxTrackedKeys <= 0 {
		d.Limits.MaxTrackedKeys = 100000
	}
	g.d = d
	g.defLim = limiter.New(d.Limits.RPS, d.Limits.Burst, d.Limits.MaxTrackedKeys)
	g.authLim = limiter.New(d.Limits.AuthPerMin/60, d.Limits.AuthBurst, d.Limits.MaxTrackedKeys)
	for _, o := range d.CORSOrigins {
		g.origins[o] = true
	}
	g.mRate = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "gateway_rate_limited_total", Help: "Запросы, отклонённые лимитом (класс default|auth)."}, []string{"class"})
	g.mAuthErr = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "gateway_auth_rejected_total", Help: "Запросы, отклонённые на периметре (причина)."}, []string{"reason"})
	g.mUpErr = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "gateway_upstream_errors_total", Help: "Ошибки обращения к сервисам (timeout|unavailable|too_large)."}, []string{"upstream", "kind"})
	g.mIntro = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "gateway_introspections_total", Help: "Онлайн-проверки токенов (active|inactive|unavailable)."}, []string{"result"})
	if d.Metrics != nil {
		d.Metrics.MustRegister("gateway", g.mRate, g.mAuthErr, g.mUpErr, g.mIntro)
	}

	mux := http.NewServeMux()
	httpx.RegisterHealth(mux, d.Checks...)
	for _, rt := range d.Routes {
		target, ok := d.Upstreams[rt.Upstream]
		if !ok {
			return nil, fmt.Errorf("route %q: unknown upstream %q", rt.Pattern, rt.Upstream)
		}
		if g.proxies[rt.Upstream] == nil {
			g.proxies[rt.Upstream] = g.newProxy(rt.Upstream, target)
		}
		mux.Handle(rt.Pattern, g.guard(rt, g.proxies[rt.Upstream]))
	}
	// Всё, чего нет в таблице (внутренние пути сервисов, вебхуки, чужие методы), — единый 404.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteProblem(w, r, http.StatusNotFound, CodeRouteNotFound, "no such route")
	})

	return httpx.Chain(httpx.TrackRoute(mux),
		httpx.RequestContext(), httpx.Recover(d.Log), httpx.AccessLog(d.Log), metricsMW(d.Metrics),
		g.cors, g.bodyLimit), nil
}

func metricsMW(m *metrics.Registry) httpx.Middleware {
	if m == nil {
		return func(h http.Handler) http.Handler { return h }
	}
	return m.Middleware()
}

// --- проверка доступа и лимиты ---

// guard — вход маршрута: лимит класса auth → личность → лимит по ключу → роли → introspect → прокси.
func (g *gw) guard(rt routes.Route, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := g.d.ClientIP.From(r)
		if rt.Limit == routes.LimitAuth && !g.allow(g.authLim, "ip:"+ip, "auth", w, r) {
			return
		}

		id := g.identify(rt, r)
		key := "ip:" + ip
		if id.err == nil && !id.guest {
			key = "u:" + id.claims.Subject
		}
		if !g.allow(g.defLim, key, "default", w, r) {
			return
		}

		if id.err != nil {
			g.reject(w, r, http.StatusUnauthorized, CodeUnauthorized, "invalid", "invalid or expired token", true)
			return
		}
		if rt.Access == routes.Required && id.guest {
			g.reject(w, r, http.StatusUnauthorized, CodeUnauthorized, "missing", "authentication required", true)
			return
		}
		if len(rt.Roles) > 0 && !slices.ContainsFunc(id.claims.Roles, func(role string) bool { return slices.Contains(rt.Roles, role) }) {
			g.reject(w, r, http.StatusForbidden, CodeForbidden, "forbidden", "insufficient permissions", false)
			return
		}
		if rt.Sensitive && !id.guest {
			active, err := g.d.Introspector.Active(r.Context(), id.claims.TokenID, id.raw, id.claims.ExpiresAt)
			switch {
			case err != nil:
				g.mIntro.WithLabelValues("unavailable").Inc()
				g.d.Log.WarnContext(r.Context(), "introspection unavailable; sensitive request refused", "error", err, "route", rt.Pattern)
				g.reject(w, r, http.StatusServiceUnavailable, CodeAuthDown, "introspect_unavailable", "authorization service is unavailable, try again later", false)
				return
			case !active:
				g.mIntro.WithLabelValues("inactive").Inc()
				g.reject(w, r, http.StatusUnauthorized, CodeUnauthorized, "inactive", "invalid or expired token", true)
				return
			default:
				g.mIntro.WithLabelValues("active").Inc()
			}
		}
		next.ServeHTTP(w, r)
	})
}

type identity struct {
	guest  bool
	claims authclient.Claims
	raw    string
	err    error
}

func (g *gw) identify(rt routes.Route, r *http.Request) identity {
	if rt.Access == routes.Public {
		return identity{guest: true}
	}
	h := strings.TrimSpace(r.Header.Get("Authorization"))
	if h == "" {
		return identity{guest: true}
	}
	raw, ok := authclient.BearerToken(h)
	if !ok {
		return identity{err: authclient.ErrUnauthenticated}
	}
	c, err := g.d.Verifier.Verify(r.Context(), raw)
	if err != nil {
		return identity{err: err}
	}
	return identity{claims: c, raw: raw}
}

func (g *gw) allow(l *limiter.Limiter, key, class string, w http.ResponseWriter, r *http.Request) bool {
	ok, retry := l.Allow(key)
	if ok {
		return true
	}
	g.mRate.WithLabelValues(class).Inc()
	secs := int(math.Ceil(retry.Seconds()))
	if secs < 1 {
		secs = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	httpx.WriteProblem(w, r, http.StatusTooManyRequests, CodeRateLimited, "too many requests, retry later")
	return false
}

func (g *gw) reject(w http.ResponseWriter, r *http.Request, status int, code, reason, detail string, challenge bool) {
	g.mAuthErr.WithLabelValues(reason).Inc()
	if challenge {
		w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
	}
	httpx.WriteProblem(w, r, status, code, detail)
}

// --- прокси ---

var stripped = []string{"X-User-Id", "X-User-Roles", "X-Restaurant-Ids", "X-Real-Ip", "X-Forwarded-Prefix", "X-Original-Forwarded-For"}

func (g *gw) newProxy(name string, target *url.URL) *httputil.ReverseProxy {
	tr := g.d.Transport
	if tr == nil {
		tr = &http.Transport{
			Proxy:                 nil, // внутренние адреса кластера: корпоративный прокси не нужен
			DialContext:           (&net.Dialer{Timeout: 2 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			ResponseHeaderTimeout: g.d.Timeout,
			MaxIdleConns:          200,
			MaxIdleConnsPerHost:   64,
			IdleConnTimeout:       90 * time.Second,
		}
	}
	return &httputil.ReverseProxy{
		Transport: tr,
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.Host = "" // Host сервиса, а не публичный
			h := pr.Out.Header
			// Личность берётся только из JWT: сервисы в режиме jwks заголовки X-User-* всё равно игнорируют,
			// но снимаем их и здесь — глубокая защита и чистые логи.
			for _, k := range stripped {
				h.Del(k)
			}
			ctx := pr.In.Context()
			if id := logging.RequestID(ctx); id != "" {
				h.Set(httpx.HeaderRequestID, id)
			}
			if h.Get("traceparent") == "" {
				if t := logging.TraceID(ctx); t != "" {
					h.Set("traceparent", "00-"+t+"-"+spanID()+"-01")
				}
			}
			h.Set("X-Forwarded-For", g.d.ClientIP.From(pr.In))
			h.Set("X-Forwarded-Host", pr.In.Host)
			h.Set("X-Forwarded-Proto", g.scheme(pr.In))
		},
		ModifyResponse: func(resp *http.Response) error {
			// X-Request-Id в ответе уже выставлен gateway (то же значение ушло сервису): копия сервиса дала бы дубль.
			resp.Header.Del(httpx.HeaderRequestID)
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			var tooBig *http.MaxBytesError
			var ne net.Error
			switch {
			case errors.As(err, &tooBig):
				g.mUpErr.WithLabelValues(name, "too_large").Inc()
				httpx.WriteProblem(w, r, http.StatusRequestEntityTooLarge, CodeTooLarge, "request body is too large")
			case errors.Is(err, context.Canceled):
				// клиент ушёл — отвечать некому
				g.d.Log.DebugContext(r.Context(), "client went away", "upstream", name)
			case errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()):
				g.mUpErr.WithLabelValues(name, "timeout").Inc()
				g.d.Log.WarnContext(r.Context(), "upstream timeout", "upstream", name, "path", r.URL.Path)
				httpx.WriteProblem(w, r, http.StatusGatewayTimeout, CodeUpstreamTimout, "the service did not respond in time")
			default:
				g.mUpErr.WithLabelValues(name, "unavailable").Inc()
				g.d.Log.WarnContext(r.Context(), "upstream unavailable", "upstream", name, "path", r.URL.Path, "error", err)
				httpx.WriteProblem(w, r, http.StatusBadGateway, CodeUpstreamDown, "the service is unavailable")
			}
		},
	}
}

func (g *gw) scheme(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	// схему от ingress принимаем только от доверенного прокси
	if p := r.Header.Get("X-Forwarded-Proto"); (p == "https" || p == "http") && g.d.ClientIP.PeerTrusted(r) {
		return p
	}
	return "http"
}

func spanID() string {
	var b [8]byte
	_, _ = randRead(b[:])
	return fmt.Sprintf("%x", b)
}

// --- CORS и лимит тела ---

var corsAllowHeaders = "Authorization, Content-Type, Idempotency-Key, If-None-Match, X-Request-Id"
var corsExposeHeaders = "ETag, Location, Retry-After, Idempotent-Replay, X-Request-Id"

func (g *gw) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin == "" {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Add("Vary", "Origin")
		if !g.origins[origin] {
			next.ServeHTTP(w, r)
			return
		}
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", origin)
		h.Set("Access-Control-Expose-Headers", corsExposeHeaders)
		if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
			h.Add("Vary", "Access-Control-Request-Method")
			h.Add("Vary", "Access-Control-Request-Headers")
			h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, OPTIONS")
			h.Set("Access-Control-Allow-Headers", corsAllowHeaders)
			h.Set("Access-Control-Max-Age", "600")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (g *gw) bodyLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength > g.d.MaxBody {
			g.mUpErr.WithLabelValues("gateway", "too_large").Inc()
			httpx.WriteProblem(w, r, http.StatusRequestEntityTooLarge, CodeTooLarge, "request body is too large")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, g.d.MaxBody)
		next.ServeHTTP(w, r)
	})
}

var _ Introspector = (*introspect.Client)(nil)
