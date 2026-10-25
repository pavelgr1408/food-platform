package service_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pavelgr1408/food-platform/pkg/httpx"
	"github.com/pavelgr1408/food-platform/pkg/kafka"
	"github.com/pavelgr1408/food-platform/services/delivery/internal/courier"
	"github.com/pavelgr1408/food-platform/services/delivery/internal/location"
	"github.com/pavelgr1408/food-platform/services/delivery/internal/service"
	"github.com/pavelgr1408/food-platform/services/delivery/internal/testkit"
)

var ctx = context.Background()

const restaurantID = "11111111-1111-4111-8111-111111111111"

type env struct {
	svc   *service.Service
	pool  *pgxpool.Pool
	cr    *testkit.FakeCourier
	rest  *testkit.FakeRestaurants
	locs  *location.Memory
	clock *testkit.Clock
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{pool: testkit.Pool(t), cr: testkit.NewCourier(), rest: testkit.NewRestaurants(), locs: location.NewMemory(), clock: testkit.NewClock()}
	e.locs.Now = e.clock.Now
	e.svc = service.New(service.Deps{Pool: e.pool, Courier: e.cr, Restaurants: e.rest, Locations: e.locs, Log: testkit.Log(), Clock: e.clock.Now,
		Config: service.Config{SearchTimeout: 10 * time.Minute, LocationTTL: time.Minute, PollEvery: 3 * time.Second, PollMax: 30 * time.Second,
			Lease: 30 * time.Second, LinkWait: 20 * time.Second}})
	return e
}

type orderOpt struct {
	method, payStatus, owner, fulfillment string
	noLocation                            bool
}

func (e *env) state(t *testing.T, id string, rev int, o orderOpt) {
	t.Helper()
	if o.fulfillment == "" {
		o.fulfillment = "delivery"
	}
	if o.method == "" {
		o.method = "online"
	}
	if o.payStatus == "" {
		o.payStatus = "paid"
	}
	body := map[string]any{"orderId": id, "revision": rev, "orderNumber": "R1-261025-0001", "restaurantId": restaurantID,
		"fulfillmentType": o.fulfillment, "deliveryAddress": "Москва, Арбат, 10",
		"customer":    map[string]any{"userId": o.owner, "guest": o.owner == "", "name": "Иван", "phone": "+79990001122"},
		"totalAmount": map[string]any{"amount": 125000, "currency": "RUB"}, "paymentMethod": o.method, "paymentStatus": o.payStatus,
		"createdAt": e.clock.Now()}
	if !o.noLocation {
		body["deliveryLocation"] = map[string]any{"lat": 55.7522, "lon": 37.5920}
	}
	e.consume(t, service.TopicState, body)
}

func (e *env) consume(t *testing.T, topic string, body any) {
	t.Helper()
	b, _ := json.Marshal(body)
	ev := kafka.Event{ID: uuid.New(), Topic: topic, Key: "k", Payload: b}
	if _, err := kafka.ProcessOnce(ctx, e.pool, "delivery-service", ev, e.svc.HandleEvent); err != nil {
		t.Fatalf("consume %s: %v", topic, err)
	}
}

func (e *env) cancelOrder(t *testing.T, id string) {
	t.Helper()
	e.consume(t, service.TopicStatus, map[string]any{"orderId": id, "status": "cancelled", "updatedAt": e.clock.Now()})
}

func (e *env) link(t *testing.T, id, url string) {
	t.Helper()
	e.consume(t, service.TopicLink, map[string]any{"orderId": id, "paymentId": uuid.NewString(), "paymentUrl": url})
}

// tick двигает часы на один период опроса и выполняет проход reconciler'а.
func (e *env) tick(t *testing.T) int {
	t.Helper()
	e.clock.Advance(4 * time.Second)
	n, err := e.svc.Reconcile(ctx)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	return n
}

