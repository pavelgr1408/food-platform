// Package config — конфигурация notification-service из окружения (12-factor).
package config

import (
	"errors"
	"net/url"
	"time"

	"github.com/pavelgr1408/food-platform/pkg/authclient"
	"github.com/pavelgr1408/food-platform/pkg/config"
	"github.com/pavelgr1408/food-platform/services/notification/internal/push"
	"github.com/pavelgr1408/food-platform/services/notification/internal/service"
)

// Dev-значения ключей провайдеров: только для APP_ENV=dev. В остальных окружениях ключи обязательны.
const (
	devAPNsKey   = "dev-push-apns-key"
	devFCMKey    = "dev-push-fcm-key"
	devHMSKey    = "dev-push-hms-key"
	minSecretLen = 16
)

// Config — настройки сервиса.
type Config struct {
	config.Base
	Auth         authclient.Options // AUTH_MODE (dev|jwks), AUTH_JWKS_URL, AUTH_ISSUER, AUTH_AUDIENCE
	KafkaBrokers []string

	Push         push.Config // адреса, ключи и идентификаторы провайдеров (в dev — mock-externals, модуль push)
	Service      service.Config
	SendInterval time.Duration // период опроса очереди отправки
}

// Load читает окружение; возвращает все найденные ошибки разом.
func Load() (Config, error) { return load(config.FromEnv()) }

func load(r *config.Reader) (Config, error) {
	c := Config{
		Base:         config.LoadBase(r, "notification", "8088", true),
		KafkaBrokers: r.List("KAFKA_BROKERS", nil),
		SendInterval: r.Duration("SEND_INTERVAL", 500*time.Millisecond),
	}
	dev := c.Env == "dev"
	secret := func(key, devDefault string) string {
		if dev {
			return r.String(key, devDefault)
		}
		return r.Required(key)
	}
	c.Push = push.Config{
		APNsURL:    r.String("PUSH_APNS_URL", "http://localhost:9095"),
		APNsKey:    secret("PUSH_APNS_KEY", devAPNsKey),
		APNsTopic:  r.String("PUSH_APNS_TOPIC", "ru.example.food"),
		FCMURL:     r.String("PUSH_FCM_URL", "http://localhost:9095"),
		FCMKey:     secret("PUSH_FCM_KEY", devFCMKey),
		FCMProject: r.String("PUSH_FCM_PROJECT", "food-platform"),
		HMSURL:     r.String("PUSH_HMS_URL", "http://localhost:9095"),
		HMSKey:     secret("PUSH_HMS_KEY", devHMSKey),
		HMSAppID:   r.String("PUSH_HMS_APP_ID", "food-platform"),
		Timeout:    r.Duration("PUSH_TIMEOUT", 5*time.Second),
	}
	c.Service = service.Config{
		MaxEventAge: r.Duration("MAX_EVENT_AGE", time.Hour),
		BackoffBase: r.Duration("SEND_BACKOFF", 2*time.Second),
		BackoffMax:  r.Duration("SEND_BACKOFF_MAX", 5*time.Minute),
		MaxAttempts: r.Int("SEND_MAX_ATTEMPTS", 6),
		Batch:       r.Int("SEND_BATCH", 50),
		Workers:     r.Int("SEND_WORKERS", 8),
		SendTimeout: r.Duration("SEND_TIMEOUT", 10*time.Second),
		Retention:   r.Duration("RETENTION", 7*24*time.Hour),
		Lease:       r.Duration("SEND_LEASE", 30*time.Second),
	}

	var bad []error
	for k, v := range map[string]string{"PUSH_APNS_URL": c.Push.APNsURL, "PUSH_FCM_URL": c.Push.FCMURL, "PUSH_HMS_URL": c.Push.HMSURL} {
		if u, e := url.Parse(v); e != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			bad = append(bad, errors.New(k+" must be an http(s) URL"))
		}
	}
	if !dev {
		for k, v := range map[string]string{"PUSH_APNS_KEY": c.Push.APNsKey, "PUSH_FCM_KEY": c.Push.FCMKey, "PUSH_HMS_KEY": c.Push.HMSKey} {
			if v != "" && len(v) < minSecretLen {
				bad = append(bad, errors.New(k+" is too short (min 16 characters)"))
			}
			if v == devAPNsKey || v == devFCMKey || v == devHMSKey {
				bad = append(bad, errors.New(k+" must not use the dev default outside APP_ENV=dev"))
			}
		}
	}
	for k, v := range map[string]time.Duration{"PUSH_TIMEOUT": c.Push.Timeout, "MAX_EVENT_AGE": c.Service.MaxEventAge,
		"SEND_BACKOFF": c.Service.BackoffBase, "SEND_BACKOFF_MAX": c.Service.BackoffMax, "SEND_TIMEOUT": c.Service.SendTimeout,
		"RETENTION": c.Service.Retention, "SEND_LEASE": c.Service.Lease, "SEND_INTERVAL": c.SendInterval} {
		if v <= 0 {
			bad = append(bad, errors.New(k+" must be positive"))
		}
	}
	if c.Service.BackoffMax < c.Service.BackoffBase {
		bad = append(bad, errors.New("SEND_BACKOFF_MAX must be >= SEND_BACKOFF"))
	}
	if c.Service.Lease <= c.Service.SendTimeout {
		// иначе аренда истечёт раньше конца вызова провайдера и уведомление уйдёт второй раз
		bad = append(bad, errors.New("SEND_LEASE must be greater than SEND_TIMEOUT"))
	}
	for k, v := range map[string]int{"SEND_MAX_ATTEMPTS": c.Service.MaxAttempts, "SEND_BATCH": c.Service.Batch, "SEND_WORKERS": c.Service.Workers} {
		if v <= 0 {
			bad = append(bad, errors.New(k+" must be positive"))
		}
	}
	c.Auth = authclient.OptionsFromEnv(r, c.Env)
	return c, errors.Join(append([]error{r.Err()}, bad...)...)
}
