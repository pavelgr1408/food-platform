package config

import (
	"strings"
	"testing"
)

func setenv(t *testing.T, kv map[string]string) {
	t.Helper()
	for _, k := range []string{"APP_ENV", "AUTH_MODE", "AUTH_URL", "AUTH_JWKS_URL", "AUTH_TIMEOUT", "DATABASE_URL", "KAFKA_BROKERS"} {
		t.Setenv(k, "")
	}
	for k, v := range kv {
		t.Setenv(k, v)
	}
}

func TestDevDefaults(t *testing.T) {
	setenv(t, map[string]string{"APP_ENV": "dev", "DATABASE_URL": "postgres://x"})
	c, err := Load()
	if err != nil || c.HTTPPort != "8086" || c.Auth.Mode != "dev" || c.AuthTimeout <= 0 {
		t.Fatalf("%+v %v", c, err)
	}
}

func TestJWKSRequiresAuthURL(t *testing.T) {
	setenv(t, map[string]string{"APP_ENV": "dev", "DATABASE_URL": "postgres://x", "AUTH_MODE": "jwks", "AUTH_JWKS_URL": "http://auth/jwks"})
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "AUTH_URL") {
		t.Fatalf("want AUTH_URL error, got %v", err)
	}
	setenv(t, map[string]string{"APP_ENV": "dev", "DATABASE_URL": "postgres://x", "AUTH_MODE": "jwks", "AUTH_JWKS_URL": "http://auth/jwks", "AUTH_URL": "http://auth:8081"})
	if c, err := Load(); err != nil || c.AuthURL != "http://auth:8081" {
		t.Fatalf("%+v %v", c, err)
	}
	setenv(t, map[string]string{"APP_ENV": "dev", "DATABASE_URL": "postgres://x", "AUTH_MODE": "jwks", "AUTH_JWKS_URL": "http://auth/jwks", "AUTH_URL": "auth"})
	if _, err := Load(); err == nil {
		t.Fatal("AUTH_URL must be http(s)")
	}
}
