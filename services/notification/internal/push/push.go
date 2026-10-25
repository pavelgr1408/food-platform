// Package push — отправка уведомлений провайдерам APNs, FCM и HMS. Все три адаптера — HTTP-клиенты с
// единым интерфейсом Sender; в dev провайдеры подменяет mock-externals (модуль push).
//
// Упрощение относительно боевых провайдеров: вместо APNs-JWT, OAuth2 FCM и OAuth2 HMS используется
// статический Bearer-ключ (PUSH_*_API_KEY), а APNs работает по HTTP/1.1. Протокол тела и пути
// соответствуют документации провайдеров, поэтому при подключении настоящих провайдеров меняется
// только способ получения токена доступа и транспорт.
package push

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

	"github.com/pavelgr1408/food-platform/services/notification/internal/domain"
)

// Result — исход отправки одного сообщения.
type Result int

// Исходы отправки.
const (
	Sent         Result = iota // провайдер принял сообщение
	InvalidToken               // токен недействителен: его нужно удалить
	Retry                      // временный сбой: повторить позже
	Failed                     // постоянный отказ: повтор не поможет
)

func (r Result) String() string {
	return [...]string{"sent", "invalid_token", "retry", "failed"}[r]
}

// Message — сообщение для одного устройства.
type Message struct {
	ID          string // идентификатор уведомления (для сверки с провайдером)
	Platform    domain.Platform
	Token       string
	Title, Body string
	Priority    domain.Priority
	OrderID     string
	OrderNumber string
	Type        domain.Type
}

// Sender отправляет сообщение провайдеру.
type Sender interface {
	Send(ctx context.Context, m Message) (Result, error)
}

// Config — настройки HTTP-клиентов провайдеров.
type Config struct {
	APNsURL, APNsKey, APNsTopic string
	FCMURL, FCMKey, FCMProject  string
	HMSURL, HMSKey, HMSAppID    string
	Timeout                     time.Duration
	HTTP                        *http.Client // nil → клиент с Timeout
}

// Router выбирает адаптер по платформе токена.
type Router struct {
	cfg Config
	hc  *http.Client
}

// NewRouter создаёт Sender для трёх провайдеров.
func NewRouter(cfg Config) *Router {
	hc := cfg.HTTP
	if hc == nil {
		if cfg.Timeout <= 0 {
			cfg.Timeout = 5 * time.Second
		}
		hc = &http.Client{Timeout: cfg.Timeout}
	}
	return &Router{cfg: cfg, hc: hc}
}

