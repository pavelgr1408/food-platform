// Package service — сценарии orders-service: корзина, оформление заказа, автомат статусов.
//
// Внешние вызовы (menu, restaurant) выполняются ВНЕ транзакций БД; транзакция оформления
// заказа атомарно пишет заказ, историю, события (outbox) и ответ под Idempotency-Key.
package service

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pavelgr1408/food-platform/pkg/httpx"
	"github.com/pavelgr1408/food-platform/services/orders/internal/clients"
	"github.com/pavelgr1408/food-platform/services/orders/internal/domain"
	"github.com/pavelgr1408/food-platform/services/orders/internal/store"
)

// MenuValidator — menu-service.validateMenuItems.
type MenuValidator interface {
	Validate(ctx context.Context, restaurantID string, lines []clients.MenuLineInput) (clients.MenuResult, error)
}

// RestaurantGetter — restaurant-service.getRestaurant.
type RestaurantGetter interface {
	Get(ctx context.Context, id string) (clients.RestaurantInfo, error)
}

// Waker будит outbox-relay сразу после коммита (снижает задержку публикации).
type Waker interface{ Wake() }

// Config — настройки сценариев.
type Config struct {
	PaymentTimeout    time.Duration // pending_payment старше — отменяется (payment_timeout)
	UnknownGrace      time.Duration // добавка к таймауту для заказов с paymentStatus=unknown (ждём итог от payment)
	CartTTL           time.Duration // открытые корзины без изменений старше — удаляются
	PrepMinutes       int           // типовое время приготовления (ETA, если свежей оценки производства нет)
	LeadTimeTTL       time.Duration // срок годности реплики production.lead.time
	DeliveryMinutes   int           // типовое время доставки поверх приготовления
	DeliveryEtaTTL    time.Duration // срок годности оценки вручения от курьерской системы (S7)
	NotReceivedAfter  time.Duration // готовый заказ самовывоза, не выданный за это время, → not_received (S6)
	DefaultCurrency   string
	PromotionCacheTTL time.Duration // как долго расчёт скидок использует снимок реплики акций (0 → 2с, <0 → без кэша), S8
}

func (c *Config) defaults() {
	if c.PaymentTimeout <= 0 {
		c.PaymentTimeout = 15 * time.Minute
	}
	if c.UnknownGrace <= 0 {
		c.UnknownGrace = time.Hour
	}
	if c.CartTTL <= 0 {
		c.CartTTL = 7 * 24 * time.Hour
	}
	if c.LeadTimeTTL <= 0 {
		c.LeadTimeTTL = 15 * time.Minute
	}
	if c.PrepMinutes <= 0 {
		c.PrepMinutes = 20
	}
	if c.DeliveryMinutes <= 0 {
		c.DeliveryMinutes = 30
	}
	if c.DeliveryEtaTTL <= 0 {
		c.DeliveryEtaTTL = 30 * time.Minute
	}
	if c.NotReceivedAfter <= 0 {
		c.NotReceivedAfter = 2 * time.Hour
	}
	if c.DefaultCurrency == "" {
		c.DefaultCurrency = "RUB"
	}
	switch {
	case c.PromotionCacheTTL == 0:
		c.PromotionCacheTTL = 2 * time.Second
	case c.PromotionCacheTTL < 0:
		c.PromotionCacheTTL = 0
	}
}

// Deps — зависимости сервиса.
type Deps struct {
	Pool        *pgxpool.Pool
	Menu        MenuValidator
	Restaurants RestaurantGetter
	Discounter  domain.Discounter // nil → ReplicaDiscounter по реплике promotions.discount
	Waker       Waker             // nil допустим
	Log         *slog.Logger
	Clock       func() time.Time // nil → time.Now
	Metrics     *Metrics         // nil → без метрик
	Config      Config
}

// Service — сценарии заказа.
type Service struct {
	pool  *pgxpool.Pool
	st    *store.Store
	menu  MenuValidator
	rest  RestaurantGetter
	disc  domain.Discounter
	waker Waker
	log   *slog.Logger
	now   func() time.Time
	m     *Metrics
	cfg   Config
}

// New создаёт сервис.
func New(d Deps) *Service {
	d.Config.defaults()
	s := &Service{pool: d.Pool, st: store.New(), menu: d.Menu, rest: d.Restaurants, disc: d.Discounter,
		waker: d.Waker, log: d.Log, now: d.Clock, m: d.Metrics, cfg: d.Config}
	if s.log == nil {
		s.log = slog.Default()
	}
	if s.now == nil {
		s.now = func() time.Time { return time.Now().UTC() }
	}
	if s.disc == nil {
		s.disc = NewReplicaDiscounter(d.Pool, s.st, d.Config.PromotionCacheTTL, s.now, s.log, d.Metrics)
	}
	return s
}

// Store отдаёт хранилище (для тестов и консьюмеров следующих этапов).
func (s *Service) Store() *store.Store { return s.st }

// Ошибки сценариев (HTTP-семантика — httpx.Error; контракт — коды в orders.yaml).
var (
	errCartNotFound  = httpx.NewError(http.StatusNotFound, "CART_NOT_FOUND", "cart not found")
	errOrderNotFound = httpx.NewError(http.StatusNotFound, "ORDER_NOT_FOUND", "order not found")
	errRestNotFound  = httpx.NewError(http.StatusNotFound, "RESTAURANT_NOT_FOUND", "restaurant not found")
	errCartClosed    = httpx.NewError(http.StatusConflict, "CART_CLOSED", "cart is already ordered and cannot be changed")
	errCartChanged   = httpx.NewError(http.StatusConflict, "CART_CHANGED", "cart was changed concurrently, reload it and retry")
	errMenuDown      = httpx.NewError(http.StatusServiceUnavailable, "MENU_UNAVAILABLE", "menu service is unavailable, the cart is kept; retry later")
	errRestDown      = httpx.NewError(http.StatusServiceUnavailable, "RESTAURANT_UNAVAILABLE", "restaurant service is unavailable, retry later")
	errUnauth        = httpx.NewError(http.StatusUnauthorized, "UNAUTHENTICATED", "authentication is required unless guest=true")
)

// ErrInvalidTransition — переход статуса запрещён автоматом.
var ErrInvalidTransition = errors.New("service: status transition is not allowed")

// Unavailable — недоступная позиция (E1).
type Unavailable struct {
	MenuItemID string
	ReasonCode string
	Reason     string
}

// ItemsUnavailableError — часть позиций недоступна: 409 ITEMS_UNAVAILABLE со списком.
type ItemsUnavailableError struct{ Items []Unavailable }

func (e *ItemsUnavailableError) Error() string {
	return "ITEMS_UNAVAILABLE: some items are unavailable"
}

// upstreamErr переводит ошибку клиента в HTTP-ошибку.
func (s *Service) upstreamErr(name string, err error, down *httpx.Error) error {
	if errors.Is(err, clients.ErrNotFound) {
		return err
	}
	if s.m != nil {
		s.m.UpstreamFailures.WithLabelValues(name).Inc()
	}
	s.log.Warn("upstream call failed", "upstream", name, "error", err)
	return down
}

func badParam(detail string) error {
	return httpx.NewError(http.StatusBadRequest, "INVALID_PARAMETER", detail)
}
func unprocessable(code, detail string) error {
	return httpx.NewError(http.StatusUnprocessableEntity, code, detail)
}
