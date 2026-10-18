// Package clients — исходящие вызовы orders-service: menu-service (validateMenuItems) и
// restaurant-service (getRestaurant). Таймаут, ретраи и circuit breaker — pkg/httpx.Client.
package clients

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Ошибки upstream-вызовов.
var (
	ErrNotFound    = errors.New("clients: not found")            // 404 от upstream
	ErrUnavailable = errors.New("clients: upstream unavailable") // сеть/5xx/breaker
)

// Doer — httpx.Client (или fake в тестах).
type Doer interface {
	Do(req *http.Request) (*http.Response, error)
}

// MenuLineInput — строка для проверки.
type MenuLineInput struct {
	MenuItemID  string
	Quantity    int
	ModifierIDs []string
}

// MenuLine — результат проверки строки.
type MenuLine struct {
	MenuItemID string
	Name       string
	Quantity   int
	Available  bool
	UnitPrice  int64 // 0, если позиции нет (ReasonCode=ITEM_NOT_FOUND)
	LineTotal  int64
	ReasonCode string
	Reason     string
}

// MenuResult — ответ validateMenuItems.
type MenuResult struct {
	Valid       bool
	MenuVersion int64
	Currency    string
	Lines       []MenuLine
	Total       int64
}

// Menu — клиент menu-service.
type Menu struct {
	BaseURL string
	HTTP    Doer
}

type menuReq struct {
	Items []menuReqItem `json:"items"`
}
type menuReqItem struct {
	MenuItemID  string   `json:"menuItemId"`
	Quantity    int      `json:"quantity"`
	ModifierIDs []string `json:"modifierIds,omitempty"`
}
type moneyJSON struct {
	Amount   int64  `json:"amount"`
	Currency string `json:"currency"`
}
type menuResp struct {
	Valid       bool  `json:"valid"`
	MenuVersion int64 `json:"menuVersion"`
	Lines       []struct {
		MenuItemID   string     `json:"menuItemId"`
		Name         string     `json:"name"`
		Quantity     int        `json:"quantity"`
		Available    bool       `json:"available"`
		CurrentPrice *moneyJSON `json:"currentPrice"`
		LineTotal    *moneyJSON `json:"lineTotal"`
		ReasonCode   string     `json:"reasonCode"`
		Reason       string     `json:"reason"`
	} `json:"lines"`
	Total moneyJSON `json:"total"`
}

// Validate вызывает POST /restaurants/{id}/menu/validation. POST здесь идемпотентен по смыслу
// (read-only проверка), поэтому разрешаем ретрай заголовком Idempotency-Key.
func (m *Menu) Validate(ctx context.Context, restaurantID string, lines []MenuLineInput) (MenuResult, error) {
	body := menuReq{Items: make([]menuReqItem, len(lines))}
	for i, l := range lines {
		body.Items[i] = menuReqItem(l)
	}
	b, _ := json.Marshal(body)
	u := strings.TrimRight(m.BaseURL, "/") + "/restaurants/" + url.PathEscape(restaurantID) + "/menu/validation"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(b))
	if err != nil {
		return MenuResult{}, err
	}
	req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(b)), nil }
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "validate") // read-only: безопасно повторять (httpx.Client ретраит только «идемпотентные»)
	resp, err := m.HTTP.Do(req)
	if err != nil {
		return MenuResult{}, fmt.Errorf("%w: menu: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return MenuResult{}, ErrNotFound
	case resp.StatusCode != http.StatusOK:
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return MenuResult{}, fmt.Errorf("%w: menu: status %d", ErrUnavailable, resp.StatusCode)
	}
	var r menuResp
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&r); err != nil {
		return MenuResult{}, fmt.Errorf("%w: menu: decode: %v", ErrUnavailable, err)
	}
	if len(r.Lines) != len(lines) {
		return MenuResult{}, fmt.Errorf("%w: menu: got %d lines for %d requested", ErrUnavailable, len(r.Lines), len(lines))
	}
	out := MenuResult{Valid: r.Valid, MenuVersion: r.MenuVersion, Currency: r.Total.Currency, Total: r.Total.Amount}
	for i, l := range r.Lines {
		ml := MenuLine{MenuItemID: l.MenuItemID, Name: l.Name, Quantity: l.Quantity, Available: l.Available, ReasonCode: l.ReasonCode, Reason: l.Reason}
		if l.CurrentPrice != nil {
			ml.UnitPrice = l.CurrentPrice.Amount
			if out.Currency == "" {
				out.Currency = l.CurrentPrice.Currency
			}
		}
		if l.LineTotal != nil {
			ml.LineTotal = l.LineTotal.Amount
		}
		// Строки приходят в порядке запроса; сверяем, чтобы чужой ответ не подменил состав.
		if ml.MenuItemID != lines[i].MenuItemID || ml.Quantity != lines[i].Quantity {
			return MenuResult{}, fmt.Errorf("%w: menu: line %d does not match request", ErrUnavailable, i)
		}
		out.Lines = append(out.Lines, ml)
	}
	return out, nil
}

// RestaurantInfo — нужное orders из карточки ресторана.
type RestaurantInfo struct {
	ID              string
	Timezone        string
	DeliveryEnabled bool
	OpenNow         bool
}

// Restaurant — клиент restaurant-service.
type Restaurant struct {
	BaseURL string
	HTTP    Doer
}

// Get вызывает GET /restaurants/{id}.
func (c *Restaurant) Get(ctx context.Context, id string) (RestaurantInfo, error) {
	u := strings.TrimRight(c.BaseURL, "/") + "/restaurants/" + url.PathEscape(id)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return RestaurantInfo{}, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return RestaurantInfo{}, fmt.Errorf("%w: restaurant: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return RestaurantInfo{}, ErrNotFound
	case resp.StatusCode != http.StatusOK:
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return RestaurantInfo{}, fmt.Errorf("%w: restaurant: status %d", ErrUnavailable, resp.StatusCode)
	}
	var r struct {
		ID              string `json:"id"`
		Timezone        string `json:"timezone"`
		DeliveryEnabled bool   `json:"deliveryEnabled"`
		OpenNow         bool   `json:"openNow"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&r); err != nil {
		return RestaurantInfo{}, fmt.Errorf("%w: restaurant: decode: %v", ErrUnavailable, err)
	}
	return RestaurantInfo{ID: r.ID, Timezone: r.Timezone, DeliveryEnabled: r.DeliveryEnabled, OpenNow: r.OpenNow}, nil
}
