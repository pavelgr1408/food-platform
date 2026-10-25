package mock

import (
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
)

// Мок провайдеров push (модуль push, :9095): APNs, FCM и HMS на одном listener'е, различаются по пути.
// Протокол совпадает с services/notification/internal/push:
//
//	POST /3/device/{token}                    APNs  (заголовки apns-id, apns-priority, apns-topic)
//	POST /v1/projects/{projectId}/messages:send  FCM
//	POST /v1/{appId}/messages:send            HMS
//
// Каждый запрос требует Authorization: Bearer <ключ провайдера> (MOCK_PUSH_APNS_KEY, MOCK_PUSH_FCM_KEY,
// MOCK_PUSH_HMS_KEY); иначе 401. «Магические» токены для детерминированных сценариев:
//
//	токен содержит "invalid"  — устройство не зарегистрировано: APNs 410 Unregistered, FCM 404 UNREGISTERED,
//	                            HMS 400 code 80300007 (сервис должен удалить токен)
//	токен содержит "reject"   — постоянный отказ (400 PayloadEmpty), повтор бессмыслен
//	токен содержит "flaky"    — первый запрос 503, дальше принимает (проверка повторов)
//
// Управление сценарием (для Postman/newman):
//
//	POST /_admin/push/mode?mode=ok|error|ratelimit|timeout   error — 503, ratelimit — 429, timeout — ответа нет (до отмены клиентом)
//	POST /_admin/push/next?outcome=ok|error|invalid          исход следующего запроса (один раз)
//	POST /_admin/push/reset                                  забыть принятые push и режимы
//	GET  /_inspect/pushes?orderId=&token=                    принятые push (фильтры необязательны)
//	GET  /_inspect/push                                      режим и счётчики (принято / отказано по причинам)
type pushMock struct {
	mu      sync.Mutex
	keys    map[string]string // провайдер → ключ
	mode    string
	next    string
	flaky   map[string]bool // токены "flaky", уже получившие первый отказ
	pushes  []pushRecord
	counts  map[string]int
	timeout time.Duration
}

// pushRecord — принятый push.
type pushRecord struct {
	ID          string    `json:"id"`
	At          time.Time `json:"at"`
	Provider    string    `json:"provider"` // apns|fcm|hms
	Token       string    `json:"token"`
	Title       string    `json:"title"`
	Body        string    `json:"body"`
	Priority    string    `json:"priority"`
	OrderID     string    `json:"orderId,omitempty"`
	OrderNumber string    `json:"orderNumber,omitempty"`
	Type        string    `json:"type,omitempty"`
}

func init() { Register("push", func() Module { return &pushMock{} }) }

func (m *pushMock) Name() string        { return "push" }
func (m *pushMock) DefaultAddr() string { return ":9095" }

func (m *pushMock) Start(context.Context, *Env) error { return nil }

func (m *pushMock) resetLocked() {
	m.mode, m.next, m.flaky, m.pushes, m.counts = "ok", "", map[string]bool{}, nil, map[string]int{}
}

