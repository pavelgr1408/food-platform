package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/pavelgr1408/food-platform/pkg/httpx"
	"github.com/pavelgr1408/food-platform/pkg/pgxutil"
	"github.com/pavelgr1408/food-platform/services/loyalty/internal/domain"
	"github.com/pavelgr1408/food-platform/services/loyalty/internal/store"
)

// Границы страницы списка управления (loyalty.yaml: listManagedPromotions).
const (
	DefaultListLimit    = 20
	MaxListLimit        = 50
	maxOwnerRestaurants = 20 // верхняя граница числа проверок прав владельца без restaurantId
)

var errNotDraft = httpx.NewError(409, "PROMOTION_NOT_DRAFT", "only a draft promotion can be deleted; complete an active one instead")

func badParam(detail string) error { return httpx.NewError(400, "INVALID_PARAMETER", detail) }

// ManagedInput — запрос списка управления. Limit/Offset == nil — значение по умолчанию.
type ManagedInput struct {
	Statuses     []string // сырые значения ?status=…; пусто — любые
	Scope        string   // "", local, national
	RestaurantID string
	Limit        *int
	Offset       *int
}

// ManagedPage — страница списка управления.
type ManagedPage struct {
	Items         []domain.Promotion
	Total         int
	Limit, Offset int
}

// ListManaged — listManagedPromotions (долг №89): акции в любом статусе в пределах полномочий.
// HQ_STAFF видит всё; FRANCHISE_OWNER — только local-акции своих ресторанов. Право определяет
// auth-service (как для остальных операций управления): fail-closed при недоступности.
func (s *Service) ListManaged(ctx context.Context, a Actor, in ManagedInput) (ManagedPage, error) {
	limit, offset := DefaultListLimit, 0
	if in.Limit != nil {
		limit = *in.Limit
	}
	if in.Offset != nil {
		offset = *in.Offset
	}
	if limit < 1 || limit > MaxListLimit {
		return ManagedPage{}, badParam(fmt.Sprintf("limit must be between 1 and %d", MaxListLimit))
	}
	if offset < 0 {
		return ManagedPage{}, badParam("offset must be 0 or greater")
	}
	if len(in.Statuses) > 3 {
		return ManagedPage{}, badParam("status may be repeated at most 3 times")
	}
	var statuses []domain.Status
	seen := map[domain.Status]bool{}
	for _, raw := range in.Statuses {
		st := domain.Status(raw)
		switch st {
		case domain.StatusDraft, domain.StatusActive, domain.StatusCompleted:
		default:
			return ManagedPage{}, badParam("unknown status: " + raw)
		}
		if !seen[st] {
			seen[st] = true
			statuses = append(statuses, st)
		}
	}
	switch in.Scope {
	case "", string(domain.ScopeLocal), string(domain.ScopeNational):
	default:
		return ManagedPage{}, badParam("scope must be local or national")
	}
	f := store.ManagedFilter{Statuses: statuses, Scope: in.Scope, Limit: limit, Offset: offset}

	switch rid := strings.TrimSpace(in.RestaurantID); {
	case rid != "":
		id, ok := validID(rid)
		if !ok {
			return ManagedPage{}, badParam("restaurantId must be a UUID")
		}
		if in.Scope == string(domain.ScopeNational) {
			return ManagedPage{}, badParam("restaurantId cannot be combined with scope=national")
		}
		if err := s.can(ctx, a, domain.ScopeLocal, id); err != nil {
			return ManagedPage{}, err
		}
		f.RestaurantIDs = []string{id}
	case in.Scope == string(domain.ScopeNational):
		if err := s.can(ctx, a, domain.ScopeNational, ""); err != nil {
			return ManagedPage{}, err
		}
	default:
		// Кто это: HQ (может управлять национальными) или владелец (SCOPE_NOT_ALLOWED)? Остальные отказы — как есть.
		err := s.can(ctx, a, domain.ScopeNational, "")
		var he *httpx.Error
		switch {
		case err == nil: // HQ: без ограничения по ресторану
		case errors.As(err, &he) && he.Code == "SCOPE_NOT_ALLOWED":
			ids, err := s.ownerRestaurants(ctx, a)
			if err != nil {
				return ManagedPage{}, err
			}
			f.RestaurantIDs = ids
		default:
			return ManagedPage{}, err
		}
	}
	items, total, err := s.st.ListManaged(ctx, s.pool, f)
	if err != nil {
		return ManagedPage{}, err
	}
	return ManagedPage{Items: items, Total: total, Limit: limit, Offset: offset}, nil
}

// ownerRestaurants — рестораны владельца из токена, право на каждый подтверждает auth (актуальные данные БД).
func (s *Service) ownerRestaurants(ctx context.Context, a Actor) ([]string, error) {
	claims := a.Principal.Restaurants
	if len(claims) == 0 {
		return nil, httpx.NewError(403, "NOT_BOUND_TO_RESTAURANT", "the owner is not bound to a restaurant")
	}
	if len(claims) > maxOwnerRestaurants {
		claims = claims[:maxOwnerRestaurants]
	}
	var ids []string
	var firstErr error
	for _, c := range claims {
		id, ok := validID(c)
		if !ok {
			continue
		}
		err := s.can(ctx, a, domain.ScopeLocal, id)
		switch {
		case err == nil:
			ids = append(ids, id)
		case firstErr == nil:
			firstErr = err
		}
	}
	if len(ids) == 0 {
		if firstErr != nil {
			return nil, firstErr
		}
		return nil, httpx.NewError(403, "NOT_BOUND_TO_RESTAURANT", "the owner is not bound to a restaurant")
	}
	return ids, nil
}

// Delete — deletePromotion (долг №59, №89): удаляет черновик вместе с историей. Право — как на изменение.
// Статус проверяется под блокировкой строки: параллельная активация либо выигрывает (тогда 409), либо
// проигрывает (тогда уже удалено и активация получит 404).
func (s *Service) Delete(ctx context.Context, a Actor, id string) (err error) {
	defer func() { s.result("delete", err) }()
	if _, err = s.load(ctx, a, id); err != nil {
		return err
	}
	id, _ = validID(id)
	return pgxutil.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := s.st.Get(ctx, tx, id, true)
		if errors.Is(err, store.ErrNotFound) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		if cur.Status != domain.StatusDraft {
			return errNotDraft
		}
		return s.st.Delete(ctx, tx, id)
	})
}
