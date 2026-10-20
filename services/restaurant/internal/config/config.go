// Package config — конфигурация restaurant-service из окружения (12-factor).
package config

import (
	"github.com/pavelgr1408/food-platform/pkg/authclient"
	"github.com/pavelgr1408/food-platform/pkg/config"
)

// Config — настройки сервиса.
type Config struct {
	config.Base
	Auth        authclient.Options // AUTH_MODE (dev|jwks), AUTH_JWKS_URL, AUTH_ISSUER, AUTH_AUDIENCE
	SeedOnStart bool               // загрузить встроенный справочник ресторанов при старте (dev/demo)
}

// Load читает окружение; возвращает все найденные ошибки разом.
func Load() (Config, error) {
	r := config.FromEnv()
	c := Config{
		Base:        config.LoadBase(r, "restaurant", "8082", true),
		SeedOnStart: r.Bool("SEED_ON_START", false),
	}
	c.Auth = authclient.OptionsFromEnv(r, c.Env)
	return c, r.Err()
}
