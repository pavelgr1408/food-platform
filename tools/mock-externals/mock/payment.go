package mock

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/pavelgr1408/food-platform/pkg/config"
	"github.com/pavelgr1408/food-platform/pkg/webhook"
)

// Мок платёжного шлюза (:9091). Протокол совпадает с services/payment/internal/gateway:
//
//	POST /v1/payments               создать оплату (заголовок Idempotency-Key обязателен и идемпотентен)
//	GET  /v1/payments/{id}          состояние операции
//	POST /v1/payments/{id}/cancel   отменить неоплаченную операцию (оплаченную вернёт как есть)
//	GET  /pay/{id}                  «страница оплаты» (confirmationUrl) — JSON с состоянием
//
// Исход определяется суммой в копейках («магические суммы», см. OutcomeForAmount): …13 → declined (вебхук),
// …99 → «тишина» (вебхука нет, итог — paid — виден только через GET), иначе paid (вебхук).
// Итог наступает через Env.Delay() после создания; вебхук подписан HMAC (pkg/webhook) и доставляется
// с повторами при ошибке получателя.
//
// Управление сценарием (для Postman/newman):
//
//	POST /_admin/gateway/mode?init=ok|timeout|error|reject&status=ok|error&webhooks=on|off
//	POST /_admin/gateway/next?outcome=paid|declined|silent   исход следующей созданной операции
//	POST /_admin/gateway/redeliver?id=<gw id>                повторить последний вебхук (тот же eventId)
//	POST /_admin/gateway/reset                               сбросить операции и режимы
//	GET  /_inspect/payments                                  операции и доставленные вебхуки
//
// init=timeout: операция СОЗДАЁТСЯ, но ответ не приходит (потерянный ответ); init=error: 503 без создания;
// init=reject: 422 без создания.
type paymentMock struct {
	mu         sync.Mutex
	ctx        context.Context
	env        *Env
	hook       string
	secret     []byte
	apiKey     string
	public     string // внешний адрес мока для confirmationUrl
	client     *http.Client
	ops        map[string]*gwOp
	byKey      map[string]*gwOp
	seq        int
	mode       gwMode
	next       Outcome
	deliveries []gwDelivery
}

type gwMode struct{ Init, Status, Webhooks string }

