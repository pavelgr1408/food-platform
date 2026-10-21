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

// Мок платёжного шлюза (модуль payment, :9091) и кассовой системы (модуль pos, :9093) — один код, два профиля.
// Протокол совпадает с services/payment/internal/gateway:
//
//	POST /v1/payments               создать оплату (заголовок Idempotency-Key обязателен и идемпотентен)
//	GET  /v1/payments/{id}          состояние операции
//	POST /v1/payments/{id}/cancel   отменить неоплаченную операцию (оплаченную вернёт как есть)
//	POST /v1/payments/{id}/refund   вернуть деньги по оплаченной операции (полная сумма; Idempotency-Key обязателен)
//	GET  /pay/{id}                  «страница оплаты» (confirmationUrl) — JSON с состоянием
//
// Профиль pos отличается: create требует tender (card|cash), оплаченная операция несёт fiscalData
// (фискальный документ: номер чека, ФН, ФД, ФПД) — и в статусе, и в вебхуке; вебхук идёт на
// /webhooks/payments/pos, подписан POS_WEBHOOK_SECRET; админка — /_admin/pos/*, инспекция — /_inspect/pos.
//
// Исход определяется суммой в копейках («магические суммы», см. OutcomeForAmount): …13 → declined (вебхук),
// …99 → «тишина» (вебхука нет, итог — paid — виден только через GET), иначе paid (вебхук).
// Итог наступает через Env.Delay() после создания; вебхук подписан HMAC (pkg/webhook) и доставляется
// с повторами при ошибке получателя.
//
// Управление сценарием (для Postman/newman):
//
//	POST /_admin/gateway/mode?init=ok|timeout|error|reject&status=ok|error&webhooks=on|off&refund=ok|timeout|error|reject
//	POST /_admin/gateway/next?outcome=paid|declined|silent   исход следующей созданной операции
//	POST /_admin/gateway/redeliver?id=<gw id>                повторить последний вебхук (тот же eventId)
//	POST /_admin/gateway/reset                               сбросить операции и режимы
//	GET  /_inspect/payments                                  операции и доставленные вебхуки
//
// refund=error: 503 без возврата; refund=reject: 422 без возврата; refund=timeout: возврат СОЗДАЁТСЯ, ответ «потерян».
// init=timeout: операция СОЗДАЁТСЯ, но ответ не приходит (потерянный ответ); init=error: 503 без создания;
// init=reject: 422 без создания.
type paymentMock struct {
	pos        bool // профиль кассовой системы
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
	refunds    map[string]*gwRefund // по Idempotency-Key
	refunded   map[string]*gwRefund // по id операции
}

type gwMode struct{ Init, Status, Webhooks, Refund string }

type gwRefund struct {
	ID        string    `json:"id"`
	Key       string    `json:"key"`
	OpID      string    `json:"opId"`
	Amount    int64     `json:"amount"`
	Currency  string    `json:"currency"`
	CreatedAt time.Time `json:"createdAt"`
}

