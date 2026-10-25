// Package config — конфигурация api-gateway из окружения (12-factor).
package config

import (
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/pavelgr1408/food-platform/pkg/authclient"
	"github.com/pavelgr1408/food-platform/pkg/config"
)

// Upstreams — сервисы, на которые ссылается таблица маршрутов.
var Upstreams = []string{"auth", "restaurant", "menu", "orders", "payment", "delivery"}

var defaultUpstreamURL = map[string]string{
	"auth": "http://localhost:8081", "restaurant": "http://localhost:8082", "menu": "http://localhost:8083",
	"orders": "http://localhost:8084", "payment": "http://localhost:8085", "delivery": "http://localhost:8087",
}

// Config — настройки gateway.
type Config struct {
	config.Base
	Auth      authclient.Options // AUTH_JWKS_URL, AUTH_ISSUER, AUTH_AUDIENCE (режим всегда jwks)
	Upstreams map[string]*url.URL

	MetricsAddr     string // /metrics и pprof-подобное наружу не отдаём: отдельный порт
	MaxBodyBytes    int64
	UpstreamTimeout time.Duration

	CORSOrigins       []string
	TrustedProxyCIDRs []netip.Prefix

	RateRPS        float64 // класс default: запросов в секунду на ключ (пользователь или IP)
	RateBurst      int
	AuthRatePerMin float64 // класс auth: запросов в минуту на IP (register/login/refresh)
	AuthBurst      int

	IntrospectCacheTTL time.Duration
	IntrospectTimeout  time.Duration
}

func Load() (Config, error) { return load(config.FromEnv()) }

func load(r *config.Reader) (Config, error) {
	c := Config{
		Base:               config.LoadBase(r, "gateway", "8080", false),
		MetricsAddr:        r.String("METRICS_ADDR", ":9100"),
		MaxBodyBytes:       int64(r.Int("MAX_BODY_BYTES", 1<<20)),
		UpstreamTimeout:    r.Duration("UPSTREAM_TIMEOUT", 10*time.Second),
		CORSOrigins:        r.List("CORS_ALLOWED_ORIGINS", nil),
		RateRPS:            float64(r.Int("RATE_LIMIT_RPS", 50)),
		RateBurst:          r.Int("RATE_LIMIT_BURST", 100),
		AuthRatePerMin:     float64(r.Int("AUTH_RATE_PER_MIN", 10)),
		AuthBurst:          r.Int("AUTH_RATE_BURST", 10),
		IntrospectCacheTTL: r.Duration("INTROSPECT_CACHE_TTL", 10*time.Second),
		IntrospectTimeout:  r.Duration("INTROSPECT_TIMEOUT", time.Second),
	}
	c.Auth = authclient.OptionsFromEnv(r, c.Env)
	c.Auth.Mode = "jwks" // gateway всегда проверяет JWT; AUTH_MODE=dev здесь не существует

	var bad []error
	c.Upstreams = map[string]*url.URL{}
	for _, name := range Upstreams {
		key := "UPSTREAM_" + strings.ToUpper(name) + "_URL"
		raw := r.String(key, defaultUpstreamURL[name])
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || (u.Path != "" && u.Path != "/") {
			bad = append(bad, fmt.Errorf("%s=%q must be an http(s) URL without a path", key, raw))
			continue
		}
		c.Upstreams[name] = u
	}
	if c.Auth.JWKSURL == "" {
		// По умолчанию — JWKS того же auth, на который указывает UPSTREAM_AUTH_URL.
		if u := c.Upstreams["auth"]; u != nil {
			c.Auth.JWKSURL = strings.TrimRight(u.String(), "/") + "/auth/.well-known/jwks.json"
		}
	}
	for _, o := range c.CORSOrigins {
		if o == "*" {
			bad = append(bad, errors.New(`CORS_ALLOWED_ORIGINS must list exact origins; "*" is not allowed`))
		} else if u, err := url.Parse(o); err != nil || u.Scheme == "" || u.Host == "" || u.Path != "" {
			bad = append(bad, fmt.Errorf("CORS_ALLOWED_ORIGINS: %q must look like https://host[:port]", o))
		}
	}
	cidrs := r.List("TRUSTED_PROXY_CIDRS", nil)
	for _, s := range cidrs {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			bad = append(bad, fmt.Errorf("TRUSTED_PROXY_CIDRS: %q: %w", s, err))
			continue
		}
		if c.Env == "prod" && p.Bits() == 0 {
			bad = append(bad, fmt.Errorf("TRUSTED_PROXY_CIDRS: %q trusts every peer and is forbidden when APP_ENV=prod", s))
		}
		c.TrustedProxyCIDRs = append(c.TrustedProxyCIDRs, p)
	}
	if c.MaxBodyBytes <= 0 || c.RateRPS <= 0 || c.RateBurst <= 0 || c.AuthRatePerMin <= 0 || c.AuthBurst <= 0 {
		bad = append(bad, errors.New("MAX_BODY_BYTES, RATE_LIMIT_RPS, RATE_LIMIT_BURST, AUTH_RATE_PER_MIN, AUTH_RATE_BURST must be positive"))
	}
	return c, errors.Join(append(bad, r.Err())...)
}