type gwOp struct {
	ID        string    `json:"id"`
	Key       string    `json:"key"`
	Reference string    `json:"reference"`
	OrderID   string    `json:"orderId"`
	Amount    int64     `json:"amount"`
	Currency  string    `json:"currency"`
	Outcome   Outcome   `json:"outcome"`
	Status    string    `json:"status"`
	Reason    string    `json:"reason,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	FinalAt   time.Time `json:"finalAt"`
	LastEvent string    `json:"lastEventId,omitempty"`
}

type gwDelivery struct {
	OpID     string `json:"opId"`
	EventID  string `json:"eventId"`
	Status   string `json:"status"`
	HTTP     int    `json:"http"`
	Attempts int    `json:"attempts"`
}

func init() { Register("payment", func() Module { return &paymentMock{} }) }

func (*paymentMock) Name() string        { return "payment" }
func (*paymentMock) DefaultAddr() string { return ":9091" }

func (m *paymentMock) resetLocked() {
	m.ops, m.byKey, m.seq, m.next = map[string]*gwOp{}, map[string]*gwOp{}, 0, ""
	m.mode = gwMode{Init: "ok", Status: "ok", Webhooks: "on"}
	m.deliveries = nil
}

func (m *paymentMock) Start(ctx context.Context, env *Env) error {
	m.ctx = ctx
	return nil
}

func (m *paymentMock) Register(mux *http.ServeMux, env *Env) {
	r := config.FromEnv()
	m.env = env
	m.hook = r.String("MOCK_PAYMENT_WEBHOOK_URL", "http://localhost:8085/webhooks/payments/gateway")
	m.secret = []byte(r.String("MOCK_PAYMENT_WEBHOOK_SECRET", "dev-gateway-webhook-secret"))
	m.apiKey = r.String("MOCK_PAYMENT_API_KEY", "dev-gateway-api-key")
	m.public = strings.TrimRight(r.String("MOCK_PAYMENT_PUBLIC_URL", "http://localhost:9091"), "/")
	m.client = &http.Client{Timeout: 5 * time.Second}
	m.ctx = context.Background()
	m.resetLocked()

	mux.HandleFunc("POST /v1/payments", m.create)
	mux.HandleFunc("GET /v1/payments/{id}", m.get)
	mux.HandleFunc("POST /v1/payments/{id}/cancel", m.cancel)
	mux.HandleFunc("GET /pay/{id}", m.get)

	mux.HandleFunc("POST /_admin/gateway/mode", func(w http.ResponseWriter, req *http.Request) {
		q := req.URL.Query()
		m.mu.Lock()
		defer m.mu.Unlock()
		pick := func(name string, cur *string, allowed ...string) bool {
			v := q.Get(name)
			if v == "" {
				return true
			}
			for _, a := range allowed {
				if a == v {
					*cur = v
					return true
				}
			}
			http.Error(w, fmt.Sprintf("%s must be one of %v", name, allowed), http.StatusBadRequest)
			return false
		}
		if pick("init", &m.mode.Init, "ok", "timeout", "error", "reject") && pick("status", &m.mode.Status, "ok", "error") &&
			pick("webhooks", &m.mode.Webhooks, "on", "off") {
			writeJSON(w, http.StatusOK, m.mode)
		}
	})
	mux.HandleFunc("POST /_admin/gateway/next", func(w http.ResponseWriter, req *http.Request) {
		o := Outcome(req.URL.Query().Get("outcome"))
		if o != OutcomePaid && o != OutcomeDeclined && o != OutcomeSilent {
			http.Error(w, "outcome must be paid|declined|silent", http.StatusBadRequest)
			return
		}
		m.mu.Lock()
		m.next = o
		m.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]string{"next": string(o)})
	})
	mux.HandleFunc("POST /_admin/gateway/reset", func(w http.ResponseWriter, _ *http.Request) {
		m.mu.Lock()
		m.resetLocked()
		m.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /_admin/gateway/redeliver", func(w http.ResponseWriter, req *http.Request) {
		m.mu.Lock()
		op := m.ops[req.URL.Query().Get("id")]
		var ev string
		if op != nil {
			ev = op.LastEvent
		}
		m.mu.Unlock()
		if op == nil || ev == "" {
			http.Error(w, "no webhook was sent for this operation", http.StatusNotFound)
			return
		}
		go m.deliver(op, ev)
		writeJSON(w, http.StatusAccepted, map[string]string{"eventId": ev})
	})
	mux.HandleFunc("GET /_inspect/payments", func(w http.ResponseWriter, _ *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		ops := make([]*gwOp, 0, len(m.ops))
		for _, o := range m.ops {
			m.settleLocked(o)
			ops = append(ops, o)
		}
		writeJSON(w, http.StatusOK, map[string]any{"mode": m.mode, "operations": ops, "deliveries": m.deliveries})
	})
}

type gwErr struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (m *paymentMock) authorized(w http.ResponseWriter, r *http.Request) bool {
	if m.apiKey == "" {
		return true
	}
	got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if subtle.ConstantTimeCompare([]byte(got), []byte(m.apiKey)) != 1 {
		writeJSON(w, http.StatusUnauthorized, gwErr{"unauthorized", "invalid API key"})
		return false
	}
	return true
}

type gwMoney struct {
	Amount   int64  `json:"amount"`
	Currency string `json:"currency"`
}

type gwView struct {
	ID              string  `json:"id"`
	Status          string  `json:"status"`
	ConfirmationURL string  `json:"confirmationUrl,omitempty"`
	Amount          gwMoney `json:"amount"`
	Reason          string  `json:"reason,omitempty"`
}

func (m *paymentMock) view(o *gwOp) gwView {
	return gwView{ID: o.ID, Status: o.Status, Amount: gwMoney{o.Amount, o.Currency}, Reason: o.Reason,
		ConfirmationURL: m.public + "/pay/" + o.ID}
}

// settleLocked применяет итог, когда пришло его время (ленивое вычисление — таймеров на операцию нет).
func (m *paymentMock) settleLocked(o *gwOp) {
	if o.Status != "pending" || time.Now().Before(o.FinalAt) {
		return
	}
	if o.Outcome == OutcomeDeclined {
		o.Status, o.Reason = "declined", "insufficient_funds"
		return
	}
	o.Status = "paid"
}

func (m *paymentMock) create(w http.ResponseWriter, r *http.Request) {
	if !m.authorized(w, r) {
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		writeJSON(w, http.StatusBadRequest, gwErr{"idempotency_key_required", "Idempotency-Key header is required"})
		return
	}
	var in struct {
		Amount    gwMoney `json:"amount"`
		Reference string  `json:"reference"`
		Metadata  struct {
			OrderID string `json:"orderId"`
		} `json:"metadata"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&in); err != nil || in.Amount.Amount <= 0 || in.Amount.Currency == "" || in.Reference == "" {
		writeJSON(w, http.StatusBadRequest, gwErr{"invalid_request", "amount (positive integer, minor units), currency and reference are required"})
		return
	}
	m.mu.Lock()
	mode := m.mode.Init
	if mode == "error" {
		m.mu.Unlock()
		writeJSON(w, http.StatusServiceUnavailable, gwErr{"unavailable", "gateway is unavailable"})
		return
	}
	if mode == "reject" {
		m.mu.Unlock()
		writeJSON(w, http.StatusUnprocessableEntity, gwErr{"payment_rejected", "the gateway refused to create the payment"})
		return
	}
	op, replay := m.byKey[key]
	if replay && (op.Amount != in.Amount.Amount || op.Currency != in.Amount.Currency) {
		m.mu.Unlock()
		writeJSON(w, http.StatusUnprocessableEntity, gwErr{"idempotency_conflict", "Idempotency-Key was used with a different request"})
		return
	}
	if !replay {
		m.seq++
		out := OutcomeForAmount(in.Amount.Amount)
		if m.next != "" {
			out, m.next = m.next, ""
		}
		now := time.Now()
		op = &gwOp{ID: fmt.Sprintf("gw_%s", uuid.NewString()[:8]), Key: key, Reference: in.Reference, OrderID: in.Metadata.OrderID,
			Amount: in.Amount.Amount, Currency: in.Amount.Currency, Outcome: out, Status: "pending", CreatedAt: now, FinalAt: now.Add(m.env.Delay())}
		m.ops[op.ID], m.byKey[key] = op, op
		if out != OutcomeSilent && m.mode.Webhooks == "on" {
			ev := uuid.NewString()
			op.LastEvent = ev
			m.env.After(m.ctx, func() { m.deliver(op, ev) })
		}
	}
	v := m.view(op)
	m.mu.Unlock()
	if mode == "timeout" { // операция создана, ответ «потерян»: держим соединение до отмены клиентом
		select {
		case <-r.Context().Done():
		case <-time.After(30 * time.Second):
		}
		return
	}
	code := http.StatusCreated
	if replay {
		code = http.StatusOK
	}
	writeJSON(w, code, v)
}

