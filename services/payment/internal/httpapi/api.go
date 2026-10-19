// Package httpapi — HTTP-слой payment-service (contracts/openapi/payment.yaml).
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/pavelgr1408/food-platform/pkg/authclient"
	"github.com/pavelgr1408/food-platform/pkg/httpx"
	"github.com/pavelgr1408/food-platform/pkg/idempotency"
	"github.com/pavelgr1408/food-platform/pkg/metrics"
	"github.com/pavelgr1408/food-platform/pkg/webhook"
	"github.com/pavelgr1408/food-platform/services/payment/internal/domain"
	"github.com/pavelgr1408/food-platform/services/payment/internal/service"
)

// Payments — сценарии, нужные HTTP-слою (реализация — service.Service).
type Payments interface {
	Methods(ctx context.Context, userID, orderID string) ([]domain.MethodView, error)
	Initiate(ctx context.Context, userID, orderID string, in service.InitiateInput) (domain.Payment, error)
	Get(ctx context.Context, userID, orderID, paymentID string) (domain.Payment, error)
	HandleCallback(ctx context.Context, source domain.Method, cb service.Callback) error
}

// Deps — зависимости HTTP-слоя.
type Deps struct {
	Log         *slog.Logger
	Metrics     *metrics.Registry
	Auth        authclient.Authenticator
	Checks      []httpx.Check
	Idempotency *idempotency.Store // nil в unit-тестах: тогда Idempotency-Key не обеспечивается
	Payments    Payments
	// Секреты подписи вебхуков (HMAC-SHA256, pkg/webhook). Пустой секрет отключает приём соответствующего вебхука.
	GatewayWebhookSecret []byte
	POSWebhookSecret     []byte
	Clock                func() time.Time // nil → time.Now
}

type handlers struct {
	log   *slog.Logger
	svc   Payments
	gwSec []byte
	posSc []byte
	now   func() time.Time
}

// maxWebhookBody — предел тела вебхука.
const maxWebhookBody = 64 << 10

// New собирает маршруты и стандартную цепочку middleware.
func New(d Deps) http.Handler {
	h := &handlers{log: d.Log, svc: d.Payments, gwSec: d.GatewayWebhookSecret, posSc: d.POSWebhookSecret, now: d.Clock}
	if h.now == nil {
		h.now = time.Now
	}
	mux := http.NewServeMux()
	httpx.RegisterHealth(mux, d.Checks...)
	mux.Handle("GET /metrics", d.Metrics.Handler())

	mux.HandleFunc("GET /orders/{orderId}/payment-methods", h.methods)
	var initiate http.Handler = http.HandlerFunc(h.initiate)
	if d.Idempotency != nil {
		initiate = d.Idempotency.Middleware(idempotency.Options{
			Required: true,
			Log:      d.Log,
			// ключ принадлежит пользователю; путь (orderId) входит в область ключа самим middleware
			Scope: func(r *http.Request) string { return authclient.FromContext(r.Context()).UserID },
		})(initiate)
	}
	mux.Handle("POST /orders/{orderId}/payments", initiate)
	mux.HandleFunc("GET /orders/{orderId}/payments/{paymentId}", h.getPayment)

	// Вебхуки: мимо gateway, защита — подпись, а не токен.
	mux.HandleFunc("POST /webhooks/payments/gateway", h.webhook(domain.MethodOnlineCard, h.gwSec))
	mux.HandleFunc("POST /webhooks/payments/pos", h.webhook(domain.MethodPOS, h.posSc))

	return httpx.Chain(httpx.TrackRoute(mux),
		httpx.RequestContext(), httpx.Recover(d.Log), httpx.AccessLog(d.Log),
		d.Metrics.Middleware(), authclient.Middleware(d.Auth))
}

func userID(r *http.Request) string { return authclient.FromContext(r.Context()).UserID }

// conflictProblem — Problem с расширением paymentId (payment.yaml: PaymentConflictProblem).
type conflictProblem struct {
	httpx.Problem
	PaymentID string `json:"paymentId,omitempty"`
}

func (h *handlers) fail(w http.ResponseWriter, r *http.Request, err error) {
	var ce *service.ConflictError
	if errors.As(err, &ce) {
		w.Header().Set("Content-Type", httpx.ContentTypeProblem)
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(conflictProblem{
			Problem: httpx.Problem{Type: "about:blank", Title: http.StatusText(http.StatusConflict), Status: http.StatusConflict,
				Detail: ce.Detail, Instance: r.URL.Path, Code: ce.Code},
			PaymentID: ce.PaymentID,
		})
		return
	}
	if service.IsOrderNotFound(err) {
		w.Header().Set("Retry-After", "1") // событие orders.state могло ещё не дойти
	}
	httpx.HandleError(w, r, h.log, err)
}

func (h *handlers) methods(w http.ResponseWriter, r *http.Request) {
	ms, err := h.svc.Methods(r.Context(), userID(r), r.PathValue("orderId"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := make([]methodDTO, 0, len(ms))
	for _, m := range ms {
		out = append(out, methodDTO{Type: string(m.Type), Title: m.Title, Enabled: m.Enabled})
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

func (h *handlers) initiate(w http.ResponseWriter, r *http.Request) {
	var req initRequest
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	m := domain.Method(req.Method)
	if !m.Valid() {
		httpx.WriteProblem(w, r, http.StatusBadRequest, "INVALID_PARAMETER", "method must be one of online_card, courier_card, pos")
		return
	}
	orderID := r.PathValue("orderId")
	p, err := h.svc.Initiate(r.Context(), userID(r), orderID, service.InitiateInput{Method: m, ReturnURL: req.ReturnURL})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	w.Header().Set("Location", "/orders/"+orderID+"/payments/"+p.ID)
	status := http.StatusCreated
	if p.Status == domain.StatusUnknown {
		status = http.StatusAccepted // шлюз не ответил в срок: итог — getPayment
	}
	httpx.WriteJSON(w, status, toPayment(p))
}

func (h *handlers) getPayment(w http.ResponseWriter, r *http.Request) {
	p, err := h.svc.Get(r.Context(), userID(r), r.PathValue("orderId"), r.PathValue("paymentId"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toPayment(p))
}

// webhook принимает уведомление: подпись → разбор → применение. Повторная доставка — 200 без эффекта.
func (h *handlers) webhook(source domain.Method, secret []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if len(secret) == 0 {
			httpx.WriteProblem(w, r, http.StatusServiceUnavailable, "WEBHOOK_DISABLED", "webhook secret is not configured")
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxWebhookBody))
		if err != nil {
			httpx.WriteProblem(w, r, http.StatusRequestEntityTooLarge, "BODY_TOO_LARGE", "request body too large")
			return
		}
		if err := webhook.Verify(secret, body, r.Header.Get(webhook.Header), h.now(), 0); err != nil {
			h.log.WarnContext(r.Context(), "webhook signature rejected", "source", source, "reason", err.Error())
			httpx.WriteProblem(w, r, http.StatusUnauthorized, "INVALID_SIGNATURE", "signature is missing, invalid or outside the replay window")
			return
		}
		var in callbackRequest
		if err := json.Unmarshal(body, &in); err != nil { // поля шлюза могут добавляться: неизвестные не отвергаем
			httpx.WriteProblem(w, r, http.StatusBadRequest, "INVALID_JSON", "malformed JSON body")
			return
		}
		if err := h.svc.HandleCallback(r.Context(), source, in.domain()); err != nil {
			h.fail(w, r, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "accepted"})
	}
}
