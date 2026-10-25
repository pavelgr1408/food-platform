// Package courier — клиент курьерской системы (в dev — tools/mock-externals, модуль courier).
//
// Протокол (REST/JSON, Bearer-ключ): создание доставки идемпотентно по заголовку Idempotency-Key
// (мы передаём deliveryId) — повтор после таймаута находит ту же заявку. Итог приходит вебхуком
// (/webhooks/delivery/status), недостающее добирает опрос GET. Вызовы выполняются вне транзакций БД.
package courier

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
	"time"

	"github.com/pavelgr1408/food-platform/pkg/httpx"
)

// Point — координаты.
type Point struct{ Lat, Lon float64 }

// Place — точка маршрута.
type Place struct {
	Point
	Address string
}

// Payment — оплата при вручении (UC-PAY-03).
type Payment struct {
	URL      string
	Amount   int64 // копейки
	Currency string
}

// CreateRequest — заявка на доставку.
type CreateRequest struct {
	Key          string // Idempotency-Key = deliveryId
	OrderID      string
	OrderNumber  string
	Pickup       Place
	Dropoff      Place
	ContactName  string
	ContactPhone string
	Payment      *Payment // nil — заказ оплачен
}

// State — состояние заявки у курьерской системы.
type State struct {
	Ref          string
	Status       string // searching | assigned | picked_up | en_route | delivered | failed
	Reason       string // для failed
	CourierID    string
	CourierName  string
	CourierPhone string
	Location     *Point
	LocationAt   time.Time
	EtaAt        *time.Time
	Paid         bool
}

// Courier — операции курьерской системы, нужные delivery-service.
type Courier interface {
	Create(ctx context.Context, r CreateRequest) (State, error)
	Status(ctx context.Context, ref string) (State, error)
	// Cancel снимает заявку; если курьер уже выехал — возвращает текущее состояние (не failed).
	Cancel(ctx context.Context, ref string) (State, error)
	// SetPayment передаёт платёжную ссылку уже созданной заявке.
	SetPayment(ctx context.Context, ref string, p Payment) error
}

// RejectedError — курьерская система определённо отказала (4xx): повторять бессмысленно.
type RejectedError struct {
	HTTPStatus int
	Code       string
	Message    string
}

func (e *RejectedError) Error() string {
	return fmt.Sprintf("courier rejected (%d %s): %s", e.HTTPStatus, e.Code, e.Message)
}

// IsRejected — определённый отказ (в отличие от «неизвестно»: таймаут, 5xx, обрыв).
func IsRejected(err error) bool {
	var r *RejectedError
	return errors.As(err, &r)
}

// ErrNotFound — курьерская система не знает заявку.
var ErrNotFound = errors.New("courier: delivery not found")

// Client — HTTP-реализация Courier.
type Client struct {
	BaseURL string
	APIKey  string
	HTTP    *httpx.Client
}

type placeDTO struct {
	Lat     float64 `json:"lat"`
	Lon     float64 `json:"lon"`
	Address string  `json:"address,omitempty"`
}

type paymentDTO struct {
	URL    string   `json:"url"`
	Amount moneyDTO `json:"amount"`
}

type moneyDTO struct {
	Amount   int64  `json:"amount"`
	Currency string `json:"currency"`
}

type createDTO struct {
	Reference string      `json:"reference"`
	OrderID   string      `json:"orderId"`
	OrderNum  string      `json:"orderNumber"`
	Pickup    placeDTO    `json:"pickup"`
	Dropoff   placeDTO    `json:"dropoff"`
	Contact   contactDTO  `json:"contact"`
	Payment   *paymentDTO `json:"payment,omitempty"`
}

type contactDTO struct {
	Name  string `json:"name,omitempty"`
	Phone string `json:"phone,omitempty"`
}

