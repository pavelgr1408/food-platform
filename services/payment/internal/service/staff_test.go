package service_test

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pavelgr1408/food-platform/services/payment/internal/domain"
	"github.com/pavelgr1408/food-platform/services/payment/internal/gateway"
	"github.com/pavelgr1408/food-platform/services/payment/internal/service"
	"github.com/pavelgr1408/food-platform/services/payment/internal/testkit"
)

var (
	staff1   = service.Actor{UserID: "staff1", Staff: true, Restaurants: []string{"r1"}}
	staff2   = service.Actor{UserID: "staff2", Staff: true, Restaurants: []string{"r2"}}
	hqStaff  = service.Actor{UserID: "hq", Staff: true, HQ: true}
	customer = service.Actor{UserID: "client"}
	guest    = service.Actor{}
)

// posEnv — окружение с отдельной «кассовой системой».
func newPOSEnv(t *testing.T) (*env, *testkit.FakeGateway) {
	t.Helper()
	e := newEnv(t)
	pos := testkit.NewGateway()
	e.svc = service.New(service.Deps{Pool: e.pool, Gateway: e.gw, POS: pos, Log: testkit.Log(), Clock: e.clock.Now, Metrics: e.m,
		Config: service.Config{WebhookWait: 10 * time.Second, PendingExpiry: 10 * time.Minute, PollMin: 5 * time.Second, PollMax: time.Minute}})
	return e, pos
}

// posOrder — заказ с оплатой на кассе в статусе accepted (ресторан r1).
func (e *env) posOrder(t *testing.T, amount int64) string {
	t.Helper()
	o := e.order(t, "restaurant", "", amount)
	e.status(t, o, "accepted", e.clock.Now())
	return o
}

func (e *env) cancelOrder(t *testing.T, o string) {
	t.Helper()
	e.clock.Advance(time.Second)
	e.status(t, o, "cancelled", e.clock.Now())
}

func posCallback(o string, p domain.Payment, status string, amount int64, fiscal string) service.Callback {
	a := amount
	cb := service.Callback{EventID: uuid.NewString(), OrderID: o, PaymentID: p.ID, Status: status, ExternalRef: "pos_1", Amount: &a, Currency: "RUB"}
	if fiscal != "" {
		cb.Fiscal = []byte(fiscal)
	}
	return cb
}

func refundStatuses(evs []service.PaymentStatusEvent) []string {
	var s []string
	for _, e := range evs {
		s = append(s, e.Status+"/"+e.RefundStatus)
	}
	return s
}

func TestInitiatePosRightsMatrix(t *testing.T) {
	e, pos := newPOSEnv(t)
	o := e.posOrder(t, 25000)

	_, err := e.svc.InitiatePos(ctx, guest, o, domain.TenderCard)
	wantCode(t, err, "UNAUTHORIZED")
	_, err = e.svc.InitiatePos(ctx, customer, o, domain.TenderCard)
	wantCode(t, err, "FORBIDDEN")
	_, err = e.svc.InitiatePos(ctx, staff2, o, domain.TenderCard) // чужой ресторан: существование не раскрываем
	wantCode(t, err, "ORDER_NOT_FOUND")
	_, err = e.svc.InitiatePos(ctx, staff1, uuid.NewString(), domain.TenderCard)
	wantCode(t, err, "ORDER_NOT_FOUND")
	_, err = e.svc.InitiatePos(ctx, staff1, "not-a-uuid", domain.TenderCard)
	wantCode(t, err, "ORDER_NOT_FOUND")
	_, err = e.svc.InitiatePos(ctx, staff1, o, domain.Tender("crypto"))
	wantCode(t, err, "INVALID_PARAMETER")
	if pos.Operations() != 0 {
		t.Fatal("rejected requests must not reach the POS")
	}

	p, err := e.svc.InitiatePos(ctx, staff1, o, domain.TenderCash)
	if err != nil || p.Status != domain.StatusPending || p.Tender != domain.TenderCash || p.Method != domain.MethodPOS {
		t.Fatalf("%+v %v", p, err)
	}
	// HQ видит любой ресторан
	o2 := e.posOrder(t, 1000)
	if _, err := e.svc.InitiatePos(ctx, hqStaff, o2, domain.TenderCard); err != nil {
		t.Fatal(err)
	}
}

