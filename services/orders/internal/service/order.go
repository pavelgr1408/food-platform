package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/pavelgr1408/food-platform/pkg/httpx"
	"github.com/pavelgr1408/food-platform/pkg/pgxutil"
	"github.com/pavelgr1408/food-platform/services/orders/internal/clients"
	"github.com/pavelgr1408/food-platform/services/orders/internal/domain"
	"github.com/pavelgr1408/food-platform/services/orders/internal/store"
)

// CreateOrderInput — параметры createOrder.
type CreateOrderInput struct {
	CartID        string
	PaymentMethod domain.PaymentMethod
	Contact       domain.Contact
	Channel       string // "" | online | staff_fax
	Actor         Actor  // субъект запроса; для staff_fax должен быть персоналом ресторана корзины (S6)
}

// CommitFunc вызывается ВНУТРИ транзакции создания заказа: HTTP-слой сохраняет здесь
// ответ под Idempotency-Key (Claim.CompleteTx), чтобы заказ и ключ фиксировались атомарно.
type CommitFunc func(ctx context.Context, tx pgx.Tx, o domain.Order) error

const uniqueViolation = "23505"

// CreateOrder оформляет заказ из корзины (UC-ORD-01/03). Порядок: локальные проверки →
// ресторан открыт (E2) → повторная проверка позиций и цен в menu (E1) → ОДНА транзакция.
func (s *Service) CreateOrder(ctx context.Context, userID string, in CreateOrderInput, commit CommitFunc) (domain.Order, error) {
	if _, err := uuid.Parse(in.CartID); err != nil {
		return domain.Order{}, badParam("cartId must be a UUID")
	}
	fax := false
	switch in.Channel {
	case "", "online":
	case domain.ChannelStaffFax:
		if err := in.Actor.requireStaff(); err != nil {
			return domain.Order{}, err
		}
		fax = true
	default:
		return domain.Order{}, badParam("channel must be online or staff_fax")
	}
	reqContact, err := validateContact(in.Contact)
	if err != nil {
		return domain.Order{}, err
	}

	cart, err := s.loadCart(ctx, userID, in.CartID)
	if err != nil {
		return domain.Order{}, err
	}
	if cart.Status != domain.CartOpen {
		return domain.Order{}, errCartClosed
	}
	if fax && !in.Actor.Can(cart.RestaurantID) {
		return domain.Order{}, errForbidden
	}
	switch {
	case len(cart.Items) == 0:
		return domain.Order{}, unprocessable("CART_EMPTY", "the cart is empty")
	case !cart.Fulfillment.Valid():
		return domain.Order{}, unprocessable("FULFILLMENT_REQUIRED", "choose delivery or pickup first")
	case !in.PaymentMethod.Allowed(cart.Fulfillment):
		return domain.Order{}, unprocessable("PAYMENT_METHOD_NOT_ALLOWED",
			fmt.Sprintf("payment method %q is not allowed for %s", in.PaymentMethod, cart.Fulfillment))
	case fax && in.PaymentMethod.Prepaid():
		return domain.Order{}, unprocessable("PAYMENT_METHOD_NOT_ALLOWED", "a fax order cannot be paid online: the customer is not present (use pos or courier_card)")
	}
	contact := cart.Contact.Merge(reqContact)
	if (cart.OwnerUserID == "" || fax) && contact.Phone == "" {
		return domain.Order{}, unprocessable("CONTACT_REQUIRED", "a guest or fax order needs contact.phone")
	}

	// E2: ресторан должен быть открыт; доставка — по-прежнему включена.
	info, err := s.rest.Get(ctx, cart.RestaurantID)
	if err != nil {
		if errors.Is(err, clients.ErrNotFound) {
			return domain.Order{}, errRestNotFound
		}
		return domain.Order{}, s.upstreamErr("restaurant", err, errRestDown)
	}
	if !info.OpenNow {
		return domain.Order{}, httpx.NewError(409, "RESTAURANT_CLOSED", "the restaurant is closed now")
	}
	if cart.Fulfillment == domain.FulfillmentDelivery && !info.DeliveryEnabled {
		return domain.Order{}, unprocessable("DELIVERY_NOT_AVAILABLE", "the restaurant does not offer delivery now; choose pickup")
	}
	if cart.Fulfillment == domain.FulfillmentDelivery {
		// ресторан мог изменить радиус, а корзина — лежать со старым адресом: проверяем заново (S7)
		if err := checkZone(info, cart.Address); err != nil {
			return domain.Order{}, err
		}
	}

	// E1: актуальные позиции и цены. Внешний вызов — до транзакции.
	lines, err := s.revalidate(ctx, cart)
	if err != nil {
		return domain.Order{}, err
	}

	now := s.now()
	loc := time.UTC
	if info.Timezone != "" {
		if l, err := time.LoadLocation(info.Timezone); err == nil {
			loc = l
		}
	}
	sub := domain.Subtotal(lines)
	d := s.disc.Apply(cart.RestaurantID, lines, sub)
	discount, total := domain.Net(sub, d)
	order := domain.Order{
		ID: uuid.NewString(), CartID: cart.ID, RestaurantID: cart.RestaurantID, OwnerUserID: cart.OwnerUserID,
		Channel: "online", Status: domain.InitialStatus(in.PaymentMethod), PaymentMethod: in.PaymentMethod,
		PaymentStatus: domain.PaymentPending, Fulfillment: cart.Fulfillment, Address: cart.Address, Contact: contact,
		Currency: cart.Currency, Items: lines, Subtotal: sub, DiscountTotal: discount, Total: total,
		Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	actor := actorCustomer
	if fax {
		// заказ по факсу принадлежит клиенту-гостю (владельца нет); сотрудник фиксируется в журнале и created_by
		order.Channel, order.OwnerUserID, order.CreatedBy = domain.ChannelStaffFax, "", in.Actor.UserID
		actor = in.Actor.label()
	}

	rcode, err := s.st.RestaurantCode(ctx, s.pool, cart.RestaurantID)
	if err != nil {
		return domain.Order{}, err
	}
	err = pgxutil.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := s.st.GetCart(ctx, tx, cart.ID, true) // блокировка: параллельное оформление ждёт здесь
		if err != nil {
			return err
		}
		if cur.Status != domain.CartOpen {
			return errCartClosed
		}
		if cur.Revision != cart.Revision {
			return errCartChanged
		}
		if order.Number, err = s.st.NextNumber(ctx, tx, cart.RestaurantID, rcode, now.In(loc)); err != nil {
			return err
		}
		if err := s.st.InsertOrder(ctx, tx, order, actor); err != nil {
			return err
		}
		if err := s.st.CloseCart(ctx, tx, cart.ID, order.ID); err != nil {
			return err
		}
		// ADR-10: orders.state (revision 1) — его читает payment; status — для уведомлений и аудита.
		if err := enqueueState(ctx, tx, order); err != nil {
			return err
		}
		var eta *time.Time
		if order.Status == domain.StatusAccepted { // оплата не нужна → заказ принят сразу (ADR-10)
			order.AcceptedAt = &now
			e, err := s.estimate(ctx, tx, order)
			if err != nil {
				return err
			}
			eta = &e.ReadyAt
		}
		if err := enqueueStatus(ctx, tx, order, order.Status, "", eta, now); err != nil {
			return err
		}
		if commit != nil {
			return commit(ctx, tx, order)
		}
		return nil
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation && pgErr.ConstraintName == "orders_cart_id_key" {
			return domain.Order{}, errCartClosed
		}
		return domain.Order{}, err
	}
	s.wake()
	if s.m != nil {
		s.m.OrdersCreated.WithLabelValues(string(order.PaymentMethod), string(order.Fulfillment)).Inc()
		s.m.Transitions.WithLabelValues(string(order.Status)).Inc()
	}
	s.log.InfoContext(ctx, "order created", "order_id", order.ID, "order_number", order.Number,
		"restaurant_id", order.RestaurantID, "status", order.Status, "total", order.Total, "payment_method", order.PaymentMethod)
	return order, nil
}

