package service_test

import (
	"testing"
	"time"

	"github.com/pavelgr1408/food-platform/services/orders/internal/domain"
	"github.com/pavelgr1408/food-platform/services/orders/internal/service"
	"github.com/pavelgr1408/food-platform/services/orders/internal/testkit"
)

func (e *env) queue(t *testing.T, a service.Actor, in service.QueueInput) ([]string, service.OrderPage) {
	t.Helper()
	p, err := e.svc.ListRestaurantOrders(ctx, a, in)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, len(p.Items))
	for i, o := range p.Items {
		ids[i] = o.ID
	}
	return ids, p
}

// moveTo переносит заказ в другой ресторан (меню второго ресторана в тестовом окружении нет).
func (e *env) moveTo(t *testing.T, orderID, restaurantID string) {
	t.Helper()
	if _, err := e.pool.Exec(ctx, `UPDATE orders SET restaurant_id = $1 WHERE id = $2`, restaurantID, orderID); err != nil {
		t.Fatal(err)
	}
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

// Долг №89: сотрудник видит заказы только своих ресторанов, очередь — старые первыми, StaffOrder с контактом.
func TestQueueScopedToStaffRestaurantsOldestFirst(t *testing.T) {
	e := newEnv(t)
	a := e.order(t, "alice", domain.PayOnlineCard)
	e.clock.Advance(time.Minute)
	b := e.order(t, "bob", domain.PayPOS)
	e.clock.Advance(time.Minute)
	other := e.order(t, "carol", domain.PayPOS)
	e.moveTo(t, other.ID, testkit.Restaurant2)
	e.clock.Advance(time.Minute)
	c := e.faxOrder(t)

	ids, p := e.queue(t, staff1, service.QueueInput{})
	if !eq(ids, []string{a.ID, b.ID, c.ID}) || p.Total != 3 || p.Limit != 20 || p.Offset != 0 {
		t.Fatalf("staff1: %v total=%d (want [%s %s %s])", ids, p.Total, a.ID, b.ID, c.ID)
	}
	if p.Items[2].Contact.Phone != "+79995550101" || p.Items[0].Contact.Phone == "" || len(p.Items[0].Items) != 2 {
		t.Fatalf("items must carry contact and lines: %+v", p.Items[0])
	}
	if ids, p = e.queue(t, staff2, service.QueueInput{}); !eq(ids, []string{other.ID}) || p.Total != 1 {
		t.Fatalf("staff2: %v", ids)
	}
	// HQ без restaurantId видит все рестораны, с restaurantId — один
	if ids, p = e.queue(t, hq, service.QueueInput{}); len(ids) != 4 || p.Total != 4 {
		t.Fatalf("hq all: %v", ids)
	}
	if ids, _ = e.queue(t, hq, service.QueueInput{RestaurantID: testkit.Restaurant2}); !eq(ids, []string{other.ID}) {
		t.Fatalf("hq r2: %v", ids)
	}
	// newest — обратный порядок
	if ids, _ = e.queue(t, staff1, service.QueueInput{Sort: "newest"}); !eq(ids, []string{c.ID, b.ID, a.ID}) {
		t.Fatalf("newest: %v", ids)
	}
	// владелец с несколькими ресторанами видит все свои
	owner := service.Actor{UserID: "own", Staff: true, Restaurants: []string{testkit.Restaurant1, testkit.Restaurant2}}
	if ids, _ = e.queue(t, owner, service.QueueInput{}); len(ids) != 4 {
		t.Fatalf("owner: %v", ids)
	}
}

// По умолчанию — незавершённые; closed/cancelled только по явному status.
func TestQueueDefaultStatusesAndFilters(t *testing.T) {
	e := newEnv(t)
	pending := e.order(t, "alice", domain.PayOnlineCard) // pending_payment
	e.clock.Advance(time.Minute)
	accepted := e.order(t, "alice", domain.PayPOS)
	e.clock.Advance(time.Minute)
	ready := e.order(t, "alice", domain.PayPOS)
	e.toReady(t, ready.ID)
	e.clock.Advance(time.Minute)
	cancelled := e.order(t, "alice", domain.PayOnlineCard)
	if _, err := e.svc.CancelOrder(ctx, staff1, cancelled.ID, "other"); err != nil {
		t.Fatal(err)
	}

	ids, p := e.queue(t, staff1, service.QueueInput{})
	if !eq(ids, []string{pending.ID, accepted.ID, ready.ID}) || p.Total != 3 {
		t.Fatalf("default must hide cancelled: %v total=%d", ids, p.Total)
	}
	cases := []struct {
		name string
		in   service.QueueInput
		want []string
	}{
		{"one", service.QueueInput{Statuses: []string{"accepted"}}, []string{accepted.ID}},
		{"several", service.QueueInput{Statuses: []string{"accepted", "ready", "accepted"}}, []string{accepted.ID, ready.ID}},
		{"cancelled explicit", service.QueueInput{Statuses: []string{"cancelled"}}, []string{cancelled.ID}},
		{"none", service.QueueInput{Statuses: []string{"cooking"}}, []string{}},
		{"pickup", service.QueueInput{Fulfillment: "pickup"}, []string{pending.ID, accepted.ID, ready.ID}},
		{"delivery", service.QueueInput{Fulfillment: "delivery"}, []string{}},
	}
	for _, c := range cases {
		ids, p := e.queue(t, staff1, c.in)
		if !eq(ids, c.want) || p.Total != len(c.want) {
			t.Errorf("%s: got %v total=%d want %v", c.name, ids, p.Total, c.want)
		}
	}
}

func TestQueuePaginationStableForEqualTimestamps(t *testing.T) {
	e := newEnv(t) // часы не двигаем: created_at равны
	for i := 0; i < 5; i++ {
		e.order(t, "alice", domain.PayPOS)
	}
	for _, sortBy := range []string{"oldest", "newest"} {
		all, p := e.queue(t, staff1, service.QueueInput{Sort: sortBy})
		if len(all) != 5 || p.Total != 5 {
			t.Fatalf("%v", all)
		}
		var paged []string
		for off := 0; off < 5; off += 2 {
			ids, pg := e.queue(t, staff1, service.QueueInput{Sort: sortBy, Limit: intp(2), Offset: intp(off)})
			if pg.Total != 5 {
				t.Fatalf("total on page: %d", pg.Total)
			}
			paged = append(paged, ids...)
		}
		if !eq(all, paged) {
			t.Fatalf("%s: pages must reproduce the full order: %v vs %v", sortBy, all, paged)
		}
	}
	if ids, p := e.queue(t, staff1, service.QueueInput{Offset: intp(100)}); len(ids) != 0 || p.Total != 5 || p.Items == nil {
		t.Fatalf("offset past the end: %v %+v", ids, p)
	}
}

func TestQueueAccessAndValidation(t *testing.T) {
	e := newEnv(t)
	e.order(t, "alice", domain.PayPOS)

	_, err := e.svc.ListRestaurantOrders(ctx, guest, service.QueueInput{})
	wantCode(t, err, "UNAUTHORIZED")
	_, err = e.svc.ListRestaurantOrders(ctx, client, service.QueueInput{})
	wantCode(t, err, "FORBIDDEN")
	// чужой ресторан
	_, err = e.svc.ListRestaurantOrders(ctx, staff2, service.QueueInput{RestaurantID: testkit.Restaurant1})
	wantCode(t, err, "FORBIDDEN")
	// сотрудник без привязки к ресторанам: 403, а не «все заказы» и не пустой список
	_, err = e.svc.ListRestaurantOrders(ctx, service.Actor{UserID: "s", Staff: true}, service.QueueInput{})
	wantCode(t, err, "FORBIDDEN")

	bad := map[string]service.QueueInput{
		"unknown status":     {Statuses: []string{"shipped"}},
		"empty status":       {Statuses: []string{""}},
		"bad fulfillment":    {Fulfillment: "drone"},
		"bad sort":           {Sort: "random"},
		"restaurant not uid": {RestaurantID: "nope"},
		"limit 0":            {Limit: intp(0)},
		"limit 51":           {Limit: intp(51)},
		"offset negative":    {Offset: intp(-1)},
	}
	for name, in := range bad {
		_, err := e.svc.ListRestaurantOrders(ctx, staff1, in)
		if err == nil {
			t.Errorf("%s: want error", name)
			continue
		}
		wantCode(t, err, "INVALID_PARAMETER")
	}
	// uppercase UUID принимается
	if _, err := e.svc.ListRestaurantOrders(ctx, staff1, service.QueueInput{RestaurantID: " " + upper(testkit.Restaurant1) + " "}); err != nil {
		t.Fatalf("uppercase restaurantId: %v", err)
	}
}

func upper(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'a' && c <= 'f' {
			b[i] = c - 32
		}
	}
	return string(b)
}