type stateDTO struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	Reason  string `json:"reason"`
	Courier *struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		Phone string `json:"phone"`
	} `json:"courier"`
	Location *struct {
		Lat       float64   `json:"lat"`
		Lon       float64   `json:"lon"`
		UpdatedAt time.Time `json:"updatedAt"`
	} `json:"location"`
	EtaAt *time.Time `json:"etaAt"`
	Paid  bool       `json:"paid"`
}

type errorDTO struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (c *Client) do(ctx context.Context, method, path, key string, body any) (stateDTO, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return stateDTO{}, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.BaseURL, "/")+path, rd)
	if err != nil {
		return stateDTO{}, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return stateDTO{}, fmt.Errorf("courier %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return stateDTO{}, ErrNotFound
	case resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusRequestTimeout:
		return stateDTO{}, fmt.Errorf("courier %s %s: status %d", method, path, resp.StatusCode)
	case resp.StatusCode >= 400:
		var e errorDTO
		_ = json.Unmarshal(raw, &e)
		return stateDTO{}, &RejectedError{HTTPStatus: resp.StatusCode, Code: e.Code, Message: e.Message}
	}
	if resp.StatusCode == http.StatusNoContent || len(bytes.TrimSpace(raw)) == 0 {
		return stateDTO{}, nil
	}
	var out stateDTO
	if err := json.Unmarshal(raw, &out); err != nil {
		return stateDTO{}, fmt.Errorf("courier %s %s: bad response: %w", method, path, err)
	}
	return out, nil
}

func toState(d stateDTO) State {
	st := State{Ref: d.ID, Status: d.Status, Reason: d.Reason, EtaAt: d.EtaAt, Paid: d.Paid}
	if d.Courier != nil {
		st.CourierID, st.CourierName, st.CourierPhone = d.Courier.ID, d.Courier.Name, d.Courier.Phone
	}
	if d.Location != nil {
		st.Location = &Point{d.Location.Lat, d.Location.Lon}
		st.LocationAt = d.Location.UpdatedAt
	}
	return st
}

// Create создаёт заявку (идемпотентно по r.Key).
func (c *Client) Create(ctx context.Context, r CreateRequest) (State, error) {
	dto := createDTO{Reference: r.Key, OrderID: r.OrderID, OrderNum: r.OrderNumber,
		Pickup:  placeDTO{r.Pickup.Lat, r.Pickup.Lon, r.Pickup.Address},
		Dropoff: placeDTO{r.Dropoff.Lat, r.Dropoff.Lon, r.Dropoff.Address},
		Contact: contactDTO{r.ContactName, r.ContactPhone}}
	if r.Payment != nil {
		dto.Payment = &paymentDTO{URL: r.Payment.URL, Amount: moneyDTO{r.Payment.Amount, r.Payment.Currency}}
	}
	d, err := c.do(ctx, http.MethodPost, "/v1/deliveries", r.Key, dto)
	if err != nil {
		return State{}, err
	}
	if d.ID == "" {
		return State{}, errors.New("courier: response without delivery id")
	}
	return toState(d), nil
}

// Status запрашивает состояние заявки.
func (c *Client) Status(ctx context.Context, ref string) (State, error) {
	d, err := c.do(ctx, http.MethodGet, "/v1/deliveries/"+url.PathEscape(ref), "", nil)
	return toState(d), err
}

// Cancel снимает заявку.
func (c *Client) Cancel(ctx context.Context, ref string) (State, error) {
	d, err := c.do(ctx, http.MethodPost, "/v1/deliveries/"+url.PathEscape(ref)+"/cancel", "", nil)
	return toState(d), err
}

// SetPayment передаёт платёжную ссылку.
func (c *Client) SetPayment(ctx context.Context, ref string, p Payment) error {
	_, err := c.do(ctx, http.MethodPut, "/v1/deliveries/"+url.PathEscape(ref)+"/payment", "",
		paymentDTO{URL: p.URL, Amount: moneyDTO{p.Amount, p.Currency}})
	return err
}