func TestInitiatePosRules(t *testing.T) {
	e, _ := newPOSEnv(t)
	// способ оплаты заказа — не pos
	online := e.order(t, "online", "", 1000)
	_, err := e.svc.InitiatePos(ctx, staff1, online, domain.TenderCard)
	wantCode(t, err, "PAYMENT_METHOD_NOT_ALLOWED")

	// отменённый и завершённый заказы платить нельзя
	for _, st := range []string{"cancelled", "handed_over", "closed", "not_received"} {
		o := e.posOrder(t, 1000)
		e.clock.Advance(time.Second)
		e.status(t, o, st, e.clock.Now())
		_, err := e.svc.InitiatePos(ctx, staff1, o, domain.TenderCard)
		if code(t, err) != "ORDER_NOT_PAYABLE" {
			t.Fatalf("status %s: %v", st, err)
		}
	}

	// клиентский эндпоинт pos не принимает
	o := e.posOrder(t, 1000)
	_, err = e.svc.Initiate(ctx, "", o, service.InitiateInput{Method: domain.MethodPOS})
	wantCode(t, err, "PAYMENT_METHOD_NOT_SUPPORTED")

	// вторая попытка при живой — конфликт; после paid — ALREADY_PAID
	p, err := e.svc.InitiatePos(ctx, staff1, o, domain.TenderCard)
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.InitiatePos(ctx, staff1, o, domain.TenderCard)
	wantCode(t, err, "PAYMENT_IN_PROGRESS")
	if err := e.svc.HandleCallback(ctx, domain.MethodPOS, posCallback(o, p, "paid", 1000, "")); err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.InitiatePos(ctx, staff1, o, domain.TenderCard)
	wantCode(t, err, "ALREADY_PAID")
}

func TestInitiatePosWithoutPOSIntegration(t *testing.T) {
	e := newEnv(t) // POS не настроен
	o := e.posOrder(t, 1000)
	_, err := e.svc.InitiatePos(ctx, staff1, o, domain.TenderCard)
	wantCode(t, err, "POS_UNAVAILABLE")
}

func TestPosPaymentWebhookStoresFiscalData(t *testing.T) {
	e, pos := newPOSEnv(t)
	o := e.posOrder(t, 25000)
	p, err := e.svc.InitiatePos(ctx, staff1, o, domain.TenderCard)
	if err != nil {
		t.Fatal(err)
	}
	if pos.Operations() != 1 || e.gw.Operations() != 0 {
		t.Fatalf("the operation belongs to the POS gateway only (pos=%d gw=%d)", pos.Operations(), e.gw.Operations())
	}
	fiscal := `{"receiptNumber":"000123","fn":"9960440300123456","fd":42}`
	if err := e.svc.HandleCallback(ctx, domain.MethodPOS, posCallback(o, p, "paid", 25000, fiscal)); err != nil {
		t.Fatal(err)
	}
	got, err := e.svc.GetAs(ctx, staff1, o, p.ID)
	if err != nil || got.Status != domain.StatusPaid {
		t.Fatalf("%+v %v", got, err)
	}
	var m map[string]any
	if err := json.Unmarshal(got.FiscalData, &m); err != nil || m["receiptNumber"] != "000123" {
		t.Fatalf("fiscal data: %s %v", got.FiscalData, err)
	}
	if evs := statuses(e.events(t, o)); !eq(evs, []string{"pending", "paid"}) {
		t.Fatalf("events: %v", evs)
	}
	// повтор того же eventId — без эффекта; фискальные данные не затираются
	cb := posCallback(o, p, "paid", 25000, `{"receiptNumber":"OTHER"}`)
	cb.EventID = uuid.NewString()
	if err := e.svc.HandleCallback(ctx, domain.MethodPOS, cb); err != nil {
		t.Fatal(err)
	}
	got, _ = e.svc.GetAs(ctx, staff1, o, p.ID)
	if string(got.FiscalData) == `{"receiptNumber":"OTHER"}` {
		t.Fatal("fiscal data of a settled payment must not be replaced")
	}
	// вебхук кассы не подтверждает платёж иного способа и наоборот
	if err := e.svc.HandleCallback(ctx, domain.MethodOnlineCard, posCallback(o, p, "paid", 25000, "")); !service.IsNotFound(err) {
		t.Fatalf("online channel must not confirm a pos payment: %v", err)
	}
}

