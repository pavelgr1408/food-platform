// Package service — сценарии loyalty-service: жизненный цикл акций (draft → active → completed),
// права через auth-service, правила конфликтов (E2), история и публикация promotions.discount.
//
// Изменение акции, запись истории и событие outbox выполняются одной транзакцией. Проверка прав
// (сетевой вызов auth) — до транзакции. Активация и изменение действующей акции сериализуются
// advisory-lock'ом: иначе две конфликтующие акции могли бы активироваться одновременно.
package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pavelgr1408/food-platform/pkg/authclient"
	"github.com/pavelgr1408/food-platform/pkg/httpx"
	"github.com/pavelgr1408/food-platform/pkg/pgxutil"
	"github.com/pavelgr1408/food-platform/services/loyalty/internal/authz"
	"github.com/pavelgr1408/food-platform/services/loyalty/internal/domain"
	"github.com/pavelgr1408/food-platform/services/loyalty/internal/dto"
	"github.com/pavelgr1408/food-platform/services/loyalty/internal/store"
)

// Waker будит outbox-relay сразу после коммита.
type Waker interface{ Wake() }

// Deps — зависимости сервиса.
type Deps struct {
	Pool    *pgxpool.Pool
	Authz   authz.Authorizer
	Waker   Waker // nil допустим
	Metrics *Metrics
	Now     func() time.Time // nil → time.Now
}

// Service — сценарии акций.
type Service struct {
	pool  *pgxpool.Pool
	st    *store.Store
	authz authz.Authorizer
	waker Waker
	m     *Metrics
	now   func() time.Time
}

// New создаёт сервис.
func New(d Deps) *Service {
	s := &Service{pool: d.Pool, st: store.New(), authz: d.Authz, waker: d.Waker, m: d.Metrics, now: d.Now}
	if s.now == nil {
		s.now = time.Now
	}
	if s.m == nil {
		s.m = NewMetrics()
	}
	return s
}

// Actor — субъект запроса: личность и заголовок Authorization (для проверки прав в auth).
type Actor struct {
	Principal     authclient.Principal
	Authorization string
}

// Ошибки домена сервиса (HTTP-семантика — httpx.Error).
var (
	errNotFound  = httpx.NewError(404, "PROMOTION_NOT_FOUND", "promotion not found")
	errCompleted = httpx.NewError(409, "PROMOTION_COMPLETED", "the promotion is completed and cannot be changed")
	errExpired   = httpx.NewError(422, "PROMOTION_EXPIRED", "the promotion period has already ended")
	errImmutable = httpx.NewError(422, "SCOPE_IMMUTABLE", "scope and restaurantId cannot be changed")
	errNoAuth    = httpx.NewError(503, "AUTH_UNAVAILABLE", "authorization service is unavailable; try again later")
)

// ConflictError — 409 PROMOTION_CONFLICT с перечнем конфликтующих акций.
type ConflictError struct{ IDs []string }

func (e *ConflictError) Error() string {
	return "PROMOTION_CONFLICT: conflicts with active promotions " + strings.Join(e.IDs, ", ")
}

func invalid(err error) error {
	var ve *domain.ValidationError
	if errors.As(err, &ve) {
		return httpx.NewError(422, "PROMOTION_INVALID", ve.Msg)
	}
	return err
}

// can проверяет право управлять акцией с таким охватом; отказ — 403 с причиной auth в code.
func (s *Service) can(ctx context.Context, a Actor, scope domain.Scope, restaurantID string) error {
	d, err := s.authz.CanManagePromotion(ctx, authz.Request{Principal: a.Principal, Authorization: a.Authorization,
		Scope: string(scope), RestaurantID: restaurantID})
	switch {
	case err != nil:
		s.m.Authz.WithLabelValues("unavailable").Inc()
		return errNoAuth
	case !d.Allowed:
		s.m.Authz.WithLabelValues("denied").Inc()
		reason := d.Reason
		if reason == "" {
			reason = "FORBIDDEN"
		}
		return httpx.NewError(403, reason, "not allowed to manage this promotion: "+reason)
	}
	s.m.Authz.WithLabelValues("allowed").Inc()
	return nil
}

func (s *Service) result(action string, err error) {
	r := "ok"
	var ce *ConflictError
	var he *httpx.Error
	switch {
	case err == nil:
	case errors.As(err, &ce):
		r = "conflict"
	case errors.As(err, &he) && he.Status == 403:
		r = "forbidden"
	case errors.As(err, &he) && (he.Status == 422 || he.Status == 409 || he.Status == 404):
		r = "invalid"
	default:
		r = "error"
	}
	s.m.Actions.WithLabelValues(action, r).Inc()
}

