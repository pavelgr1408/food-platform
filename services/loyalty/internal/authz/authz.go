// Package authz — проверка права управлять акциями (действие `promotion.manage` каталога auth-service).
//
// Две реализации одного интерфейса:
//   - HTTP — спрашивает auth-service (`POST /auth/authorize`) с токеном пользователя; решение
//     принимается по актуальным данным БД auth (блокировка и смена привязки действуют сразу).
//     Недоступный auth — ошибка (fail-closed): управлять ценами без проверки прав нельзя.
//   - Claims — те же правила по ролям и привязкам из Principal. Только для dev-режима
//     (`AUTH_MODE=dev`, срезы без auth), где токена нет; в prod dev-режим запрещён authclient.
package authz

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/pavelgr1408/food-platform/pkg/authclient"
	"github.com/pavelgr1408/food-platform/pkg/httpx"
)

// Причины отказа (совпадают с auth.yaml AuthorizeDecision.reason).
const (
	ReasonNotActive            = "NOT_ACTIVE"
	ReasonRoleRequired         = "ROLE_REQUIRED"
	ReasonNotBoundToRestaurant = "NOT_BOUND_TO_RESTAURANT"
	ReasonScopeNotAllowed      = "SCOPE_NOT_ALLOWED"
)

// ErrUnavailable — auth-service не дал решения (сеть, 5xx, невалидный ответ).
var ErrUnavailable = errors.New("authz: auth-service unavailable")

// Request — вопрос «может ли субъект управлять акцией с таким охватом».
type Request struct {
	Principal     authclient.Principal
	Authorization string // заголовок Authorization запроса (для HTTP-реализации)
	Scope         string // local | national
	RestaurantID  string // для local
}

// Decision — решение; отказ — не ошибка.
type Decision struct {
	Allowed bool
	Reason  string
}

// Authorizer принимает решение.
type Authorizer interface {
	CanManagePromotion(ctx context.Context, r Request) (Decision, error)
}

// Claims — правила auth-service по claims токена (dev-режим и юнит-тесты).
type Claims struct{}

// CanManagePromotion повторяет services/auth/internal/domain/authorize.go для promotion.manage.
func (Claims) CanManagePromotion(_ context.Context, r Request) (Decision, error) {
	p := r.Principal
	deny := func(reason string) (Decision, error) { return Decision{Reason: reason}, nil }
	if p.Guest || p.UserID == "" {
		return deny(ReasonRoleRequired)
	}
	switch r.Scope {
	case "national":
		if p.HasRole(authclient.RoleHQStaff) {
			return Decision{Allowed: true}, nil
		}
		if p.HasRole(authclient.RoleFranchiseOwner) {
			return deny(ReasonScopeNotAllowed)
		}
		return deny(ReasonRoleRequired)
	case "local":
		if p.HasRole(authclient.RoleHQStaff) {
			return Decision{Allowed: true}, nil
		}
		if p.HasRole(authclient.RoleFranchiseOwner) {
			if slices.Contains(p.Restaurants, strings.ToLower(r.RestaurantID)) {
				return Decision{Allowed: true}, nil
			}
			return deny(ReasonNotBoundToRestaurant)
		}
		return deny(ReasonRoleRequired)
	}
	return Decision{}, fmt.Errorf("authz: unknown scope %q", r.Scope)
}

// HTTP — клиент auth-service.
type HTTP struct {
	BaseURL string
	Client  *httpx.Client
}

// NewHTTP создаёт клиента; baseURL — адрес auth-service без хвостового слэша.
func NewHTTP(baseURL string, c *httpx.Client) *HTTP {
	return &HTTP{BaseURL: strings.TrimRight(baseURL, "/"), Client: c}
}

// CanManagePromotion вызывает POST /auth/authorize с токеном пользователя.
func (h *HTTP) CanManagePromotion(ctx context.Context, r Request) (Decision, error) {
	if r.Authorization == "" {
		return Decision{Reason: ReasonRoleRequired}, nil
	}
	body := map[string]string{"action": "promotion.manage", "scope": r.Scope}
	if r.RestaurantID != "" {
		body["restaurantId"] = r.RestaurantID
	}
	raw, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.BaseURL+"/auth/authorize", bytes.NewReader(raw))
	if err != nil {
		return Decision{}, err
	}
	req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(raw)), nil }
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", r.Authorization)
	// запрос не меняет состояние auth: ключ разрешает безопасный повтор при сетевом сбое
	req.Header.Set("Idempotency-Key", "authz-read")
	resp, err := h.Client.Do(req)
	if err != nil {
		return Decision{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		// токен не принят auth-ом (отозван/истёк между gateway и сервисом)
		return Decision{Reason: ReasonNotActive}, nil
	case resp.StatusCode != http.StatusOK:
		return Decision{}, fmt.Errorf("%w: status %d", ErrUnavailable, resp.StatusCode)
	}
	var d struct {
		Allowed bool   `json:"allowed"`
		Reason  string `json:"reason"`
	}
	if err := json.Unmarshal(data, &d); err != nil {
		return Decision{}, fmt.Errorf("%w: bad response: %v", ErrUnavailable, err)
	}
	return Decision{Allowed: d.Allowed, Reason: d.Reason}, nil
}
