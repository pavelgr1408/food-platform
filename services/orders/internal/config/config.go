// Package config — конфигурация orders-service из окружения (12-factor).
package config

import (
	"errors"
	"net/url"
	"time"

	"github.com/pavelgr1408/food-platform/pkg/config"
)

// Config — настройки сервиса.
type Config struct {
	config.Base
	AuthMode     string
	KafkaBrokers []string

	MenuURL         string        // menu-service
	RestaurantURL   string        // restaurant-service
	UpstreamTimeout time.Duration // на одну попытку вызова menu/restaurant

	PaymentTimeout  time.Duration // неоплаченный заказ отменяется через это время
	CartTTL         time.Duration // заброшенная корзина удаляется через это время
	JanitorInterval time.Duration

	PrepMinutes     int // типовое время приготовления для ETA (до S4)
	DeliveryMinutes int // типовое время доставки
}

// Load читает окружение; возвращает все найденные ошибки разом.
func Load() (Config, error) {
	r := config.FromEnv()
	c := Config{
		Base:            config.LoadBase(r, "orders", "8084", true),
		AuthMode:        r.String("AUTH_MODE", "dev"),
		KafkaBrokers:    r.List("KAFKA_BROKERS", nil),
		MenuURL:         r.String("MENU_URL", "http://localhost:8083"),
		RestaurantURL:   r.String("RESTAURANT_URL", "http://localhost:8082"),
		UpstreamTimeout: r.Duration("UPSTREAM_TIMEOUT", 800*time.Millisecond),
		PaymentTimeout:  r.Duration("PAYMENT_TIMEOUT", 15*time.Minute),
		CartTTL:         r.Duration("CART_TTL", 7*24*time.Hour),
		JanitorInterval: r.Duration("JANITOR_INTERVAL", 30*time.Second),
		PrepMinutes:     r.Int("ETA_PREP_MINUTES", 20),
		DeliveryMinutes: r.Int("ETA_DELIVERY_MINUTES", 30),
	}
	err := r.Err()
	var bad []error
	for k, v := range map[string]string{"MENU_URL": c.MenuURL, "RESTAURANT_URL": c.RestaurantURL} {
		if u, e := url.Parse(v); e != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			bad = append(bad, errors.New(k+" must be an http(s) URL"))
		}
	}
	for k, v := range map[string]time.Duration{"UPSTREAM_TIMEOUT": c.UpstreamTimeout, "PAYMENT_TIMEOUT": c.PaymentTimeout,
		"CART_TTL": c.CartTTL, "JANITOR_INTERVAL": c.JanitorInterval} {
		if v <= 0 {
			bad = append(bad, errors.New(k+" must be positive"))
		}
	}
	if c.PrepMinutes <= 0 || c.DeliveryMinutes < 0 {
		bad = append(bad, errors.New("ETA_PREP_MINUTES must be positive, ETA_DELIVERY_MINUTES non-negative"))
	}
	return c, errors.Join(append([]error{err}, bad...)...)
}
