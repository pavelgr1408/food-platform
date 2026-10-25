// Package config — конфигурация delivery-service из окружения (12-factor).
package config

import (
	"errors"
	"net/url"
	"time"

	"github.com/pavelgr1408/food-platform/pkg/authclient"
	"github.com/pavelgr1408/food-platform/pkg/config"
)

// Dev-значения секретов: только для APP_ENV=dev. В остальных окружениях секреты обязательны.
const (
	devCourierAPIKey        = "dev-courier-api-key"
	devCourierWebhookSecret = "dev-courier-webhook-secret"
	minSecretLen            = 16
)

// Config — настройки сервиса.
type Config struct {
	config.Base
	Auth         authclient.Options // AUTH_MODE (dev|jwks), AUTH_JWKS_URL, AUTH_ISSUER, AUTH_AUDIENCE
	KafkaBrokers []string

	RedisAddr     string // delivery-Redis (координаты курьера); пусто — координаты не сохраняются
	RedisPassword string // секрет

	RestaurantURL string // restaurant-service (координаты ресторана)

	CourierURL           string        // курьерская система (в dev — mock-externals, модуль courier)
	CourierAPIKey        string        // ключ API (секрет)
	CourierWebhookSecret string        // подпись вебхуков курьерской системы (секрет)
	CourierTimeout       time.Duration // на одну попытку вызова

	SearchTimeout     time.Duration // курьер не назначен за это время → failed/no_courier
	LocationTTL       time.Duration // срок жизни точки в Redis
	PollEvery         time.Duration // период опроса курьерской системы
	PollMax           time.Duration // верхняя граница backoff
	LinkWait          time.Duration // ожидание платёжной ссылки перед заявкой (оплата курьеру)
	ReconcileInterval time.Duration // тик цикла диспетчеризации/опроса
}

// Load читает окружение; возвращает все найденные ошибки разом.
func Load() (Config, error) { return load(config.FromEnv()) }

func load(r *config.Reader) (Config, error) {
	c := Config{
		Base:              config.LoadBase(r, "delivery", "8087", true),
		KafkaBrokers:      r.List("KAFKA_BROKERS", nil),
		RedisAddr:         r.String("REDIS_ADDR", ""),
		RestaurantURL:     r.String("RESTAURANT_URL", "http://localhost:8082"),
		CourierURL:        r.String("COURIER_URL", "http://localhost:9094"),
		CourierTimeout:    r.Duration("COURIER_TIMEOUT", 5*time.Second),
		SearchTimeout:     r.Duration("SEARCH_TIMEOUT", 10*time.Minute),
		LocationTTL:       r.Duration("LOCATION_TTL", time.Minute),
		PollEvery:         r.Duration("POLL_INTERVAL", 3*time.Second),
		PollMax:           r.Duration("POLL_BACKOFF_MAX", 30*time.Second),
		LinkWait:          r.Duration("LINK_WAIT", 20*time.Second),
		ReconcileInterval: r.Duration("RECONCILE_INTERVAL", time.Second),
	}
	dev := c.Env == "dev"
	secret := func(key, devDefault string) string {
		if dev {
			return r.String(key, devDefault)
		}
		return r.Required(key)
	}
	c.CourierAPIKey = secret("COURIER_API_KEY", devCourierAPIKey)
	c.CourierWebhookSecret = secret("COURIER_WEBHOOK_SECRET", devCourierWebhookSecret)
	c.RedisPassword = r.String("REDIS_PASSWORD", "")

	var bad []error
	for k, v := range map[string]string{"RESTAURANT_URL": c.RestaurantURL, "COURIER_URL": c.CourierURL} {
		if u, e := url.Parse(v); e != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			bad = append(bad, errors.New(k+" must be an http(s) URL"))
		}
	}
	if !dev {
		for k, v := range map[string]string{"COURIER_API_KEY": c.CourierAPIKey, "COURIER_WEBHOOK_SECRET": c.CourierWebhookSecret} {
			if v != "" && len(v) < minSecretLen {
				bad = append(bad, errors.New(k+" is too short (min 16 characters)"))
			}
			if v == devCourierAPIKey || v == devCourierWebhookSecret {
				bad = append(bad, errors.New(k+" must not use the dev default outside APP_ENV=dev"))
			}
		}
	}
	for k, v := range map[string]time.Duration{"COURIER_TIMEOUT": c.CourierTimeout, "SEARCH_TIMEOUT": c.SearchTimeout,
		"LOCATION_TTL": c.LocationTTL, "POLL_INTERVAL": c.PollEvery, "POLL_BACKOFF_MAX": c.PollMax, "LINK_WAIT": c.LinkWait,
		"RECONCILE_INTERVAL": c.ReconcileInterval} {
		if v <= 0 {
			bad = append(bad, errors.New(k+" must be positive"))
		}
	}
	if c.PollMax < c.PollEvery {
		bad = append(bad, errors.New("POLL_BACKOFF_MAX must be >= POLL_INTERVAL"))
	}
	c.Auth = authclient.OptionsFromEnv(r, c.Env)
	return c, errors.Join(append([]error{r.Err()}, bad...)...)
}
