package service_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/pavelgr1408/food-platform/pkg/httpx"
	"github.com/pavelgr1408/food-platform/pkg/kafka"
	"github.com/pavelgr1408/food-platform/pkg/pgxutil"
	"github.com/pavelgr1408/food-platform/services/payment/internal/domain"
	"github.com/pavelgr1408/food-platform/services/payment/internal/gateway"
	"github.com/pavelgr1408/food-platform/services/payment/internal/service"
	"github.com/pavelgr1408/food-platform/services/payment/internal/testkit"
)

var ctx = context.Background()

// counter читает значение счётчика Prometheus.
func counter(c prometheus.Metric) float64 {
	var d dto.Metric
	if err := c.Write(&d); err != nil {
		panic(err)
	}
	return d.GetCounter().GetValue()
}

type env struct {
	svc   *service.Service
	pool  *pgxpool.Pool
	gw    *testkit.FakeGateway
	clock *testkit.Clock
	m     *service.Metrics
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{pool: testkit.Pool(t), gw: testkit.NewGateway(), clock: testkit.NewClock(), m: service.NewMetrics()}
	e.svc = service.New(service.Deps{Pool: e.pool, Gateway: e.gw, Log: testkit.Log(), Clock: e.clock.Now, Metrics: e.m,
		Config: service.Config{WebhookWait: 10 * time.Second, PendingExpiry: 10 * time.Minute, PollMin: 5 * time.Second, PollMax: time.Minute}})
	return e
}

// order создаёт реплику заказа через настоящий консьюмерский обработчик (orders.state).
func (e *env) order(t *testing.T, method, owner string, amount int64) string {
	t.Helper()
	id := uuid.NewString()
	e.state(t, id, 1, method, owner, amount, "pickup")
	return id
}

func (e *env) state(t *testing.T, id string, rev int, method, owner string, amount int64, fulfillment string) {
	t.Helper()
	body := map[string]any{"orderId": id, "revision": rev, "orderNumber": "R1-261019-0001", "restaurantId": "r1",
		"fulfillmentType": fulfillment, "customer": map[string]any{"userId": owner, "guest": owner == ""},
		"totalAmount": map[string]any{"amount": amount, "currency": "RUB"}, "paymentMethod": method, "paymentStatus": "pending"}
	e.consume(t, service.TopicState, body)
}

func (e *env) status(t *testing.T, id, status string, at time.Time) {
	t.Helper()
	e.consume(t, service.TopicStatus, map[string]any{"orderId": id, "orderNumber": "R1-261019-0001", "status": status, "updatedAt": at})
}

func (e *env) consume(t *testing.T, topic string, body any) {
	t.Helper()
	b, _ := json.Marshal(body)
	ev := kafka.Event{ID: uuid.New(), Topic: topic, Key: "k", Payload: b}
	if _, err := kafka.ProcessOnce(ctx, e.pool, "payment-service", ev, e.svc.HandleEvent); err != nil {
		t.Fatalf("consume %s: %v", topic, err)
	}
}

func code(t *testing.T, err error) string {
	t.Helper()
	var he *httpx.Error
	var ce *service.ConflictError
	switch {
	case errors.As(err, &he):
		return he.Code
	case errors.As(err, &ce):
		return ce.Code
	}
	t.Fatalf("unexpected error type %T: %v", err, err)
	return ""
}

func wantCode(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("want %s, got nil", want)
	}
	if got := code(t, err); got != want {
		t.Fatalf("want %s, got %s (%v)", want, got, err)
	}
}

// events возвращает payload'ы orders.payments.status по порядку.
func (e *env) events(t *testing.T, orderID string) []service.PaymentStatusEvent {
	t.Helper()
	rows, err := e.pool.Query(ctx, `SELECT payload FROM outbox WHERE topic = 'orders.payments.status' AND key = $1 ORDER BY id`, orderID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []service.PaymentStatusEvent
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			t.Fatal(err)
		}
		var ev service.PaymentStatusEvent
		if err := json.Unmarshal(b, &ev); err != nil {
			t.Fatal(err)
		}
		out = append(out, ev)
	}
	return out
}

