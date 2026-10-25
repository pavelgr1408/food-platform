package mock

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/pavelgr1408/food-platform/pkg/config"
	"github.com/pavelgr1408/food-platform/pkg/geo"
	"github.com/pavelgr1408/food-platform/pkg/webhook"
)

// Мок курьерской системы (модуль courier, :9094). Протокол совпадает с services/delivery/internal/courier:
//
//	POST /v1/deliveries               заявка (Idempotency-Key обязателен и идемпотентен)
//	GET  /v1/deliveries/{id}          состояние: статус, курьер, последняя точка, ETA
//	POST /v1/deliveries/{id}/cancel   снять заявку, пока курьер не выехал (иначе вернёт текущее состояние)
//	PUT  /v1/deliveries/{id}/payment  передать/обновить платёжную ссылку (оплата курьеру, UC-PAY-03)
//
// Ход заявки (D = Env.Delay()): searching → assigned (D) → picked_up (2D) → en_route (3D) → delivered (5D).
// Каждый переход — подписанный вебхук на /webhooks/delivery/status (с повторами при ошибке получателя).
// Пока курьер в пути, координаты «идут» по прямой от ресторана к адресу (pkg/geo), ETA уменьшается.
// Если заявка с оплатой курьеру доходит до delivered, «курьер принимает оплату»: мок просит платёжный шлюз
// (модуль payment, /_admin/gateway/pay) завершить операцию по ссылке — итог придёт в payment-service
// вебхуком шлюза, как и в проде (деньги подтверждает только шлюз).
//
// Управление сценарием (для Postman/newman):
//
//	POST /_admin/courier/mode?couriers=on|off&create=ok|timeout|error|reject&status=ok|error&webhooks=on|off
//	     couriers=off — «нет курьеров» (E2): заявки остаются в searching; create=timeout — заявка создаётся, ответ «потерян»
//	POST /_admin/courier/next?outcome=delivered|undelivered|nocourier   исход следующей заявки
//	POST /_admin/courier/advance?id=<id>&status=assigned|picked_up|en_route|delivered|failed   сделать шаг сейчас
//	POST /_admin/courier/redeliver?id=<id>                              повторить последний вебхук (тот же eventId)
//	POST /_admin/courier/reset                                          забыть заявки и режимы
//	GET  /_inspect/courier                                              режим, заявки, вебхуки
type courierMock struct {
	mu     sync.Mutex
	ctx    context.Context
	env    *Env
	hook   string
	secret []byte
	apiKey string
	payURL string // админка шлюза: завершить операцию по ссылке оплаты
	client *http.Client

	jobs   map[string]*cJob
	byKey  map[string]*cJob
	seq    int
	mode   cMode
	next   string
	events []cHook
}

type cMode struct{ Couriers, Create, Status, Webhooks string }

type cPoint struct {
	Lat float64 `json:"lat"`
	Lon float64 `json:"lon"`
}

type cJob struct {
	ID        string     `json:"id"`
	Key       string     `json:"key"`
	OrderID   string     `json:"orderId"`
	Status    string     `json:"status"`
	Reason    string     `json:"reason,omitempty"`
	Outcome   string     `json:"outcome"`
	Pickup    cPoint     `json:"pickup"`
	Dropoff   cPoint     `json:"dropoff"`
	Contact   cContact   `json:"contact"`
	PayURL    string     `json:"paymentUrl,omitempty"`
	Amount    int64      `json:"amount,omitempty"`
	Currency  string     `json:"currency,omitempty"`
	Paid      bool       `json:"paid"`
	CourierID string     `json:"courierId,omitempty"`
	Name      string     `json:"courierName,omitempty"`
	Phone     string     `json:"courierPhone,omitempty"`
	Seq       int        `json:"seq"`
	EtaAt     *time.Time `json:"etaAt,omitempty"`
	PickedAt  time.Time  `json:"-"`
	WalkEnd   time.Time  `json:"-"`
	LastEvent string     `json:"lastEventId,omitempty"`
	Cancelled bool       `json:"cancelled,omitempty"`
	CreatedAt time.Time  `json:"createdAt"`
}

