package service_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/pavelgr1408/food-platform/pkg/kafka"
	"github.com/pavelgr1408/food-platform/services/orders/internal/domain"
	"github.com/pavelgr1408/food-platform/services/orders/internal/service"
)

// pay доставляет orders.payments.status через настоящий inbox (как консьюмер).
func (e *env) pay(t *testing.T, orderID, status string, at time.Time) error {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"orderId": orderID, "paymentId": uuid.NewString(), "attempt": 1, "status": status,
		"method": "online", "updatedAt": at})
	ev := kafka.Event{ID: uuid.New(), Topic: service.TopicPaymentStatus, Key: orderID, Payload: b}
	_, err := kafka.ProcessOnce(ctx, e.pool, "orders-service", ev, e.svc.HandlePaymentStatus)
	return err
}

func (e *env) mustPay(t *testing.T, orderID, status string, at time.Time) {
	t.Helper()
	if err := e.pay(t, orderID, status, at); err != nil {
		t.Fatalf("payment event %s: %v", status, err)
	}
}

func counterValue(c prometheus.Counter) float64 {
	var d dto.Metric
	_ = c.Write(&d)
	return d.GetCounter().GetValue()
}

func TestPaidEventAcceptsOrderAndRepublishesState(t *testing.T) {
	e := newEnv(t)
	o := e.order(t, "", domain.PayOnlineCard)
	before := len(e.outbox(t)) // state(rev1) + status(pending_payment)
	e.mustPay(t, o.ID, "paid", e.clock.Now())

	got, _ := e.svc.GetOrder(ctx, "", o.ID)
	if got.Status != domain.StatusAccepted || got.PaymentStatus != domain.PaymentPaid || got.Revision != 2 {
		t.Fatalf("%+v", got)
	}
	ob := e.outbox(t)[before:]
	if len(ob) != 2 {
		t.Fatalf("want orders.status + orders.state, got %+v", ob)
	}
	var st service.OrderStateEvent
	var ss service.OrderStatusEvent
	for _, r := range ob {
		switch r.Topic {
		case "orders.state":
			_ = json.Unmarshal(r.Payload, &st)
		case "orders.status":
			_ = json.Unmarshal(r.Payload, &ss)
		}
	}
	if st.Revision != 2 || st.PaymentStatus != "paid" || st.TotalAmount.Amount != 100500 || ss.Status != "accepted" || ss.Reason != "paid" {
		t.Fatalf("state=%+v status=%+v", st, ss)
	}
	h, _ := e.svc.Store().History(ctx, e.pool, o.ID)
	if len(h) != 2 || h[1].To != domain.StatusAccepted || h[1].Actor != "system" || h[1].Reason != "paid" {
		t.Fatalf("history: %+v", h)
	}
}

func TestRepeatedPaidEventsHaveNoEffect(t *testing.T) {
	e := newEnv(t)
	o := e.order(t, "", domain.PayOnlineCard)
	at := e.clock.Now()
	e.mustPay(t, o.ID, "paid", at)
	n := len(e.outbox(t))
	for i := 0; i < 3; i++ { // другие event-id, тот же факт (повторная публикация payment)
		e.mustPay(t, o.ID, "paid", at.Add(time.Duration(i)*time.Second))
	}
	e.mustPay(t, o.ID, "declined", at.Add(time.Minute)) // paid терминален
	got, _ := e.svc.GetOrder(ctx, "", o.ID)
	if got.Revision != 2 || got.PaymentStatus != domain.PaymentPaid || len(e.outbox(t)) != n {
		t.Fatalf("revision=%d payment=%s outbox=%d→%d", got.Revision, got.PaymentStatus, n, len(e.outbox(t)))
	}
}

func TestDeclinedKeepsOrderPayableAndRetryWorks(t *testing.T) {
	e := newEnv(t)
	o := e.order(t, "", domain.PayOnlineCard)
	n := len(e.outbox(t))
	t0 := e.clock.Now()
	e.mustPay(t, o.ID, "declined", t0)
	got, _ := e.svc.GetOrder(ctx, "", o.ID)
	if got.Status != domain.StatusPendingPayment || got.PaymentStatus != domain.PaymentDeclined || got.Revision != 1 || len(e.outbox(t)) != n {
		t.Fatalf("declined: %+v outbox=%d", got, len(e.outbox(t)))
	}
	e.mustPay(t, o.ID, "pending", t0.Add(time.Second)) // новая попытка
	e.mustPay(t, o.ID, "paid", t0.Add(2*time.Second))
	got, _ = e.svc.GetOrder(ctx, "", o.ID)
	if got.Status != domain.StatusAccepted || got.PaymentStatus != domain.PaymentPaid || got.Revision != 2 {
		t.Fatalf("after retry: %+v", got)
	}
}