type gwOp struct {
	ID        string    `json:"id"`
	Key       string    `json:"key"`
	Reference string    `json:"reference"`
	OrderID   string    `json:"orderId"`
	Amount    int64     `json:"amount"`
	Currency  string    `json:"currency"`
	Seq       int       `json:"seq"`
	Tender    string    `json:"tender,omitempty"`
	Fiscal    gwFiscal  `json:"fiscalData,omitempty"`
	Outcome   Outcome   `json:"outcome"`
	Status    string    `json:"status"`
	Reason    string    `json:"reason,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	FinalAt   time.Time `json:"finalAt"`
	LastEvent string    `json:"lastEventId,omitempty"`
}

// gwFiscal — фискальный документ кассы (генерируется при переходе операции в paid).
type gwFiscal map[string]any

type gwDelivery struct {
	OpID     string `json:"opId"`
	EventID  string `json:"eventId"`
	Status   string `json:"status"`
	HTTP     int    `json:"http"`
	Attempts int    `json:"attempts"`
}

func init() {
	Register("payment", func() Module { return &paymentMock{} })
	Register("pos", func() Module { return &paymentMock{pos: true} })
}

// profile — различия шлюза и кассы: имя, порт, префиксы переменных, пути админки/инспекции, умолчания.
func (m *paymentMock) profile() (name, addr, envp, admin, inspect, hook, secret, key string) {
	if m.pos {
		return "pos", ":9093", "MOCK_POS", "pos", "/_inspect/pos",
			"http://localhost:8085/webhooks/payments/pos", "dev-pos-webhook-secret", "dev-pos-api-key"
	}
	return "payment", ":9091", "MOCK_PAYMENT", "gateway", "/_inspect/payments",
		"http://localhost:8085/webhooks/payments/gateway", "dev-gateway-webhook-secret", "dev-gateway-api-key"
}

func (m *paymentMock) Name() string        { n, _, _, _, _, _, _, _ := m.profile(); return n }
func (m *paymentMock) DefaultAddr() string { _, a, _, _, _, _, _, _ := m.profile(); return a }

func (m *paymentMock) resetLocked() {
	m.ops, m.byKey, m.seq, m.next = map[string]*gwOp{}, map[string]*gwOp{}, 0, ""
	m.refunds, m.refunded = map[string]*gwRefund{}, map[string]*gwRefund{}
	m.mode = gwMode{Init: "ok", Status: "ok", Webhooks: "on", Refund: "ok"}
	m.deliveries = nil
}

func (m *paymentMock) Start(ctx context.Context, env *Env) error {
	m.ctx = ctx
	return nil
}

func (m *paymentMock) Register(mux *http.ServeMux, env *Env) {
	r := config.FromEnv()
	m.env = env
	_, addr, envp, admin, inspect, hook, secret, key := m.profile()
	m.hook = r.String(envp+"_WEBHOOK_URL", hook)
	m.secret = []byte(r.String(envp+"_WEBHOOK_SECRET", secret))
	m.apiKey = r.String(envp+"_API_KEY", key)
	m.public = strings.TrimRight(r.String(envp+"_PUBLIC_URL", "http://localhost"+addr), "/")
	m.client = &http.Client{Timeout: 5 * time.Second}
	m.ctx = context.Background()
	m.resetLocked()

	mux.HandleFunc("POST /v1/payments", m.create)
	mux.HandleFunc("GET /v1/payments/{id}", m.get)
	mux.HandleFunc("POST /v1/payments/{id}/cancel", m.cancel)
	mux.HandleFunc("POST /v1/payments/{id}/refund", m.refund)
	mux.HandleFunc("GET /pay/{id}", m.get)

	mux.HandleFunc("POST /_admin/"+admin+"/mode", func(w http.ResponseWriter, req *http.Request) {
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
			pick("webhooks", &m.mode.Webhooks, "on", "off") && pick("refund", &m.mode.Refund, "ok", "timeout", "error", "reject") {
			writeJSON(w, http.StatusOK, m.mode)
		}
	})
	mux.HandleFunc("POST /_admin/"+admin+"/next", func(w http.ResponseWriter, req *http.Request) {
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
	mux.HandleFunc("POST /_admin/"+admin+"/reset", func(w http.ResponseWriter, _ *http.Request) {
		m.mu.Lock()
		m.resetLocked()
		m.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /_admin/"+admin+"/redeliver", func(w http.ResponseWriter, req *http.Request) {
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
	mux.HandleFunc("GET "+inspect, func(w http.ResponseWriter, _ *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		ops := make([]*gwOp, 0, len(m.ops))
		for _, o := range m.ops {
			m.settleLocked(o)
			ops = append(ops, o)
		}
		refunds := make([]*gwRefund, 0, len(m.refunds))
		for _, rf := range m.refunds {
			refunds = append(refunds, rf)
		}
		writeJSON(w, http.StatusOK, map[string]any{"mode": m.mode, "operations": ops, "refunds": refunds, "deliveries": m.deliveries})
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
	ID              string   `json:"id"`
	Status          string   `json:"status"`
	ConfirmationURL string   `json:"confirmationUrl,omitempty"`
	Amount          gwMoney  `json:"amount"`
	Reason          string   `json:"reason,omitempty"`
	FiscalData      gwFiscal `json:"fiscalData,omitempty"`
}

func (m *paymentMock) view(o *gwOp) gwView {
	v := gwView{ID: o.ID, Status: o.Status, Amount: gwMoney{o.Amount, o.Currency}, Reason: o.Reason,
		ConfirmationURL: m.public + "/pay/" + o.ID}
	if m.pos {
		v.ConfirmationURL = "" // у кассы нет страницы оплаты
		v.FiscalData = o.Fiscal
	}
	return v
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
	m.payLocked(o)
}

// payLocked переводит операцию в paid; касса при этом «пробивает чек» (детерминированные фискальные данные).
func (m *paymentMock) payLocked(o *gwOp) {
	o.Status = "paid"
	if m.pos && o.Fiscal == nil {
		o.Fiscal = gwFiscal{"receiptNumber": fmt.Sprintf("%06d", o.Seq), "fn": "9960440300123456",
			"fd": 1000 + o.Seq, "fpd": "3566271234", "tender": o.Tender, "total": o.Amount}
	}
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
		Tender    string  `json:"tender"`
		Metadata  struct {
			OrderID string `json:"orderId"`
		} `json:"metadata"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&in); err != nil || in.Amount.Amount <= 0 || in.Amount.Currency == "" || in.Reference == "" {
		writeJSON(w, http.StatusBadRequest, gwErr{"invalid_request", "amount (positive integer, minor units), currency and reference are required"})
		return
	}
	if m.pos && in.Tender != "card" && in.Tender != "cash" {
		writeJSON(w, http.StatusBadRequest, gwErr{"invalid_request", "tender must be card or cash"})
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
			Amount: in.Amount.Amount, Currency: in.Amount.Currency, Seq: m.seq, Tender: in.Tender, Outcome: out, Status: "pending", CreatedAt: now, FinalAt: now.Add(m.env.Delay())}
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
	if status == "paid" && m.pos && op.Fiscal == nil { // доставка чуть раньше FinalAt: чек пробит к моменту уведомления
		m.payLocked(op)
	}
	payload := map[string]any{"eventId": eventID, "orderId": op.OrderID, "paymentId": op.Reference, "status": status,
		"externalRef": op.ID, "amount": gwMoney{op.Amount, op.Currency}, "reason": op.Reason, "occurredAt": time.Now().UTC()}
	if status == "paid" && op.Fiscal != nil {
		payload["fiscalData"] = op.Fiscal
	}
	body, _ := json.Marshal(payload)
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

