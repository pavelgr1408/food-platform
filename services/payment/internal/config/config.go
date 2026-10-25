// Package config — конфигурация payment-service из окружения (12-factor).
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
	devGatewayWebhookSecret = "dev-gateway-webhook-secret"
	devPOSWebhookSecret     = "dev-pos-webhook-secret"
	devPOSAPIKey            = "dev-pos-api-key"
	devGatewayAPIKey        = "dev-gateway-api-key"
	minSecretLen            = 16
)

// Config — настройки сервиса.
type Config struct {
	config.Base
	Auth         authclient.Options // AUTH_MODE (dev|jwks), AUTH_JWKS_URL, AUTH_ISSUER, AUTH_AUDIENCE
	KafkaBrokers []string

	GatewayURL     string        // платёжный шлюз (в dev — mock-externals)
	GatewayAPIKey  string        // ключ API шлюза (секрет)
	GatewayTimeout time.Duration // на одну попытку вызова шлюза

	POSURL    string // кассовая система (в dev — mock-externals, модуль pos)
	POSAPIKey string // ключ API кассовой системы (секрет)

	GatewayWebhookSecret string // подпись вебхуков шлюза (секрет)
	POSWebhookSecret     string // подпись вебхуков кассы (секрет)

	WebhookWait       time.Duration // ждать вебхук до первой сверки со шлюзом
	PendingExpiry     time.Duration // неоплаченная попытка старше — отменяется у шлюза
	CourierExpiry     time.Duration // то же для оплаты курьеру (S7)
	PollMin, PollMax  time.Duration // backoff сверки
	ReconcileInterval time.Duration
	DefaultReturnURL  string
}

// Load читает окружение; возвращает все найденные ошибки разом.
func Load() (Config, error) {
	r := config.FromEnv()
	c := Config{
		Base:              config.LoadBase(r, "payment", "8085", true),
		KafkaBrokers:      r.List("KAFKA_BROKERS", nil),
		GatewayURL:        r.String("GATEWAY_URL", "http://localhost:9091"),
		GatewayTimeout:    r.Duration("GATEWAY_TIMEOUT", 5*time.Second),
		POSURL:            r.String("POS_URL", "http://localhost:9093"),
		WebhookWait:       r.Duration("WEBHOOK_WAIT", 10*time.Second),
		PendingExpiry:     r.Duration("PENDING_EXPIRY", 10*time.Minute),
		CourierExpiry:     r.Duration("COURIER_PAYMENT_TTL", 4*time.Hour),
		PollMin:           r.Duration("RECONCILE_BACKOFF_MIN", 5*time.Second),
		PollMax:           r.Duration("RECONCILE_BACKOFF_MAX", 5*time.Minute),
		ReconcileInterval: r.Duration("RECONCILE_INTERVAL", 2*time.Second),
		DefaultReturnURL:  r.String("DEFAULT_RETURN_URL", "http://localhost:3000/orders"),
	}
	dev := c.Env == "dev"
	secret := func(key, devDefault string) string {
		if dev {
			return r.String(key, devDefault)
		}
		return r.Required(key)
	}
	c.GatewayAPIKey = secret("GATEWAY_API_KEY", devGatewayAPIKey)
	c.POSAPIKey = secret("POS_API_KEY", devPOSAPIKey)
	c.GatewayWebhookSecret = secret("GATEWAY_WEBHOOK_SECRET", devGatewayWebhookSecret)
	c.POSWebhookSecret = secret("POS_WEBHOOK_SECRET", devPOSWebhookSecret)

	err := r.Err()
	var bad []error
	for k, v := range map[string]string{"GATEWAY_URL": c.GatewayURL, "POS_URL": c.POSURL, "DEFAULT_RETURN_URL": c.DefaultReturnURL} {
		if u, e := url.Parse(v); e != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			bad = append(bad, errors.New(k+" must be an http(s) URL"))
		}
	}
	if !dev {
		for k, v := range map[string]string{"GATEWAY_API_KEY": c.GatewayAPIKey, "POS_API_KEY": c.POSAPIKey, "GATEWAY_WEBHOOK_SECRET": c.GatewayWebhookSecret,
			"POS_WEBHOOK_SECRET": c.POSWebhookSecret} {
			if v != "" && len(v) < minSecretLen {
				bad = append(bad, errors.New(k+" is too short (min 16 characters)"))
			}
			if v == devGatewayAPIKey || v == devGatewayWebhookSecret || v == devPOSWebhookSecret || v == devPOSAPIKey {
				bad = append(bad, errors.New(k+" must not use the dev default outside APP_ENV=dev"))
			}
		}
	}
	for k, v := range map[string]time.Duration{"GATEWAY_TIMEOUT": c.GatewayTimeout, "WEBHOOK_WAIT": c.WebhookWait,
		"PENDING_EXPIRY": c.PendingExpiry, "COURIER_PAYMENT_TTL": c.CourierExpiry, "RECONCILE_BACKOFF_MIN": c.PollMin, "RECONCILE_BACKOFF_MAX": c.PollMax,
		"RECONCILE_INTERVAL": c.ReconcileInterval} {
		if v <= 0 {
			bad = append(bad, errors.New(k+" must be positive"))
		}
	}
	if c.PollMax < c.PollMin {
		bad = append(bad, errors.New("RECONCILE_BACKOFF_MAX must be >= RECONCILE_BACKOFF_MIN"))
	}
	c.Auth = authclient.OptionsFromEnv(r, c.Env)
	return c, errors.Join(append([]error{err}, bad...)...)
}