func TestStalePaymentEventsAreIgnored(t *testing.T) {
	e := newEnv(t)
	o := e.order(t, "", domain.PayOnlineCard)
	t0 := e.clock.Now()
	e.mustPay(t, o.ID, "unknown", t0.Add(10*time.Second))
	e.mustPay(t, o.ID, "pending", t0) // более старое событие пришло позже
	got, _ := e.svc.GetOrder(ctx, "", o.ID)
	if got.PaymentStatus != domain.PaymentUnknown {
		t.Fatalf("a stale event must not overwrite a newer one: %s", got.PaymentStatus)
	}
}

func TestUnknownPaymentPostponesExpiry(t *testing.T) {
	e := newEnv(t)
	plain := e.order(t, "", domain.PayOnlineCard)
	unknown := e.order(t, "", domain.PayOnlineCard)
	e.mustPay(t, unknown.ID, "unknown", e.clock.Now())

	e.clock.Advance(16 * time.Minute) // > PaymentTimeout (15 мин)
	n, err := e.svc.ExpireUnpaid(ctx)
	if err != nil || n != 1 {
		t.Fatalf("only the plain order expires: %d %v", n, err)
	}
	if g, _ := e.svc.GetOrder(ctx, "", plain.ID); g.Status != domain.StatusCancelled {
		t.Fatalf("plain: %s", g.Status)
	}
	if g, _ := e.svc.GetOrder(ctx, "", unknown.ID); g.Status != domain.StatusPendingPayment {
		t.Fatalf("an order with unknown payment must wait: %s", g.Status)
	}
	e.clock.Advance(time.Hour) // истёк UnknownGrace
	if n, _ := e.svc.ExpireUnpaid(ctx); n != 1 {
		t.Fatalf("grace expired: %d", n)
	}
	if g, _ := e.svc.GetOrder(ctx, "", unknown.ID); g.Status != domain.StatusCancelled {
		t.Fatalf("unknown: %s", g.Status)
	}
}

func TestPaidAfterCancellationDoesNotReviveOrder(t *testing.T) {
	e := newEnv(t)
	o := e.order(t, "", domain.PayOnlineCard)
	e.clock.Advance(16 * time.Minute)
	if n, _ := e.svc.ExpireUnpaid(ctx); n != 1 {
		t.Fatal("must expire")
	}
	n := len(e.outbox(t))
	e.mustPay(t, o.ID, "paid", e.clock.Now())
	got, _ := e.svc.GetOrder(ctx, "", o.ID)
	if got.Status != domain.StatusCancelled || got.PaymentStatus != domain.PaymentPaid || got.Revision != 1 {
		t.Fatalf("money received, order stays cancelled: %+v", got)
	}
	if len(e.outbox(t)) != n {
		t.Fatalf("orders.state(paid) must NOT be published for a cancelled order (production would start): %+v", e.outbox(t)[n:])
	}
	if counterValue(e.m.PaidAfterCancel) != 1 {
		t.Fatal("must be counted")
	}
}

func TestTimerCannotCancelAnOrderThePaymentJustAccepted(t *testing.T) {
	e := newEnv(t)
	o := e.order(t, "", domain.PayOnlineCard)
	e.mustPay(t, o.ID, "paid", e.clock.Now()) // оплата выиграла гонку, а таймер уже выбрал заказ
	changed, err := e.svc.CancelIfUnpaid(ctx, o.ID)
	if err != nil || changed {
		t.Fatalf("a paid order must not be cancelled: %v %v", changed, err)
	}
	if g, _ := e.svc.GetOrder(ctx, "", o.ID); g.Status != domain.StatusAccepted {
		t.Fatalf("status %s", g.Status)
	}
}

func TestMalformedPaymentEventsGoToRetries(t *testing.T) {
	e := newEnv(t)
	if err := e.pay(t, uuid.NewString(), "paid", time.Now()); err == nil {
		t.Fatal("an event for an unknown order must fail (→ retries, DLQ)")
	}
	o := e.order(t, "", domain.PayOnlineCard)
	if err := e.pay(t, o.ID, "refunded", time.Now()); err == nil {
		t.Fatal("unknown status must fail")
	}
	if err := e.pay(t, "not-a-uuid", "paid", time.Now()); err == nil {
		t.Fatal("bad order id must fail")
	}
}