func (m *paymentMock) get(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/v1/") && !m.authorized(w, r) {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.mode.Status == "error" && strings.HasPrefix(r.URL.Path, "/v1/") {
		writeJSON(w, http.StatusServiceUnavailable, gwErr{"unavailable", "gateway is unavailable"})
		return
	}
	op := m.ops[r.PathValue("id")]
	if op == nil {
		writeJSON(w, http.StatusNotFound, gwErr{"not_found", "operation not found"})
		return
	}
	m.settleLocked(op)
	writeJSON(w, http.StatusOK, m.view(op))
}

func (m *paymentMock) cancel(w http.ResponseWriter, r *http.Request) {
	if !m.authorized(w, r) {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.mode.Status == "error" {
		writeJSON(w, http.StatusServiceUnavailable, gwErr{"unavailable", "gateway is unavailable"})
		return
	}
	op := m.ops[r.PathValue("id")]
	if op == nil {
		writeJSON(w, http.StatusNotFound, gwErr{"not_found", "operation not found"})
		return
	}
	m.settleLocked(op)
	if op.Status == "pending" {
		op.Status, op.Reason = "declined", "canceled"
	}
	writeJSON(w, http.StatusOK, m.view(op))
}

// deliver отправляет подписанный вебхук (до 4 попыток с нарастающей паузой) и записывает результат.
// eventId одной операции постоянен: повторная доставка (redeliver) — это именно дубликат.
func (m *paymentMock) deliver(op *gwOp, eventID string) {
	m.mu.Lock()
	m.settleLocked(op)
	status := op.Status
	if status == "pending" { // доставка по таймеру наступила чуть раньше FinalAt — округление
		status = map[Outcome]string{OutcomeDeclined: "declined"}[op.Outcome]
		if status == "" {
			status = "paid"
		}
	}
	body, _ := json.Marshal(map[string]any{"eventId": eventID, "orderId": op.OrderID, "paymentId": op.Reference, "status": status,
		"externalRef": op.ID, "amount": gwMoney{op.Amount, op.Currency}, "reason": op.Reason, "occurredAt": time.Now().UTC()})
	m.mu.Unlock()

	d := gwDelivery{OpID: op.ID, EventID: eventID, Status: status}
	wait := 500 * time.Millisecond
	for attempt := 1; attempt <= 4; attempt++ {
		d.Attempts = attempt
		req, err := http.NewRequestWithContext(m.ctx, http.MethodPost, m.hook, bytes.NewReader(body))
		if err != nil {
			break
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(webhook.Header, webhook.Sign(m.secret, body, time.Now()))
		resp, err := m.client.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			d.HTTP = resp.StatusCode
			if resp.StatusCode/100 == 2 {
				break
			}
		} else {
			d.HTTP = 0
			m.env.Log.Warn("payment mock: webhook delivery failed", "op", op.ID, "attempt", attempt, "error", err)
		}
		select {
		case <-time.After(wait):
			wait *= 2
		case <-m.ctx.Done():
			attempt = 5
		}
	}
	m.mu.Lock()
	m.deliveries = append(m.deliveries, d)
	m.mu.Unlock()
}