func statuses(evs []service.PaymentStatusEvent) []string {
	var s []string
	for _, e := range evs {
		s = append(s, e.Status)
	}
	return s
}

func eq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (e *env) initiate(t *testing.T, user, order string) domain.Payment {
	t.Helper()
	p, err := e.svc.Initiate(ctx, user, order, service.InitiateInput{Method: domain.MethodOnlineCard})
	if err != nil {
		t.Fatalf("initiate: %v", err)
	}
	return p
}

func (e *env) callback(order string, p domain.Payment, status string, amount int64) service.Callback {
	a := amount
	return service.Callback{EventID: uuid.NewString(), OrderID: order, PaymentID: p.ID, Status: status, ExternalRef: p.GatewayRef, Amount: &a, Currency: "RUB"}
}

func TestInitiateCreatesPendingPaymentAndOneGatewayOperation(t *testing.T) {
	e := newEnv(t)
	o := e.order(t, "online", "", 54990)
	p := e.initiate(t, "", o)
	if p.Status != domain.StatusPending || p.Attempt != 1 || p.Amount != 54990 || p.GatewayRef == "" || p.PaymentURL == "" {
		t.Fatalf("%+v", p)
	}
	if e.gw.Operations() != 1 {
		t.Fatalf("operations=%d", e.gw.Operations())
	}
	if got := statuses(e.events(t, o)); !eq(got, []string{"pending"}) {
		t.Fatalf("events: %v", got)
	}
	ev := e.events(t, o)[0]
	if ev.Method != "online" || ev.Amount.Amount != 54990 || ev.Attempt != 1 || ev.PaymentID != p.ID {
		t.Fatalf("event: %+v", ev)
	}
}

func TestInitiateRules(t *testing.T) {
	e := newEnv(t)
	unknownOrder := uuid.NewString()
	_, err := e.svc.Initiate(ctx, "", unknownOrder, service.InitiateInput{Method: domain.MethodOnlineCard})
	wantCode(t, err, "ORDER_NOT_FOUND")
	if !service.IsOrderNotFound(err) {
		t.Fatal("IsOrderNotFound")
	}

	mine := e.order(t, "online", "u1", 1000)
	_, err = e.svc.Initiate(ctx, "u2", mine, service.InitiateInput{Method: domain.MethodOnlineCard})
	wantCode(t, err, "ORDER_NOT_FOUND") // чужой заказ не раскрываем

	_, err = e.svc.Initiate(ctx, "u1", mine, service.InitiateInput{Method: domain.MethodPOS})
	wantCode(t, err, "PAYMENT_METHOD_NOT_ALLOWED")

	pos := e.order(t, "restaurant", "", 1000)
	_, err = e.svc.Initiate(ctx, "", pos, service.InitiateInput{Method: domain.MethodPOS})
	wantCode(t, err, "PAYMENT_METHOD_NOT_SUPPORTED")

	_, err = e.svc.Initiate(ctx, "u1", mine, service.InitiateInput{Method: domain.MethodOnlineCard, ReturnURL: "javascript:alert(1)"})
	wantCode(t, err, "INVALID_PARAMETER")

	e.status(t, mine, "cancelled", e.clock.Now())
	_, err = e.svc.Initiate(ctx, "u1", mine, service.InitiateInput{Method: domain.MethodOnlineCard})
	wantCode(t, err, "ORDER_NOT_PAYABLE")
	if e.gw.Calls.Create != 0 {
		t.Fatal("the gateway must not be called for rejected requests")
	}
}

func TestSecondAttemptWhilePendingIsBlocked(t *testing.T) {
	e := newEnv(t)
	o := e.order(t, "online", "", 1000)
	first := e.initiate(t, "", o)
	_, err := e.svc.Initiate(ctx, "", o, service.InitiateInput{Method: domain.MethodOnlineCard})
	wantCode(t, err, "PAYMENT_IN_PROGRESS")
	var ce *service.ConflictError
	if !errors.As(err, &ce) || ce.PaymentID != first.ID {
		t.Fatalf("conflict must point to the current attempt: %+v", err)
	}
}