func (e *env) delivery(t *testing.T, id string) (string, string) {
	t.Helper()
	d, err := e.svc.Get(ctx, service.Actor{}, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	return string(d.Status), string(d.FailReason)
}

func (e *env) deliveryID(t *testing.T, orderID string) string {
	t.Helper()
	d, err := e.svc.Get(ctx, service.Actor{}, orderID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	return d.ID
}

func (e *env) events(t *testing.T, orderID string) []service.StatusEvent {
	t.Helper()
	rows, err := e.pool.Query(ctx, `SELECT payload FROM outbox WHERE topic = 'orders.delivery.status' AND key = $1 ORDER BY id`, orderID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []service.StatusEvent
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			t.Fatal(err)
		}
		var ev service.StatusEvent
		if err := json.Unmarshal(b, &ev); err != nil {
			t.Fatal(err)
		}
		out = append(out, ev)
	}
	return out
}

func statuses(evs []service.StatusEvent) []string {
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

func wantCode(t *testing.T, err error, want string) {
	t.Helper()
	var he *httpx.Error
	if !errors.As(err, &he) || he.Code != want {
		t.Fatalf("want %s, got %v", want, err)
	}
}

func (e *env) cb(t *testing.T, id, eventID, status string, mod func(*service.Callback)) error {
	t.Helper()
	c := service.Callback{EventID: eventID, OrderID: id, Status: status, Timestamp: e.clock.Now()}
	if mod != nil {
		mod(&c)
	}
	return e.svc.HandleCallback(ctx, c)
}

func TestFullLifecycleWithLocation(t *testing.T) {
	e := newEnv(t)
	id := uuid.NewString()
	e.state(t, id, 1, orderOpt{})
	if st, _ := e.delivery(t, id); st != "searching" {
		t.Fatalf("status = %s", st)
	}
	e.tick(t) // заявка курьерской системе
	did := e.deliveryID(t, id)
	job, ok := e.cr.Job(did)
	if !ok {
		t.Fatal("courier job not created")
	}
	if job.Req.Dropoff.Lat != 55.7522 || job.Req.Pickup.Lat != 55.7512 || job.Req.Payment != nil {
		t.Fatalf("bad request: %+v", job.Req)
	}
	eta := e.clock.Now().Add(25 * time.Minute)
	e.cr.Set(did, func(s *courier.State) {
		s.Status, s.CourierID, s.CourierName, s.CourierPhone, s.EtaAt = "assigned", "c1", "Пётр", "+79991112233", &eta
	})
	e.tick(t)
	d, _ := e.svc.Get(ctx, service.Actor{}, id)
	if d.Status != "assigned" || d.CourierName != "Пётр" {
		t.Fatalf("assigned expected: %+v", d)
	}
	if _, err := e.svc.Location(ctx, service.Actor{}, id); err == nil {
		t.Fatal("no location expected before pickup")
	} else {
		wantCode(t, err, "LOCATION_NOT_AVAILABLE")
	}
	e.cr.Set(did, func(s *courier.State) {
		s.Status = "picked_up"
		s.Location, s.LocationAt = &courier.Point{Lat: 55.75, Lon: 37.61}, e.clock.Now()
	})
	e.tick(t)
	p, err := e.svc.Location(ctx, service.Actor{}, id)
	if err != nil || p.Lat != 55.75 {
		t.Fatalf("location: %+v %v", p, err)
	}
	e.cr.Set(did, func(s *courier.State) { s.Status = "en_route" })
	e.tick(t)
	e.cr.Set(did, func(s *courier.State) { s.Status = "delivered" })
	e.tick(t)
	d, _ = e.svc.Get(ctx, service.Actor{}, id)
	if d.Status != "delivered" || d.CourierName != "" || d.CourierPhone != "" {
		t.Fatalf("delivered must hide courier contacts: %+v", d)
	}
	if _, err := e.svc.Location(ctx, service.Actor{}, id); err == nil {
		t.Fatal("location must be gone after delivery")
	}
	if got := statuses(e.events(t, id)); !eq(got, []string{"searching", "assigned", "assigned", "picked_up", "en_route", "delivered"}) &&
		!eq(got, []string{"searching", "assigned", "picked_up", "en_route", "delivered"}) {
		t.Fatalf("event statuses = %v", got)
	}
	// updatedAt строго растёт
	evs := e.events(t, id)
	for i := 1; i < len(evs); i++ {
		if !evs[i].UpdatedAt.After(evs[i-1].UpdatedAt) {
			t.Fatalf("updatedAt not monotonic at %d: %v <= %v", i, evs[i].UpdatedAt, evs[i-1].UpdatedAt)
		}
	}
	if last := evs[len(evs)-1]; last.EtaAt != nil {
		t.Fatalf("terminal event must not carry eta: %+v", last)
	}
}

func TestOrderSelection(t *testing.T) {
	e := newEnv(t)
	pickup, prepay, courierPay, noLoc := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	e.state(t, pickup, 1, orderOpt{fulfillment: "pickup"})
	e.state(t, prepay, 1, orderOpt{payStatus: "pending"})
	e.state(t, courierPay, 1, orderOpt{method: "courier", payStatus: "pending"})
	e.state(t, noLoc, 1, orderOpt{noLocation: true})
	for _, id := range []string{pickup, prepay, noLoc} {
		_, err := e.svc.Get(ctx, service.Actor{}, id)
		wantCode(t, err, "DELIVERY_NOT_FOUND")
	}
	if st, _ := e.delivery(t, courierPay); st != "searching" {
		t.Fatalf("pay-on-delivery order must be taken at once, got %s", st)
	}
	// предоплата подтверждена — следующая ревизия берёт заказ в доставку (ADR-10)
	e.state(t, prepay, 2, orderOpt{})
	if st, _ := e.delivery(t, prepay); st != "searching" {
		t.Fatalf("paid order must be taken, got %s", st)
	}
	// старая ревизия не откатывает
	e.state(t, prepay, 1, orderOpt{payStatus: "pending"})
	if st, _ := e.delivery(t, prepay); st != "searching" {
		t.Fatalf("stale revision must be ignored, got %s", st)
	}
	// некорректное событие уходит в DLQ (ошибка), а не молча теряется
	b, _ := json.Marshal(map[string]any{"orderId": "bad", "revision": 1})
	if _, err := kafka.ProcessOnce(ctx, e.pool, "delivery-service", kafka.Event{ID: uuid.New(), Topic: service.TopicState, Key: "k", Payload: b}, e.svc.HandleEvent); err == nil {
		t.Fatal("invalid payload must fail")
	}
}

func TestDuplicateAndBackwardWebhooks(t *testing.T) {
	e := newEnv(t)
	id := uuid.NewString()
	e.state(t, id, 1, orderOpt{})
	e.tick(t)
	if err := e.cb(t, id, "ev1", "assigned", func(c *service.Callback) { c.CourierID, c.CourierName = "c1", "Пётр" }); err != nil {
		t.Fatal(err)
	}
	if err := e.cb(t, id, "ev1", "assigned", nil); err != nil { // дубликат
		t.Fatal(err)
	}
	if err := e.cb(t, id, "ev2", "en_route", nil); err != nil { // пропуск picked_up допустим: статусы идут вперёд
		t.Fatal(err)
	}
	if err := e.cb(t, id, "ev3", "assigned", nil); err != nil { // откат игнорируется
		t.Fatal(err)
	}
	if st, _ := e.delivery(t, id); st != "en_route" {
		t.Fatalf("status = %s", st)
	}
	if err := e.cb(t, id, "ev4", "delivered", func(c *service.Callback) { c.Paid = true }); err != nil {
		t.Fatal(err)
	}
	if err := e.cb(t, id, "ev5", "failed", func(c *service.Callback) { c.Reason = "undelivered" }); err != nil { // терминальный неизменен
		t.Fatal(err)
	}
	if st, _ := e.delivery(t, id); st != "delivered" {
		t.Fatalf("terminal must be immutable, got %s", st)
	}
	if got := statuses(e.events(t, id)); !eq(got, []string{"searching", "assigned", "en_route", "delivered"}) {
		t.Fatalf("event statuses = %v", got)
	}
	// вебхук для неизвестного заказа — 404 (курьерская система повторит), неверная структура — ошибка валидации
	wantCode(t, e.cb(t, uuid.NewString(), "ev9", "assigned", nil), "DELIVERY_NOT_FOUND")
	if err := e.cb(t, id, "", "assigned", nil); !service.IsValidation(err) {
		t.Fatalf("want validation error, got %v", err)
	}
	if err := e.cb(t, id, "ev10", "teleported", nil); !service.IsValidation(err) {
		t.Fatalf("want validation error, got %v", err)
	}
}

func TestNoCourierAfterSearchTimeout(t *testing.T) {
	e := newEnv(t)
	id := uuid.NewString()
	e.state(t, id, 1, orderOpt{})
	e.tick(t)
	e.clock.Advance(11 * time.Minute)
	e.tick(t)
	d, err := e.svc.Get(ctx, service.Actor{}, id)
	if err != nil {
		t.Fatal(err)
	}
	if d.Status != "failed" || d.FailReason != "no_courier" || d.Hint() != "pickup" {
		t.Fatalf("want failed/no_courier with pickup hint, got %+v", d)
	}
	job, _ := e.cr.Job(d.ID)
	if !job.Cancelled {
		t.Fatal("courier job must be cancelled on timeout")
	}
	if got := statuses(e.events(t, id)); !eq(got, []string{"searching", "failed"}) {
		t.Fatalf("event statuses = %v", got)
	}
	if last := e.events(t, id); last[len(last)-1].Reason != "no_courier" {
		t.Fatalf("reason = %q", last[len(last)-1].Reason)
	}
}

func TestCourierAssignedWhileCancellingOnTimeout(t *testing.T) {
	e := newEnv(t)
	id := uuid.NewString()
	e.state(t, id, 1, orderOpt{})
	e.tick(t)
	did := e.deliveryID(t, id)
	e.cr.Set(did, func(s *courier.State) { s.Status, s.CourierID = "picked_up", "c1" }) // успел выехать
	e.clock.Advance(11 * time.Minute)
	e.tick(t)
	if st, _ := e.delivery(t, id); st != "picked_up" {
		t.Fatalf("a courier on the way must not be dropped by search timeout, got %s", st)
	}
}

func TestCreateRetriesAndIdempotency(t *testing.T) {
	e := newEnv(t)
	id := uuid.NewString()
	e.state(t, id, 1, orderOpt{})
	e.cr.LostReply = testkit.ErrDown // заявка создана, ответ потерян
	e.tick(t)
	if e.cr.Jobs() != 1 {
		t.Fatalf("jobs = %d", e.cr.Jobs())
	}
	e.cr.LostReply = nil
	for i := 0; i < 5; i++ {
		e.clock.Advance(30 * time.Second) // дольше любого backoff
		e.tick(t)
	}
	if e.cr.Jobs() != 1 {
		t.Fatalf("retry must reuse the same job by idempotency key, jobs = %d", e.cr.Jobs())
	}
	d, _ := e.svc.Get(ctx, service.Actor{}, id)
	if d.CourierRef == "" {
		t.Fatal("courier reference must be stored after retry")
	}
}

func TestCreateRejectedFailsDelivery(t *testing.T) {
	e := newEnv(t)
	id := uuid.NewString()
	e.state(t, id, 1, orderOpt{})
	e.cr.CreateErr = &courier.RejectedError{HTTPStatus: 422, Code: "OUT_OF_ZONE", Message: "no couriers there"}
	e.tick(t)
	if st, why := e.delivery(t, id); st != "failed" || why != "no_courier" {
		t.Fatalf("got %s/%s", st, why)
	}
}

func TestRestaurantUnavailableRetries(t *testing.T) {
	e := newEnv(t)
	id := uuid.NewString()
	e.state(t, id, 1, orderOpt{})
	e.rest.Err = testkit.ErrDown
	e.tick(t)
	if e.cr.Jobs() != 0 {
		t.Fatal("courier must not be called without restaurant coordinates")
	}
	e.rest.Err = nil
	e.clock.Advance(time.Minute)
	e.tick(t)
	if e.cr.Jobs() != 1 {
		t.Fatalf("jobs = %d", e.cr.Jobs())
	}
}

func TestCancelOrder(t *testing.T) {
	t.Run("после заявки", func(t *testing.T) {
		e := newEnv(t)
		id := uuid.NewString()
		e.state(t, id, 1, orderOpt{})
		e.tick(t)
		e.cancelOrder(t, id)
		e.tick(t)
		if st, why := e.delivery(t, id); st != "failed" || why != "cancelled" {
			t.Fatalf("got %s/%s", st, why)
		}
		job, _ := e.cr.Job(e.deliveryID(t, id))
		if !job.Cancelled {
			t.Fatal("courier job must be cancelled")
		}
		d, _ := e.svc.Get(ctx, service.Actor{}, id)
		if d.Hint() != "" {
			t.Fatal("no pickup hint for cancelled order")
		}
	})
	t.Run("до заявки", func(t *testing.T) {
		e := newEnv(t)
		id := uuid.NewString()
		e.state(t, id, 1, orderOpt{})
		e.cancelOrder(t, id)
		e.tick(t)
		if e.cr.Jobs() != 0 {
			t.Fatal("no courier request for a cancelled order")
		}
		if st, why := e.delivery(t, id); st != "failed" || why != "cancelled" {
			t.Fatalf("got %s/%s", st, why)
		}
	})
	t.Run("отмена обогнала orders.state", func(t *testing.T) {
		e := newEnv(t)
		id := uuid.NewString()
		e.cancelOrder(t, id)
		e.state(t, id, 1, orderOpt{})
		_, err := e.svc.Get(ctx, service.Actor{}, id)
		wantCode(t, err, "DELIVERY_NOT_FOUND")
	})
	t.Run("после доставки не трогаем", func(t *testing.T) {
		e := newEnv(t)
		id := uuid.NewString()
		e.state(t, id, 1, orderOpt{})
		e.tick(t)
		if err := e.cb(t, id, "e1", "delivered", nil); err != nil {
			t.Fatal(err)
		}
		e.cancelOrder(t, id)
		e.tick(t)
		if st, _ := e.delivery(t, id); st != "delivered" {
			t.Fatalf("status = %s", st)
		}
	})
}

func TestPayOnDelivery(t *testing.T) {
	t.Run("ссылка пришла до заявки", func(t *testing.T) {
		e := newEnv(t)
		id := uuid.NewString()
		e.link(t, id, "https://pay.test/p/1")
		e.state(t, id, 1, orderOpt{method: "courier", payStatus: "pending"})
		e.tick(t)
		job, ok := e.cr.Job(e.deliveryID(t, id))
		if !ok || job.Req.Payment == nil || job.Req.Payment.URL != "https://pay.test/p/1" || job.Req.Payment.Amount != 125000 {
			t.Fatalf("payment must be passed with the request: %+v", job.Req.Payment)
		}
		d, _ := e.svc.Get(ctx, service.Actor{}, id)
		if !d.PayOnDelivery {
			t.Fatal("payOnDelivery expected")
		}
	})
	t.Run("ссылку ждём, затем доставляем без неё", func(t *testing.T) {
		e := newEnv(t)
		id := uuid.NewString()
		e.state(t, id, 1, orderOpt{method: "courier", payStatus: "pending"})
		e.tick(t)
		if e.cr.Jobs() != 0 {
			t.Fatal("must wait for the payment link before the request")
		}
		e.clock.Advance(25 * time.Second)
		e.tick(t)
		if e.cr.Jobs() != 1 {
			t.Fatal("after LINK_WAIT the courier is requested anyway")
		}
		e.link(t, id, "https://pay.test/p/2") // ссылка догнала: передаётся при ближайшем опросе
		e.tick(t)
		e.tick(t)
		job, _ := e.cr.Job(e.deliveryID(t, id))
		if job.Payment == nil || job.Payment.URL != "https://pay.test/p/2" {
			t.Fatalf("late link must reach the courier system: %+v", job.Payment)
		}
		if e.cr.Calls.SetPayment != 1 {
			t.Fatalf("set_payment calls = %d", e.cr.Calls.SetPayment)
		}
	})
}

func TestAccessMatrix(t *testing.T) {
	e := newEnv(t)
	guest, owned := uuid.NewString(), uuid.NewString()
	e.state(t, guest, 1, orderOpt{})
	e.state(t, owned, 1, orderOpt{owner: "u1"})
	cases := []struct {
		name  string
		a     service.Actor
		order string
		ok    bool
	}{
		{"гость знает UUID гостевого заказа", service.Actor{}, guest, true},
		{"владелец", service.Actor{UserID: "u1"}, owned, true},
		{"анонимный к чужому", service.Actor{}, owned, false},
		{"другой пользователь", service.Actor{UserID: "u2"}, owned, false},
		{"сотрудник своего ресторана", service.Actor{UserID: "s", Staff: true, Restaurants: []string{restaurantID}}, owned, true},
		{"сотрудник чужого ресторана", service.Actor{UserID: "s", Staff: true, Restaurants: []string{uuid.NewString()}}, owned, false},
		{"HQ", service.Actor{UserID: "h", Staff: true, HQ: true}, owned, true},
		{"не UUID", service.Actor{}, "nope", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := e.svc.Get(ctx, c.a, c.order)
			if c.ok && err != nil {
				t.Fatalf("want access, got %v", err)
			}
			if !c.ok {
				wantCode(t, err, "DELIVERY_NOT_FOUND")
			}
		})
	}
}

type brokenLocs struct{ location.Store }

func (brokenLocs) Get(context.Context, string) (location.Point, error) {
	return location.Point{}, testkit.ErrDown
}

func TestLocationStoreDown(t *testing.T) {
	e := newEnv(t)
	id := uuid.NewString()
	e.state(t, id, 1, orderOpt{})
	e.tick(t)
	did := e.deliveryID(t, id)
	e.cr.Set(did, func(s *courier.State) { s.Status, s.CourierID = "en_route", "c1" })
	e.tick(t)
	svc := service.New(service.Deps{Pool: e.pool, Courier: e.cr, Restaurants: e.rest, Locations: brokenLocs{e.locs}, Log: testkit.Log(), Clock: e.clock.Now})
	_, err := svc.Location(ctx, service.Actor{}, id)
	wantCode(t, err, "LOCATION_STORE_UNAVAILABLE")
}

func TestLocationExpires(t *testing.T) {
	e := newEnv(t)
	id := uuid.NewString()
	e.state(t, id, 1, orderOpt{})
	e.tick(t)
	did := e.deliveryID(t, id)
	e.cr.Set(did, func(s *courier.State) {
		s.Status, s.CourierID = "en_route", "c1"
		s.Location, s.LocationAt = &courier.Point{Lat: 55.7, Lon: 37.6}, e.clock.Now()
	})
	e.tick(t)
	if _, err := e.svc.Location(ctx, service.Actor{}, id); err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(2 * time.Minute) // курьерская система молчит дольше LOCATION_TTL
	_, err := e.svc.Location(ctx, service.Actor{}, id)
	wantCode(t, err, "LOCATION_NOT_AVAILABLE")
}

func TestCourierSystemForgotDelivery(t *testing.T) {
	e := newEnv(t)
	id := uuid.NewString()
	e.state(t, id, 1, orderOpt{})
	e.tick(t)
	did := e.deliveryID(t, id)
	e.cr.StatusErr = courier.ErrNotFound
	e.tick(t) // ссылка сброшена
	e.cr.StatusErr = nil
	e.tick(t) // повторная заявка по тому же ключу
	if e.cr.Jobs() != 1 {
		t.Fatalf("re-dispatch must reuse the key, jobs = %d", e.cr.Jobs())
	}
	if d, _ := e.svc.Get(ctx, service.Actor{}, id); d.ID != did || d.CourierRef == "" {
		t.Fatalf("delivery must be re-linked: %+v", d)
	}
}
