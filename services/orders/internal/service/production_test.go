package service_test

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/pavelgr1408/food-platform/pkg/kafka"
	"github.com/pavelgr1408/food-platform/services/orders/internal/domain"
	"github.com/pavelgr1408/food-platform/services/orders/internal/service"
	"github.com/pavelgr1408/food-platform/services/orders/internal/testkit"
)

// dishes — снимок блюд: пары «блюдо, статус». Блюда заказа из e.order: Burger и Cola.
type dishes map[string]string

func (e *env) dish(t *testing.T, orderID string, snap dishes, at time.Time) error {
	t.Helper()
	ds := make([]map[string]string, 0, len(snap))
	for id, st := range snap {
		ds = append(ds, map[string]string{"dishId": id, "status": st})
	}
	b, _ := json.Marshal(map[string]any{"orderId": orderID, "dishes": ds, "updatedAt": at})
	ev := kafka.Event{ID: uuid.New(), Topic: service.TopicDishStatus, Key: orderID, Payload: b}
	_, err := kafka.ProcessOnce(ctx, e.pool, "orders-service-production", ev, e.svc.HandleProduction)
	return err
}

func (e *env) mustDish(t *testing.T, orderID string, snap dishes, at time.Time) {
	t.Helper()
	if err := e.dish(t, orderID, snap, at); err != nil {
		t.Fatalf("dish status %v: %v", snap, err)
	}
}

func (e *env) lead(t *testing.T, restaurant string, minutes int, at time.Time) error {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"restaurantId": restaurant, "currentLoad": 0.3, "leadTimeMinutes": minutes, "measuredAt": at})
	ev := kafka.Event{ID: uuid.New(), Topic: service.TopicLeadTime, Key: restaurant, Payload: b}
	_, err := kafka.ProcessOnce(ctx, e.pool, "orders-service-production", ev, e.svc.HandleProduction)
	return err
}

func (e *env) mustLead(t *testing.T, restaurant string, minutes int, at time.Time) {
	t.Helper()
	if err := e.lead(t, restaurant, minutes, at); err != nil {
		t.Fatal(err)
	}
}

