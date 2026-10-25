// Package service — сценарии notification-service: регистрация push-токенов, постановка уведомлений в
// очередь по orders.status и их отправка провайдерам.
//
// Внешние вызовы (APNs/FCM/HMS) выполняются ВНЕ транзакций БД: консьюмер пишет строки очереди в
// транзакции inbox, отдельный процесс (Dispatcher) берёт их в аренду и отправляет. Сбой push никогда
// не влияет на заказ: консьюмер ничего не вызывает по сети.
package service

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pavelgr1408/food-platform/pkg/httpx"
	"github.com/pavelgr1408/food-platform/pkg/pgxutil"
	"github.com/pavelgr1408/food-platform/services/notification/internal/domain"
	"github.com/pavelgr1408/food-platform/services/notification/internal/push"
	"github.com/pavelgr1408/food-platform/services/notification/internal/store"
)

// TopicStatus — единственный потребляемый топик.
const TopicStatus = "orders.status"

// Config — настройки сценариев.
type Config struct {
	MaxEventAge time.Duration // событие старше — не отправляем (простой консьюмера, чтение топика с начала)
	Lease       time.Duration // аренда уведомления на время отправки
	BackoffBase time.Duration // пауза перед первым повтором; далее удваивается
	BackoffMax  time.Duration
	MaxAttempts int           // попыток отправки до failed
	Batch       int           // уведомлений за тик
	Workers     int           // параллельных отправок
	SendTimeout time.Duration // на один вызов провайдера
	Retention   time.Duration // сколько хранить закрытые уведомления
}

func (c *Config) defaults() {
	if c.MaxEventAge <= 0 {
		c.MaxEventAge = time.Hour
	}
	if c.Lease <= 0 {
		c.Lease = 30 * time.Second
	}
	if c.BackoffBase <= 0 {
		c.BackoffBase = 2 * time.Second
	}
	if c.BackoffMax <= 0 {
		c.BackoffMax = 5 * time.Minute
	}
	if c.MaxAttempts <= 0 {
		c.MaxAttempts = 6
	}
	if c.Batch <= 0 {
		c.Batch = 50
	}
	if c.Workers <= 0 {
		c.Workers = 8
	}
	if c.SendTimeout <= 0 {
		c.SendTimeout = 10 * time.Second
	}
	if c.Retention <= 0 {
		c.Retention = 7 * 24 * time.Hour
	}
}

// Deps — зависимости сервиса.
type Deps struct {
	Pool    *pgxpool.Pool
	Sender  push.Sender
	Log     *slog.Logger
	Clock   func() time.Time // nil → time.Now
	Metrics *Metrics         // nil → без метрик
	Config  Config
}

// Service — сценарии уведомлений.
type Service struct {
	pool   *pgxpool.Pool
	st     *store.Store
	sender push.Sender
	log    *slog.Logger
	now    func() time.Time
	m      *Metrics
	cfg    Config
	// последняя очистка закрытых уведомлений; трогается только из Tick
	lastPurge time.Time
}

// New создаёт сервис.
func New(d Deps) *Service {
	d.Config.defaults()
	s := &Service{pool: d.Pool, st: store.New(), sender: d.Sender, log: d.Log, now: d.Clock, m: d.Metrics, cfg: d.Config}
	if s.now == nil {
		s.now = time.Now
	}
	if s.m == nil {
		s.m = NewMetrics()
	}
	if s.log == nil {
		s.log = slog.Default()
	}
	return s
}

// Topics — топики консьюмера.
func Topics() []string { return []string{TopicStatus} }

// RegisterInput — запрос на регистрацию токена.
type RegisterInput struct {
	UserID      string
	Platform    domain.Platform
	Token       string
	Permissions bool
	Locale      domain.Locale
}

var errRetry = errors.New("retry registration")

