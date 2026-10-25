package config

import (
	"strings"
	"testing"

	"github.com/pavelgr1408/food-platform/pkg/config"
)

func TestDevDefaults(t *testing.T) {
	c, err := load(config.FromMap(map[string]string{"DATABASE_URL": "postgres://x"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.HTTPPort != "8087" || c.CourierAPIKey != devCourierAPIKey || c.CourierWebhookSecret != devCourierWebhookSecret || c.RedisAddr != "" {
		t.Fatalf("%+v", c)
	}
	if c.SearchTimeout.Minutes() != 10 || c.LocationTTL.Seconds() != 60 {
		t.Fatalf("durations: %+v", c)
	}
}

func TestProdRequiresRealSecrets(t *testing.T) {
	_, err := load(config.FromMap(map[string]string{"DATABASE_URL": "postgres://x", "APP_ENV": "prod"}))
	if err == nil || !strings.Contains(err.Error(), "COURIER_API_KEY is required") || !strings.Contains(err.Error(), "COURIER_WEBHOOK_SECRET is required") {
		t.Fatalf("secrets must be required in prod: %v", err)
	}
	_, err = load(config.FromMap(map[string]string{"DATABASE_URL": "postgres://x", "APP_ENV": "prod",
		"COURIER_API_KEY": devCourierAPIKey, "COURIER_WEBHOOK_SECRET": "short"}))
	if err == nil || !strings.Contains(err.Error(), "dev default") || !strings.Contains(err.Error(), "too short") {
		t.Fatalf("dev default and short secrets must be rejected: %v", err)
	}
	if _, err := load(config.FromMap(map[string]string{"DATABASE_URL": "postgres://x", "APP_ENV": "prod", "AUTH_JWKS_URL": "http://auth/jwks",
		"COURIER_API_KEY": "0123456789abcdef-key", "COURIER_WEBHOOK_SECRET": "0123456789abcdef-secret"})); err != nil {
		t.Fatalf("valid prod config: %v", err)
	}
}

func TestBadValues(t *testing.T) {
	_, err := load(config.FromMap(map[string]string{"DATABASE_URL": "postgres://x", "COURIER_URL": "ftp://x", "SEARCH_TIMEOUT": "-1s",
		"POLL_INTERVAL": "1m", "POLL_BACKOFF_MAX": "1s"}))
	if err == nil {
		t.Fatal("expected errors")
	}
	for _, want := range []string{"COURIER_URL must be an http(s) URL", "SEARCH_TIMEOUT must be positive", "POLL_BACKOFF_MAX must be >= POLL_INTERVAL"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in %v", want, err)
		}
	}
}
