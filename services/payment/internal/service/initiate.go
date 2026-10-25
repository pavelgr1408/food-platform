package service

import (
	"context"
	"errors"
	"net/url"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/pavelgr1408/food-platform/pkg/pgxutil"
	"github.com/pavelgr1408/food-platform/services/payment/internal/domain"
	"github.com/pavelgr1408/food-platform/services/payment/internal/gateway"
	"github.com/pavelgr1408/food-platform/services/payment/internal/store"
)

// loadOrder читает реплику заказа и проверяет доступ; чужой/неизвестный — 404.
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

// payable — можно ли сейчас оплатить заказ онлайн (статус "" — orders.status ещё не дошёл: заказ только создан).
func payable(o domain.Order, orderStatus string) bool {
	return o.Method == domain.MethodOnlineCard && (orderStatus == "" || orderStatus == domain.OrderPendingPayment)
}

// posPayable — можно ли сейчас провести заказ через кассу.
func posPayable(o domain.Order, orderStatus string) bool {
	return o.Method == domain.MethodPOS && domain.PosPayable(orderStatus)
}

// Methods — getPaymentMethods (UC-PAY-01).
func (s *Service) Methods(ctx context.Context, userID, orderID string) ([]domain.MethodView, error) {
	o, err := s.loadOrder(ctx, userID, orderID)
	if err != nil {
		return nil, err
	}
	st, err := s.st.OrderStatus(ctx, s.pool, orderID)
	if err != nil {
		return nil, err
	}
	live := true
	ps, err := s.st.OrderPayments(ctx, s.pool, orderID)
	if err != nil {
		return nil, err
	}
	for _, p := range ps {
		if p.Status == domain.StatusPaid {
			live = false
		}
	}
	return domain.MethodsFor(o.Fulfillment, o.Method, live && payable(o, st)), nil
}

// Get — getPayment: платёж заказа из БД.
func (s *Service) Get(ctx context.Context, userID, orderID, paymentID string) (domain.Payment, error) {
	if _, err := s.loadOrder(ctx, userID, orderID); err != nil {
		return domain.Payment{}, err
	}
	return s.getPaymentOf(ctx, orderID, paymentID)
}

// getPaymentOf читает платёж заказа (доступ к заказу уже проверен).
func (s *Service) getPaymentOf(ctx context.Context, orderID, paymentID string) (domain.Payment, error) {
	if _, err := uuid.Parse(paymentID); err != nil {
		return domain.Payment{}, errPaymentNotFound
	}
	p, err := s.st.GetPayment(ctx, s.pool, paymentID, false)
	if errors.Is(err, store.ErrNotFound) || (err == nil && p.OrderID != orderID) {
		return domain.Payment{}, errPaymentNotFound
	}
	return p, err
}

// InitiateInput — тело initiatePayment.
type InitiateInput struct {
	Method    domain.Method
	ReturnURL string
}

// Initiate — initiatePayment (UC-PAY-02): запись попытки → вызов шлюза вне транзакции → итог.
// Возвращённый платёж имеет статус pending (201) или unknown (202: шлюз не ответил в срок).
// Синхронный отказ шлюза — ошибка 402 PAYMENT_DECLINED.
func (s *Service) Initiate(ctx context.Context, userID, orderID string, in InitiateInput) (domain.Payment, error) {
	ord, err := s.loadOrder(ctx, userID, orderID)
	if err != nil {
		return domain.Payment{}, err
	}
	if in.Method != ord.Method {
		return domain.Payment{}, errNotAllowed
	}
	if in.Method != domain.MethodOnlineCard {
		return domain.Payment{}, errNotSupported
	}
	ret := in.ReturnURL
	if ret == "" {
		ret = s.cfg.DefaultReturnURL
	}
	if u, err := url.Parse(ret); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return domain.Payment{}, errBadParam("returnUrl must be an absolute http(s) URL")
	}

	p, err := s.startAttempt(ctx, orderID, payable, "", ret)
	if err != nil {
		return domain.Payment{}, err
	}
	return s.callGateway(ctx, p, ord.Number)
}