// revalidate сверяет корзину с актуальным меню: недоступное → 409 ITEMS_UNAVAILABLE,
// изменившиеся цены → корзина обновляется и 409 PRICE_CHANGED. Возвращает строки со свежими названиями.
func (s *Service) revalidate(ctx context.Context, cart domain.Cart) ([]domain.Line, error) {
	in := make([]clients.MenuLineInput, len(cart.Items))
	for i, l := range cart.Items {
		in[i] = clients.MenuLineInput{MenuItemID: l.MenuItemID, Quantity: l.Quantity, ModifierIDs: l.ModifierIDs}
	}
	res, err := s.menu.Validate(ctx, cart.RestaurantID, in)
	if errors.Is(err, clients.ErrNotFound) {
		items := make([]Unavailable, 0, len(cart.Items))
		for _, l := range cart.Items {
			items = append(items, Unavailable{l.MenuItemID, "ITEM_NOT_FOUND", "Меню ресторана не найдено"})
		}
		return nil, &ItemsUnavailableError{Items: items}
	}
	if err != nil {
		return nil, s.upstreamErr("menu", err, errMenuDown)
	}
	var unavailable []Unavailable
	fresh := make([]domain.Line, len(res.Lines))
	changed := res.Currency != "" && res.Currency != cart.Currency
	for i, l := range res.Lines {
		if !l.Available {
			unavailable = append(unavailable, Unavailable{l.MenuItemID, l.ReasonCode, l.Reason})
			continue
		}
		old := cart.Items[i]
		if l.UnitPrice != old.UnitPrice || l.LineTotal != old.LineTotal {
			changed = true
		}
		fresh[i] = domain.Line{MenuItemID: l.MenuItemID, Name: l.Name, Quantity: l.Quantity,
			ModifierIDs: nonNil(old.ModifierIDs), UnitPrice: l.UnitPrice, LineTotal: l.LineTotal}
	}
	if len(unavailable) > 0 {
		return nil, &ItemsUnavailableError{Items: unavailable}
	}
	if changed {
		oldTotal := cart.Subtotal()
		cur := res.Currency
		if cur == "" {
			cur = cart.Currency
		}
		// Обновляем цены в корзине, чтобы клиент увидел и подтвердил новую сумму. Если корзину
		// успели изменить — не затираем чужую правку.
		err := pgxutil.InTx(ctx, s.pool, func(tx pgx.Tx) error {
			c, err := s.st.GetCart(ctx, tx, cart.ID, true)
			if err != nil {
				return err
			}
			if c.Status != domain.CartOpen || c.Revision != cart.Revision {
				return nil
			}
			_, err = s.st.ReplaceCartItems(ctx, tx, cart.ID, cur, fresh)
			return err
		})
		if err != nil {
			return nil, err
		}
		return nil, httpx.NewError(409, "PRICE_CHANGED",
			fmt.Sprintf("prices changed: cart total was %d, now %d (%s); review the cart and confirm", oldTotal, domain.Subtotal(fresh), cur))
	}
	return fresh, nil
}

