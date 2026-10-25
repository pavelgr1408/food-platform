// Package config — конфигурация loyalty-service из окружения (12-factor).
package config

import (
	"errors"
	"net/url"
	"time"

	"github.com/pavelgr1408/food-platform/pkg/authclient"
	"github.com/pavelgr1408/food-platform/pkg/config"
)

// Config — настройки сервиса.
type Config struct {
	config.Base
	Auth         authclient.Options // AUTH_MODE (dev|jwks), AUTH_JWKS_URL, AUTH_ISSUER, AUTH_AUDIENCE
	KafkaBrokers []string
	// AuthURL — адрес auth-service для POST /auth/authorize (в режиме jwks обязателен).
	// В dev-режиме (токена нет) права проверяются по claims из заголовков X-User-*.
	AuthURL     string
	AuthTimeout time.Duration
}

// Load читает окружение; возвращает все найденные ошибки разом.
func Load() (Config, error) {
	r := config.FromEnv()
	c := Config{
		Base:         config.LoadBase(r, "loyalty", "8086", true),
		KafkaBrokers: r.List("KAFKA_BROKERS", nil),
		AuthURL:      r.String("AUTH_URL", ""),
		AuthTimeout:  r.Duration("AUTH_TIMEOUT", 2*time.Second),
	}
	c.Auth = authclient.OptionsFromEnv(r, c.Env)
	var bad []error
	if c.Auth.Mode == "jwks" {
		if u, err := url.Parse(c.AuthURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			bad = append(bad, errors.New("AUTH_URL must be an http(s) URL when AUTH_MODE=jwks (rights are checked via auth-service /auth/authorize)"))
		}
	}
	if c.AuthTimeout <= 0 {
		bad = append(bad, errors.New("AUTH_TIMEOUT must be positive"))
	}
	return c, errors.Join(append([]error{r.Err()}, bad...)...)
}
