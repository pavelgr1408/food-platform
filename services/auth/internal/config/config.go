package config

import (
	"fmt"
	"os"
	"time"
)

type Config struct {
	HTTPPort       string
	DatabaseURL    string
	Issuer         string
	Audience       string
	AccessTTL      time.Duration
	RefreshTTL     time.Duration
	KeyID          string
	PrivateKeyPath string
	PublicKeyPath  string
}

func Load() (Config, error) {
	accessTTL, err := duration("JWT_ACCESS_TTL", "15m")
	if err != nil {
		return Config{}, err
	}
	refreshTTL, err := duration("REFRESH_TOKEN_TTL", "720h")
	if err != nil {
		return Config{}, err
	}
	return Config{
		HTTPPort:       env("HTTP_PORT", "8081"),
		DatabaseURL:    env("DATABASE_URL", "postgres://restaurant_auth:restaurant_auth@localhost:5432/restaurant_auth?sslmode=disable"),
		Issuer:         env("JWT_ISSUER", "restaurant-auth-service"),
		Audience:       env("JWT_AUDIENCE", "restaurant-api"),
		AccessTTL:      accessTTL,
		RefreshTTL:     refreshTTL,
		KeyID:          env("JWT_KEY_ID", "local-dev-key"),
		PrivateKeyPath: env("JWT_PRIVATE_KEY_PATH", "./config/keys/private.pem"),
		PublicKeyPath:  env("JWT_PUBLIC_KEY_PATH", "./config/keys/public.pem"),
	}, nil
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func duration(key, fallback string) (time.Duration, error) {
	value := env(key, fallback)
	d, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("parse %s=%q: %w", key, value, err)
	}
	return d, nil
}
