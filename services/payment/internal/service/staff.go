package service

import (
	"context"
	"errors"
	"net/http"

	"github.com/google/uuid"

	"github.com/pavelgr1408/food-platform/pkg/httpx"
	"github.com/pavelgr1408/food-platform/services/payment/internal/domain"
	"github.com/pavelgr1408/food-platform/services/payment/internal/store"
)

// Actor — вызывающий сотрудник (из токена). Нулевое значение — не аутентифицирован.
type Actor struct {
	UserID      string
	Staff       bool // STAFF | FRANCHISE_OWNER | HQ_STAFF
	HQ          bool // HQ_STAFF: видит все рестораны
	Restaurants []string
}

// Can — разрешён ли ресторан этому сотруднику.
func (a Actor) Can(restaurantID string) bool {
	if !a.Staff {
		return false
	}
	if a.HQ {
		return true
	}
	for _, r := range a.Restaurants {
		if r == restaurantID {
			return true
		}
	}
	return false
}

var (
	errUnauthorized = httpx.NewError(http.StatusUnauthorized, "UNAUTHORIZED", "authentication is required")
	errForbidden    = httpx.NewError(http.StatusForbidden, "FORBIDDEN", "staff role is required")
)

// requireStaff — защита в глубину: HTTP-слой уже проверил роль, сервис не полагается на это молча.
func (a Actor) requireStaff() error {
	switch {
	case a.UserID == "":
		return errUnauthorized
	case !a.Staff:
		return errForbidden
	}
	return nil
}

// loadOrderStaff читает реплику заказа строго в пределах ресторанов сотрудника; чужой/неизвестный — 404
// (существование чужого заказа не раскрываем).
func (s *Service) loadOrderStaff(ctx context.Context, a Actor, id string) (domain.Order, error) {
	if _, err := uuid.Parse(id); err != nil {
		return domain.Order{}, errOrderNotFound
	}
	o, err := s.st.GetOrder(ctx, s.pool, id, false)
	if errors.Is(err, store.ErrNotFound) || (err == nil && !a.Can(o.Restaurant)) {
		return domain.Order{}, errOrderNotFound
	}
	return o, err
}

// InitiatePos — initiatePosPayment (UC-PAY-04): сотрудник ресторана инициирует оплату заказа на кассе.
// Те же правила, что у онлайн-оплаты: попытка пишется до вызова кассы, вызов — вне транзакции,
// одна живая и одна успешная попытка; таймаут кассы → unknown (итог добудет reconciler).
func (s *Service) InitiatePos(ctx context.Context, a Actor, orderID string, tender domain.Tender) (domain.Payment, error) {
	if err := a.requireStaff(); err != nil {
		return domain.Payment{}, err
	}
	if !tender.Valid() {
		return domain.Payment{}, errBadParam("tender must be one of card, cash")
	}
	ord, err := s.loadOrderStaff(ctx, a, orderID)
	if err != nil {
		return domain.Payment{}, err
	}
	if ord.Method != domain.MethodPOS {
		return domain.Payment{}, errNotAllowed
	}
	if s.pos == nil {
		return domain.Payment{}, errPOSUnavailable
	}
	p, err := s.startAttempt(ctx, orderID, posPayable, tender, "")
	if err != nil {
		return domain.Payment{}, err
	}
	return s.callGateway(ctx, p, ord.Number)
}

// GetAs — getPayment для клиента и сотрудника: сотрудник читает платежи заказов своих ресторанов,
// остальные — по правилу владельца (гостевой заказ доступен знающему id).
func (s *Service) GetAs(ctx context.Context, a Actor, orderID, paymentID string) (domain.Payment, error) {
	if _, err := uuid.Parse(orderID); err != nil {
		return domain.Payment{}, errOrderNotFound
	}
	o, err := s.st.GetOrder(ctx, s.pool, orderID, false)
	if errors.Is(err, store.ErrNotFound) {
		return domain.Payment{}, errOrderNotFound
	}
	if err != nil {
		return domain.Payment{}, err
	}
	if !(a.Can(o.Restaurant) || domain.CanBeAccessedBy(o.OwnerUserID, a.UserID)) {
		return domain.Payment{}, errOrderNotFound
	}
	return s.getPaymentOf(ctx, orderID, paymentID)
}