func TestPosTimeoutBecomesUnknownAndReconcilerGetsFiscalData(t *testing.T) {
	e, pos := newPOSEnv(t)
	o := e.posOrder(t, 25000)
	pos.LostReply = testkit.ErrTimeout
	p, err := e.svc.InitiatePos(ctx, staff1, o, domain.TenderCash)
	if err != nil || p.Status != domain.StatusUnknown {
		t.Fatalf("%+v %v", p, err)
	}
	_, err = e.svc.InitiatePos(ctx, staff1, o, domain.TenderCash)
	wantCode(t, err, "PAYMENT_STATUS_UNKNOWN")

	pos.LostReply = nil
	pos.Finish(p.ID, gateway.StatePaid, "")
	pos.SetFiscal(p.ID, []byte(`{"receiptNumber":"777"}`))
	for i := 0; i < 2; i++ { // unknown → pending → paid
		e.clock.Advance(10 * time.Second)
		if _, err := e.svc.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := e.svc.GetAs(ctx, staff1, o, p.ID)
	if got.Status != domain.StatusPaid || string(got.FiscalData) != `{"receiptNumber": "777"}` {
		t.Fatalf("%+v %s", got, got.FiscalData)
	}
	if pos.Operations() != 1 {
		t.Fatalf("no second operation at the POS, got %d", pos.Operations())
	}
}

func TestGetPaymentAccessForStaff(t *testing.T) {
	e, _ := newPOSEnv(t)
	o := e.order(t, "restaurant", "client", 1000) // заказ клиента-владельца
	e.status(t, o, "accepted", e.clock.Now())
	p, err := e.svc.InitiatePos(ctx, staff1, o, domain.TenderCard)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.GetAs(ctx, staff1, o, p.ID); err != nil {
		t.Fatalf("staff of the restaurant: %v", err)
	}
	if _, err := e.svc.GetAs(ctx, hqStaff, o, p.ID); err != nil {
		t.Fatalf("hq: %v", err)
	}
	if _, err := e.svc.GetAs(ctx, service.Actor{UserID: "client"}, o, p.ID); err != nil {
		t.Fatalf("owner: %v", err)
	}
	for name, a := range map[string]service.Actor{"staff of another restaurant": staff2, "another customer": {UserID: "other"}, "guest": guest} {
		if _, err := e.svc.GetAs(ctx, a, o, p.ID); !service.IsNotFound(err) {
			t.Fatalf("%s must get 404, got %v", name, err)
		}
	}
}

// ---- возвраты ----

func TestCancelledPaidOrderIsRefundedExactlyOnce(t *testing.T) {
	e := newEnv(t)
	o := e.order(t, "online", "", 54990)
	p := e.initiate(t, "", o)
	if err := e.svc.HandleCallback(ctx, domain.MethodOnlineCard, e.callback(o, p, "paid", 54990)); err != nil {
		t.Fatal(err)
	}
	e.gw.Finish(p.ID, gateway.StatePaid, "")

	// пока заказ не отменён — возвратов нет
	if n, err := e.svc.ProcessRefunds(ctx); err != nil || n != 0 || e.gw.Calls.Refund != 0 {
		t.Fatalf("n=%d calls=%d err=%v", n, e.gw.Calls.Refund, err)
	}

	e.cancelOrder(t, o)
	n, err := e.svc.ProcessRefunds(ctx)
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	got, _ := e.svc.Get(ctx, "", o, p.ID)
	if got.Status != domain.StatusPaid || got.RefundStatus != domain.RefundRefunded || got.RefundedAt == nil || got.RefundRef == "" {
		t.Fatalf("%+v", got)
	}
	if evs := refundStatuses(e.events(t, o)); !eq(evs, []string{"pending/", "paid/", "paid/pending", "paid/refunded"}) {
		t.Fatalf("events: %v", evs)
	}
	last := e.events(t, o)[3]
	if last.RefundedAt == nil || last.Amount.Amount != 54990 {
		t.Fatalf("%+v", last)
	}
	// повторные проходы и повторное событие об отмене ничего не делают
	e.cancelOrder(t, o)
	for i := 0; i < 3; i++ {
		if n, err := e.svc.ProcessRefunds(ctx); err != nil || n != 0 {
			t.Fatalf("n=%d err=%v", n, err)
		}
	}
	if e.gw.Calls.Refund != 1 || counter(e.m.Refunds.WithLabelValues("refunded")) != 1 {
		t.Fatalf("refund calls=%d", e.gw.Calls.Refund)
	}
	if len(e.events(t, o)) != 4 {
		t.Fatalf("no extra events: %v", refundStatuses(e.events(t, o)))
	}
}

func TestParallelRefundPassesRefundOnce(t *testing.T) {
	e := newEnv(t)
	o := e.order(t, "online", "", 1000)
	p := e.initiate(t, "", o)
	if err := e.svc.HandleCallback(ctx, domain.MethodOnlineCard, e.callback(o, p, "paid", 1000)); err != nil {
		t.Fatal(err)
	}
	e.gw.Finish(p.ID, gateway.StatePaid, "")
	e.cancelOrder(t, o)
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = e.svc.ProcessRefunds(ctx) }()
	}
	wg.Wait()
	if _, err := e.svc.ProcessRefunds(ctx); err != nil {
		t.Fatal(err)
	}
	if got := refundStatuses(e.events(t, o)); !eq(got, []string{"pending/", "paid/", "paid/pending", "paid/refunded"}) {
		t.Fatalf("events: %v", got)
	}
	if e.gw.Calls.Refund != 1 {
		t.Fatalf("the gateway must be called once, got %d", e.gw.Calls.Refund)
	}
}