func (s *Service) wake() {
	if s.waker != nil {
		s.waker.Wake()
	}
}

func validID(id string) (string, bool) {
	u, err := uuid.Parse(id)
	return strings.ToLower(u.String()), err == nil
}

// snapshot — запись истории и (для действующей/только что завершённой акции) событие outbox.
func (s *Service) record(ctx context.Context, tx pgx.Tx, p domain.Promotion, action, actor string, publish bool) error {
	snap, err := json.Marshal(dto.From(p))
	if err != nil {
		return err
	}
	if err := s.st.AddHistory(ctx, tx, p.ID, p.Version, action, actor, p.UpdatedAt, snap); err != nil {
		return err
	}
	if publish {
		return enqueue(ctx, tx, p)
	}
	return nil
}

// Create создаёт акцию в статусе draft.
func (s *Service) Create(ctx context.Context, a Actor, in domain.Promotion) (p domain.Promotion, err error) {
	defer func() { s.result("create", err) }()
	in, err = domain.Normalize(in)
	if err != nil {
		return in, invalid(err)
	}
	if err := s.can(ctx, a, in.Scope, in.RestaurantID); err != nil {
		return in, err
	}
	now := s.now().UTC()
	in.ID, in.Status, in.Version = uuid.NewString(), domain.StatusDraft, 1
	in.CreatedBy, in.CreatedAt, in.UpdatedAt = a.Principal.UserID, now, now
	err = pgxutil.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := s.st.Insert(ctx, tx, in); err != nil {
			return err
		}
		return s.record(ctx, tx, in, "created", a.Principal.UserID, false)
	})
	return in, err
}

// load читает акцию и проверяет право управлять ею (без блокировки; для чтения и предварительной проверки).
func (s *Service) load(ctx context.Context, a Actor, id string) (domain.Promotion, error) {
	id, ok := validID(id)
	if !ok {
		return domain.Promotion{}, errNotFound
	}
	p, err := s.st.Get(ctx, s.pool, id, false)
	if errors.Is(err, store.ErrNotFound) {
		return p, errNotFound
	}
	if err != nil {
		return p, err
	}
	return p, s.can(ctx, a, p.Scope, p.RestaurantID)
}

// Get возвращает акцию тому, кто вправе ею управлять.
func (s *Service) Get(ctx context.Context, a Actor, id string) (domain.Promotion, error) {
	return s.load(ctx, a, id)
}

// HistoryEntry — запись журнала (loyalty.yaml PromotionRevision).
type HistoryEntry struct {
	Version    int             `json:"version"`
	Action     string          `json:"action"`
	ActorID    string          `json:"actorId,omitempty"`
	OccurredAt time.Time       `json:"occurredAt"`
	Snapshot   json.RawMessage `json:"snapshot"`
}

// History возвращает журнал изменений акции.
func (s *Service) History(ctx context.Context, a Actor, id string) ([]HistoryEntry, error) {
	p, err := s.load(ctx, a, id)
	if err != nil {
		return nil, err
	}
	revs, err := s.st.History(ctx, s.pool, p.ID)
	if err != nil {
		return nil, err
	}
	out := make([]HistoryEntry, 0, len(revs))
	for _, r := range revs {
		out = append(out, HistoryEntry{Version: r.Version, Action: r.Action, ActorID: r.ActorID, OccurredAt: r.OccurredAt, Snapshot: r.Snapshot})
	}
	return out, nil
}

// ListActive — акции, действующие сейчас; без ресторана — только национальные.
func (s *Service) ListActive(ctx context.Context, restaurantID string) ([]domain.Promotion, error) {
	if restaurantID != "" {
		id, ok := validID(restaurantID)
		if !ok {
			return nil, httpx.NewError(400, "VALIDATION_FAILED", "restaurantId must be a UUID")
		}
		restaurantID = id
	}
	ps, err := s.st.ListCurrent(ctx, s.pool, s.now().UTC(), restaurantID)
	if err != nil {
		return nil, httpx.NewError(503, "PROMOTIONS_UNAVAILABLE", "promotions are temporarily unavailable")
	}
	return ps, nil
}

