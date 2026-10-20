package config

import (
	"strings"
	"testing"

	"github.com/pavelgr1408/food-platform/pkg/config"
)

func TestDefaultsAndPrevKeys(t *testing.T) {
	c, err := load(config.FromMap(map[string]string{"DATABASE_URL": "postgres://x", "JWT_PREVIOUS_PUBLIC_KEYS": "k1=/a.pem, k2=/b.pem"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.HTTPPort != "8081" || c.BcryptCost != 12 || c.Audience != "restaurant-api" || len(c.PreviousKeys) != 2 || c.PreviousKeys["k2"] != "/b.pem" {
		t.Fatalf("unexpected config %+v", c)
	}
}

func TestInvalidConfigReportsEverything(t *testing.T) {
	_, err := load(config.FromMap(map[string]string{"APP_ENV": "prod", "SEED_ON_START": "true", "BCRYPT_COST": "4", "JWT_PREVIOUS_PUBLIC_KEYS": "oops"}))
	if err == nil {
		t.Fatal("expected errors")
	}
	for _, want := range []string{"DATABASE_URL", "SEED_ON_START", "BCRYPT_COST", "kid=path"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %s", err, want)
		}
	}
}