func TestRefundRetriesAfterGatewayFailureWithBackoff(t *testing.T) {
	e := newEnv(t)
	o := e.order(t, "online", "", 1000)
	p := e.initiate(t, "", o)
	_ = e.svc.HandleCallback(ctx, domain.MethodOnlineCard, e.callback(o, p, "paid", 1000))
	e.gw.Finish(p.ID, gateway.StatePaid, "")
	e.cancelOrder(t, o)

	e.gw.RefundErr = testkit.ErrTimeout // шлюз не ответил: исход неизвестен
	if n, _ := e.svc.ProcessRefunds(ctx); n != 1 {
		t.Fatalf("n=%d", n)
	}
	got, _ := e.svc.Get(ctx, "", o, p.ID)
	if got.RefundStatus != domain.RefundPending || got.RefundAttempts != 1 {
		t.Fatalf("%+v", got)
	}
	// пауза ещё не прошла — шлюз не трогаем
	if n, _ := e.svc.ProcessRefunds(ctx); n != 0 || e.gw.Calls.Refund != 1 {
		t.Fatalf("n=%d calls=%d", n, e.gw.Calls.Refund)
	}
	e.gw.RefundErr = nil
	e.gw.RefundPendingOnce = true // шлюз принял, но ещё обрабатывает
	e.clock.Advance(30 * time.Second)
	if n, _ := e.svc.ProcessRefunds(ctx); n != 1 {
		t.Fatalf("n=%d", n)
	}
	got, _ = e.svc.Get(ctx, "", o, p.ID)
	if got.RefundStatus != domain.RefundPending || got.RefundAttempts != 2 {
		t.Fatalf("%+v", got)
	}
	e.clock.Advance(time.Minute)
	if n, _ := e.svc.ProcessRefunds(ctx); n != 1 {
		t.Fatalf("n=%d", n)
	}
	got, _ = e.svc.Get(ctx, "", o, p.ID)
	if got.RefundStatus != domain.RefundRefunded {
		t.Fatalf("%+v", got)
	}
	if evs := refundStatuses(e.events(t, o)); !eq(evs, []string{"pending/", "paid/", "paid/pending", "paid/refunded"}) {
		t.Fatalf("retries must not publish events: %v", evs)
	}
}