// Send реализует Sender.
func (r *Router) Send(ctx context.Context, m Message) (Result, error) {
	var req *http.Request
	var err error
	switch m.Platform {
	case domain.APNS:
		req, err = r.apns(ctx, m)
	case domain.FCM:
		req, err = r.fcm(ctx, m)
	case domain.Huawei:
		req, err = r.hms(ctx, m)
	default:
		return Failed, fmt.Errorf("unsupported platform %q", m.Platform)
	}
	if err != nil {
		return Failed, err
	}
	resp, err := r.hc.Do(req)
	if err != nil {
		// сетевая ошибка, таймаут, отмена: исход неизвестен — повторяем (дубль возможен, но push не критичен)
		return Retry, fmt.Errorf("%s: %w", m.Platform, stripURL(err))
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	return classify(resp.StatusCode, string(body)), statusErr(m.Platform, resp.StatusCode, body)
}

// stripURL убирает из ошибки URL: в пути APNs находится push-токен, а токены в журналы не пишутся.
func stripURL(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

func statusErr(p domain.Platform, code int, body []byte) error {
	if code/100 == 2 {
		return nil
	}
	reason := strings.TrimSpace(string(body))
	if len(reason) > 200 {
		reason = reason[:200]
	}
	return fmt.Errorf("%s: http %d: %s", p, code, reason)
}

// classify превращает ответ провайдера в исход. 404/410 и «плохой токен» в теле — токен недействителен;
// 401/403/408/429/5xx — повтор (ключ могут поправить, провайдер может прийти в себя); остальное — отказ.
func classify(code int, body string) Result {
	switch {
	case code/100 == 2:
		return Sent
	case code == http.StatusNotFound || code == http.StatusGone:
		return InvalidToken
	case code == http.StatusBadRequest && isBadToken(body):
		return InvalidToken
	case code == http.StatusUnauthorized || code == http.StatusForbidden || code == http.StatusRequestTimeout ||
		code == http.StatusTooManyRequests || code >= 500:
		return Retry
	}
	return Failed
}

func isBadToken(body string) bool {
	for _, s := range []string{"BadDeviceToken", "Unregistered", "UNREGISTERED", "80300007"} {
		if strings.Contains(body, s) {
			return true
		}
	}
	return false
}

func (r *Router) post(ctx context.Context, rawURL, key string, payload any) (*http.Request, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, bytes.NewReader(b))
	if err != nil {
		return nil, stripURL(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	return req, nil
}

func data(m Message) map[string]string {
	return map[string]string{"orderId": m.OrderID, "orderNumber": m.OrderNumber, "type": string(m.Type)}
}

// apns: POST /3/device/{token}; apns-priority 10 — немедленно, 5 — с учётом энергосбережения.
func (r *Router) apns(ctx context.Context, m Message) (*http.Request, error) {
	prio := "5"
	if m.Priority == domain.PriorityHigh {
		prio = "10"
	}
	payload := map[string]any{
		"aps":         map[string]any{"alert": map[string]string{"title": m.Title, "body": m.Body}, "sound": "default"},
		"orderId":     m.OrderID,
		"orderNumber": m.OrderNumber,
		"type":        string(m.Type),
	}
	req, err := r.post(ctx, strings.TrimRight(r.cfg.APNsURL, "/")+"/3/device/"+url.PathEscape(m.Token), r.cfg.APNsKey, payload)
	if err != nil {
		return nil, err
	}
	req.Header.Set("apns-push-type", "alert")
	req.Header.Set("apns-priority", prio)
	req.Header.Set("apns-id", m.ID)
	if r.cfg.APNsTopic != "" {
		req.Header.Set("apns-topic", r.cfg.APNsTopic)
	}
	return req, nil
}

// fcm: POST /v1/projects/{projectId}/messages:send.
func (r *Router) fcm(ctx context.Context, m Message) (*http.Request, error) {
	prio := "NORMAL"
	if m.Priority == domain.PriorityHigh {
		prio = "HIGH"
	}
	payload := map[string]any{"message": map[string]any{
		"token":        m.Token,
		"notification": map[string]string{"title": m.Title, "body": m.Body},
		"data":         data(m),
		"android":      map[string]string{"priority": prio},
	}}
	return r.post(ctx, strings.TrimRight(r.cfg.FCMURL, "/")+"/v1/projects/"+url.PathEscape(r.cfg.FCMProject)+"/messages:send", r.cfg.FCMKey, payload)
}

// hms: POST /v1/{appId}/messages:send; data передаётся строкой JSON.
func (r *Router) hms(ctx context.Context, m Message) (*http.Request, error) {
	urgency := "NORMAL"
	if m.Priority == domain.PriorityHigh {
		urgency = "HIGH"
	}
	d, _ := json.Marshal(data(m))
	payload := map[string]any{"validate_only": false, "message": map[string]any{
		"token": []string{m.Token},
		"data":  string(d),
		"android": map[string]any{
			"urgency":      urgency,
			"notification": map[string]any{"title": m.Title, "body": m.Body, "click_action": map[string]int{"type": 3}},
		},
	}}
	return r.post(ctx, strings.TrimRight(r.cfg.HMSURL, "/")+"/v1/"+url.PathEscape(r.cfg.HMSAppID)+"/messages:send", r.cfg.HMSKey, payload)
}
