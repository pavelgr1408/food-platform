package httpapi

import (
	"encoding/json"
	"time"

	"github.com/pavelgr1408/food-platform/services/payment/internal/domain"
	"github.com/pavelgr1408/food-platform/services/payment/internal/service"
)

type moneyDTO struct {
	Amount   int64  `json:"amount"`
	Currency string `json:"currency"`
}

type methodDTO struct {
	Type    string `json:"type"`
	Title   string `json:"title"`
	Enabled bool   `json:"enabled"`
}

type initRequest struct {
	Method    string `json:"method"`
	ReturnURL string `json:"returnUrl"`
}

type posInitRequest struct {
	Tender string `json:"tender"`
}

type refundDTO struct {
	Status      string     `json:"status"`
	RequestedAt *time.Time `json:"requestedAt,omitempty"`
	RefundedAt  *time.Time `json:"refundedAt,omitempty"`
	Reason      string     `json:"reason,omitempty"`
}

type paymentDTO struct {
	ID            string          `json:"id"`
	OrderID       string          `json:"orderId"`
	Method        string          `json:"method"`
	Status        string          `json:"status"`
	Amount        moneyDTO        `json:"amount"`
	Attempt       int             `json:"attempt"`
	PaymentURL    string          `json:"paymentUrl,omitempty"`
	DeclineReason string          `json:"declineReason,omitempty"`
	Tender        string          `json:"tender,omitempty"`
	FiscalData    json.RawMessage `json:"fiscalData,omitempty"`
	Refund        *refundDTO      `json:"refund,omitempty"`
	PaidAt        *time.Time      `json:"paidAt,omitempty"`
	CreatedAt     time.Time       `json:"createdAt"`
	UpdatedAt     time.Time       `json:"updatedAt"`
}

func toPayment(p domain.Payment) paymentDTO {
	d := paymentDTO{ID: p.ID, OrderID: p.OrderID, Method: string(p.Method), Status: string(p.Status),
		Amount: moneyDTO{p.Amount, p.Currency}, Attempt: p.Attempt, PaymentURL: p.PaymentURL, DeclineReason: p.DeclineReason,
		PaidAt: p.PaidAt, CreatedAt: p.CreatedAt.UTC(), UpdatedAt: p.UpdatedAt.UTC(),
		Tender: string(p.Tender), FiscalData: p.FiscalData}
	if p.RefundStatus != "" {
		d.Refund = &refundDTO{Status: string(p.RefundStatus), RequestedAt: p.RefundRequestedAt, RefundedAt: p.RefundedAt, Reason: p.RefundReason}
	}
	return d
}

// callbackRequest — PaymentStatusCallback.
type callbackRequest struct {
	EventID     string          `json:"eventId"`
	OrderID     string          `json:"orderId"`
	PaymentID   string          `json:"paymentId"`
	Status      string          `json:"status"`
	ExternalRef string          `json:"externalRef"`
	Reason      string          `json:"reason"`
	Amount      *moneyDTO       `json:"amount"`
	FiscalData  json.RawMessage `json:"fiscalData"`
}

func (c callbackRequest) domain() service.Callback {
	cb := service.Callback{EventID: c.EventID, OrderID: c.OrderID, PaymentID: c.PaymentID, Status: c.Status,
		ExternalRef: c.ExternalRef, Reason: c.Reason}
	if len(c.FiscalData) > 0 && c.FiscalData[0] == '{' { // только объект; null/мусор отбрасываем
		cb.Fiscal = c.FiscalData
	}
	if c.Amount != nil {
		a := c.Amount.Amount
		cb.Amount, cb.Currency = &a, c.Amount.Currency
	}
	return cb
}
