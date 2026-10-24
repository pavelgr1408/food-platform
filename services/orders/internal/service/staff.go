package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/pavelgr1408/food-platform/pkg/httpx"
	"github.com/pavelgr1408/food-platform/pkg/pgxutil"
	"github.com/pavelgr1408/food-platform/services/orders/internal/domain"
	"github.com/pavelgr1408/food-platform/services/orders/internal/store"
)

// Причины статусов, которые ставит персонал и таймеры (OrderStatus.reason в AsyncAPI).
const (
	ReasonIssued        = "issued"         // handed_over → closed при выдаче в ресторане
	ReasonPickupTimeout = "pickup_timeout" // самовывоз не забрали: ready → not_received
)

// Actor — субъект запроса персонала (строится из authclient.Principal в HTTP-слое).
type Actor struct {
	UserID      string
	Staff       bool     // роль STAFF, FRANCHISE_OWNER или HQ_STAFF
	HQ          bool     // HQ_STAFF: видит все рестораны
	Restaurants []string // привязка сотрудника/владельца из токена
}

// Can — доступен ли ресторан субъекту (HQ — любой).
func (a Actor) Can(restaurantID string) bool {
	return a.HQ || slices.Contains(a.Restaurants, restaurantID)
}

// requireStaff: гость → 401, не персонал → 403.
func (a Actor) requireStaff() error {
	switch {
	case a.UserID == "":
		return errUnauthorized
	case !a.Staff:
		return errForbidden
	}
	return nil
}

func (a Actor) label() string { return "staff:" + a.UserID }

var (
	errUnauthorized  = httpx.NewError(http.StatusUnauthorized, "UNAUTHORIZED", "authentication is required")
	errForbidden     = httpx.NewError(http.StatusForbidden, "FORBIDDEN", "staff role and access to the restaurant are required")
	errNotPaid       = httpx.NewError(http.StatusConflict, "ORDER_NOT_PAID", "the order is not paid: accept the payment at the till first")
	errNotPickup     = httpx.NewError(http.StatusUnprocessableEntity, "NOT_PICKUP_ORDER", "only pickup orders are issued in the restaurant")
	errInvalidNumber = httpx.NewError(http.StatusBadRequest, "INVALID_ORDER_NUMBER", "order number must look like R1-261021-0007")
	numberRe         = regexp.MustCompile(`^R[0-9]{1,9}-[0-9]{6}-[0-9]{4,9}$`)
)

// staffOrder читает заказ и проверяет, что актор — персонал этого ресторана; иначе 404 (существование не раскрываем).
// lock=true — FOR UPDATE (внутри tx).
func (s *Service) staffOrder(ctx context.Context, db store.DBTX, a Actor, id string, lock bool) (domain.Order, error) {
	o, err := s.st.GetOrder(ctx, db, id, lock)
	if errors.Is(err, store.ErrNotFound) || (err == nil && !a.Can(o.RestaurantID)) {
		return domain.Order{}, errOrderNotFound
	}
	return o, err
}

// SearchOrder — searchOrder (UC-RCV-03): заказ по номеру, только своего ресторана.
func (s *Service) SearchOrder(ctx context.Context, a Actor, number string) (domain.Order, error) {
	if err := a.requireStaff(); err != nil {
		return domain.Order{}, err
	}
	number = strings.ToUpper(strings.TrimSpace(number))
	if number == "" {
		return domain.Order{}, badParam("number is required")
	}
	if !numberRe.MatchString(number) {
		return domain.Order{}, errInvalidNumber
	}
	o, err := s.st.GetOrderByNumber(ctx, s.pool, number)
	if errors.Is(err, store.ErrNotFound) || (err == nil && !a.Can(o.RestaurantID)) {
		return domain.Order{}, errOrderNotFound
	}
	return o, err
}