type cContact struct {
	Name  string `json:"name,omitempty"`
	Phone string `json:"phone,omitempty"`
}

type cHook struct {
	JobID    string `json:"id"`
	EventID  string `json:"eventId"`
	Status   string `json:"status"`
	HTTP     int    `json:"http"`
	Attempts int    `json:"attempts"`
}

var courierNames = []string{"Пётр", "Анна", "Сергей", "Мария", "Олег"}

func init() { Register("courier", func() Module { return &courierMock{} }) }

func (m *courierMock) Name() string        { return "courier" }
func (m *courierMock) DefaultAddr() string { return ":9094" }

func (m *courierMock) resetLocked() {
	m.jobs, m.byKey, m.seq, m.next, m.events = map[string]*cJob{}, map[string]*cJob{}, 0, "", nil
	m.mode = cMode{Couriers: "on", Create: "ok", Status: "ok", Webhooks: "on"}
}

func (m *courierMock) Start(ctx context.Context, _ *Env) error { m.ctx = ctx; return nil }

func (m *courierMock) Register(mux *http.ServeMux, env *Env) {
	r := config.FromEnv()
	m.env = env
	m.hook = r.String("MOCK_COURIER_WEBHOOK_URL", "http://localhost:8087/webhooks/delivery/status")
	m.secret = []byte(r.String("MOCK_COURIER_WEBHOOK_SECRET", "dev-courier-webhook-secret"))
	m.apiKey = r.String("MOCK_COURIER_API_KEY", "dev-courier-api-key")
	m.payURL = r.String("MOCK_COURIER_PAY_ADMIN_URL", "http://localhost:9091/_admin/gateway/pay")
	m.client = &http.Client{Timeout: 5 * time.Second}
	m.ctx = context.Background()
	m.resetLocked()

	mux.HandleFunc("POST /v1/deliveries", m.create)
	mux.HandleFunc("GET /v1/deliveries/{id}", m.get)
	mux.HandleFunc("POST /v1/deliveries/{id}/cancel", m.cancel)
	mux.HandleFunc("PUT /v1/deliveries/{id}/payment", m.setPayment)

	mux.HandleFunc("POST /_admin/courier/mode", func(w http.ResponseWriter, req *http.Request) {
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
		if pick("couriers", &m.mode.Couriers, "on", "off") && pick("create", &m.mode.Create, "ok", "timeout", "error", "reject") &&
			pick("status", &m.mode.Status, "ok", "error") && pick("webhooks", &m.mode.Webhooks, "on", "off") {
			writeJSON(w, http.StatusOK, m.mode)
		}
	})
	mux.HandleFunc("POST /_admin/courier/next", func(w http.ResponseWriter, req *http.Request) {
		o := req.URL.Query().Get("outcome")
		if o != "delivered" && o != "undelivered" && o != "nocourier" {
			http.Error(w, "outcome must be delivered|undelivered|nocourier", http.StatusBadRequest)
			return
		}
		m.mu.Lock()
		m.next = o
		m.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]string{"next": o})
	})
	mux.HandleFunc("POST /_admin/courier/advance", func(w http.ResponseWriter, req *http.Request) {
		m.mu.Lock()
		j := m.jobs[req.URL.Query().Get("id")]
		m.mu.Unlock()
		st := req.URL.Query().Get("status")
		if j == nil {
			http.Error(w, "unknown delivery", http.StatusNotFound)
			return
		}
		switch st {
		case "assigned", "picked_up", "en_route", "delivered", "failed":
		default:
			http.Error(w, "status must be assigned|picked_up|en_route|delivered|failed", http.StatusBadRequest)
			return
		}
		m.step(j, st)
		m.mu.Lock()
		v := m.view(j)
		m.mu.Unlock()
		writeJSON(w, http.StatusOK, v)
	})
	mux.HandleFunc("POST /_admin/courier/redeliver", func(w http.ResponseWriter, req *http.Request) {
		m.mu.Lock()
		j := m.jobs[req.URL.Query().Get("id")]
		var ev string
		if j != nil {
			ev = j.LastEvent
		}
		m.mu.Unlock()
		if j == nil || ev == "" {
			http.Error(w, "no webhook was sent for this delivery", http.StatusNotFound)
			return
		}
		go m.deliver(j, ev, "")
		writeJSON(w, http.StatusAccepted, map[string]string{"eventId": ev})
	})
	mux.HandleFunc("POST /_admin/courier/reset", func(w http.ResponseWriter, _ *http.Request) {
		m.mu.Lock()
		m.resetLocked()
		m.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /_inspect/courier", func(w http.ResponseWriter, _ *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		jobs := make([]*cJob, 0, len(m.jobs))
		for _, j := range m.jobs {
			jobs = append(jobs, j)
		}
		writeJSON(w, http.StatusOK, map[string]any{"mode": m.mode, "next": m.next, "deliveries": jobs, "webhooks": m.events})
	})
}