// refund возвращает деньги по оплаченной операции (только полной суммой). Идемпотентен по Idempotency-Key:
// повтор возвращает тот же возврат (200), второй возврат по той же операции с другим ключом — 422.
func (m *paymentMock) refund(w http.ResponseWriter, r *http.Request) {
	if !m.authorized(w, r) {
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		writeJSON(w, http.StatusBadRequest, gwErr{"idempotency_key_required", "Idempotency-Key header is required"})
		return
	}
	var in struct {
		Amount gwMoney `json:"amount"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&in); err != nil || in.Amount.Amount <= 0 || in.Amount.Currency == "" {
		writeJSON(w, http.StatusBadRequest, gwErr{"invalid_request", "amount (positive integer, minor units) and currency are required"})
		return
	}
	m.mu.Lock()
	mode := m.mode.Refund
	switch mode {
	case "error":
		m.mu.Unlock()
		writeJSON(w, http.StatusServiceUnavailable, gwErr{"unavailable", "gateway is unavailable"})
		return
	case "reject":
		m.mu.Unlock()
		writeJSON(w, http.StatusUnprocessableEntity, gwErr{"refund_rejected", "the refund was refused"})
		return
	}
	op := m.ops[r.PathValue("id")]
	if op == nil {
		m.mu.Unlock()
		writeJSON(w, http.StatusNotFound, gwErr{"not_found", "operation not found"})
		return
	}
	m.settleLocked(op)
	rf, replay := m.refunds[key]
	switch {
	case replay && rf.OpID != op.ID:
		m.mu.Unlock()
		writeJSON(w, http.StatusUnprocessableEntity, gwErr{"idempotency_conflict", "Idempotency-Key was used with a different request"})
		return
	case !replay && op.Status != "paid":
		m.mu.Unlock()
		writeJSON(w, http.StatusUnprocessableEntity, gwErr{"not_paid", "only paid operations can be refunded"})
		return
	case !replay && (in.Amount.Amount != op.Amount || in.Amount.Currency != op.Currency):
		m.mu.Unlock()
		writeJSON(w, http.StatusUnprocessableEntity, gwErr{"refund_amount_mismatch", "only a full refund is supported"})
		return
	case !replay && m.refunded[op.ID] != nil:
		m.mu.Unlock()
		writeJSON(w, http.StatusUnprocessableEntity, gwErr{"already_refunded", "the operation is already refunded"})
		return
	}
	if !replay {
		rf = &gwRefund{ID: "rf_" + uuid.NewString()[:8], Key: key, OpID: op.ID, Amount: op.Amount, Currency: op.Currency, CreatedAt: time.Now()}
		m.refunds[key], m.refunded[op.ID] = rf, rf
	}
	m.mu.Unlock()
	if mode == "timeout" { // возврат создан, ответ «потерян»
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
	writeJSON(w, code, gwView{ID: rf.ID, Status: "refunded", Amount: gwMoney{rf.Amount, rf.Currency}})
}
