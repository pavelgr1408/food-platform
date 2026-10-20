package domain

import (
	"errors"

	"github.com/google/uuid"
)

// Действия каталога authorize (auth.yaml: AuthorizeRequest.action).
const (
	ActionPromotionManage = "promotion.manage"
	ActionOrderManage     = "order.manage"
)

// Области действия акций.
const (
	ScopeLocal    = "local"
	ScopeNational = "national"
)

// Причины отказа (auth.yaml: AuthorizeDecision.reason).
const (
	ReasonNotActive            = "NOT_ACTIVE"
	ReasonRoleRequired         = "ROLE_REQUIRED"
	ReasonNotBoundToRestaurant = "NOT_BOUND_TO_RESTAURANT"
	ReasonScopeNotAllowed      = "SCOPE_NOT_ALLOWED"
)

// Ошибки запроса authorize (не отказ, а некорректный вопрос → 400).
var (
	ErrUnknownAction = errors.New("unknown action")
	ErrInvalidAuthz  = errors.New("scope or restaurantId is required for this action")
)

// AuthzRequest — вопрос «может ли пользователь выполнить действие».
type AuthzRequest struct {
	Action       string
	Scope        string
	RestaurantID *uuid.UUID
}

// Decision — решение; отказ — не ошибка.
type Decision struct {
	Allowed bool
	Reason  string
}

func allow() Decision             { return Decision{Allowed: true} }
func deny(reason string) Decision { return Decision{Reason: reason} }

// Authorize принимает решение по актуальному состоянию пользователя (статус, роли, привязка).
// Чистая функция: правила каталога действий живут здесь, а не в HTTP-слое.
func Authorize(u User, req AuthzRequest) (Decision, error) {
	switch req.Action {
	case ActionPromotionManage:
		switch req.Scope {
		case ScopeNational:
			if !u.Active() {
				return deny(ReasonNotActive), nil
			}
			if u.HasRole(RoleHQStaff) {
				return allow(), nil
			}
			// Владелец франшизы не расширяет охват до национального (UC-PRM-03, альтернативный поток).
			if u.HasRole(RoleFranchiseOwner) {
				return deny(ReasonScopeNotAllowed), nil
			}
			return deny(ReasonRoleRequired), nil
		case ScopeLocal:
			if req.RestaurantID == nil {
				return Decision{}, ErrInvalidAuthz
			}
			if !u.Active() {
				return deny(ReasonNotActive), nil
			}
			if u.HasRole(RoleHQStaff) {
				return allow(), nil
			}
			if u.HasRole(RoleFranchiseOwner) {
				if u.BoundTo(*req.RestaurantID) {
					return allow(), nil
				}
				return deny(ReasonNotBoundToRestaurant), nil
			}
			return deny(ReasonRoleRequired), nil
		default:
			return Decision{}, ErrInvalidAuthz
		}
	case ActionOrderManage:
		if req.RestaurantID == nil {
			return Decision{}, ErrInvalidAuthz
		}
		if !u.Active() {
			return deny(ReasonNotActive), nil
		}
		if u.HasRole(RoleHQStaff) {
			return allow(), nil
		}
		if u.HasRole(RoleStaff, RoleFranchiseOwner) {
			if u.BoundTo(*req.RestaurantID) {
				return allow(), nil
			}
			return deny(ReasonNotBoundToRestaurant), nil
		}
		return deny(ReasonRoleRequired), nil
	default:
		return Decision{}, ErrUnknownAction
	}
}
