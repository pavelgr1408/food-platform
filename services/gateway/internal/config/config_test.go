package config

import (
	"strings"
	"testing"

	"github.com/pavelgr1408/food-platform/pkg/config"
)

func TestDefaults(t *testing.T) {
	c, err := load(config.FromMap(nil))
	if err != nil {
		t.Fatal(err)
	}
	if c.HTTPPort != "8080" || c.Auth.Mode != "jwks" || c.Auth.JWKSURL != "http://localhost:8081/auth/.well-known/jwks.json" ||
		c.Upstreams["orders"].Host != "localhost:8084" || c.RateBurst != 100 || c.AuthRatePerMin != 10 {
		t.Fatalf("unexpected defaults: %+v", c)
	}
}

func TestAuthModeDevIsNotAvailable(t *testing.T) {
	c, err := load(config.FromMap(map[string]string{"AUTH_MODE": "dev"}))
	if err != nil || c.Auth.Mode != "jwks" {
		t.Fatalf("gateway must always verify tokens: %+v %v", c.Auth, err)
	}
}

func TestValidation(t *testing.T) {
	_, err := load(config.FromMap(map[string]string{
		"APP_ENV": "prod", "CORS_ALLOWED_ORIGINS": "*, https://ok.example, notaurl", "TRUSTED_PROXY_CIDRS": "0.0.0.0/0, 10.0.0.0/8, bad",
		"UPSTREAM_ORDERS_URL": "ftp://x", "UPSTREAM_MENU_URL": "http://menu:8083/api", "RATE_LIMIT_RPS": "0"}))
	if err == nil {
		t.Fatal("expected errors")
	}
	for _, want := range []string{`"*" is not allowed`, "notaurl", "trusts every peer", `"bad"`, "UPSTREAM_ORDERS_URL", "UPSTREAM_MENU_URL", "must be positive"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q:\n%v", want, err)
		}
	}
}

func TestTrustedProxyAndOrigins(t *testing.T) {
	c, err := load(config.FromMap(map[string]string{"APP_ENV": "prod", "TRUSTED_PROXY_CIDRS": "10.0.0.0/8,192.168.0.0/16",
		"CORS_ALLOWED_ORIGINS": "https://web.example,http://localhost:5173"}))
	if err != nil || len(c.TrustedProxyCIDRs) != 2 || len(c.CORSOrigins) != 2 {
		t.Fatalf("%+v %v", c, err)
	}
}