func (e *env) status(t *testing.T, id string) domain.Order {
	t.Helper()
	o, err := e.svc.GetOrder(ctx, "", id)
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func (e *env) statusEvents(t *testing.T, orderID string) []service.OrderStatusEvent {
	t.Helper()
	var out []service.OrderStatusEvent
	for _, r := range e.outbox(t) {
		if r.Topic != service.TopicStatus || r.Key != orderID {
			continue
		}
		var s service.OrderStatusEvent
		if err := json.Unmarshal(r.Payload, &s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

func allDishes(st string) dishes { return dishes{testkit.Burger: st, testkit.Cola: st} }

// paid — онлайн-заказ, оплаченный в момент создания.
func (e *env) paid(t *testing.T) domain.Order {
	t.Helper()
	o := e.order(t, "", domain.PayOnlineCard)
	e.mustPay(t, o.ID, "paid", e.clock.Now())
	return e.status(t, o.ID)
}

func statuses(h []service.OrderStatusEvent) []string {
	var s []string
	for _, x := range h {
		s = append(s, x.Status)
	}
	return s
}

func TestProductionLifecycleAcceptedCookingReady(t *testing.T) {
	e := newEnv(t)
	e.mustLead(t, testkit.Restaurant1, 25, e.clock.Now())
	o := e.paid(t)
	t0 := e.clock.Now()

	e.mustDish(t, o.ID, allDishes("queued"), t0.Add(1*time.Second))
	if got := e.status(t, o.ID); got.Status != domain.StatusAccepted {
		t.Fatalf("queued dishes must not change the status: %s", got.Status)
	}
	e.clock.Advance(2 * time.Second)
	e.mustDish(t, o.ID, allDishes("cooking"), t0.Add(2*time.Second))
	if got := e.status(t, o.ID); got.Status != domain.StatusCooking {
		t.Fatalf("cooking dishes → cooking, got %s", got.Status)
	}
	e.mustDish(t, o.ID, dishes{testkit.Burger: "ready", testkit.Cola: "cooking"}, t0.Add(3*time.Second))
	if got := e.status(t, o.ID); got.Status != domain.StatusCooking {
		t.Fatalf("a partially ready order stays cooking, got %s", got.Status)
	}
	e.clock.Advance(3 * time.Second)
	e.mustDish(t, o.ID, allDishes("ready"), t0.Add(4*time.Second))
	got := e.status(t, o.ID)
	if got.Status != domain.StatusReady {
		t.Fatalf("all ready → ready, got %s", got.Status)
	}

	h, _ := e.svc.Store().History(ctx, e.pool, o.ID)
	var path []domain.Status
	for _, x := range h {
		path = append(path, x.To)
	}
	if len(path) != 4 || path[1] != domain.StatusAccepted || path[2] != domain.StatusCooking || path[3] != domain.StatusReady || h[3].Actor != "production" {
		t.Fatalf("history: %+v", h)
	}

	ev := e.statusEvents(t, o.ID)
	if got := statuses(ev); len(got) != 4 || got[3] != "ready" || got[2] != "cooking" {
		t.Fatalf("orders.status sequence: %v", got)
	}
	// etaReadyAt: у accepted и cooking — принятие + лид-тайм ресторана (25 мин), у ready — нет
	want := (*got.AcceptedAt).Add(25 * time.Minute)
	for _, i := range []int{1, 2} {
		if ev[i].EtaReadyAt == nil || !ev[i].EtaReadyAt.Equal(want) {
			t.Fatalf("%s: etaReadyAt=%v, want %v", ev[i].Status, ev[i].EtaReadyAt, want)
		}
	}
	if ev[3].EtaReadyAt != nil || ev[0].EtaReadyAt != nil {
		t.Fatalf("etaReadyAt must be absent for pending_payment and ready: %+v", ev)
	}
}

func TestSingleSnapshotAllReadyPassesThroughCooking(t *testing.T) {
	e := newEnv(t)
	o := e.paid(t)
	n := len(e.statusEvents(t, o.ID))
	e.mustDish(t, o.ID, allDishes("ready"), e.clock.Now().Add(time.Second))
	if got := e.status(t, o.ID); got.Status != domain.StatusReady {
		t.Fatalf("got %s", got.Status)
	}
	ev := e.statusEvents(t, o.ID)[n:]
	if s := statuses(ev); len(s) != 2 || s[0] != "cooking" || s[1] != "ready" {
		t.Fatalf("accepted → ready must go through cooking, published %v", s)
	}
}

func TestStaleDuplicateAndRollbackSnapshots(t *testing.T) {
	e := newEnv(t)
	o := e.paid(t)
	t0 := e.clock.Now()
	e.mustDish(t, o.ID, allDishes("cooking"), t0.Add(5*time.Second))
	n := len(e.outbox(t))

	// более старый снимок пришёл позже — игнорируется
	e.mustDish(t, o.ID, allDishes("ready"), t0.Add(2*time.Second))
	if got := e.status(t, o.ID); got.Status != domain.StatusCooking {
		t.Fatalf("a stale snapshot must be ignored, got %s", got.Status)
	}
	// тот же снимок ещё раз (другой event-id) — без эффекта
	e.mustDish(t, o.ID, allDishes("cooking"), t0.Add(5*time.Second))
	if len(e.outbox(t)) != n {
		t.Fatalf("repeated snapshot produced events")
	}
	e.mustDish(t, o.ID, allDishes("ready"), t0.Add(9*time.Second))
	// откат: заказ уже ready, пришёл «cooking» с более новым updatedAt
	e.mustDish(t, o.ID, allDishes("cooking"), t0.Add(10*time.Second))
	if got := e.status(t, o.ID); got.Status != domain.StatusReady {
		t.Fatalf("status must not roll back, got %s", got.Status)
	}
	if v := counterVec(t, e.m.ProductionEvents, "dish_status", "stale"); v != 1 {
		t.Fatalf("stale counter = %v", v)
	}
	if v := counterVec(t, e.m.ProductionEvents, "dish_status", "ignored_status"); v != 1 {
		t.Fatalf("ignored counter = %v", v)
	}
}

func counterVec(t *testing.T, c *prometheus.CounterVec, lv ...string) float64 {
	t.Helper()
	return counterValue(c.WithLabelValues(lv...))
}

func TestDishStatusForUnpaidOrCancelledOrderIsIgnored(t *testing.T) {
	e := newEnv(t)
	unpaid := e.order(t, "", domain.PayOnlineCard)
	e.mustDish(t, unpaid.ID, allDishes("ready"), e.clock.Now())
	if got := e.status(t, unpaid.ID); got.Status != domain.StatusPendingPayment {
		t.Fatalf("an unpaid order must not move: %s", got.Status)
	}

	cancelled := e.paid(t)
	if _, _, err := e.svc.Transition(ctx, cancelled.ID, domain.StatusCancelled, "test", "system"); err != nil {
		t.Fatal(err)
	}
	e.mustDish(t, cancelled.ID, allDishes("cooking"), e.clock.Now())
	if got := e.status(t, cancelled.ID); got.Status != domain.StatusCancelled {
		t.Fatalf("a cancelled order must not be revived: %s", got.Status)
	}
	if v := counterVec(t, e.m.ProductionEvents, "dish_status", "ignored_status"); v != 2 {
		t.Fatalf("ignored = %v, want 2", v)
	}
}

func TestDishStatusInvalidInputsFail(t *testing.T) {
	e := newEnv(t)
	o := e.paid(t)
	at := e.clock.Now()
	if err := e.dish(t, uuid.NewString(), allDishes("cooking"), at); err == nil {
		t.Fatal("unknown order must fail (retry → DLQ)")
	}
	if err := e.dish(t, o.ID, dishes{testkit.Burger: "burnt"}, at); err == nil {
		t.Fatal("unknown dish status must fail")
	}
	if err := e.dish(t, o.ID, dishes{}, at); err == nil {
		t.Fatal("empty dishes must fail")
	}
	if err := e.dish(t, "not-a-uuid", allDishes("ready"), at); err == nil {
		t.Fatal("invalid orderId must fail")
	}
	if err := e.dish(t, o.ID, allDishes("ready"), time.Time{}); err == nil {
		t.Fatal("missing updatedAt must fail")
	}
	if got := e.status(t, o.ID); got.Status != domain.StatusAccepted {
		t.Fatalf("invalid events must not change the order: %s", got.Status)
	}
}

func TestParallelDuplicateSnapshotsReadyOnce(t *testing.T) {
	e := newEnv(t)
	o := e.paid(t)
	at := e.clock.Now().Add(time.Second)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = e.dish(t, o.ID, allDishes("ready"), at) }()
	}
	wg.Wait()
	h, _ := e.svc.Store().History(ctx, e.pool, o.ID)
	if len(h) != 4 { // pending_payment, accepted, cooking, ready — каждый ровно один раз
		t.Fatalf("history: %+v", h)
	}
}

func TestCourierPaidOrderGoesThroughProductionWithoutPayment(t *testing.T) {
	e := newEnv(t)
	e.mustLead(t, testkit.Restaurant1, 30, e.clock.Now())
	o := e.order(t, "", domain.PayPOS) // предоплаты нет → accepted сразу
	if o.Status != domain.StatusAccepted || o.AcceptedAt == nil {
		t.Fatalf("%+v", o)
	}
	ev := e.statusEvents(t, o.ID)
	if len(ev) != 1 || ev[0].Status != "accepted" || ev[0].EtaReadyAt == nil || !ev[0].EtaReadyAt.Equal(o.CreatedAt.Add(30*time.Minute)) {
		t.Fatalf("accepted at creation must carry etaReadyAt: %+v", ev)
	}
	e.mustDish(t, o.ID, allDishes("cooking"), e.clock.Now().Add(time.Second))
	e.mustDish(t, o.ID, allDishes("ready"), e.clock.Now().Add(2*time.Second))
	if got := e.status(t, o.ID); got.Status != domain.StatusReady || got.PaymentStatus == domain.PaymentPaid {
		t.Fatalf("%+v", got)
	}
}

func TestEtaFromFreshLeadTime(t *testing.T) {
	e := newEnv(t)
	o := e.order(t, "", domain.PayOnlineCard)
	e.mustLead(t, testkit.Restaurant1, 35, e.clock.Now())
	// заказ ещё не оплачен: «если оплатить сейчас»
	eta, err := e.svc.GetEta(ctx, "", o.ID)
	if err != nil || eta.Approximate || eta.EstimateMinutes != 35 || !eta.ReadyAt.Equal(e.clock.Now().Add(35*time.Minute)) {
		t.Fatalf("fresh lead time: %v %+v", err, eta)
	}
	// оплата через 5 минут: отсчёт идёт от принятия заказа
	e.clock.Advance(5 * time.Minute)
	e.mustLead(t, testkit.Restaurant1, 20, e.clock.Now()) // свежее измерение
	e.mustPay(t, o.ID, "paid", e.clock.Now())
	acc := e.status(t, o.ID).AcceptedAt
	eta, _ = e.svc.GetEta(ctx, "", o.ID)
	if eta.Approximate || !eta.ReadyAt.Equal(acc.Add(20*time.Minute)) || eta.EstimateMinutes != 20 {
		t.Fatalf("after accept: %+v (accepted %v)", eta, acc)
	}
	// время идёт, estimateMinutes уменьшается
	e.clock.Advance(8 * time.Minute)
	e.mustLead(t, testkit.Restaurant1, 20, e.clock.Now())
	eta, _ = e.svc.GetEta(ctx, "", o.ID)
	if eta.EstimateMinutes != 12 {
		t.Fatalf("estimate after 8 minutes: %+v", eta)
	}
}

func TestEtaFallsBackWhenLeadTimeIsMissingOrStale(t *testing.T) {
	e := newEnv(t)
	o := e.paid(t)
	if eta, _ := e.svc.GetEta(ctx, "", o.ID); !eta.Approximate || eta.EstimateMinutes != 20 {
		t.Fatalf("no replica → typical: %+v", eta)
	}
	e.mustLead(t, testkit.Restaurant2, 5, e.clock.Now()) // чужой ресторан не влияет
	if eta, _ := e.svc.GetEta(ctx, "", o.ID); !eta.Approximate {
		t.Fatalf("another restaurant's lead time must not apply: %+v", eta)
	}
	e.mustLead(t, testkit.Restaurant1, 10, e.clock.Now())
	if eta, _ := e.svc.GetEta(ctx, "", o.ID); eta.Approximate || eta.EstimateMinutes != 10 {
		t.Fatalf("fresh: %+v", eta)
	}
	e.clock.Advance(16 * time.Minute) // TTL по умолчанию 15 минут — производство молчит
	eta, _ := e.svc.GetEta(ctx, "", o.ID)
	if !eta.Approximate || !eta.ReadyAt.Equal(e.status(t, o.ID).AcceptedAt.Add(20*time.Minute)) {
		t.Fatalf("stale replica → typical: %+v", eta)
	}
}

func TestLeadTimeOlderMeasurementIsIgnoredAndInvalidFails(t *testing.T) {
	e := newEnv(t)
	t0 := e.clock.Now()
	e.mustLead(t, testkit.Restaurant1, 30, t0)
	e.mustLead(t, testkit.Restaurant1, 5, t0.Add(-time.Minute)) // устарело
	lt, found, err := e.svc.Store().GetLeadTime(ctx, e.pool, testkit.Restaurant1)
	if err != nil || !found || lt.Minutes != 30 {
		t.Fatalf("%+v %v %v", lt, found, err)
	}
	e.mustLead(t, testkit.Restaurant1, 40, t0.Add(time.Minute))
	if lt, _, _ = e.svc.Store().GetLeadTime(ctx, e.pool, testkit.Restaurant1); lt.Minutes != 40 {
		t.Fatalf("newer measurement must win: %+v", lt)
	}
	if err := e.lead(t, "not-a-uuid", 10, t0); err == nil {
		t.Fatal("invalid restaurantId must fail")
	}
	if err := e.lead(t, testkit.Restaurant1, -1, t0); err == nil {
		t.Fatal("negative minutes must fail")
	}
	if err := e.lead(t, testkit.Restaurant1, 10, time.Time{}); err == nil {
		t.Fatal("missing measuredAt must fail")
	}
	if v := counterVec(t, e.m.ProductionEvents, "lead_time", "stale"); v != 1 {
		t.Fatalf("stale lead time counter = %v", v)
	}
}

func TestEtaOfReadyOrderIsTheReadyMoment(t *testing.T) {
	e := newEnv(t)
	e.mustLead(t, testkit.Restaurant1, 25, e.clock.Now())
	o := e.paid(t)
	e.clock.Advance(7 * time.Minute)
	e.mustDish(t, o.ID, allDishes("ready"), e.clock.Now())
	readyAt := e.clock.Now()
	e.clock.Advance(30 * time.Minute)
	eta, err := e.svc.GetEta(ctx, "", o.ID)
	if err != nil || eta.Approximate || eta.EstimateMinutes != 0 || !eta.ReadyAt.Equal(readyAt) {
		t.Fatalf("ready order eta: %v %+v (ready at %v)", err, eta, readyAt)
	}
}

func TestEtaPickupSlotDelaysReadiness(t *testing.T) {
	e := newEnv(t)
	e.mustLead(t, testkit.Restaurant1, 15, e.clock.Now())
	o := e.order(t, "", domain.PayOnlineCard)
	from := e.clock.Now().Add(90 * time.Minute)
	if _, err := e.svc.SetPickupSlot(ctx, "", o.ID, from, from.Add(30*time.Minute)); err != nil {
		t.Fatal(err)
	}
	eta, _ := e.svc.GetEta(ctx, "", o.ID)
	if eta.Approximate || !eta.ReadyAt.Equal(from) || eta.EstimateMinutes != 90 {
		t.Fatalf("slot later than lead time: %+v", eta)
	}
}

func TestStuckInAcceptedMetric(t *testing.T) {
	e := newEnv(t)
	reg := prometheus.NewRegistry()
	reg.MustRegister(service.StuckCollectors(e.pool)...)
	value := func(name string) float64 {
		mfs, err := reg.Gather()
		if err != nil {
			t.Fatal(err)
		}
		for _, mf := range mfs {
			if mf.GetName() == name {
				return mf.GetMetric()[0].GetGauge().GetValue()
			}
		}
		t.Fatalf("metric %s not found", name)
		return 0
	}
	if v := value("orders_stuck_in_accepted_seconds"); v != 0 {
		t.Fatalf("no accepted orders → 0, got %v", v)
	}
	o := e.paid(t)
	if _, err := e.pool.Exec(ctx, `UPDATE orders SET accepted_at = now() - interval '90 seconds' WHERE id = $1`, o.ID); err != nil {
		t.Fatal(err)
	}
	if v := value("orders_stuck_in_accepted_seconds"); v < 89 || v > 150 {
		t.Fatalf("stuck seconds = %v, want ≈90", v)
	}
	if v := value("orders_accepted"); v != 1 {
		t.Fatalf("accepted = %v", v)
	}
	e.mustDish(t, o.ID, allDishes("cooking"), e.clock.Now().Add(time.Second))
	if v := value("orders_stuck_in_accepted_seconds"); v != 0 {
		t.Fatalf("after production started → 0, got %v", v)
	}
}