// startAttempt записывает новую попытку оплаты заказа (status=pending) и событие в outbox — одной транзакцией
// под блокировкой заказа. allowed решает, можно ли платить при текущем статусе заказа. Вызов шлюза — на вызывающем.
func (s *Service) startAttempt(ctx context.Context, orderID string, allowed func(domain.Order, string) bool,
	tender domain.Tender, returnURL string) (domain.Payment, error) {
	var p domain.Payment
	err := pgxutil.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		o, err := s.st.GetOrder(ctx, tx, orderID, true) // сериализует попытки одного заказа
		if err != nil {
			return err
		}
		st, err := s.st.OrderStatus(ctx, tx, orderID)
		if err != nil {
			return err
		}
		list, err := s.st.OrderPayments(ctx, tx, orderID)
		if err != nil {
			return err
		}
		attempt := 1
		for _, x := range list {
			attempt = x.Attempt + 1
			if x.Status == domain.StatusPaid {
				return &ConflictError{"ALREADY_PAID", "the order is already paid", x.ID}
			}
		}
		if !allowed(o, st) {
			return errNotPayable
		}
		for _, x := range list {
			switch x.Status {
			case domain.StatusUnknown:
				return &ConflictError{"PAYMENT_STATUS_UNKNOWN", "the status of the previous attempt is unknown: wait for the gateway answer (getPayment) before paying again", x.ID}
			case domain.StatusPending, domain.StatusAuthorized:
				return &ConflictError{"PAYMENT_IN_PROGRESS", "the order has a payment in progress", x.ID}
			}
		}
		now := s.now()
		next := now.Add(s.cfg.WebhookWait)
		p = domain.Payment{ID: uuid.NewString(), OrderID: orderID, Attempt: attempt, Method: o.Method, Status: domain.StatusPending,
			Amount: o.Amount, Currency: o.Currency, ReturnURL: returnURL, Tender: tender, NextPollAt: &next, CreatedAt: now, UpdatedAt: now}
		if err := s.st.InsertPayment(ctx, tx, p); err != nil {
			if errors.Is(err, store.ErrConflict) {
				return &ConflictError{"PAYMENT_IN_PROGRESS", "the order has a payment in progress", ""}
			}
			return err
		}
		return enqueueStatus(ctx, tx, p)
	})
	if err != nil {
		return domain.Payment{}, err
	}
	s.wake()
	s.m.Created.WithLabelValues(string(p.Method)).Inc()
	s.m.Transitions.WithLabelValues(string(domain.StatusPending)).Inc()
	return p, nil
}

// gwCreate вызывает шлюз и учитывает метрики/длительность.
func (s *Service) gwCreate(ctx context.Context, p domain.Payment, orderNumber string) (gateway.Created, error) {
	gw := s.gwFor(p.Method)
	if gw == nil {
		return gateway.Created{}, errors.New("gateway for " + string(p.Method) + " is not configured")
	}
	ctx, cancel := context.WithTimeout(ctx, s.cfg.GatewayTimeout)
	defer cancel()
	start := time.Now()
	res, err := gw.Create(ctx, gateway.CreateRequest{Key: p.ID, Reference: p.ID, OrderID: p.OrderID, Amount: p.Amount, Currency: p.Currency,
		ReturnURL: p.ReturnURL, Description: "Заказ " + orderNumber, Tender: string(p.Tender), Method: string(p.Method)})
	s.observeGateway("create", start, err)
	return res, err
}

func (s *Service) observeGateway(op string, start time.Time, err error) {
	res := "ok"
	switch {
	case err == nil:
	case gateway.IsRejected(err):
		res = "rejected"
	default:
		res = "error"
	}
	s.m.GatewayCalls.WithLabelValues(op, res).Inc()
	s.m.GatewayDuration.WithLabelValues(op).Observe(time.Since(start).Seconds())
}

// callGateway создаёт оплату у шлюза для записанной попытки и фиксирует исход. Выполняется
// независимо от отмены запроса клиента: бухгалтерия попытки должна быть доведена до конца.
func (s *Service) callGateway(ctx context.Context, p domain.Payment, orderNumber string) (domain.Payment, error) {
	ctx = context.WithoutCancel(ctx)
	res, err := s.gwCreate(ctx, p, orderNumber)
	switch {
	case err == nil:
		if err := pgxutil.InTx(ctx, s.pool, func(tx pgx.Tx) error {
			return s.st.SetGatewayOperation(ctx, tx, p.ID, res.Ref, res.URL, s.now())
		}); err != nil {
			// операция у шлюза создана, но записать ссылку не удалось: reconciler повторит Create с тем же ключом
			s.log.ErrorContext(ctx, "cannot store gateway operation", "payment_id", p.ID, "error", err)
			return s.markUnknown(ctx, p)
		}
		return s.st.GetPayment(ctx, s.pool, p.ID, false) // вебхук мог успеть раньше нас
	case gateway.IsRejected(err):
		p, _, aerr := s.applyByID(ctx, p.ID, domain.StatusDeclined, "gateway_rejected", "")
		if aerr != nil {
			return p, aerr
		}
		s.log.WarnContext(ctx, "gateway rejected the payment", "payment_id", p.ID, "error", err)
		return p, errDeclined
	default:
		s.log.WarnContext(ctx, "gateway call failed: payment status is unknown", "payment_id", p.ID, "error", err)
		return s.markUnknown(ctx, p)
	}
}

func (s *Service) markUnknown(ctx context.Context, p domain.Payment) (domain.Payment, error) {
	cur, _, err := s.applyByID(ctx, p.ID, domain.StatusUnknown, "", "")
	return cur, err
}