func TestDeclinedAllowsNewAttempt(t *testing.T) {
	e := newEnv(t)
	o := e.order(t, "online", "", 1000)
	p1 := e.initiate(t, "", o)
	if err := e.svc.HandleCallback(ctx, domain.MethodOnlineCard, e.callback(o, p1, "declined", 1000)); err != nil {
		t.Fatal(err)
	}
	p2 := e.initiate(t, "", o)
	if p2.Attempt != 2 || p2.ID == p1.ID || p2.Status != domain.StatusPending {
		t.Fatalf("%+v", p2)
	}
	if got := statuses(e.events(t, o)); !eq(got, []string{"pending", "declined", "pending"}) {
		t.Fatalf("events: %v", got)
	}
	if e.gw.Operations() != 2 {
		t.Fatalf("operations=%d", e.gw.Operations())
	}
}

func TestPaidIsFinalAndBlocksFurtherPayments(t *testing.T) {
	e := newEnv(t)
	o := e.order(t, "online", "", 1000)
	p := e.initiate(t, "", o)
	if err := e.svc.HandleCallback(ctx, domain.MethodOnlineCard, e.callback(o, p, "paid", 1000)); err != nil {
		t.Fatal(err)
	}
	_, err := e.svc.Initiate(ctx, "", o, service.InitiateInput{Method: domain.MethodOnlineCard})
	wantCode(t, err, "ALREADY_PAID")
	// противоречащее уведомление (declined после paid) не применяется
	if err := e.svc.HandleCallback(ctx, domain.MethodOnlineCard, e.callback(o, p, "declined", 1000)); err != nil {
		t.Fatal(err)
	}
	got, _ := e.svc.Get(ctx, "", o, p.ID)
	if got.Status != domain.StatusPaid || got.PaidAt == nil {
		t.Fatalf("%+v", got)
	}
	if counter(e.m.Conflicts.WithLabelValues("paid", "declined")) != 1 {
		t.Fatal("conflict must be counted")
	}
	if got := statuses(e.events(t, o)); !eq(got, []string{"pending", "paid"}) {
		t.Fatalf("events: %v", got)
	}
	methods, _ := e.svc.Methods(ctx, "", o)
	for _, m := range methods {
		if m.Enabled {
			t.Fatalf("nothing can be paid for a paid order: %+v", methods)
		}
	}
}

func TestWebhookIsIdempotentByEventID(t *testing.T) {
	e := newEnv(t)
	o := e.order(t, "online", "", 1000)
	p := e.initiate(t, "", o)
	cb := e.callback(o, p, "paid", 1000)
	for i := 0; i < 3; i++ {
		if err := e.svc.HandleCallback(ctx, domain.MethodOnlineCard, cb); err != nil {
			t.Fatal(err)
		}
	}
	// тот же итог с новым eventId (шлюз пересоздал уведомление) — тоже без эффекта
	if err := e.svc.HandleCallback(ctx, domain.MethodOnlineCard, e.callback(o, p, "paid", 1000)); err != nil {
		t.Fatal(err)
	}
	if got := statuses(e.events(t, o)); !eq(got, []string{"pending", "paid"}) {
		t.Fatalf("one paid event expected, got %v", got)
	}
	if counter(e.m.Webhooks.WithLabelValues("online_card", "duplicate")) != 2 {
		t.Fatal("duplicates must be counted")
	}
}

