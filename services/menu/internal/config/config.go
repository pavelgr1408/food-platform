// Package config — конфигурация menu-service из окружения (12-factor).
package config

import "github.com/pavelgr1408/food-platform/pkg/config"

// Config — настройки сервиса.
type Config struct {
	config.Base
	AuthMode    string
	SeedOnStart bool // загрузить встроенные меню при старте (dev/demo)
}

// Load читает окружение; возвращает все найденные ошибки разом.
func Load() (Config, error) {
	r := config.FromEnv()
	c := Config{
		Base:        config.LoadBase(r, "menu", "8083", true),
		AuthMode:    r.String("AUTH_MODE", "dev"),
		SeedOnStart: r.Bool("SEED_ON_START", false),
	}
	return c, r.Err()
}
