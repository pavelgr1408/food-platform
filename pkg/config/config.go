// Package config — 12-factor конфигурация из переменных окружения.
//
// Reader накапливает ошибки разбора, чтобы сервис при старте сообщил обо всех
// проблемах конфигурации сразу, а не по одной.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"
)

// Reader читает значения через lookup (по умолчанию os.LookupEnv).
type Reader struct {
	lookup func(string) (string, bool)
	errs   []error
}

// FromEnv — Reader поверх окружения процесса.
func FromEnv() *Reader { return &Reader{lookup: os.LookupEnv} }

// FromMap — Reader поверх map (для тестов).
func FromMap(m map[string]string) *Reader {
	return &Reader{lookup: func(k string) (string, bool) { v, ok := m[k]; return v, ok }}
}

func (r *Reader) get(key string) (string, bool) {
	v, ok := r.lookup(key)
	if !ok || v == "" {
		return "", false
	}
	return v, true
}

// String возвращает значение или fallback.
func (r *Reader) String(key, fallback string) string {
	if v, ok := r.get(key); ok {
		return v
	}
	return fallback
}

// Required возвращает значение; если переменная не задана — фиксирует ошибку.
func (r *Reader) Required(key string) string {
	v, ok := r.get(key)
	if !ok {
		r.errs = append(r.errs, fmt.Errorf("%s is required", key))
	}
	return v
}

// Int читает целое.
func (r *Reader) Int(key string, fallback int) int {
	v, ok := r.get(key)
	if !ok {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		r.errs = append(r.errs, fmt.Errorf("%s=%q: %w", key, v, err))
		return fallback
	}
	return n
}

// Bool читает булево (true/false/1/0).
func (r *Reader) Bool(key string, fallback bool) bool {
	v, ok := r.get(key)
	if !ok {
		return fallback
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		r.errs = append(r.errs, fmt.Errorf("%s=%q: %w", key, v, err))
		return fallback
	}
	return b
}

// Duration читает time.Duration ("15m", "500ms").
func (r *Reader) Duration(key string, fallback time.Duration) time.Duration {
	v, ok := r.get(key)
	if !ok {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		r.errs = append(r.errs, fmt.Errorf("%s=%q: %w", key, v, err))
		return fallback
	}
	return d
}

// List читает значения через запятую (пустые отбрасываются).
func (r *Reader) List(key string, fallback []string) []string {
	v, ok := r.get(key)
	if !ok {
		return fallback
	}
	var out []string
	start := 0
	for i := 0; i <= len(v); i++ {
		if i == len(v) || v[i] == ',' {
			if s := trim(v[start:i]); s != "" {
				out = append(out, s)
			}
			start = i + 1
		}
	}
	return out
}

func trim(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t') {
		s = s[:len(s)-1]
	}
	return s
}

// Err возвращает объединённую ошибку конфигурации (nil, если всё корректно).
func (r *Reader) Err() error { return errors.Join(r.errs...) }

// Base — поля, общие для всех сервисов.
type Base struct {
	Service     string
	Env         string // dev | stage | prod
	HTTPPort    string
	LogLevel    string
	DatabaseURL string
}

// LoadBase читает общие настройки. DATABASE_URL обязателен только если needDB.
func LoadBase(r *Reader, service, defaultPort string, needDB bool) Base {
	b := Base{
		Service:  service,
		Env:      r.String("APP_ENV", "dev"),
		HTTPPort: r.String("HTTP_PORT", defaultPort),
		LogLevel: r.String("LOG_LEVEL", "info"),
	}
	if needDB {
		b.DatabaseURL = r.Required("DATABASE_URL")
	}
	return b
}