func TestWebhookRejections(t *testing.T) {
	e := newEnv(t)
	o := e.order(t, "online", "", 1000)
	p := e.initiate(t, "", o)

	bad := e.callback(o, p, "paid", 999)
	wantCode(t, e.svc.HandleCallback(ctx, domain.MethodOnlineCard, bad), "AMOUNT_MISMATCH")
	if counter(e.m.AmountMismatch) != 1 {
		t.Fatal("amount mismatch must be counted")
	}
	got, _ := e.svc.Get(ctx, "", o, p.ID)
	if got.Status != domain.StatusPending {
		t.Fatalf("a mismatching notification must not change the payment: %s", got.Status)
	}

	other := e.order(t, "online", "", 1000)
	wrongOrder := e.callback(other, p, "paid", 1000)
	wantCode(t, e.svc.HandleCallback(ctx, domain.MethodOnlineCard, wrongOrder), "PAYMENT_NOT_FOUND")
	wantCode(t, e.svc.HandleCallback(ctx, domain.MethodPOS, e.callback(o, p, "paid", 1000)), "PAYMENT_NOT_FOUND") // не тот канал
	unknown := e.callback(o, p, "paid", 1000)
	unknown.PaymentID = uuid.NewString()
	wantCode(t, e.svc.HandleCallback(ctx, domain.MethodOnlineCard, unknown), "PAYMENT_NOT_FOUND")

	badStatus := e.callback(o, p, "pending", 1000)
	wantCode(t, e.svc.HandleCallback(ctx, domain.MethodOnlineCard, badStatus), "INVALID_PARAMETER")
	badID := e.callback(o, p, "paid", 1000)
	badID.EventID = "nope"
	wantCode(t, e.svc.HandleCallback(ctx, domain.MethodOnlineCard, badID), "INVALID_PARAMETER")
}

func TestGatewayTimeoutMakesUnknownBlocksRetryAndReconcilerResolves(t *testing.T) {
	e := newEnv(t)
	o := e.order(t, "online", "", 54990)
	e.gw.LostReply = testkit.ErrTimeout // шлюз создал операцию, но ответ потерян
	p := e.initiate(t, "", o)
	if p.Status != domain.StatusUnknown {
		t.Fatalf("status=%s", p.Status)
	}
	if e.gw.Operations() != 1 {
		t.Fatal("the operation exists at the gateway")
	}
	// повторная оплата заблокирована
	_, err := e.svc.Initiate(ctx, "", o, service.InitiateInput{Method: domain.MethodOnlineCard})
	wantCode(t, err, "PAYMENT_STATUS_UNKNOWN")
	if e.gw.Operations() != 1 || e.gw.Calls.Create != 1 {
		t.Fatalf("a blocked attempt must not reach the gateway (ops=%d creates=%d)", e.gw.Operations(), e.gw.Calls.Create)
	}
	// пока не пора — reconciler ничего не трогает
	if n, _ := e.svc.Reconcile(ctx); n != 0 {
		t.Fatalf("not due yet, got %d", n)
	}
	// шлюз снова отвечает, пользователь оплатил
	e.gw.LostReply = nil
	e.gw.Finish(p.ID, gateway.StatePaid, "")
	e.clock.Advance(6 * time.Second)
	if n, err := e.svc.Reconcile(ctx); err != nil || n != 1 {
		t.Fatalf("reconcile: %d %v", n, err)
	}
	// первый проход: повторный Create с тем же ключом вернул уже созданную операцию → unknown стал pending
	got, _ := e.svc.Get(ctx, "", o, p.ID)
	if got.Status != domain.StatusPending || got.GatewayRef == "" {
		t.Fatalf("%+v", got)
	}
	e.clock.Advance(10 * time.Second)
	if n, err := e.svc.Reconcile(ctx); err != nil || n != 1 {
		t.Fatalf("reconcile: %d %v", n, err)
	}
	got, _ = e.svc.Get(ctx, "", o, p.ID)
	if got.Status != domain.StatusPaid {
		t.Fatalf("%+v", got)
	}
	if e.gw.Operations() != 1 {
		t.Fatalf("no second operation may be created at the gateway, got %d", e.gw.Operations())
	}
	if evs := statuses(e.events(t, o)); !eq(evs, []string{"pending", "unknown", "pending", "paid"}) {
		t.Fatalf("events: %v", evs)
	}
}