// Register регистрирует токен. created=true — новый токен (201), false — токен уже был: данные обновлены
// (или токен перешёл от другого пользователя) — 200.
func (s *Service) Register(ctx context.Context, in RegisterInput) (domain.DeviceToken, bool, error) {
	if in.UserID == "" {
		return domain.DeviceToken{}, false, httpx.NewError(http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
	}
	if !in.Platform.Valid() {
		return domain.DeviceToken{}, false, httpx.NewError(http.StatusBadRequest, "VALIDATION_FAILED", "platform must be one of apns, fcm, huawei")
	}
	if err := domain.ValidateToken(in.Token); err != nil {
		return domain.DeviceToken{}, false, httpx.NewError(http.StatusBadRequest, "VALIDATION_FAILED", err.Error())
	}
	var tok domain.DeviceToken
	var created bool
	var op string
	var evicted int64
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		err = pgxutil.InTx(ctx, s.pool, func(tx pgx.Tx) error {
			now := s.now().UTC().Truncate(time.Microsecond)
			cur, err := s.st.TokenByValue(ctx, tx, in.Platform, in.Token, true)
			switch {
			case err == nil:
				tok, created = cur, false
				tok.PermissionsGranted, tok.Locale, tok.UpdatedAt = in.Permissions, in.Locale, now
				op = "updated"
				if cur.UserID != in.UserID {
					// устройство сменило владельца: ожидающие уведомления прежнего владельца новому не отдаём
					tok.UserID, tok.RegisteredAt = in.UserID, now
					if err := s.st.DropPending(ctx, tx, cur.ID); err != nil {
						return err
					}
					op = "moved"
				}
				if err := s.st.UpdateToken(ctx, tx, tok); err != nil {
					return err
				}
			case errors.Is(err, store.ErrNotFound):
				tok = domain.DeviceToken{ID: uuid.NewString(), UserID: in.UserID, Platform: in.Platform, Token: in.Token,
					PermissionsGranted: in.Permissions, Locale: in.Locale, RegisteredAt: now, UpdatedAt: now}
				ok, err := s.st.InsertToken(ctx, tx, tok)
				if err != nil {
					return err
				}
				if !ok {
					return errRetry // параллельная регистрация того же токена: перечитываем уже с блокировкой
				}
				created, op = true, "created"
			default:
				return err
			}
			evicted, err = s.st.EvictOldTokens(ctx, tx, in.UserID, domain.MaxTokensPerUser)
			return err
		})
		if !errors.Is(err, errRetry) {
			break
		}
	}
	if err != nil {
		return domain.DeviceToken{}, false, err
	}
	s.m.Tokens.WithLabelValues(op).Inc()
	if evicted > 0 {
		s.m.Tokens.WithLabelValues("evicted").Add(float64(evicted))
	}
	s.log.InfoContext(ctx, "device token registered", "op", op, "platform", tok.Platform, "token_id", tok.ID, "user_id", in.UserID)
	return tok, created, nil
}

// List — токены пользователя.
func (s *Service) List(ctx context.Context, userID string) ([]domain.DeviceToken, error) {
	if userID == "" {
		return nil, httpx.NewError(http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
	}
	return s.st.ListTokens(ctx, s.pool, userID)
}

// Delete удаляет токен пользователя; чужой или несуществующий — 404 DEVICE_TOKEN_NOT_FOUND.
func (s *Service) Delete(ctx context.Context, userID, tokenID string) error {
	if userID == "" {
		return httpx.NewError(http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
	}
	notFound := httpx.NewError(http.StatusNotFound, "DEVICE_TOKEN_NOT_FOUND", "device token not found")
	if _, err := uuid.Parse(tokenID); err != nil {
		return notFound
	}
	ok, err := s.st.DeleteUserToken(ctx, s.pool, userID, tokenID)
	if err != nil {
		return err
	}
	if !ok {
		return notFound
	}
	s.m.Tokens.WithLabelValues("deleted").Inc()
	s.log.InfoContext(ctx, "device token deleted", "token_id", tokenID, "user_id", userID)
	return nil
}