type cErr struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (m *courierMock) authorized(w http.ResponseWriter, r *http.Request) bool {
	if m.apiKey == "" {
		return true
	}
	got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if subtle.ConstantTimeCompare([]byte(got), []byte(m.apiKey)) != 1 {
		writeJSON(w, http.StatusUnauthorized, cErr{"unauthorized", "invalid API key"})
		return false
	}
	return true
}

type cView struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	Reason  string `json:"reason,omitempty"`
	Courier *struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		Phone string `json:"phone"`
	} `json:"courier,omitempty"`
	Location *struct {
		Lat       float64   `json:"lat"`
		Lon       float64   `json:"lon"`
		UpdatedAt time.Time `json:"updatedAt"`
	} `json:"location,omitempty"`
	EtaAt *time.Time `json:"etaAt,omitempty"`
	Paid  bool       `json:"paid"`
}

// view строит ответ; вызывать под m.mu. Координаты курьера считаются «по часам»: прямая ресторан → адрес.
func (m *courierMock) view(j *cJob) cView {
	v := cView{ID: j.ID, Status: j.Status, Reason: j.Reason, EtaAt: j.EtaAt, Paid: j.Paid}
	if j.CourierID != "" {
		v.Courier = &struct {
			ID    string `json:"id"`
			Name  string `json:"name"`
			Phone string `json:"phone"`
		}{j.CourierID, j.Name, j.Phone}
	}
	if j.Status == "picked_up" || j.Status == "en_route" {
		p := walkPoint(j, time.Now())
		v.Location = &struct {
			Lat       float64   `json:"lat"`
			Lon       float64   `json:"lon"`
			UpdatedAt time.Time `json:"updatedAt"`
		}{p.Lat, p.Lon, time.Now().UTC()}
	}
	return v
}

// walkPoint — положение курьера: от ресторана к адресу пропорционально времени пути.
func walkPoint(j *cJob, now time.Time) cPoint {
	total := j.WalkEnd.Sub(j.PickedAt)
	f := 1.0
	if total > 0 {
		f = float64(now.Sub(j.PickedAt)) / float64(total)
	}
	if f < 0 {
		f = 0
	}
	if f > 1 {
		f = 1
	}
	lat, lon := geo.Move(j.Pickup.Lat, j.Pickup.Lon, j.Dropoff.Lat, j.Dropoff.Lon, f)
	return cPoint{lat, lon}
}

