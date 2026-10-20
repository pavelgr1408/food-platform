// Package routes — таблица маршрутов gateway (источник правды для кода — routes.json; для людей и
// контрактных проверок — x-routes в contracts/openapi/gateway.yaml, расхождение ловит `make check-routes`).
package routes

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Режимы доступа.
const (
	Public   = "public"   // токен не проверяется
	Optional = "optional" // нет токена — гость; есть, но недействителен — 401
	Required = "required" // нужен действительный токен (и роль, если задана)
)

// Классы лимита запросов.
const (
	LimitDefault = ""     // по пользователю или IP
	LimitAuth    = "auth" // строже, по IP (register/login/refresh)
)

// Route — одна строка таблицы.
type Route struct {
	Pattern   string   `json:"pattern"`  // синтаксис http.ServeMux: "METHOD /path/{param}"
	Upstream  string   `json:"upstream"` // имя сервиса (config.Upstreams)
	Access    string   `json:"access"`   // public | optional | required
	Roles     []string `json:"roles,omitempty"`
	Sensitive bool     `json:"sensitive,omitempty"` // при наличии токена — онлайн-проверка через auth /introspect
	Limit     string   `json:"limit,omitempty"`
}

//go:embed routes.json
var embedded []byte

var knownRoles = []string{"CUSTOMER", "STAFF", "FRANCHISE_OWNER", "HQ_STAFF"}

var patternRe = regexp.MustCompile(`^(GET|POST|PUT|PATCH|DELETE) /[A-Za-z0-9._~/{}-]*$`)

// Load разбирает и проверяет встроенную таблицу.
func Load() ([]Route, error) { return Parse(embedded) }

// Parse разбирает JSON-таблицу и проверяет её: паттерны, режимы, роли, дубли.
func Parse(b []byte) ([]Route, error) {
	var rs []Route
	if err := json.Unmarshal(b, &rs); err != nil {
		return nil, fmt.Errorf("routes: %w", err)
	}
	if len(rs) == 0 {
		return nil, fmt.Errorf("routes: table is empty")
	}
	seen := map[string]bool{}
	for i, r := range rs {
		where := fmt.Sprintf("routes[%d] %q", i, r.Pattern)
		if !patternRe.MatchString(r.Pattern) {
			return nil, fmt.Errorf("%s: pattern must be \"METHOD /path\"", where)
		}
		if seen[r.Pattern] {
			return nil, fmt.Errorf("%s: duplicate pattern", where)
		}
		seen[r.Pattern] = true
		if r.Upstream == "" {
			return nil, fmt.Errorf("%s: upstream is required", where)
		}
		if !slices.Contains([]string{Public, Optional, Required}, r.Access) {
			return nil, fmt.Errorf("%s: access must be public|optional|required", where)
		}
		if r.Limit != LimitDefault && r.Limit != LimitAuth {
			return nil, fmt.Errorf("%s: unknown limit class %q", where, r.Limit)
		}
		for _, role := range r.Roles {
			if !slices.Contains(knownRoles, role) {
				return nil, fmt.Errorf("%s: unknown role %q", where, role)
			}
		}
		if len(r.Roles) > 0 && r.Access != Required {
			return nil, fmt.Errorf("%s: roles require access=required", where)
		}
		if r.Sensitive && r.Access == Public {
			return nil, fmt.Errorf("%s: sensitive route cannot be public (token is not looked at)", where)
		}
		if strings.Contains(r.Pattern, "/webhooks") || strings.Contains(r.Pattern, "/metrics") ||
			strings.HasSuffix(r.Pattern, "/auth/introspect") || strings.HasSuffix(r.Pattern, "/auth/authorize") {
			return nil, fmt.Errorf("%s: internal and webhook paths must not be published through the gateway", where)
		}
	}
	return rs, nil
}
