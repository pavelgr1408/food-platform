// Package service — сценарии payment-service: оплата заказа, приём вебхуков, сверка со шлюзом.
//
// Правила, на которых держится «деньги не списываются дважды»:
//  1. платёж записывается в БД ДО вызова шлюза; вызов шлюза — только вне транзакции и с Idempotency-Key = paymentId;
//  2. на заказ — не более одной живой попытки и не более одного успешного платежа (уникальные индексы БД);
//  3. таймаут шлюза → unknown, новая попытка блокируется, итог получает reconciler (GET status шлюза);
//  4. статус меняется только по автомату (domain.CanTransition) под блокировкой строки платежа;
//  5. вебхук дедуплицируется по eventId, сумма сверяется с суммой платежа.
package service

import (
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pavelgr1408/food-platform/pkg/httpx"
	"github.com/pavelgr1408/food-platform/services/payment/internal/domain"
	"github.com/pavelgr1408/food-platform/services/payment/internal/gateway"
	"github.com/pavelgr1408/food-platform/services/payment/internal/returnurl"
	"github.com/pavelgr1408/food-platform/services/payment/internal/store"
)

// Waker будит outbox-relay сразу после коммита.
type Waker interface{ Wake() }

// Config — настройки сценариев.
type Config struct {
	GatewayTimeout   time.Duration // потолок вызова шлюза из запроса пользователя
	WebhookWait      time.Duration // сколько ждать вебхук, прежде чем опросить шлюз
	PendingExpiry    time.Duration // неоплаченная попытка старше — отменяется у шлюза (declined, expired)
	CourierExpiry    time.Duration // то же для оплаты курьеру (S7): доставка длится дольше онлайн-оплаты
	PollMin, PollMax time.Duration // backoff опроса шлюза
	PollLease        time.Duration // «аренда» платежа reconciler-ом на время вызова шлюза
	ReconcileBatch   int
	DefaultReturnURL string
	// AllowedReturnOrigins — origin'ы, на которые можно вернуть клиента после оплаты (scheme://host[:port]).
	// Origin DefaultReturnURL разрешён всегда. Всё остальное → 400 INVALID_PARAMETER (ТД-QA-1, долг №91).
	AllowedReturnOrigins []string
	// OrderReplicaWait — сколько initiatePayment ждёт появления реплики заказа (orders.state идёт по Kafka),
	// прежде чем ответить 404 (ТД-QA-2, долг №92). 0 → 3 с; отрицательное — не ждать.
	OrderReplicaWait time.Duration
}

func (c *Config) defaults() {
	def := func(d *time.Duration, v time.Duration) {
		if *d <= 0 {
			*d = v
		}
	}
	def(&c.GatewayTimeout, 5*time.Second)
	def(&c.WebhookWait, 10*time.Second)
	def(&c.PendingExpiry, 10*time.Minute)
	def(&c.CourierExpiry, 4*time.Hour)
	def(&c.PollMin, 5*time.Second)
	def(&c.PollMax, 5*time.Minute)
	def(&c.PollLease, 30*time.Second)
	if c.OrderReplicaWait == 0 {
		c.OrderReplicaWait = 3 * time.Second
	}
	if c.ReconcileBatch <= 0 {
		c.ReconcileBatch = 20
	}
	if c.DefaultReturnURL == "" {
		c.DefaultReturnURL = "http://localhost:3000/orders"
	}
}

// Deps — зависимости сервиса.
type Deps struct {
	Pool    *pgxpool.Pool
	Gateway gateway.Gateway // онлайн-эквайринг (online_card, courier_card)
	POS     gateway.Gateway // кассовая система (pos); nil — оплата на кассе недоступна (503)
	Waker   Waker           // nil допустим
	Log     *slog.Logger
	Clock   func() time.Time // nil → time.Now
	Metrics *Metrics         // nil → без метрик
	Config  Config
}

// Service — сценарии оплаты.
type Service struct {
	pool  *pgxpool.Pool
	st    *store.Store
	gw    gateway.Gateway
	pos   gateway.Gateway
	waker Waker
	log   *slog.Logger
	now   func() time.Time
	m     *Metrics
	cfg   Config
	ret   returnurl.Allowed
}