// loadOrder читает заказ и проверяет доступ; чужой — 404.
func (s *Service) loadOrder(ctx context.Context, userID, id string) (domain.Order, error) {
	if _, err := uuid.Parse(id); err != nil {
		return domain.Order{}, errOrderNotFound
	}
	o, err := s.st.GetOrder(ctx, s.pool, id, false)
	if errors.Is(err, store.ErrNotFound) || (err == nil && !domain.CanBeAccessedBy(o.OwnerUserID, userID)) {
		return domain.Order{}, errOrderNotFound
	}
	return o, err
}

// GetOrder возвращает заказ.
func (s *Service) GetOrder(ctx context.Context, userID, id string) (domain.Order, error) {
	return s.loadOrder(ctx, userID, id)
}

// Eta — оценка времени получения.
type Eta struct {
	ReadyAt         time.Time
	DeliveryAt      *time.Time
	EstimateMinutes int
	Approximate     bool
}

// GetEta возвращает оценку времени получения (S4): по свежей реплике production.lead.time,
// иначе — типовое время с approximate=true (E1 UC-ORD-05); синхронных вызовов производства нет.
func (s *Service) GetEta(ctx context.Context, userID, id string) (Eta, error) {
	o, err := s.loadOrder(ctx, userID, id)
	if err != nil {
		return Eta{}, err
	}
	return s.estimate(ctx, s.pool, o)
}