func TestUnknownStaysUnknownWhileGatewayIsDownWithBackoff(t *testing.T) {
	e := newEnv(t)
	o := e.order(t, "online", "", 1000)
	e.gw.CreateErr = testkit.ErrTimeout
	p := e.initiate(t, "", o)
	if p.Status != domain.StatusUnknown || p.GatewayRef != "" {
		t.Fatalf("%+v", p)
	}
	var last time.Time
	for i := 0; i < 3; i++ {
		e.clock.Advance(2 * time.Minute)
		if n, _ := e.svc.Reconcile(ctx); n != 1 {
			t.Fatalf("round %d: %d", i, n)
		}
		got, _ := e.svc.Get(ctx, "", o, p.ID)
		if got.Status != domain.StatusUnknown || got.NextPollAt == nil || !got.NextPollAt.After(last) {
			t.Fatalf("round %d: %+v", i, got)
		}
		last = *got.NextPollAt
	}
	got, _ := e.svc.Get(ctx, "", o, p.ID)
	if got.PollAttempts != 3 {
		t.Fatalf("poll attempts: %d", got.PollAttempts)
	}
	// шлюз ожил: Create с тем же ключом создаёт ровно одну операцию → pending → затем paid
	e.gw.CreateErr = nil
	e.clock.Advance(2 * time.Minute)
	if _, err := e.svc.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ = e.svc.Get(ctx, "", o, p.ID)
	if got.Status != domain.StatusPending || got.GatewayRef == "" || e.gw.Operations() != 1 {
		t.Fatalf("%+v ops=%d", got, e.gw.Operations())
	}
}

func TestGatewayRejectionDeclinesSynchronously(t *testing.T) {
	e := newEnv(t)
	o := e.order(t, "online", "", 1000)
	e.gw.CreateErr = &gateway.RejectedError{HTTPStatus: 422, Code: "bad_amount"}
	_, err := e.svc.Initiate(ctx, "", o, service.InitiateInput{Method: domain.MethodOnlineCard})
	wantCode(t, err, "PAYMENT_DECLINED")
	e.gw.CreateErr = nil
	p := e.initiate(t, "", o) // новая попытка разрешена
	if p.Attempt != 2 {
		t.Fatalf("%+v", p)
	}
	if got := statuses(e.events(t, o)); !eq(got, []string{"pending", "declined", "pending"}) {
		t.Fatalf("events: %v", got)
	}
}

func TestReconcilerPicksUpLostWebhookAndExpiresAbandoned(t *testing.T) {
	e := newEnv(t)
	// 1) вебхук потерян, шлюз знает итог
	o1 := e.order(t, "online", "", 1000)
	p1 := e.initiate(t, "", o1)
	e.gw.Finish(p1.ID, gateway.StatePaid, "")
	// 2) пользователь ушёл со страницы оплаты: у шлюза вечный pending
	o2 := e.order(t, "online", "", 2000)
	p2 := e.initiate(t, "", o2)

	e.clock.Advance(11 * time.Second)
	if n, _ := e.svc.Reconcile(ctx); n != 2 {
		t.Fatalf("due: %d", n)
	}
	if got, _ := e.svc.Get(ctx, "", o1, p1.ID); got.Status != domain.StatusPaid {
		t.Fatalf("lost webhook: %+v", got)
	}
	if got, _ := e.svc.Get(ctx, "", o2, p2.ID); got.Status != domain.StatusPending {
		t.Fatalf("not expired yet: %+v", got)
	}
	e.clock.Advance(11 * time.Minute)
	if _, err := e.svc.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := e.svc.Get(ctx, "", o2, p2.ID)
	if got.Status != domain.StatusDeclined || got.DeclineReason != "expired" || e.gw.Calls.Cancel != 1 {
		t.Fatalf("%+v cancels=%d", got, e.gw.Calls.Cancel)
	}
	// после истечения можно оплатить заново
	if p := e.initiate(t, "", o2); p.Attempt != 2 {
		t.Fatalf("%+v", p)
	}
}

func TestExpiryDoesNotLoseAPaymentMadeAtTheLastMoment(t *testing.T) {
	e := newEnv(t)
	o := e.order(t, "online", "", 1000)
	p := e.initiate(t, "", o)
	e.clock.Advance(11 * time.Minute)
	// Status ещё видит pending, но к моменту Cancel пользователь успел заплатить: шлюз отвечает paid
	e.gw.PaidOnCancel = true
	if _, err := e.svc.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.svc.Get(ctx, "", o, p.ID); got.Status != domain.StatusPaid {
		t.Fatalf("a paid operation must win over expiry: %+v", got)
	}
}

