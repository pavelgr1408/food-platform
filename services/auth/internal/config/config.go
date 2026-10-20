// Package config — конфигурация auth-service из окружения (12-factor).
package config

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/pavelgr1408/food-platform/pkg/config"
)

type Config struct {
	config.Base
	Issuer         string
	Audience       string
	AccessTTL      time.Duration
	RefreshTTL     time.Duration
	KeyID          string
	PrivateKeyPath string
	PublicKeyPath  string
	// PreviousKeys — публичные ключи прежних подписей для ротации без простоя: JWT_PREVIOUS_PUBLIC_KEYS="kid=path,kid2=path2".
	PreviousKeys map[string]string
	BcryptCost   int
	SeedOnStart  bool // dev-пользователи всех ролей (запрещено вне dev)
}

func Load() (Config, error) { return load(config.FromEnv()) }

func load(r *config.Reader) (Config, error) {
	c := Config{
		Base:           config.LoadBase(r, "auth", "8081", true),
		Issuer:         r.String("JWT_ISSUER", "restaurant-auth-service"),
		Audience:       r.String("JWT_AUDIENCE", "restaurant-api"),
		AccessTTL:      r.Duration("JWT_ACCESS_TTL", 15*time.Minute),
		RefreshTTL:     r.Duration("REFRESH_TOKEN_TTL", 720*time.Hour),
		KeyID:          r.String("JWT_KEY_ID", "local-dev-key"),
		PrivateKeyPath: r.String("JWT_PRIVATE_KEY_PATH", "./config/keys/private.pem"),
		PublicKeyPath:  r.String("JWT_PUBLIC_KEY_PATH", "./config/keys/public.pem"),
		BcryptCost:     r.Int("BCRYPT_COST", 12),
		SeedOnStart:    r.Bool("SEED_ON_START", false),
	}
	prev, perr := parseKeyList(r.String("JWT_PREVIOUS_PUBLIC_KEYS", ""))
	c.PreviousKeys = prev
	var bad []error
	if perr != nil {
		bad = append(bad, perr)
	}
	if c.BcryptCost < 10 || c.BcryptCost > 15 {
		bad = append(bad, errors.New("BCRYPT_COST must be between 10 and 15"))
	}
	if c.Env == "prod" && c.SeedOnStart {
		bad = append(bad, errors.New("SEED_ON_START=true is forbidden when APP_ENV=prod (dev passwords)"))
	}
	return c, errors.Join(append(bad, r.Err())...)
}

// parseKeyList разбирает "kid=path,kid2=path2".
func parseKeyList(s string) (map[string]string, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	out := map[string]string{}
	for _, part := range strings.Split(s, ",") {
		kid, path, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok || kid == "" || path == "" {
			return nil, fmt.Errorf("JWT_PREVIOUS_PUBLIC_KEYS: %q must be kid=path", part)
		}
		out[kid] = path
	}
	return out, nil
}
