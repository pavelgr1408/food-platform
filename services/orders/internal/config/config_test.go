package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoadDefaultsAndValidation(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://x")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.HTTPPort != "8084" || c.PaymentTimeout != 15*time.Minute || c.MenuURL == "" || c.PrepMinutes != 20 || c.Auth.Mode != "dev" {
		t.Fatalf("%+v", c)
	}
	t.Setenv("MENU_URL", "menu:8083")
	t.Setenv("PAYMENT_TIMEOUT", "0s")
	t.Setenv("ETA_PREP_MINUTES", "0")
	t.Setenv("KAFKA_BROKERS", "a:1, b:2")
	c, err = Load()
	if err == nil {
		t.Fatal("expected validation errors")
	}
	for _, want := range []string{"MENU_URL", "PAYMENT_TIMEOUT", "ETA_PREP_MINUTES"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error must mention %s: %v", want, err)
		}
	}
	if len(c.KafkaBrokers) != 2 {
		t.Errorf("brokers: %v", c.KafkaBrokers)
	}
}

func TestDatabaseURLIsRequired(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("got %v", err)
	}
}

func TestPromotionCacheTTL(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://x")
	c, err := Load()
	if err != nil || c.PromotionCacheTTL != 2*time.Second {
		t.Fatalf("default: %v %v", c.PromotionCacheTTL, err)
	}
	t.Setenv("PROMOTION_CACHE_TTL", "0s")
	if c, err = Load(); err != nil || c.PromotionCacheTTL != 0 {
		t.Fatalf("zero is allowed (no cache): %v %v", c.PromotionCacheTTL, err)
	}
	t.Setenv("PROMOTION_CACHE_TTL", "-1s")
	if _, err = Load(); err == nil || !strings.Contains(err.Error(), "PROMOTION_CACHE_TTL") {
		t.Fatalf("negative must be rejected: %v", err)
	}
}