func TestPaidAfterCancelledOrderFlagsRefund(t *testing.T) {
	e := newEnv(t)
	o := e.order(t, "online", "", 1000)
	p := e.initiate(t, "", o)
	e.status(t, o, "cancelled", e.clock.Now()) // orders отменил заказ по таймауту
	if err := e.svc.HandleCallback(ctx, domain.MethodOnlineCard, e.callback(o, p, "paid", 1000)); err != nil {
		t.Fatal(err)
	}
	got, _ := e.svc.Get(ctx, "", o, p.ID)
	if got.Status != domain.StatusPaid || !got.RefundNeeded {
		t.Fatalf("money was received for a cancelled order: %+v", got)
	}
	if counter(e.m.PaidAfterCancel) != 1 {
		t.Fatal("must be counted")
	}
}

func TestParallelInitiateCreatesExactlyOnePayment(t *testing.T) {
	e := newEnv(t)
	o := e.order(t, "online", "", 1000)
	var ok, blocked, other atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := e.svc.Initiate(ctx, "", o, service.InitiateInput{Method: domain.MethodOnlineCard})
			var ce *service.ConflictError
			switch {
			case err == nil:
				ok.Add(1)
			case errors.As(err, &ce) && ce.Code == "PAYMENT_IN_PROGRESS":
				blocked.Add(1)
			default:
				other.Add(1)
				t.Errorf("unexpected: %v", err)
			}
		}()
	}
	wg.Wait()
	if ok.Load() != 1 || blocked.Load() != 23 || e.gw.Operations() != 1 || e.gw.Calls.Create != 1 {
		t.Fatalf("ok=%d blocked=%d other=%d ops=%d creates=%d", ok.Load(), blocked.Load(), other.Load(), e.gw.Operations(), e.gw.Calls.Create)
	}
}

