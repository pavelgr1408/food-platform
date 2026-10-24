// Package gateway — клиент платёжного шлюза (в dev — tools/mock-externals, модуль payment).
//
// Протокол (REST/JSON, Bearer-ключ): создание оплаты идемпотентно по заголовку Idempotency-Key
// (мы передаём paymentId) — повторный вызов возвращает ту же операцию, поэтому его безопасно
// повторять после таймаута. Карточные данные сюда не попадают: пользователь платит на странице шлюза.
package gateway

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

	"github.com/pavelgr1408/food-platform/pkg/httpx"
)

// Состояния операции у шлюза.
const (
	StatePending  = "pending"
	StatePaid     = "paid"
	StateDeclined = "declined"
)

// CreateRequest — запрос на создание оплаты.
type CreateRequest struct {
	Key         string // Idempotency-Key = paymentId
	Reference   string // наш paymentId (возвращается в вебхуке)
	OrderID     string // идентификатор заказа (metadata; возвращается в вебхуке)
	Amount      int64  // копейки
	Currency    string
	ReturnURL   string
	Description string
	Tender      string // card | cash — только касса (pos)
}

// Created — созданная у шлюза операция.
type Created struct {
	Ref string // идентификатор операции у шлюза
	URL string // страница оплаты
}

// State — состояние операции.
type State struct {
	Ref      string
	Status   string // pending | paid | declined
	Amount   int64
	Currency string
	Reason   string
	Fiscal   []byte // фискальный документ кассы (JSON-объект), если есть
}

// Refunded — итог запроса на возврат.
type Refunded struct {
	Ref    string // идентификатор возврата у шлюза
	Status string // refunded | pending
}

// Gateway — операции шлюза, нужные payment-service.
type Gateway interface {
	Create(ctx context.Context, r CreateRequest) (Created, error)
	Status(ctx context.Context, ref string) (State, error)
	// Cancel отменяет неоплаченную операцию; если её успели оплатить — возвращает paid.
	Cancel(ctx context.Context, ref string) (State, error)
	// Refund возвращает деньги по оплаченной операции. key (= paymentId) делает вызов идемпотентным:
	// повтор после таймаута не заведёт второй возврат. Определённый отказ — *RejectedError.
	Refund(ctx context.Context, ref, key string, amount int64, currency string) (Refunded, error)
}

// RejectedError — шлюз определённо отказал (4xx): операция не создана, повторять бессмысленно.
type RejectedError struct {
	HTTPStatus int
	Code       string
	Message    string
}

func (e *RejectedError) Error() string {
	return fmt.Sprintf("gateway rejected (%d %s): %s", e.HTTPStatus, e.Code, e.Message)
}

// IsRejected — определённый отказ шлюза (в отличие от «неизвестно»: таймаут, 5xx, обрыв).
func IsRejected(err error) bool {
	var r *RejectedError
	return errors.As(err, &r)
}

// ErrNotFound — шлюз не знает операцию (Status/Cancel).
var ErrNotFound = errors.New("gateway: operation not found")

// Client — HTTP-реализация Gateway.
type Client struct {
	BaseURL string
	APIKey  string
	HTTP    *httpx.Client
}

type moneyDTO struct {
	Amount   int64  `json:"amount"`
	Currency string `json:"currency"`
}

type createDTO struct {
	Amount      moneyDTO `json:"amount"`
	Reference   string   `json:"reference"`
	ReturnURL   string   `json:"returnUrl,omitempty"`
	Description string   `json:"description,omitempty"`
	Metadata    metaDTO  `json:"metadata"`
	Tender      string   `json:"tender,omitempty"`
}

type metaDTO struct {
	OrderID string `json:"orderId"`
}

type paymentDTO struct {
	ID              string          `json:"id"`
	Status          string          `json:"status"`
	ConfirmationURL string          `json:"confirmationUrl"`
	Amount          moneyDTO        `json:"amount"`
	Reason          string          `json:"reason"`
	FiscalData      json.RawMessage `json:"fiscalData,omitempty"`
}

type errorDTO struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (c *Client) do(ctx context.Context, method, path, key string, body any) (paymentDTO, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return paymentDTO{}, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.BaseURL, "/")+path, rd)
	if err != nil {
		return paymentDTO{}, err
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
		return paymentDTO{}, fmt.Errorf("gateway %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return paymentDTO{}, ErrNotFound
	case resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusRequestTimeout:
		return paymentDTO{}, fmt.Errorf("gateway %s %s: status %d", method, path, resp.StatusCode)
	case resp.StatusCode >= 400:
		var e errorDTO
		_ = json.Unmarshal(raw, &e)
		return paymentDTO{}, &RejectedError{HTTPStatus: resp.StatusCode, Code: e.Code, Message: e.Message}
	}
	var out paymentDTO
	if err := json.Unmarshal(raw, &out); err != nil {
		return paymentDTO{}, fmt.Errorf("gateway %s %s: bad response: %w", method, path, err)
	}
	return out, nil
}

// Create создаёт оплату (идемпотентно по req.Key).
func (c *Client) Create(ctx context.Context, r CreateRequest) (Created, error) {
	d, err := c.do(ctx, http.MethodPost, "/v1/payments", r.Key, createDTO{
		Amount: moneyDTO{r.Amount, r.Currency}, Reference: r.Reference, ReturnURL: r.ReturnURL, Description: r.Description, Metadata: metaDTO{OrderID: r.OrderID}, Tender: r.Tender})
	if err != nil {
		return Created{}, err
	}
	if d.ID == "" {
		return Created{}, errors.New("gateway: response without operation id")
	}
	return Created{Ref: d.ID, URL: d.ConfirmationURL}, nil
}

func toState(d paymentDTO) State {
	st := State{Ref: d.ID, Status: d.Status, Amount: d.Amount.Amount, Currency: d.Amount.Currency, Reason: d.Reason}
	if len(d.FiscalData) > 0 && d.FiscalData[0] == '{' { // только объект; null и мусор отбрасываем
		st.Fiscal = d.FiscalData
	}
	return st
}

// Status запрашивает состояние операции.
func (c *Client) Status(ctx context.Context, ref string) (State, error) {
	d, err := c.do(ctx, http.MethodGet, "/v1/payments/"+url.PathEscape(ref), "", nil)
	return toState(d), err
}

// Cancel отменяет неоплаченную операцию.
func (c *Client) Cancel(ctx context.Context, ref string) (State, error) {
	d, err := c.do(ctx, http.MethodPost, "/v1/payments/"+url.PathEscape(ref)+"/cancel", "", nil)
	return toState(d), err
}

// Refund запрашивает возврат полной суммы.
func (c *Client) Refund(ctx context.Context, ref, key string, amount int64, currency string) (Refunded, error) {
	d, err := c.do(ctx, http.MethodPost, "/v1/payments/"+url.PathEscape(ref)+"/refund", key, struct {
		Amount moneyDTO `json:"amount"`
	}{moneyDTO{amount, currency}})
	if err != nil {
		return Refunded{}, err
	}
	if d.ID == "" {
		return Refunded{}, errors.New("gateway: refund response without id")
	}
	return Refunded{Ref: d.ID, Status: d.Status}, nil
}
