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
	if c.HTTPPort != "8088" || c.Push.APNsKey != devAPNsKey || c.Push.FCMKey != devFCMKey || c.Push.HMSKey != devHMSKey {
		t.Fatalf("%+v", c)
	}
	if c.Service.MaxEventAge.Hours() != 1 || c.Service.MaxAttempts != 6 || c.Service.Workers != 8 {
		t.Fatalf("%+v", c.Service)
	}
	if c.Push.APNsURL != "http://localhost:9095" || c.Push.FCMProject != "food-platform" {
		t.Fatalf("%+v", c.Push)
	}
}

func TestProdRequiresRealKeys(t *testing.T) {
	_, err := load(config.FromMap(map[string]string{"DATABASE_URL": "postgres://x", "APP_ENV": "prod"}))
	for _, k := range []string{"PUSH_APNS_KEY", "PUSH_FCM_KEY", "PUSH_HMS_KEY"} {
		if err == nil || !strings.Contains(err.Error(), k+" is required") {
			t.Fatalf("%s must be required in prod: %v", k, err)
		}
	}
	_, err = load(config.FromMap(map[string]string{"DATABASE_URL": "postgres://x", "APP_ENV": "prod",
		"PUSH_APNS_KEY": devAPNsKey, "PUSH_FCM_KEY": "short", "PUSH_HMS_KEY": "0123456789abcdef-hms"}))
	if err == nil || !strings.Contains(err.Error(), "dev default") || !strings.Contains(err.Error(), "too short") {
		t.Fatalf("dev default and short keys must be rejected: %v", err)
	}
	if _, err := load(config.FromMap(map[string]string{"DATABASE_URL": "postgres://x", "APP_ENV": "prod", "AUTH_JWKS_URL": "http://auth/jwks",
		"PUSH_APNS_KEY": "0123456789abcdef-apns", "PUSH_FCM_KEY": "0123456789abcdef-fcm", "PUSH_HMS_KEY": "0123456789abcdef-hms"})); err != nil {
		t.Fatalf("valid prod config: %v", err)
	}
}

func TestBadValues(t *testing.T) {
	_, err := load(config.FromMap(map[string]string{"DATABASE_URL": "postgres://x", "PUSH_FCM_URL": "ftp://x", "MAX_EVENT_AGE": "-1s",
		"SEND_BACKOFF": "1m", "SEND_BACKOFF_MAX": "1s", "SEND_WORKERS": "0", "SEND_LEASE": "5s", "SEND_TIMEOUT": "10s"}))
	if err == nil {
		t.Fatal("expected errors")
	}
	for _, want := range []string{"PUSH_FCM_URL must be an http(s) URL", "MAX_EVENT_AGE must be positive",
		"SEND_BACKOFF_MAX must be >= SEND_BACKOFF", "SEND_WORKERS must be positive", "SEND_LEASE must be greater than SEND_TIMEOUT"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in %v", want, err)
		}
	}
}