// New создаёт сервис.
func New(d Deps) *Service {
	d.Config.defaults()
	s := &Service{pool: d.Pool, st: store.New(), gw: d.Gateway, pos: d.POS, waker: d.Waker, log: d.Log, now: d.Clock, m: d.Metrics, cfg: d.Config}
	if s.log == nil {
		s.log = slog.Default()
	}
	if s.now == nil {
		s.now = func() time.Time { return time.Now().UTC() }
	}
	if s.m == nil {
		s.m = NewMetrics()
	}
	// Недопустимые origin'ы отбрасываются (множество только сужается — fail-closed); config.Load их отвергает при старте.
	s.ret = returnurl.Allowed{}
	for _, o := range append([]string{d.Config.DefaultReturnURL}, d.Config.AllowedReturnOrigins...) {
		a, err := returnurl.NewAllowed(originOf(o))
		if err != nil {
			s.log.Warn("ignored invalid return origin", "origin", o, "error", err)
			continue
		}
		for k := range a {
			s.ret[k] = struct{}{}
		}
	}
	return s
}

// originOf отбрасывает путь/запрос из полного URL, оставляя scheme://host[:port] (для DEFAULT_RETURN_URL).
func originOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return raw
	}
	return u.Scheme + "://" + u.Host
}

// gwFor — шлюз способа оплаты: касса для pos, эквайринг для остальных. nil — не настроен.
func (s *Service) gwFor(m domain.Method) gateway.Gateway {
	if m == domain.MethodPOS {
		return s.pos
	}
	return s.gw
}

// Store отдаёт хранилище (для тестов).
func (s *Service) Store() *store.Store { return s.st }

func (s *Service) wake() {
	if s.waker != nil {
		s.waker.Wake()
	}
}

// Ошибки сценариев (HTTP-семантика — httpx.Error; коды описаны в payment.yaml).
var (
	errOrderNotFound   = httpx.NewError(http.StatusNotFound, "ORDER_NOT_FOUND", "order not found (or its event has not been processed yet, retry)")
	errPaymentNotFound = httpx.NewError(http.StatusNotFound, "PAYMENT_NOT_FOUND", "payment not found")
	errNotPayable      = httpx.NewError(http.StatusConflict, "ORDER_NOT_PAYABLE", "the order is cancelled or does not await online payment")
	errNotAllowed      = httpx.NewError(http.StatusUnprocessableEntity, "PAYMENT_METHOD_NOT_ALLOWED", "the method differs from the payment method of the order")
	errNotSupported    = httpx.NewError(http.StatusUnprocessableEntity, "PAYMENT_METHOD_NOT_SUPPORTED", "only online_card payments can be initiated here (pos — POST /orders/{id}/payments/pos by staff, courier_card is created by the system)")
	errPOSUnavailable  = httpx.NewError(http.StatusServiceUnavailable, "POS_UNAVAILABLE", "the POS integration is not configured")
	errDeclined        = httpx.NewError(http.StatusPaymentRequired, "PAYMENT_DECLINED", "the payment gateway refused to create the payment, try again")
	errAmountMismatch  = httpx.NewError(http.StatusUnprocessableEntity, "AMOUNT_MISMATCH", "amount in the notification differs from the payment amount")
	errBadParam        = func(d string) *httpx.Error { return httpx.NewError(http.StatusBadRequest, "INVALID_PARAMETER", d) }
)

// ConflictError — 409 с указанием текущей попытки (расширение paymentId в problem).
type ConflictError struct {
	Code      string // PAYMENT_IN_PROGRESS | PAYMENT_STATUS_UNKNOWN | ALREADY_PAID
	Detail    string
	PaymentID string
}

func (e *ConflictError) Error() string { return e.Code + ": " + e.Detail }

// IsNotFound — заказ или платёж не найден.
func IsNotFound(err error) bool {
	var he *httpx.Error
	return errors.As(err, &he) && he.Status == http.StatusNotFound
}

// IsOrderNotFound — именно «заказа нет» (HTTP-слой добавляет Retry-After: событие могло не дойти).
func IsOrderNotFound(err error) bool {
	var he *httpx.Error
	return errors.As(err, &he) && he.Code == "ORDER_NOT_FOUND"
}