// mutate — общий каркас: право на существующую акцию → транзакция с блокировкой → fn меняет акцию.
// fn возвращает (изменённая акция, действие истории, публиковать ли событие, изменилось ли что-то).
func (s *Service) mutate(ctx context.Context, a Actor, id string, lockActivation bool,
	fn func(cur domain.Promotion, now time.Time, tx pgx.Tx) (next domain.Promotion, action string, publish, changed bool, err error)) (domain.Promotion, error) {
	if _, err := s.load(ctx, a, id); err != nil {
		return domain.Promotion{}, err
	}
	id, _ = validID(id)
	var out domain.Promotion
	var published bool
	err := pgxutil.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if lockActivation {
			if err := s.st.LockActivation(ctx, tx); err != nil {
				return err
			}
		}
		cur, err := s.st.Get(ctx, tx, id, true)
		if errors.Is(err, store.ErrNotFound) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		now := s.now().UTC()
		next, action, publish, changed, err := fn(cur, now, tx)
		if err != nil {
			return err
		}
		out = next
		if !changed {
			out = cur
			return nil
		}
		next.Version, next.UpdatedAt = cur.Version+1, now
		out = next
		if err := s.st.Update(ctx, tx, next); err != nil {
			return err
		}
		published = publish
		return s.record(ctx, tx, next, action, a.Principal.UserID, publish)
	})
	if err == nil && published {
		s.wake()
	}
	return out, err
}

// conflicts возвращает ConflictError, если кандидат конфликтует с действующими акциями.
func (s *Service) conflicts(ctx context.Context, tx pgx.Tx, cand domain.Promotion, now time.Time) error {
	active, err := s.st.ListActive(ctx, tx, now)
	if err != nil {
		return err
	}
	if ids := domain.FindConflicts(cand, active); len(ids) > 0 {
		return &ConflictError{IDs: ids}
	}
	return nil
}

// Update заменяет параметры черновика или действующей акции.
func (s *Service) Update(ctx context.Context, a Actor, id string, in domain.Promotion) (p domain.Promotion, err error) {
	defer func() { s.result("update", err) }()
	in, err = domain.Normalize(in)
	if err != nil {
		return in, invalid(err)
	}
	// охват из запроса тоже должен быть в пределах полномочий (владелец не «расширяет» акцию до national)
	if err := s.can(ctx, a, in.Scope, in.RestaurantID); err != nil {
		return in, err
	}
	return s.mutate(ctx, a, id, true, func(cur domain.Promotion, now time.Time, tx pgx.Tx) (domain.Promotion, string, bool, bool, error) {
		if cur.Status == domain.StatusCompleted {
			return cur, "", false, false, errCompleted
		}
		if in.Scope != cur.Scope || in.RestaurantID != cur.RestaurantID {
			return cur, "", false, false, errImmutable
		}
		next := cur
		next.Title, next.Description, next.Priority = in.Title, in.Description, in.Priority
		next.StartsAt, next.EndsAt, next.Currency = in.StartsAt, in.EndsAt, in.Currency
		next.Discount, next.Conditions = in.Discount, in.Conditions
		if cur.Status == domain.StatusActive {
			if next.Expired(now) {
				return cur, "", false, false, errExpired
			}
			if err := s.conflicts(ctx, tx, next, now); err != nil {
				return cur, "", false, false, err
			}
		}
		return next, "updated", cur.Status == domain.StatusActive, true, nil
	})
}

// Activate публикует акцию (draft → active). Повтор для действующей — без эффекта.
func (s *Service) Activate(ctx context.Context, a Actor, id string) (p domain.Promotion, err error) {
	defer func() { s.result("activate", err) }()
	return s.mutate(ctx, a, id, true, func(cur domain.Promotion, now time.Time, tx pgx.Tx) (domain.Promotion, string, bool, bool, error) {
		switch cur.Status {
		case domain.StatusActive:
			return cur, "", false, false, nil
		case domain.StatusCompleted:
			return cur, "", false, false, errCompleted
		}
		if cur.Expired(now) {
			return cur, "", false, false, errExpired
		}
		if err := s.conflicts(ctx, tx, cur, now); err != nil {
			return cur, "", false, false, err
		}
		next := cur
		next.Status = domain.StatusActive
		return next, "activated", true, true, nil
	})
}

// Complete завершает акцию. Для черновика событие не публикуется (orders о нём не знает).
func (s *Service) Complete(ctx context.Context, a Actor, id string) (p domain.Promotion, err error) {
	defer func() { s.result("complete", err) }()
	return s.mutate(ctx, a, id, false, func(cur domain.Promotion, now time.Time, tx pgx.Tx) (domain.Promotion, string, bool, bool, error) {
		if cur.Status == domain.StatusCompleted {
			return cur, "", false, false, nil
		}
		next := cur
		next.Status = domain.StatusCompleted
		return next, "completed", cur.Status == domain.StatusActive, true, nil
	})
}