func (m *pushMock) Register(mux *http.ServeMux, _ *Env) {
	r := config.FromEnv()
	m.keys = map[string]string{
		"apns": r.String("MOCK_PUSH_APNS_KEY", "dev-push-apns-key"),
		"fcm":  r.String("MOCK_PUSH_FCM_KEY", "dev-push-fcm-key"),
		"hms":  r.String("MOCK_PUSH_HMS_KEY", "dev-push-hms-key"),
	}
	m.timeout = r.Duration("MOCK_PUSH_HOLD", 30*time.Second)
	m.resetLocked()

	mux.HandleFunc("POST /3/device/{token}", func(w http.ResponseWriter, req *http.Request) { m.handle(w, req, "apns") })
	mux.HandleFunc("POST /v1/projects/{projectId}/messages:send", func(w http.ResponseWriter, req *http.Request) { m.handle(w, req, "fcm") })
	mux.HandleFunc("POST /v1/{appId}/messages:send", func(w http.ResponseWriter, req *http.Request) { m.handle(w, req, "hms") })

	mux.HandleFunc("POST /_admin/push/mode", func(w http.ResponseWriter, req *http.Request) {
		v := req.URL.Query().Get("mode")
		if v != "ok" && v != "error" && v != "ratelimit" && v != "timeout" {
			http.Error(w, "mode must be ok|error|ratelimit|timeout", http.StatusBadRequest)
			return
		}
		m.mu.Lock()
		m.mode = v
		m.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]string{"mode": v})
	})
	mux.HandleFunc("POST /_admin/push/next", func(w http.ResponseWriter, req *http.Request) {
		v := req.URL.Query().Get("outcome")
		if v != "ok" && v != "error" && v != "invalid" {
			http.Error(w, "outcome must be ok|error|invalid", http.StatusBadRequest)
			return
		}
		m.mu.Lock()
		m.next = v
		m.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]string{"next": v})
	})
	mux.HandleFunc("POST /_admin/push/reset", func(w http.ResponseWriter, _ *http.Request) {
		m.mu.Lock()
		m.resetLocked()
		m.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /_inspect/pushes", func(w http.ResponseWriter, req *http.Request) {
		q := req.URL.Query()
		m.mu.Lock()
		defer m.mu.Unlock()
		out := []pushRecord{}
		for _, p := range m.pushes {
			if (q.Get("orderId") == "" || p.OrderID == q.Get("orderId")) && (q.Get("token") == "" || p.Token == q.Get("token")) {
				out = append(out, p)
			}
		}
		writeJSON(w, http.StatusOK, out)
	})
	mux.HandleFunc("GET /_inspect/push", func(w http.ResponseWriter, _ *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		counts := map[string]int{}
		for k, v := range m.counts {
			counts[k] = v
		}
		writeJSON(w, http.StatusOK, map[string]any{"mode": m.mode, "next": m.next, "accepted": len(m.pushes), "counts": counts})
	})
}

func (m *pushMock) count(what string) {
	m.mu.Lock()
	m.counts[what]++
	m.mu.Unlock()
}

// parsed — единый вид сообщения любого провайдера.
type parsed struct{ token, title, body, priority, orderID, orderNumber, typ string }

func (m *pushMock) handle(w http.ResponseWriter, req *http.Request, provider string) {
	if !m.authorized(req, provider) {
		m.count("unauthorized")
		m.fail(w, provider, http.StatusUnauthorized, "Unauthorized")
		return
	}
	raw, err := io.ReadAll(io.LimitReader(req.Body, 64<<10))
	if err != nil {
		m.fail(w, provider, http.StatusBadRequest, "BadRequest")
		return
	}
	p, err := parse(provider, req, raw)
	if err != nil || p.token == "" {
		m.count("bad_request")
		m.fail(w, provider, http.StatusBadRequest, "PayloadEmpty")
		return
	}

	m.mu.Lock()
	mode, next := m.mode, m.next
	if next != "" {
		m.next = ""
	}
	first := false
	if strings.Contains(p.token, "flaky") && !m.flaky[p.token] {
		m.flaky[p.token], first = true, true
	}
	hold := m.timeout
	m.mu.Unlock()

	switch {
	case mode == "timeout":
		m.count("timeout")
		select {
		case <-req.Context().Done():
		case <-time.After(hold):
		}
		return
	case mode == "ratelimit":
		m.count("rate_limited")
		w.Header().Set("Retry-After", "1")
		m.fail(w, provider, http.StatusTooManyRequests, "TooManyRequests")
		return
	case mode == "error" || next == "error" || first:
		m.count("unavailable")
		m.fail(w, provider, http.StatusServiceUnavailable, "ServiceUnavailable")
		return
	case next == "invalid" || strings.Contains(p.token, "invalid"):
		m.count("invalid_token")
		m.invalid(w, provider)
		return
	case strings.Contains(p.token, "reject"):
		m.count("rejected")
		m.fail(w, provider, http.StatusBadRequest, "PayloadEmpty")
		return
	}

	rec := pushRecord{ID: uuid.NewString(), At: time.Now().UTC(), Provider: provider, Token: p.token, Title: p.title, Body: p.body,
		Priority: p.priority, OrderID: p.orderID, OrderNumber: p.orderNumber, Type: p.typ}
	m.mu.Lock()
	m.pushes = append(m.pushes, rec)
	m.counts["accepted"]++
	m.mu.Unlock()
	if provider == "apns" {
		w.Header().Set("apns-id", req.Header.Get("apns-id"))
		w.WriteHeader(http.StatusOK)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"name": "projects/mock/messages/" + rec.ID})
}