// estimate считает ETA заказа:
//
//	готов (ready и дальше)        → момент готовности из журнала, estimate=0, approximate=false;
//	свежая реплика лид-тайма      → (accepted_at | max(created_at, now)) + leadTime, approximate=false;
//	реплики нет / она устарела    → то же с типовым ETA_PREP_MINUTES, approximate=true.
//
// Для самовывоза со слотом готовность не раньше начала слота; для доставки поверх готовности добавляется
// типовое ETA_DELIVERY_MINUTES (своей оценки доставки до S7 нет → approximate=true).
func (s *Service) estimate(ctx context.Context, db store.DBTX, o domain.Order) (Eta, error) {
	now := s.now()
	var e Eta
	if o.Status == domain.StatusReady || o.Status == domain.StatusHandedOver || o.Status == domain.StatusOnDelivery ||
		o.Status == domain.StatusDelivered || o.Status == domain.StatusClosed {
		at, found, err := s.st.StatusTime(ctx, db, o.ID, domain.StatusReady)
		if err != nil {
			return Eta{}, err
		}
		if found {
			e.ReadyAt = at
		}
	}
	if e.ReadyAt.IsZero() {
		base := o.CreatedAt
		switch {
		case o.AcceptedAt != nil:
			base = *o.AcceptedAt
		case now.After(base):
			base = now
		}
		prep, approximate := time.Duration(s.cfg.PrepMinutes)*time.Minute, true
		lt, found, err := s.st.GetLeadTime(ctx, db, o.RestaurantID)
		if err != nil {
			return Eta{}, err
		}
		if found && now.Sub(lt.MeasuredAt) <= s.cfg.LeadTimeTTL {
			prep, approximate = time.Duration(lt.Minutes)*time.Minute, false
		}
		e.ReadyAt, e.Approximate = base.Add(prep), approximate
		if o.PickupFrom != nil && o.PickupFrom.After(e.ReadyAt) {
			e.ReadyAt = *o.PickupFrom
		}
	}
	target := e.ReadyAt
	if o.Fulfillment == domain.FulfillmentDelivery {
		d, typical, err := s.deliveryAt(ctx, db, o, e.ReadyAt, now)
		if err != nil {
			return Eta{}, err
		}
		if d != nil {
			e.DeliveryAt, target = d, *d
		}
		if typical {
			e.Approximate = true // оценки курьерской системы нет: типовое время доставки
		}
	}
	if mins := int((target.Sub(now) + time.Minute - 1) / time.Minute); mins > 0 {
		e.EstimateMinutes = mins
	}
	return e, nil
}

// deliveryEtaFresh — есть ли свежая оценка вручения от курьерской системы: доставка не завершена, оценка в
// будущем, снимок обновлялся не позже DeliveryEtaTTL назад.
func (s *Service) deliveryEtaFresh(o domain.Order, now time.Time) bool {
	d := o.Delivery
	if d == nil || d.EtaAt == nil || d.Status == domain.DeliveryDelivered || d.Status == domain.DeliveryFailed {
		return false
	}
	return d.EtaAt.After(now) && now.Sub(d.UpdatedAt) <= s.cfg.DeliveryEtaTTL
}

// deliveryAt — время вручения (S7); typical=true — взято типовое время, а не оценка:
//
//	доставлен                        → фактический момент delivered (по журналу);
//	доставка не состоится (failed)   → nil: клиенту предложено забрать заказ (E2) либо заказ не доставлен;
//	есть свежая оценка курьерской    → она, но не раньше готовности заказа;
//	иначе                            → готовность + ETA_DELIVERY_MINUTES (typical).
func (s *Service) deliveryAt(ctx context.Context, db store.DBTX, o domain.Order, ready, now time.Time) (at *time.Time, typical bool, err error) {
	switch {
	case o.Delivery != nil && o.Delivery.Status == domain.DeliveryFailed:
		return nil, false, nil
	case o.Status == domain.StatusDelivered || o.Status == domain.StatusClosed:
		if t, found, err := s.st.StatusTime(ctx, db, o.ID, domain.StatusDelivered); err != nil || found {
			return &t, false, err
		}
	case s.deliveryEtaFresh(o, now):
		d := *o.Delivery.EtaAt
		if d.Before(ready) {
			d = ready
		}
		return &d, false, nil
	}
	d := ready.Add(time.Duration(s.cfg.DeliveryMinutes) * time.Minute)
	return &d, true, nil
}