// IssueOrder — markOrderIssued (UC-RCV-01/03): ready → handed_over → closed одной транзакцией.
// Условия: самовывоз, статус ready, оплата принята. Повтор для уже выданного заказа — без эффекта.
func (s *Service) IssueOrder(ctx context.Context, a Actor, id string) (domain.Order, error) {
	if err := a.requireStaff(); err != nil {
		return domain.Order{}, err
	}
	if !validUUID(id) {
		return domain.Order{}, errOrderNotFound
	}
	var out domain.Order
	var steps []domain.Status
	err := pgxutil.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		steps = steps[:0]
		o, err := s.staffOrder(ctx, tx, a, id, true)
		if err != nil {
			return err
		}
		if o.Status == domain.StatusClosed || o.Status == domain.StatusHandedOver {
			if _, issued, err := s.st.StatusTime(ctx, tx, o.ID, domain.StatusHandedOver); err != nil || !issued {
				if err != nil {
					return err
				}
				return orderStateConflict(o.Status, "issue")
			}
			if o.Status == domain.StatusClosed { // повторный запрос: уже выдан
				out = o
				return nil
			}
			// handed_over без closed — довести до конца (прерванный прежний вызов невозможен, но страхуемся)
		} else {
			if o.Fulfillment != domain.FulfillmentPickup {
				return errNotPickup
			}
			if o.Status != domain.StatusReady {
				return orderStateConflict(o.Status, "issue")
			}
			if o.PaymentStatus != domain.PaymentPaid {
				return errNotPaid
			}
			if o, _, err = s.transitionTx(ctx, tx, o.ID, domain.StatusHandedOver, ReasonIssued, a.label()); err != nil {
				return err
			}
			steps = append(steps, domain.StatusHandedOver)
		}
		if o, _, err = s.transitionTx(ctx, tx, o.ID, domain.StatusClosed, ReasonIssued, a.label()); err != nil {
			return err
		}
		steps = append(steps, domain.StatusClosed)
		out = o
		return nil
	})
	if err != nil {
		return domain.Order{}, err
	}
	if len(steps) > 0 {
		s.wake()
		for _, st := range steps {
			s.noteTransition(ctx, out, st, ReasonIssued, a.label())
		}
	}
	return out, nil
}

// CancelOrder — cancelOrder: отмена персоналом в pending_payment или accepted (до начала приготовления).
// Оплаченный заказ сразу помечается refundStatus=pending; сам возврат делает payment-service по orders.status.
// Повтор для уже отменённого заказа — без эффекта.
func (s *Service) CancelOrder(ctx context.Context, a Actor, id, reason string) (domain.Order, error) {
	if err := a.requireStaff(); err != nil {
		return domain.Order{}, err
	}
	if !slices.Contains(domain.CancelReasons, reason) {
		return domain.Order{}, badParam("reason must be one of " + strings.Join(domain.CancelReasons, ", "))
	}
	if !validUUID(id) {
		return domain.Order{}, errOrderNotFound
	}
	var out domain.Order
	var changed bool
	err := pgxutil.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		o, err := s.staffOrder(ctx, tx, a, id, true)
		if err != nil {
			return err
		}
		if o.Status == domain.StatusCancelled {
			out, changed = o, false
			return nil
		}
		if o.Status != domain.StatusPendingPayment && o.Status != domain.StatusAccepted {
			return orderStateConflict(o.Status, "cancel")
		}
		if o, changed, err = s.transitionTx(ctx, tx, o.ID, domain.StatusCancelled, reason, a.label()); err != nil {
			return err
		}
		if o.PaymentStatus == domain.PaymentPaid {
			if _, err := s.st.SetRefundStatus(ctx, tx, o.ID, domain.RefundPending, s.now()); err != nil {
				return err
			}
			o.RefundStatus = domain.RefundPending
		}
		out = o
		return nil
	})
	if err != nil {
		return domain.Order{}, err
	}
	if changed {
		s.wake()
		s.noteTransition(ctx, out, domain.StatusCancelled, reason, a.label())
	}
	return out, nil
}

func orderStateConflict(st domain.Status, action string) error {
	return httpx.NewError(http.StatusConflict, "ORDER_STATE_CONFLICT", fmt.Sprintf("cannot %s an order in status %s", action, st))
}

// ExpireNotReceived переводит готовые, но не забранные заказы самовывоза в not_received (reason=pickup_timeout).
// Возвращает число переведённых. Статус перепроверяется под блокировкой: выдача и таймер не конфликтуют.
func (s *Service) ExpireNotReceived(ctx context.Context) (int, error) {
	ids, err := s.st.ReadyPickupBefore(ctx, s.pool, s.now().Add(-s.cfg.NotReceivedAfter), 100)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, id := range ids {
		var o domain.Order
		var changed bool
		err := pgxutil.InTx(ctx, s.pool, func(tx pgx.Tx) error {
			cur, err := s.st.GetOrder(ctx, tx, id, true)
			if err != nil {
				return err
			}
			if cur.Status != domain.StatusReady || cur.Fulfillment != domain.FulfillmentPickup {
				return nil // успели выдать
			}
			o, changed, err = s.transitionTx(ctx, tx, id, domain.StatusNotReceived, ReasonPickupTimeout, actorSystem)
			return err
		})
		if err != nil {
			return n, err
		}
		if changed {
			s.wake()
			s.noteTransition(ctx, o, domain.StatusNotReceived, ReasonPickupTimeout, actorSystem)
			n++
		}
	}
	return n, nil
}

func validUUID(id string) bool {
	if len(id) != 36 {
		return false
	}
	for i, c := range id {
		switch {
		case i == 8 || i == 13 || i == 18 || i == 23:
			if c != '-' {
				return false
			}
		case !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'):
			return false
		}
	}
	return true
}