func TestRefundRejectedByGatewayFailsWithoutRetry(t *testing.T) {
	e := newEnv(t)
	o := e.order(t, "online", "", 1000)
	p := e.initiate(t, "", o)
	_ = e.svc.HandleCallback(ctx, domain.MethodOnlineCard, e.callback(o, p, "paid", 1000))
	e.cancelOrder(t, o)
	e.gw.RefundErr = &gateway.RejectedError{HTTPStatus: 422, Code: "refund_not_allowed", Message: "window closed"}

	if _, err := e.svc.ProcessRefunds(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := e.svc.Get(ctx, "", o, p.ID)
	if got.RefundStatus != domain.RefundFailed || got.RefundReason != "refund_not_allowed" || got.Status != domain.StatusPaid {
		t.Fatalf("%+v", got)
	}
	e.clock.Advance(time.Hour)
	if n, _ := e.svc.ProcessRefunds(ctx); n != 0 || e.gw.Calls.Refund != 1 {
		t.Fatalf("a failed refund must not be retried automatically: n=%d calls=%d", n, e.gw.Calls.Refund)
	}
	evs := e.events(t, o)
	if evs[len(evs)-1].RefundStatus != "failed" || counter(e.m.Refunds.WithLabelValues("failed")) != 1 {
		t.Fatalf("%v", refundStatuses(evs))
	}
}

func TestPaymentPaidAfterCancellationIsRefunded(t *testing.T) {
	e := newEnv(t)
	o := e.order(t, "online", "", 2000)
	p := e.initiate(t, "", o)
	e.cancelOrder(t, o) // заказ отменён, пока пользователь на странице оплаты
	e.gw.Finish(p.ID, gateway.StatePaid, "")
	if err := e.svc.HandleCallback(ctx, domain.MethodOnlineCard, e.callback(o, p, "paid", 2000)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.ProcessRefunds(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := e.svc.Get(ctx, "", o, p.ID)
	if got.RefundStatus != domain.RefundRefunded || !got.RefundNeeded {
		t.Fatalf("%+v", got)
	}
}

func TestCancelledOrderLivePaymentIsCancelledAtGatewayNotRefunded(t *testing.T) {
	e := newEnv(t)
	o := e.order(t, "online", "", 1000)
	p := e.initiate(t, "", o)
	e.cancelOrder(t, o)
	// событие об отмене ускоряет сверку: не ждём WebhookWait
	if n, err := e.svc.Reconcile(ctx); err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	got, _ := e.svc.Get(ctx, "", o, p.ID)
	if got.Status != domain.StatusDeclined || got.DeclineReason != "order_cancelled" {
		t.Fatalf("%+v", got)
	}
	if e.gw.Calls.Cancel != 1 {
		t.Fatalf("the operation must be cancelled at the gateway, calls=%d", e.gw.Calls.Cancel)
	}
	if n, _ := e.svc.ProcessRefunds(ctx); n != 0 || e.gw.Calls.Refund != 0 {
		t.Fatal("nothing was paid: nothing to refund")
	}
}

func TestCancelledOrderPaymentPaidAtLastMomentIsRefunded(t *testing.T) {
	e := newEnv(t)
	o := e.order(t, "online", "", 1000)
	p := e.initiate(t, "", o)
	e.cancelOrder(t, o)
	e.gw.PaidOnCancel = true // пользователь успел заплатить, пока отменяем
	if _, err := e.svc.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.ProcessRefunds(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := e.svc.Get(ctx, "", o, p.ID)
	if got.Status != domain.StatusPaid || got.RefundStatus != domain.RefundRefunded {
		t.Fatalf("money must be returned: %+v", got)
	}
}

func TestPosPaymentRefundGoesThroughPOSGateway(t *testing.T) {
	e, pos := newPOSEnv(t)
	o := e.posOrder(t, 25000)
	p, err := e.svc.InitiatePos(ctx, staff1, o, domain.TenderCard)
	if err != nil {
		t.Fatal(err)
	}
	pos.Finish(p.ID, gateway.StatePaid, "")
	if err := e.svc.HandleCallback(ctx, domain.MethodPOS, posCallback(o, p, "paid", 25000, `{"receiptNumber":"1"}`)); err != nil {
		t.Fatal(err)
	}
	e.cancelOrder(t, o)
	if _, err := e.svc.ProcessRefunds(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := e.svc.GetAs(ctx, staff1, o, p.ID)
	if got.RefundStatus != domain.RefundRefunded || pos.Calls.Refund != 1 || e.gw.Calls.Refund != 0 {
		t.Fatalf("%+v pos=%d gw=%d", got, pos.Calls.Refund, e.gw.Calls.Refund)
	}
}

func TestRefundWithoutGatewayOperationFailsForManualHandling(t *testing.T) {
	e := newEnv(t)
	o := e.order(t, "online", "", 1000)
	p := e.initiate(t, "", o)
	e.cancelOrder(t, o)
	// оплата подтверждена, но ссылки на операцию шлюза нет (повреждённые данные): автоматически вернуть нельзя
	if _, err := e.pool.Exec(ctx, `UPDATE payments SET status='paid', paid_at=now(), gateway_ref=NULL WHERE id=$1`, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.ProcessRefunds(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := e.svc.Get(ctx, "", o, p.ID)
	if got.RefundStatus != domain.RefundFailed || got.RefundReason != "no_gateway_operation" {
		t.Fatalf("%+v", got)
	}
}
