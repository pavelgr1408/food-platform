// Package config — конфигурация restaurant-service из окружения (12-factor).
package config

import "github.com/pavelgr1408/food-platform/pkg/config"

// Config — настройки сервиса.
type Config struct {
	config.Base
	AuthMode    string
	SeedOnStart bool // загрузить встроенный справочник ресторанов при старте (dev/demo)
}

// Load читает окружение; возвращает все найденные ошибки разом.
func Load() (Config, error) {
	r := config.FromEnv()
	c := Config{
		Base:        config.LoadBase(r, "restaurant", "8082", true),
		AuthMode:    r.String("AUTH_MODE", "dev"),
		SeedOnStart: r.Bool("SEED_ON_START", false),
	}
	return c, r.Err()
}