func TestParallelWebhooksAndReconcilerApplyPaidOnce(t *testing.T) {
	e := newEnv(t)
	o := e.order(t, "online", "", 1000)
	p := e.initiate(t, "", o)
	e.gw.Finish(p.ID, gateway.StatePaid, "")
	e.clock.Advance(time.Minute)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = e.svc.HandleCallback(ctx, domain.MethodOnlineCard, e.callback(o, p, "paid", 1000))
		}()
	}
	wg.Add(1)
	go func() { defer wg.Done(); _, _ = e.svc.Reconcile(ctx) }()
	wg.Wait()
	if got := statuses(e.events(t, o)); !eq(got, []string{"pending", "paid"}) {
		t.Fatalf("exactly one paid event expected: %v", got)
	}
	var paid int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM payments WHERE order_id = $1 AND status = 'paid'`, o).Scan(&paid); err != nil || paid != 1 {
		t.Fatalf("paid rows=%d err=%v", paid, err)
	}
}

func TestDatabaseForbidsTwoLiveOrTwoPaidPayments(t *testing.T) {
	e := newEnv(t)
	o := e.order(t, "online", "", 1000)
	ins := func(attempt int, status string) error {
		_, err := e.pool.Exec(ctx, `INSERT INTO payments (id, order_id, attempt, method, status, amount, currency, created_at, updated_at)
			VALUES ($1,$2,$3,'online_card',$4,1000,'RUB',now(),now())`, uuid.NewString(), o, attempt, status)
		return err
	}
	if err := ins(1, "pending"); err != nil {
		t.Fatal(err)
	}
	if err := ins(2, "unknown"); err == nil {
		t.Fatal("two live attempts must be impossible at the database level")
	}
	if _, err := e.pool.Exec(ctx, `UPDATE payments SET status = 'paid' WHERE order_id = $1`, o); err != nil {
		t.Fatal(err)
	}
	if err := ins(3, "paid"); err == nil {
		t.Fatal("two paid payments must be impossible at the database level")
	}
	if err := ins(4, "declined"); err != nil {
		t.Fatalf("declined attempts do not conflict: %v", err)
	}
}

func TestMethodsReflectOrderState(t *testing.T) {
	e := newEnv(t)
	o := e.order(t, "online", "u1", 1000)
	ms, err := e.svc.Methods(ctx, "u1", o)
	if err != nil || len(ms) != 2 || ms[0].Type != domain.MethodOnlineCard || !ms[0].Enabled || ms[1].Enabled {
		t.Fatalf("%+v %v", ms, err)
	}
	if _, err := e.svc.Methods(ctx, "u2", o); err == nil {
		t.Fatal("someone else's order must be hidden")
	}
	e.status(t, o, "cancelled", e.clock.Now())
	ms, _ = e.svc.Methods(ctx, "u1", o)
	if ms[0].Enabled {
		t.Fatal("cancelled order is not payable")
	}
	del := uuid.NewString()
	e.state(t, del, 1, "courier", "", 1000, "delivery")
	ms, _ = e.svc.Methods(ctx, "", del)
	if ms[0].Type != domain.MethodOnlineCard || ms[1].Type != domain.MethodCourierCard || ms[0].Enabled {
		t.Fatalf("delivery with courier payment: %+v", ms)
	}
}

func TestGetPaymentChecksOwnership(t *testing.T) {
	e := newEnv(t)
	o := e.order(t, "online", "u1", 1000)
	p := e.initiate(t, "u1", o)
	if _, err := e.svc.Get(ctx, "u1", o, p.ID); err != nil {
		t.Fatal(err)
	}
	_, err := e.svc.Get(ctx, "u2", o, p.ID)
	wantCode(t, err, "ORDER_NOT_FOUND")
	other := e.order(t, "online", "u1", 1000)
	_, err = e.svc.Get(ctx, "u1", other, p.ID)
	wantCode(t, err, "PAYMENT_NOT_FOUND")
	_, err = e.svc.Get(ctx, "u1", o, "nope")
	wantCode(t, err, "PAYMENT_NOT_FOUND")
}

func TestConsumerIgnoresStaleRevisionsAndStatuses(t *testing.T) {
	e := newEnv(t)
	id := uuid.NewString()
	e.state(t, id, 2, "online", "", 5000, "pickup")
	e.state(t, id, 1, "online", "", 1, "pickup") // устаревшая ревизия
	e.state(t, id, 3, "online", "", 5000, "pickup")
	var amount int64
	var rev int
	if err := e.pool.QueryRow(ctx, `SELECT amount, revision FROM payment_orders WHERE order_id = $1`, id).Scan(&amount, &rev); err != nil || amount != 5000 || rev != 3 {
		t.Fatalf("amount=%d rev=%d err=%v", amount, rev, err)
	}
	now := e.clock.Now()
	e.status(t, id, "accepted", now)
	e.status(t, id, "pending_payment", now.Add(-time.Minute)) // более старый статус не затирает новый
	var st string
	if err := e.pool.QueryRow(ctx, `SELECT status FROM order_statuses WHERE order_id = $1`, id).Scan(&st); err != nil || st != "accepted" {
		t.Fatalf("status=%q err=%v", st, err)
	}
}

func TestConsumerRejectsMalformedEvents(t *testing.T) {
	e := newEnv(t)
	for name, body := range map[string]string{
		"not json":       `{`,
		"no order id":    `{"revision":1,"paymentMethod":"online","totalAmount":{"amount":1,"currency":"RUB"}}`,
		"unknown method": `{"orderId":"` + uuid.NewString() + `","revision":1,"paymentMethod":"barter","totalAmount":{"amount":1,"currency":"RUB"}}`,
		"zero revision":  `{"orderId":"` + uuid.NewString() + `","revision":0,"paymentMethod":"online","totalAmount":{"amount":1,"currency":"RUB"}}`,
	} {
		err := pgxutil.InTx(ctx, e.pool, func(tx pgx.Tx) error {
			return e.svc.HandleEvent(ctx, tx, kafka.Event{ID: uuid.New(), Topic: service.TopicState, Payload: []byte(body)})
		})
		if err == nil {
			t.Errorf("%s: error expected (the event goes to the DLQ after retries)", name)
		}
	}
	err := pgxutil.InTx(ctx, e.pool, func(tx pgx.Tx) error {
		return e.svc.HandleEvent(ctx, tx, kafka.Event{ID: uuid.New(), Topic: "other", Payload: []byte(`{}`)})
	})
	if err == nil {
		t.Error("unexpected topic must fail")
	}
}