func (m *pushMock) authorized(req *http.Request, provider string) bool {
	got := strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer ")
	return got != "" && subtle.ConstantTimeCompare([]byte(got), []byte(m.keys[provider])) == 1
}

// fail пишет ошибку в формате провайдера.
func (m *pushMock) fail(w http.ResponseWriter, provider string, code int, reason string) {
	switch provider {
	case "apns":
		writeJSON(w, code, map[string]string{"reason": reason})
	case "fcm":
		writeJSON(w, code, map[string]any{"error": map[string]any{"code": code, "status": reason}})
	default:
		writeJSON(w, code, map[string]any{"code": fmt.Sprint(code), "msg": reason})
	}
}

// invalid — как провайдеры сообщают о неизвестном устройстве.
func (m *pushMock) invalid(w http.ResponseWriter, provider string) {
	switch provider {
	case "apns":
		writeJSON(w, http.StatusGone, map[string]string{"reason": "Unregistered"})
	case "fcm":
		writeJSON(w, http.StatusNotFound, map[string]any{"error": map[string]any{"code": 404, "status": "UNREGISTERED"}})
	default:
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": "80300007", "msg": "all tokens are invalid"})
	}
}

func parse(provider string, req *http.Request, raw []byte) (parsed, error) {
	var p parsed
	switch provider {
	case "apns":
		var b struct {
			APS struct {
				Alert struct{ Title, Body string } `json:"alert"`
			} `json:"aps"`
			OrderID     string `json:"orderId"`
			OrderNumber string `json:"orderNumber"`
			Type        string `json:"type"`
		}
		if err := json.Unmarshal(raw, &b); err != nil {
			return p, err
		}
		p = parsed{token: req.PathValue("token"), title: b.APS.Alert.Title, body: b.APS.Alert.Body, orderID: b.OrderID, orderNumber: b.OrderNumber, typ: b.Type}
		p.priority = "normal"
		if req.Header.Get("apns-priority") == "10" {
			p.priority = "high"
		}
	case "fcm":
		var b struct {
			Message struct {
				Token        string `json:"token"`
				Notification struct{ Title, Body string }
				Data         map[string]string `json:"data"`
				Android      struct{ Priority string }
			} `json:"message"`
		}
		if err := json.Unmarshal(raw, &b); err != nil {
			return p, err
		}
		p = parsed{token: b.Message.Token, title: b.Message.Notification.Title, body: b.Message.Notification.Body,
			orderID: b.Message.Data["orderId"], orderNumber: b.Message.Data["orderNumber"], typ: b.Message.Data["type"], priority: "normal"}
		if b.Message.Android.Priority == "HIGH" {
			p.priority = "high"
		}
	default:
		var b struct {
			Message struct {
				Token   []string `json:"token"`
				Data    string   `json:"data"`
				Android struct {
					Urgency      string `json:"urgency"`
					Notification struct{ Title, Body string }
				} `json:"android"`
			} `json:"message"`
		}
		if err := json.Unmarshal(raw, &b); err != nil || len(b.Message.Token) == 0 {
			return p, fmt.Errorf("no token")
		}
		var d map[string]string
		_ = json.Unmarshal([]byte(b.Message.Data), &d)
		p = parsed{token: b.Message.Token[0], title: b.Message.Android.Notification.Title, body: b.Message.Android.Notification.Body,
			orderID: d["orderId"], orderNumber: d["orderNumber"], typ: d["type"], priority: "normal"}
		if b.Message.Android.Urgency == "HIGH" {
			p.priority = "high"
		}
	}
	return p, nil
}
