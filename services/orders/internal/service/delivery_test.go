package service_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pavelgr1408/food-platform/pkg/kafka"
	"github.com/pavelgr1408/food-platform/services/orders/internal/clients"
	"github.com/pavelgr1408/food-platform/services/orders/internal/domain"
	"github.com/pavelgr1408/food-platform/services/orders/internal/service"
	"github.com/pavelgr1408/food-platform/services/orders/internal/testkit"
)

// deliveryOrder оформляет заказ с доставкой (адрес в радиусе ресторана 1).
func (e *env) deliveryOrder(t *testing.T, pm domain.PaymentMethod) domain.Order {
	t.Helper()
	v := e.cartWith(t, "")
	if _, err := e.svc.SetFulfillment(ctx, "", v.Cart.ID, service.FulfillmentInput{Type: domain.FulfillmentDelivery, Address: nearAddr("Москва, Арбат, 10")}); err != nil {
		t.Fatal(err)
	}
	o, err := e.svc.CreateOrder(ctx, "", service.CreateOrderInput{CartID: v.Cart.ID, PaymentMethod: pm}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return o
}

// paidDelivery — доставка, оплаченная онлайн (accepted, paymentStatus=paid).
func (e *env) paidDelivery(t *testing.T) domain.Order {
	t.Helper()
	o := e.deliveryOrder(t, domain.PayOnlineCard)
	e.mustPay(t, o.ID, "paid", e.clock.Now())
	return e.status(t, o.ID)
}

// delivery доставляет orders.delivery.status через настоящий inbox.
func (e *env) delivery(t *testing.T, orderID, status, reason string, eta *time.Time, at time.Time) error {
	t.Helper()
	body := map[string]any{"orderId": orderID, "deliveryId": uuid.NewString(), "status": status, "updatedAt": at}
	if reason != "" {
		body["reason"] = reason
	}
	if eta != nil {
		body["etaAt"] = eta
	}
	b, _ := json.Marshal(body)
	ev := kafka.Event{ID: uuid.New(), Topic: service.TopicDeliveryStatus, Key: orderID, Payload: b}
	_, err := kafka.ProcessOnce(ctx, e.pool, "orders-service-delivery", ev, e.svc.HandleDeliveryStatus)
	return err
}

func (e *env) mustDelivery(t *testing.T, orderID, status, reason string, eta *time.Time, at time.Time) {
	t.Helper()
	if err := e.delivery(t, orderID, status, reason, eta, at); err != nil {
		t.Fatalf("delivery event %s: %v", status, err)
	}
}

// ready проводит оплаченный заказ через производство до ready (как внешняя система).
func (e *env) ready(t *testing.T, id string) {
	t.Helper()
	e.mustDish(t, id, allDishes("cooking"), e.clock.Now())
	e.clock.Advance(time.Minute)
	e.mustDish(t, id, allDishes("ready"), e.clock.Now())
}

func (e *env) reasons(t *testing.T, id string) []string {
	t.Helper()
	var out []string
	for _, s := range e.statusEvents(t, id) {
		out = append(out, s.Status+"/"+s.Reason)
	}
	return out
}

func eqs(a, b []string) bool {
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

func TestZoneChecks(t *testing.T) {
	e := newEnv(t)
	v := e.cartWith(t, "")
	far := func(lat, lon float64) domain.Address {
		return domain.Address{Raw: "Дальний адрес", Lat: &lat, Lon: &lon}
	}
	deliver := func(a domain.Address) error {
		_, err := e.svc.SetFulfillment(ctx, "", v.Cart.ID, service.FulfillmentInput{Type: domain.FulfillmentDelivery, Address: a})
		return err
	}
	wantCode(t, deliver(domain.Address{Raw: "Без координат"}), "ADDRESS_LOCATION_REQUIRED")
	// ≈ 11 км от ресторана при радиусе 5 км
	err := deliver(far(55.85, 37.6184))
	var oz *service.OutOfZoneError
	if !errors.As(err, &oz) || oz.RadiusM != 5000 || oz.DistanceM < 10000 || oz.DistanceM > 12000 {
		t.Fatalf("want out of zone with distance/radius, got %v", err)
	}
	if err := deliver(nearAddr("Рядом")); err != nil {
		t.Fatalf("address in zone must be accepted: %v", err)
	}
	// радиус не задан → сравнивать не с чем, проверка радиуса пропускается (координаты по-прежнему обязательны)
	e.rest.Update(testkit.Restaurant1, func(i *clients.RestaurantInfo) { i.DeliveryRadiusM = 0 })
	if err := deliver(far(55.85, 37.6184)); err != nil {
		t.Fatalf("no radius configured: %v", err)
	}
	wantCode(t, deliver(domain.Address{Raw: "Без координат"}), "ADDRESS_LOCATION_REQUIRED")
	// самовывозу координаты не нужны
	if _, err := e.svc.SetFulfillment(ctx, "", v.Cart.ID, service.FulfillmentInput{Type: domain.FulfillmentPickup}); err != nil {
		t.Fatal(err)
	}
}

func TestCreateOrderRechecksZone(t *testing.T) {
	e := newEnv(t)
	v := e.cartWith(t, "")
	if _, err := e.svc.SetFulfillment(ctx, "", v.Cart.ID, service.FulfillmentInput{Type: domain.FulfillmentDelivery, Address: nearAddr("Арбат, 10")}); err != nil {
		t.Fatal(err)
	}
	e.rest.Update(testkit.Restaurant1, func(i *clients.RestaurantInfo) { i.DeliveryRadiusM = 100 }) // ресторан сузил зону
	_, err := e.svc.CreateOrder(ctx, "", service.CreateOrderInput{CartID: v.Cart.ID, PaymentMethod: domain.PayCourierCard}, nil)
	var oz *service.OutOfZoneError
	if !errors.As(err, &oz) || oz.RadiusM != 100 {
		t.Fatalf("want out of zone at createOrder, got %v", err)
	}
	if n := e.count(t, "orders"); n != 0 {
		t.Fatalf("no order must be created, got %d", n)
	}
	e.rest.Fail(clients.ErrUnavailable)
	_, err = e.svc.CreateOrder(ctx, "", service.CreateOrderInput{CartID: v.Cart.ID, PaymentMethod: domain.PayCourierCard}, nil)
	wantCode(t, err, "RESTAURANT_UNAVAILABLE") // fail-closed
}

func TestStateCarriesDeliveryLocation(t *testing.T) {
	e := newEnv(t)
	d := e.deliveryOrder(t, domain.PayCourierCard)
	p := e.order(t, "", domain.PayOnlineCard)
	for _, r := range e.outbox(t) {
		if r.Topic != service.TopicState {
			continue
		}
		var ev map[string]any
		_ = json.Unmarshal(r.Payload, &ev)
		switch r.Key {
		case d.ID:
			loc, _ := ev["deliveryLocation"].(map[string]any)
			if loc == nil || loc["lat"] != 55.764 || loc["lon"] != 37.605 || ev["deliveryAddress"] != "Москва, Арбат, 10" {
				t.Fatalf("delivery state must carry the location: %v", ev)
			}
		case p.ID:
			if _, has := ev["deliveryLocation"]; has {
				t.Fatalf("pickup state must not carry a location: %v", ev)
			}
		}
	}
}

func TestDeliveryLifecyclePrepaid(t *testing.T) {
	e := newEnv(t)
	o := e.paidDelivery(t)
	e.ready(t, o.ID)
	t0 := e.clock.Now()
	e.mustDelivery(t, o.ID, "searching", "", nil, t0)
	e.mustDelivery(t, o.ID, "assigned", "", nil, t0.Add(time.Second))
	if got := e.status(t, o.ID); got.Status != domain.StatusReady || got.Delivery == nil || got.Delivery.Status != domain.DeliveryAssigned {
		t.Fatalf("assigned must not move a ready order, snapshot must be stored: %+v", got)
	}
	e.mustDelivery(t, o.ID, "picked_up", "", nil, t0.Add(2*time.Second))
	if got := e.status(t, o.ID); got.Status != domain.StatusOnDelivery {
		t.Fatalf("picked_up → on_delivery, got %s", got.Status)
	}
	e.mustDelivery(t, o.ID, "en_route", "", nil, t0.Add(3*time.Second)) // повтор on_delivery — без эффекта
	e.mustDelivery(t, o.ID, "delivered", "", nil, t0.Add(4*time.Second))
	got := e.status(t, o.ID)
	if got.Status != domain.StatusClosed {
		t.Fatalf("delivered + paid → closed, got %s", got.Status)
	}
	want := []string{"pending_payment/", "accepted/paid", "cooking/production", "ready/production",
		"on_delivery/courier_picked_up", "delivered/delivered", "closed/delivered"}
	if gotR := e.reasons(t, o.ID); !eqs(gotR, want) {
		t.Fatalf("status events:\n got %v\nwant %v", gotR, want)
	}
}

func TestDeliveryAheadOfProductionIsDeferredUntilReady(t *testing.T) {
	e := newEnv(t)
	o := e.paidDelivery(t)
	t0 := e.clock.Now()
	e.mustDelivery(t, o.ID, "picked_up", "", nil, t0)
	e.mustDelivery(t, o.ID, "delivered", "", nil, t0.Add(time.Second))
	if got := e.status(t, o.ID); got.Status != domain.StatusAccepted || got.Delivery.Status != domain.DeliveryDelivered {
		t.Fatalf("a not yet ready order must not move: %+v", got)
	}
	e.mustDish(t, o.ID, allDishes("cooking"), e.clock.Now())
	if got := e.status(t, o.ID); got.Status != domain.StatusCooking {
		t.Fatalf("still cooking: %s", got.Status)
	}
	e.clock.Advance(time.Minute)
	e.mustDish(t, o.ID, allDishes("ready"), e.clock.Now()) // готовность догоняет доставку в одной транзакции
	if got := e.status(t, o.ID); got.Status != domain.StatusClosed {
		t.Fatalf("ready must catch up with delivery: %s", got.Status)
	}
	want := []string{"pending_payment/", "accepted/paid", "cooking/production", "ready/production",
		"on_delivery/courier_picked_up", "delivered/delivered", "closed/delivered"}
	if gotR := e.reasons(t, o.ID); !eqs(gotR, want) {
		t.Fatalf("status events:\n got %v\nwant %v", gotR, want)
	}
}

func TestStaleDeliveryEventIgnored(t *testing.T) {
	e := newEnv(t)
	o := e.paidDelivery(t)
	e.ready(t, o.ID)
	t0 := e.clock.Now()
	e.mustDelivery(t, o.ID, "en_route", "", nil, t0.Add(10*time.Second))
	e.mustDelivery(t, o.ID, "assigned", "", nil, t0.Add(5*time.Second))  // опоздавшее событие
	e.mustDelivery(t, o.ID, "en_route", "", nil, t0.Add(10*time.Second)) // повтор того же снимка
	got := e.status(t, o.ID)
	if got.Status != domain.StatusOnDelivery || got.Delivery.Status != domain.DeliveryEnRoute {
		t.Fatalf("stale event must not roll the snapshot back: %+v", got)
	}
}

func TestPayOnDeliveryClosesWhenPaymentConfirmed(t *testing.T) {
	e := newEnv(t)
	o := e.deliveryOrder(t, domain.PayCourierCard) // принят сразу, оплата — курьеру
	if o.Status != domain.StatusAccepted {
		t.Fatalf("status %s", o.Status)
	}
	e.ready(t, o.ID)
	t0 := e.clock.Now()
	e.mustDelivery(t, o.ID, "picked_up", "", nil, t0)
	e.mustDelivery(t, o.ID, "delivered", "", nil, t0.Add(time.Second))
	if got := e.status(t, o.ID); got.Status != domain.StatusDelivered {
		t.Fatalf("unpaid delivered order must wait for the payment, got %s", got.Status)
	}
	e.mustPay(t, o.ID, "paid", e.clock.Now())
	if got := e.status(t, o.ID); got.Status != domain.StatusClosed || got.PaymentStatus != domain.PaymentPaid {
		t.Fatalf("payment confirmation closes the delivered order: %+v", got)
	}
}

func TestUndeliveredBecomesNotReceived(t *testing.T) {
	e := newEnv(t)
	o := e.paidDelivery(t)
	e.ready(t, o.ID)
	t0 := e.clock.Now()
	e.mustDelivery(t, o.ID, "en_route", "", nil, t0)
	e.mustDelivery(t, o.ID, "failed", "undelivered", nil, t0.Add(time.Second))
	got := e.status(t, o.ID)
	if got.Status != domain.StatusNotReceived || got.StatusReason != "undelivered" {
		t.Fatalf("undelivered → not_received: %s/%s", got.Status, got.StatusReason)
	}
}

func TestNoCourierKeepsOrderAndAllowsIssue(t *testing.T) {
	e := newEnv(t)
	o := e.paidDelivery(t)
	t0 := e.clock.Now()
	e.mustDelivery(t, o.ID, "failed", "no_courier", nil, t0) // курьера не нашли, пока заказ готовился
	e.ready(t, o.ID)
	got := e.status(t, o.ID)
	if got.Status != domain.StatusReady || got.Delivery == nil || got.Delivery.Hint() != "pickup" {
		t.Fatalf("order must stay ready with a pickup hint: %+v", got)
	}
	// сотрудник выдаёт заказ в ресторане (E2)
	closed, err := e.svc.IssueOrder(ctx, staff1, o.ID)
	if err != nil || closed.Status != domain.StatusClosed {
		t.Fatalf("issue a no-courier delivery order: %+v %v", closed, err)
	}
	// обычную доставку (курьер ещё может приехать) выдать нельзя
	o2 := e.paidDelivery(t)
	e.ready(t, o2.ID)
	_, err = e.svc.IssueOrder(ctx, staff1, o2.ID)
	wantCode(t, err, "NOT_PICKUP_ORDER")
	e.mustDelivery(t, o2.ID, "assigned", "", nil, e.clock.Now())
	_, err = e.svc.IssueOrder(ctx, staff1, o2.ID)
	wantCode(t, err, "NOT_PICKUP_ORDER")
}

func TestNoCourierUnpaidCourierOrderNeedsPaymentBeforeIssue(t *testing.T) {
	e := newEnv(t)
	o := e.deliveryOrder(t, domain.PayCourierCard)
	e.ready(t, o.ID)
	e.mustDelivery(t, o.ID, "failed", "no_courier", nil, e.clock.Now())
	_, err := e.svc.IssueOrder(ctx, staff1, o.ID)
	wantCode(t, err, "ORDER_NOT_PAID")
	e.mustPay(t, o.ID, "paid", e.clock.Now()) // клиент оплатил по ссылке
	if closed, err := e.svc.IssueOrder(ctx, staff1, o.ID); err != nil || closed.Status != domain.StatusClosed {
		t.Fatalf("%+v %v", closed, err)
	}
}

func TestNoCourierOrderExpiresAsNotReceived(t *testing.T) {
	e := newEnv(t)
	o := e.paidDelivery(t)
	e.ready(t, o.ID)
	e.clock.Advance(3 * time.Hour) // заказ давно готов, но решение «курьера нет» принято только сейчас
	e.mustDelivery(t, o.ID, "failed", "no_courier", nil, e.clock.Now())
	if n, err := e.svc.ExpireNotReceived(ctx); err != nil || n != 0 {
		t.Fatalf("the customer has just been told to pick up: n=%d err=%v", n, err)
	}
	e.clock.Advance(2*time.Hour + time.Minute)
	n, err := e.svc.ExpireNotReceived(ctx)
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if got := e.status(t, o.ID); got.Status != domain.StatusNotReceived || got.StatusReason != "pickup_timeout" {
		t.Fatalf("%s/%s", got.Status, got.StatusReason)
	}
	// доставка с живым курьером таймером неполучения не затрагивается
	o2 := e.paidDelivery(t)
	e.ready(t, o2.ID)
	e.mustDelivery(t, o2.ID, "assigned", "", nil, e.clock.Now())
	e.clock.Advance(24 * time.Hour)
	if n, _ := e.svc.ExpireNotReceived(ctx); n != 0 {
		t.Fatalf("delivery with a courier must not expire: %d", n)
	}
}

func TestDeliveryEventValidation(t *testing.T) {
	e := newEnv(t)
	pickup := e.order(t, "", domain.PayOnlineCard)
	if err := e.delivery(t, pickup.ID, "picked_up", "", nil, e.clock.Now()); err != nil {
		t.Fatalf("delivery event for a pickup order is skipped, not retried: %v", err)
	}
	if got := e.status(t, pickup.ID); got.Delivery != nil {
		t.Fatalf("pickup order must not get a delivery snapshot: %+v", got.Delivery)
	}
	o := e.paidDelivery(t)
	for name, f := range map[string]func() error{
		"unknown order":   func() error { return e.delivery(t, uuid.NewString(), "assigned", "", nil, e.clock.Now()) },
		"unknown status":  func() error { return e.delivery(t, o.ID, "teleported", "", nil, e.clock.Now()) },
		"unknown reason":  func() error { return e.delivery(t, o.ID, "failed", "lazy", nil, e.clock.Now()) },
		"no updatedAt":    func() error { return e.delivery(t, o.ID, "assigned", "", nil, time.Time{}) },
		"not an order id": func() error { return e.delivery(t, "x", "assigned", "", nil, e.clock.Now()) },
	} {
		if err := f(); err == nil {
			t.Errorf("%s: must fail (retry/DLQ)", name)
		}
	}
}

func TestDeliveryEta(t *testing.T) {
	e := newEnv(t)
	e.mustLead(t, testkit.Restaurant1, 25, e.clock.Now())
	o := e.paidDelivery(t)
	// оценки курьерской системы нет → типовое время доставки, приблизительно
	eta, err := e.svc.GetEta(ctx, "", o.ID)
	if err != nil || eta.DeliveryAt == nil || !eta.Approximate || !eta.DeliveryAt.Equal(eta.ReadyAt.Add(30*time.Minute)) {
		t.Fatalf("typical eta: %v %+v", err, eta)
	}
	// свежая оценка курьерской системы → deliveryAt = она, approximate=false (оценка производства свежая)
	courierEta := e.clock.Now().Add(55 * time.Minute)
	e.mustDelivery(t, o.ID, "assigned", "", &courierEta, e.clock.Now())
	eta, _ = e.svc.GetEta(ctx, "", o.ID)
	if eta.DeliveryAt == nil || !eta.DeliveryAt.Equal(courierEta) || eta.Approximate {
		t.Fatalf("courier eta must be used: %+v (want %v)", eta, courierEta)
	}
	// оценка курьерской раньше готовности не может опередить готовность
	early := e.clock.Now().Add(time.Minute)
	e.mustDelivery(t, o.ID, "assigned", "", &early, e.clock.Now().Add(time.Second))
	eta, _ = e.svc.GetEta(ctx, "", o.ID)
	if !eta.DeliveryAt.Equal(eta.ReadyAt) {
		t.Fatalf("delivery cannot precede readiness: %+v", eta)
	}
	// устаревшая оценка (снимок старше DELIVERY_ETA_TTL) → снова типовое
	e.mustDelivery(t, o.ID, "assigned", "", &courierEta, e.clock.Now().Add(2*time.Second))
	e.clock.Advance(40 * time.Minute)
	e.mustLead(t, testkit.Restaurant1, 25, e.clock.Now())
	eta, _ = e.svc.GetEta(ctx, "", o.ID)
	if eta.DeliveryAt == nil || !eta.Approximate {
		t.Fatalf("stale courier eta falls back to typical: %+v", eta)
	}
}

func TestDeliveryEtaTerminalStates(t *testing.T) {
	e := newEnv(t)
	o := e.paidDelivery(t)
	e.ready(t, o.ID)
	t0 := e.clock.Now()
	e.mustDelivery(t, o.ID, "picked_up", "", nil, t0)
	e.clock.Advance(7 * time.Minute)
	e.mustDelivery(t, o.ID, "delivered", "", nil, e.clock.Now())
	deliveredAt := e.clock.Now()
	e.clock.Advance(time.Hour)
	eta, err := e.svc.GetEta(ctx, "", o.ID)
	if err != nil || eta.DeliveryAt == nil || !eta.DeliveryAt.Equal(deliveredAt) || eta.Approximate || eta.EstimateMinutes != 0 {
		t.Fatalf("delivered: the actual time, no estimate: %v %+v", err, eta)
	}
	f := e.paidDelivery(t)
	e.ready(t, f.ID)
	e.mustDelivery(t, f.ID, "failed", "no_courier", nil, e.clock.Now())
	eta, _ = e.svc.GetEta(ctx, "", f.ID)
	if eta.DeliveryAt != nil {
		t.Fatalf("no delivery will happen: %+v", eta)
	}
}
