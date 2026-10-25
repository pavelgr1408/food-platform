// Package service — сценарии delivery-service: заявка на курьера по orders.state, статусы от курьерской
// системы (вебхук + опрос), координаты курьера в Redis, публикация orders.delivery.status.
//
// Внешние вызовы (курьерская система, restaurant-service, Redis) выполняются ВНЕ транзакций БД;
// смена статуса и событие outbox пишутся одной транзакцией (applyTx — единственное место смены статуса).
package service

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pavelgr1408/food-platform/pkg/httpx"
	"github.com/pavelgr1408/food-platform/services/delivery/internal/courier"
	"github.com/pavelgr1408/food-platform/services/delivery/internal/location"
	"github.com/pavelgr1408/food-platform/services/delivery/internal/store"
)

// Топики и типы событий (contracts/asyncapi/kafka-events.yaml).
const (
	TopicState   = "orders.state"
	TopicStatus  = "orders.status"
	TopicLink    = "orders.payments.link"
	TopicOut     = "orders.delivery.status"
	TypeOut      = "orders.delivery.status.v1"
	sourceState  = "state"
	sourceHook   = "webhook"
	sourcePoll   = "poll"
	sourceTimout = "timeout"
	sourceCancel = "cancel"
)

// Place — точка ресторана (забор заказа).
type Place struct {
	Lat, Lon float64
	Address  string
}

// Restaurants — координаты ресторана (restaurant-service.getRestaurant).
type Restaurants interface {
	Place(ctx context.Context, restaurantID string) (Place, error)
}

// Waker будит outbox-relay сразу после коммита.
type Waker interface{ Wake() }

// Config — настройки сценариев.
type Config struct {
	SearchTimeout  time.Duration // курьер не назначен за это время → failed/no_courier (E2)
	LocationTTL    time.Duration // срок жизни точки в Redis
	PollEvery      time.Duration // период опроса курьерской системы (координаты, сверка статуса)
	PollMax        time.Duration // верхняя граница backoff при сбоях
	Lease          time.Duration // аренда доставки на время обработки
	LinkWait       time.Duration // сколько ждать платёжную ссылку перед заявкой (оплата курьеру)
	ReconcileBatch int
}

func (c *Config) defaults() {
	if c.SearchTimeout <= 0 {
		c.SearchTimeout = 10 * time.Minute
	}
	if c.LocationTTL <= 0 {
		c.LocationTTL = time.Minute
	}
	if c.PollEvery <= 0 {
		c.PollEvery = 3 * time.Second
	}
	if c.PollMax <= 0 {
		c.PollMax = 30 * time.Second
	}
	if c.Lease <= 0 {
		c.Lease = 30 * time.Second
	}
	if c.LinkWait <= 0 {
		c.LinkWait = 20 * time.Second
	}
	if c.ReconcileBatch <= 0 {
		c.ReconcileBatch = 20
	}
}

// Deps — зависимости сервиса.
type Deps struct {
	Pool        *pgxpool.Pool
	Courier     courier.Courier
	Restaurants Restaurants
	Locations   location.Store // nil → координаты не сохраняются (Redis не настроен)
	Waker       Waker          // nil допустим
	Log         *slog.Logger
	Clock       func() time.Time // nil → time.Now
	Metrics     *Metrics         // nil → без метрик
	Config      Config
}

// Service — сценарии доставки.
type Service struct {
	pool  *pgxpool.Pool
	st    *store.Store
	cr    courier.Courier
	rest  Restaurants
	locs  location.Store
	waker Waker
	log   *slog.Logger
	now   func() time.Time
	m     *Metrics
	cfg   Config
}

// New создаёт сервис.
func New(d Deps) *Service {
	d.Config.defaults()
	s := &Service{pool: d.Pool, st: store.New(), cr: d.Courier, rest: d.Restaurants, locs: d.Locations,
		waker: d.Waker, log: d.Log, now: d.Clock, m: d.Metrics, cfg: d.Config}
	if s.log == nil {
		s.log = slog.Default()
	}
	if s.now == nil {
		s.now = func() time.Time { return time.Now().UTC() }
	}
	if s.m == nil {
		s.m = NewMetrics()
	}
	return s
}

// Store отдаёт хранилище (для тестов).
func (s *Service) Store() *store.Store { return s.st }

// Topics — топики, которые потребляет delivery-service.
func Topics() []string { return []string{TopicState, TopicStatus, TopicLink} }

// Ошибки сценариев (контракт — коды в delivery.yaml).
var (
	errNotFound      = httpx.NewError(http.StatusNotFound, "DELIVERY_NOT_FOUND", "delivery not found")
	errNoLocation    = httpx.NewError(http.StatusNotFound, "LOCATION_NOT_AVAILABLE", "the courier location is not available")
	errLocationsDown = httpx.NewError(http.StatusServiceUnavailable, "LOCATION_STORE_UNAVAILABLE", "the courier location store is unavailable, retry later")
)

// IsNotFound — доставки нет (для вебхука: 404, курьерская система повторит позже).
func IsNotFound(err error) bool {
	var he *httpx.Error
	return errors.As(err, &he) && he.Code == "DELIVERY_NOT_FOUND"
}

func (s *Service) wake() {
	if s.waker != nil {
		s.waker.Wake()
	}
}