// SetPickupSlot сохраняет желаемый интервал получения (UC-ORD-05 A1) и публикует
// orders.state с revision+1 (ADR-10).
func (s *Service) SetPickupSlot(ctx context.Context, userID, id string, from, to time.Time) (domain.Order, error) {
	if _, err := s.loadOrder(ctx, userID, id); err != nil {
		return domain.Order{}, err
	}
	now := s.now()
	if msg := domain.ValidateSlot(from, to, now); msg != "" {
		return domain.Order{}, unprocessable("INVALID_SLOT", msg)
	}
	var out domain.Order
	err := pgxutil.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		o, err := s.st.GetOrder(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if o.Fulfillment != domain.FulfillmentPickup {
			return unprocessable("INVALID_SLOT", "a pickup slot can be set only for pickup orders")
		}
		if o.Status != domain.StatusPendingPayment && o.Status != domain.StatusAccepted {
			return httpx.NewError(409, "ORDER_STATE_CONFLICT", fmt.Sprintf("the slot cannot be changed in status %s", o.Status))
		}
		rev, err := s.st.SetPickupSlot(ctx, tx, id, from.UTC(), to.UTC(), now)
		if err != nil {
			return err
		}
		o.PickupFrom, o.PickupTo, o.Revision, o.UpdatedAt = ptr(from.UTC()), ptr(to.UTC()), rev, now
		if err := enqueueState(ctx, tx, o); err != nil {
			return err
		}
		out = o
		return nil
	})
	if err != nil {
		return domain.Order{}, err
	}
	s.wake()
	return out, nil
}

func ptr[T any](v T) *T { return &v }

// Transition переводит заказ в статус to по таблице переходов (domain). Идемпотентен:
// если заказ уже в to — changed=false без побочных эффектов (консьюмеры S3/S4 повторяют события).
// Запрещённый переход → ErrInvalidTransition. Пишет историю и orders.status в той же транзакции.
func (s *Service) Transition(ctx context.Context, orderID string, to domain.Status, reason, actor string) (o domain.Order, changed bool, err error) {
	err = pgxutil.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		o, changed, err = s.transitionTx(ctx, tx, orderID, to, reason, actor)
		return err
	})
	if err != nil {
		return domain.Order{}, false, err
	}
	if changed {
		s.wake()
		s.noteTransition(ctx, o, to, reason, actor)
	}
	return o, changed, nil
}

// transitionTx — Transition внутри чужой транзакции (консьюмеры: эффект и запись inbox атомарны).
// Побочные эффекты после коммита (wake, метрики, лог) выполняет вызывающий через noteTransition.
func (s *Service) transitionTx(ctx context.Context, tx pgx.Tx, orderID string, to domain.Status, reason, actor string) (o domain.Order, changed bool, err error) {
	cur, err := s.st.GetOrder(ctx, tx, orderID, true)
	if err != nil {
		return domain.Order{}, false, err
	}
	o = cur
	if cur.Status == to {
		return o, false, nil
	}
	if !domain.CanTransition(cur.Status, to) {
		return domain.Order{}, false, fmt.Errorf("%w: %s → %s", ErrInvalidTransition, cur.Status, to)
	}
	at := s.now()
	if err := s.st.SetStatus(ctx, tx, cur, to, reason, actor, at); err != nil {
		return domain.Order{}, false, err
	}
	o.Status, o.StatusReason, o.UpdatedAt = to, reason, at
	if to == domain.StatusAccepted {
		o.AcceptedAt = &at
	}
	var eta *time.Time
	if to == domain.StatusAccepted || to == domain.StatusCooking {
		// ожидаемая готовность — в orders.status, чтобы клиент видел её без отдельного запроса (S4)
		e, err := s.estimate(ctx, tx, o)
		if err != nil {
			return domain.Order{}, false, err
		}
		eta = &e.ReadyAt
	}
	return o, true, enqueueStatus(ctx, tx, o, to, reason, eta, at)
}

func (s *Service) noteTransition(ctx context.Context, o domain.Order, to domain.Status, reason, actor string) {
	if s.m != nil {
		s.m.Transitions.WithLabelValues(string(to)).Inc()
	}
	s.log.InfoContext(ctx, "order status changed", "order_id", o.ID, "status", to, "reason", reason, "actor", actor)
}
