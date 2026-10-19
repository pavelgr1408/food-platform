package config

import (
	"strings"
	"testing"
)

func load(t *testing.T, env map[string]string) (Config, error) {
	t.Helper()
	for _, k := range []string{"APP_ENV", "DATABASE_URL", "GATEWAY_API_KEY", "GATEWAY_WEBHOOK_SECRET", "POS_WEBHOOK_SECRET", "GATEWAY_URL"} {
		t.Setenv(k, "")
	}
	for k, v := range env {
		t.Setenv(k, v)
	}
	return Load()
}

func TestDevDefaults(t *testing.T) {
	c, err := load(t, map[string]string{"DATABASE_URL": "postgres://x"})
	if err != nil {
		t.Fatal(err)
	}
	if c.HTTPPort != "8085" || c.GatewayWebhookSecret == "" || c.GatewayURL != "http://localhost:9091" {
		t.Fatalf("%+v", c)
	}
}

func TestProdRequiresRealSecrets(t *testing.T) {
	_, err := load(t, map[string]string{"DATABASE_URL": "postgres://x", "APP_ENV": "prod"})
	if err == nil || !strings.Contains(err.Error(), "GATEWAY_WEBHOOK_SECRET") {
		t.Fatalf("prod without secrets must fail, got %v", err)
	}
	_, err = load(t, map[string]string{"DATABASE_URL": "postgres://x", "APP_ENV": "prod", "GATEWAY_API_KEY": devGatewayAPIKey,
		"GATEWAY_WEBHOOK_SECRET": "short", "POS_WEBHOOK_SECRET": "0123456789abcdef0123"})
	if err == nil || !strings.Contains(err.Error(), "too short") || !strings.Contains(err.Error(), "dev default") {
		t.Fatalf("weak and dev secrets must be rejected: %v", err)
	}
	_, err = load(t, map[string]string{"DATABASE_URL": "postgres://x", "APP_ENV": "prod", "GATEWAY_API_KEY": "0123456789abcdef0123",
		"GATEWAY_WEBHOOK_SECRET": "abcdef0123456789abcd", "POS_WEBHOOK_SECRET": "0123456789abcdef0123"})
	if err != nil {
		t.Fatalf("strong secrets must pass: %v", err)
	}
}

func TestBadURL(t *testing.T) {
	_, err := load(t, map[string]string{"DATABASE_URL": "postgres://x", "GATEWAY_URL": "ftp://nope"})
	if err == nil || !strings.Contains(err.Error(), "GATEWAY_URL") {
		t.Fatalf("%v", err)
	}
}
