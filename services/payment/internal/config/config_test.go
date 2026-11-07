package config

import (
	"strings"
	"testing"
	"time"
)

func load(t *testing.T, env map[string]string) (Config, error) {
	t.Helper()
	for _, k := range []string{"APP_ENV", "DATABASE_URL", "GATEWAY_API_KEY", "GATEWAY_WEBHOOK_SECRET", "POS_WEBHOOK_SECRET", "GATEWAY_URL", "DEFAULT_RETURN_URL", "ALLOWED_RETURN_ORIGINS", "ORDER_REPLICA_WAIT"} {
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
		"GATEWAY_WEBHOOK_SECRET": "abcdef0123456789abcd", "POS_WEBHOOK_SECRET": "0123456789abcdef0123", "POS_API_KEY": "pos-key-0123456789ab",
		"DEFAULT_RETURN_URL": "https://shop.example/orders", "ALLOWED_RETURN_ORIGINS": "https://shop.example"})
	if err != nil {
		t.Fatalf("strong secrets must pass: %v", err)
	}
}

func TestReturnOrigins(t *testing.T) {
	prod := map[string]string{"DATABASE_URL": "postgres://x", "APP_ENV": "prod", "GATEWAY_API_KEY": "0123456789abcdef0123",
		"GATEWAY_WEBHOOK_SECRET": "abcdef0123456789abcd", "POS_WEBHOOK_SECRET": "0123456789abcdef0123", "POS_API_KEY": "pos-key-0123456789ab"}
	with := func(kv ...string) map[string]string {
		m := map[string]string{}
		for k, v := range prod {
			m[k] = v
		}
		for i := 0; i < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return m
	}
	for name, tc := range map[string]struct {
		env  map[string]string
		want string
	}{
		"prod без списка":    {with("DEFAULT_RETURN_URL", "https://shop.example/o"), "ALLOWED_RETURN_ORIGINS is required"},
		"prod без default":   {with("ALLOWED_RETURN_ORIGINS", "https://shop.example"), "DEFAULT_RETURN_URL"},
		"prod http":          {with("DEFAULT_RETURN_URL", "http://shop.example/o", "ALLOWED_RETURN_ORIGINS", "http://shop.example"), "must be https"},
		"подстановка":        {with("DEFAULT_RETURN_URL", "https://a.example/o", "ALLOWED_RETURN_ORIGINS", "https://*.example"), "wildcards"},
		"origin с путём":     {with("DEFAULT_RETURN_URL", "https://a.example/o", "ALLOWED_RETURN_ORIGINS", "https://a.example/orders"), "must be an origin"},
		"default вне списка": {with("DEFAULT_RETURN_URL", "https://other.example/o", "ALLOWED_RETURN_ORIGINS", "https://a.example"), "origin is not allowed"},
	} {
		if _, err := load(t, tc.env); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: want error with %q, got %v", name, tc.want, err)
		}
	}
	c, err := load(t, with("DEFAULT_RETURN_URL", "https://a.example/o", "ALLOWED_RETURN_ORIGINS", "https://a.example, https://b.example:8443"))
	if err != nil || len(c.AllowedReturnOrigins) != 2 {
		t.Fatalf("valid origins: %v %+v", err, c.AllowedReturnOrigins)
	}
	// dev: безопасные значения по умолчанию.
	d, err := load(t, map[string]string{"DATABASE_URL": "postgres://x"})
	if err != nil || len(d.AllowedReturnOrigins) != 1 || d.AllowedReturnOrigins[0] != "http://localhost:3000" {
		t.Fatalf("dev defaults: %v %v", err, d.AllowedReturnOrigins)
	}
}

func TestProdRequiresPOSAPIKey(t *testing.T) {
	_, err := load(t, map[string]string{"DATABASE_URL": "postgres://x", "APP_ENV": "prod", "GATEWAY_API_KEY": "0123456789abcdef0123",
		"GATEWAY_WEBHOOK_SECRET": "abcdef0123456789abcd", "POS_WEBHOOK_SECRET": "0123456789abcdef0123"})
	if err == nil || !strings.Contains(err.Error(), "POS_API_KEY") {
		t.Fatalf("prod without POS_API_KEY must fail, got %v", err)
	}
}

func TestBadURL(t *testing.T) {
	_, err := load(t, map[string]string{"DATABASE_URL": "postgres://x", "GATEWAY_URL": "ftp://nope"})
	if err == nil || !strings.Contains(err.Error(), "GATEWAY_URL") {
		t.Fatalf("%v", err)
	}
}

func TestOrderReplicaWait(t *testing.T) {
	c, err := load(t, map[string]string{"DATABASE_URL": "postgres://x"})
	if err != nil || c.OrderReplicaWait != 3*time.Second {
		t.Fatalf("default 3s: %v %v", err, c.OrderReplicaWait)
	}
	if c, err = load(t, map[string]string{"DATABASE_URL": "postgres://x", "ORDER_REPLICA_WAIT": "0s"}); err != nil || c.OrderReplicaWait != 0 {
		t.Fatalf("0 disables waiting: %v %v", err, c.OrderReplicaWait)
	}
	if _, err = load(t, map[string]string{"DATABASE_URL": "postgres://x", "ORDER_REPLICA_WAIT": "30s"}); err == nil || !strings.Contains(err.Error(), "ORDER_REPLICA_WAIT") {
		t.Fatalf("too long wait must be rejected: %v", err)
	}
}