func (m *courierMock) create(w http.ResponseWriter, r *http.Request) {
	if !m.authorized(w, r) {
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		writeJSON(w, http.StatusBadRequest, cErr{"idempotency_key_required", "Idempotency-Key header is required"})
		return
	}
	var in struct {
		OrderID string  `json:"orderId"`
		Pickup  *cPoint `json:"pickup"`
		Dropoff *cPoint `json:"dropoff"`
		Contact cContact
		Payment *struct {
			URL    string  `json:"url"`
			Amount gwMoney `json:"amount"`
		} `json:"payment"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&in); err != nil || in.Pickup == nil || in.Dropoff == nil ||
		!geo.Valid(in.Pickup.Lat, in.Pickup.Lon) || !geo.Valid(in.Dropoff.Lat, in.Dropoff.Lon) {
		writeJSON(w, http.StatusBadRequest, cErr{"invalid_request", "pickup and dropoff coordinates are required"})
		return
	}
	m.mu.Lock()
	mode := m.mode.Create
	if mode == "error" {
		m.mu.Unlock()
		writeJSON(w, http.StatusServiceUnavailable, cErr{"unavailable", "courier system is unavailable"})
		return
	}
	if mode == "reject" {
		m.mu.Unlock()
		writeJSON(w, http.StatusUnprocessableEntity, cErr{"out_of_zone", "no couriers serve this address"})
		return
	}
	j, replay := m.byKey[key]
	if !replay {
		m.seq++
		outcome := "delivered"
		if m.next != "" {
			outcome, m.next = m.next, ""
		}
		if m.mode.Couriers == "off" {
			outcome = "nocourier"
		}
		j = &cJob{ID: "cr_" + uuid.NewString()[:8], Key: key, OrderID: in.OrderID, Status: "searching", Outcome: outcome, Seq: m.seq,
			Pickup: *in.Pickup, Dropoff: *in.Dropoff, Contact: in.Contact, CreatedAt: time.Now().UTC()}
		if in.Payment != nil {
			j.PayURL, j.Amount, j.Currency = in.Payment.URL, in.Payment.Amount.Amount, in.Payment.Amount.Currency
		}
		m.jobs[j.ID], m.byKey[key] = j, j
		if outcome != "nocourier" {
			m.schedule(j)
		}
	}
	v := m.view(j)
	m.mu.Unlock()
	if mode == "timeout" { // заявка создана, ответ «потерян»: держим соединение до отмены клиентом
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

// schedule планирует шаги заявки (под m.mu).
func (m *courierMock) schedule(j *cJob) {
	d := m.env.Delay()
	steps := []struct {
		k int
		s string
	}{{1, "assigned"}, {2, "picked_up"}, {3, "en_route"}}
	for _, st := range steps {
		s := st.s
		m.env.AfterD(m.ctx, time.Duration(st.k)*d, func() { m.step(j, s) })
	}
	final := "delivered"
	if j.Outcome == "undelivered" {
		final = "failed"
	}
	m.env.AfterD(m.ctx, 5*d, func() { m.step(j, final) })
}

var cRank = map[string]int{"searching": 0, "assigned": 1, "picked_up": 2, "en_route": 3, "delivered": 4, "failed": 4}

// step переводит заявку в статус (только вперёд) и отправляет вебхук.
func (m *courierMock) step(j *cJob, status string) {
	m.mu.Lock()
	if j.Cancelled || cRank[j.Status] >= 4 || cRank[status] <= cRank[j.Status] {
		m.mu.Unlock()
		return
	}
	now := time.Now().UTC()
	j.Status = status
	switch status {
	case "assigned":
		n := courierNames[j.Seq%len(courierNames)]
		j.CourierID, j.Name, j.Phone = fmt.Sprintf("c%02d", j.Seq%100), n, fmt.Sprintf("+7999000%04d", j.Seq%10000)
		eta := now.Add(25 * time.Minute)
		j.EtaAt = &eta
	case "picked_up":
		j.PickedAt, j.WalkEnd = now, now.Add(3*m.env.Delay())
		eta := now.Add(15 * time.Minute)
		j.EtaAt = &eta
	case "en_route":
		eta := now.Add(8 * time.Minute)
		j.EtaAt = &eta
	case "delivered":
		j.EtaAt = nil
		j.Paid = j.PayURL != ""
	case "failed":
		j.EtaAt, j.Reason = nil, "undelivered"
		if j.PickedAt.IsZero() {
			j.Reason = "no_courier"
		}
	}
	payURL := ""
	if status == "delivered" {
		payURL = j.PayURL
	}
	ev := uuid.NewString()
	j.LastEvent = ev
	hooks := m.mode.Webhooks == "on"
	m.mu.Unlock()
	if payURL != "" {
		m.collectPayment(payURL)
	}
	if hooks {
		m.deliver(j, ev, status)
	}
}

// collectPayment — «курьер принял оплату»: просим шлюз завершить операцию по ссылке (id — последний сегмент пути).
func (m *courierMock) collectPayment(link string) {
	u, err := url.Parse(link)
	if err != nil {
		return
	}
	id := u.Path[strings.LastIndex(u.Path, "/")+1:]
	if id == "" {
		return
	}
	req, err := http.NewRequestWithContext(m.ctx, http.MethodPost, m.payURL+"?id="+url.QueryEscape(id), nil)
	if err != nil {
		return
	}
	resp, err := m.client.Do(req)
	if err != nil {
		m.env.Log.Warn("courier mock: cannot complete the payment at the gateway", "error", err)
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	resp.Body.Close()
}

func (m *courierMock) get(w http.ResponseWriter, r *http.Request) {
	if !m.authorized(w, r) {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.mode.Status == "error" {
		writeJSON(w, http.StatusServiceUnavailable, cErr{"unavailable", "courier system is unavailable"})
		return
	}
	j := m.jobs[r.PathValue("id")]
	if j == nil {
		writeJSON(w, http.StatusNotFound, cErr{"not_found", "delivery not found"})
		return
	}
	writeJSON(w, http.StatusOK, m.view(j))
}

func (m *courierMock) cancel(w http.ResponseWriter, r *http.Request) {
	if !m.authorized(w, r) {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	j := m.jobs[r.PathValue("id")]
	if j == nil {
		writeJSON(w, http.StatusNotFound, cErr{"not_found", "delivery not found"})
		return
	}
	if j.Status == "searching" || j.Status == "assigned" {
		j.Status, j.Reason, j.Cancelled, j.EtaAt = "failed", "cancelled", true, nil
	}
	writeJSON(w, http.StatusOK, m.view(j))
}

func (m *courierMock) setPayment(w http.ResponseWriter, r *http.Request) {
	if !m.authorized(w, r) {
		return
	}
	var in struct {
		URL    string  `json:"url"`
		Amount gwMoney `json:"amount"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&in); err != nil || in.URL == "" {
		writeJSON(w, http.StatusBadRequest, cErr{"invalid_request", "url is required"})
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	j := m.jobs[r.PathValue("id")]
	if j == nil {
		writeJSON(w, http.StatusNotFound, cErr{"not_found", "delivery not found"})
		return
	}
	j.PayURL, j.Amount, j.Currency = in.URL, in.Amount.Amount, in.Amount.Currency
	w.WriteHeader(http.StatusNoContent)
}

// deliver отправляет подписанный вебхук (до 4 попыток с нарастающей паузой). status == "" — повтор последнего.
func (m *courierMock) deliver(j *cJob, eventID, status string) {
	m.mu.Lock()
	if status == "" {
		status = j.Status
	}
	v := m.view(j)
	payload := map[string]any{"eventId": eventID, "orderId": j.OrderID, "deliveryId": j.Key, "status": status,
		"reason": j.Reason, "paid": j.Paid, "timestamp": time.Now().UTC()}
	if v.Courier != nil {
		payload["courierId"] = v.Courier.ID
		payload["courier"] = map[string]string{"name": v.Courier.Name, "phone": v.Courier.Phone}
	}
	if j.EtaAt != nil {
		payload["etaAt"] = j.EtaAt
	}
	body, _ := json.Marshal(payload)
	m.mu.Unlock()

	d := cHook{JobID: j.ID, EventID: eventID, Status: status}
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
			m.env.Log.Warn("courier mock: webhook delivery failed", "delivery", j.ID, "attempt", attempt, "error", err)
		}
		select {
		case <-time.After(wait):
			wait *= 2
		case <-m.ctx.Done():
			attempt = 5
		}
	}
	m.mu.Lock()
	m.events = append(m.events, d)
	m.mu.Unlock()
}
