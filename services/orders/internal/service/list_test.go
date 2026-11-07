package service_test

import (
	"testing"
	"time"

	"github.com/pavelgr1408/food-platform/services/orders/internal/domain"
	"github.com/pavelgr1408/food-platform/services/orders/internal/service"
	"github.com/pavelgr1408/food-platform/services/orders/internal/testkit"
)

func intp(n int) *int { return &n }

// listOrders: id заказов в порядке выдачи.
func (e *env) listIDs(t *testing.T, userID string, in service.ListOrdersInput) ([]string, service.OrderPage) {
	t.Helper()
	p, err := e.svc.ListOrders(ctx, userID, in)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, len(p.Items))
	for i, o := range p.Items {
		ids[i] = o.ID
	}
	return ids, p
}

// Долг №80: пользователь видит только свои заказы, от новых к старым; чужие, гостевые и заказы по факсу не попадают.
func TestListOrdersReturnsOnlyOwnNewestFirst(t *testing.T) {
	e := newEnv(t)
	a1 := e.order(t, "alice", domain.PayOnlineCard)
	e.clock.Advance(time.Minute)
	b1 := e.order(t, "bob", domain.PayOnlineCard)
	e.clock.Advance(time.Minute)
	a2 := e.order(t, "alice", domain.PayPOS)
	e.clock.Advance(time.Minute)
	g1 := e.order(t, "", domain.PayOnlineCard) // гостевой заказ
	_ = g1

	v := e.cartWith(t, "staff-1") // персонал оформляет заказ по факсу: владельца у него нет
	if _, err := e.svc.CreateOrder(ctx, "staff-1", service.CreateOrderInput{CartID: v.Cart.ID, PaymentMethod: domain.PayPOS,
		Channel: "staff_fax", Actor: staff1, Contact: domain.Contact{Phone: "+79995550101"}}, nil); err != nil {
		t.Fatal(err)
	}

	ids, p := e.listIDs(t, "alice", service.ListOrdersInput{})
	if len(ids) != 2 || ids[0] != a2.ID || ids[1] != a1.ID || p.Total != 2 || p.Limit != 20 || p.Offset != 0 {
		t.Fatalf("alice: %v total=%d limit=%d offset=%d (want [%s %s])", ids, p.Total, p.Limit, p.Offset, a2.ID, a1.ID)
	}
	ids, p = e.listIDs(t, "bob", service.ListOrdersInput{})
	if len(ids) != 1 || ids[0] != b1.ID || p.Total != 1 {
		t.Fatalf("bob: %v", ids)
	}
	if ids, p = e.listIDs(t, "staff-1", service.ListOrdersInput{}); len(ids) != 0 || p.Total != 0 || p.Items == nil {
		t.Fatalf("fax order has no owner and must not be listed for the staff member; empty page must be non-nil: %v %+v", ids, p)
	}
	if ids, _ = e.listIDs(t, "nobody", service.ListOrdersInput{}); len(ids) != 0 {
		t.Fatalf("unknown user: %v", ids)
	}
}

// Элемент списка — тот же заказ, что отдаёт getOrder, включая позиции со снимком модификаторов.
func TestListOrdersItemsMatchGetOrder(t *testing.T) {
	e := newEnv(t)
	o := e.order(t, "alice", domain.PayOnlineCard)
	_, p := e.listIDs(t, "alice", service.ListOrdersInput{})
	got, err := e.svc.GetOrder(ctx, "alice", o.ID)
	if err != nil {
		t.Fatal(err)
	}
	li := p.Items[0]
	if li.ID != got.ID || li.Number != got.Number || li.Status != got.Status || li.Total != got.Total || len(li.Items) != 2 || len(got.Items) != 2 {
		t.Fatalf("list item differs from getOrder:\n%+v\n%+v", li, got)
	}
	for i := range li.Items {
		a, b := li.Items[i], got.Items[i]
		if a.MenuItemID != b.MenuItemID || a.Name != b.Name || a.Quantity != b.Quantity || a.LineTotal != b.LineTotal || len(a.Modifiers) != len(b.Modifiers) {
			t.Fatalf("line %d: %+v vs %+v", i, a, b)
		}
	}
	if len(li.Items[0].Modifiers) != 1 || li.Items[0].Modifiers[0].Name != "Бекон" {
		t.Fatalf("modifier snapshot must be in the list: %+v", li.Items[0].Modifiers)
	}
}

func TestListOrdersPagination(t *testing.T) {
	e := newEnv(t)
	var made []string
	for i := 0; i < 5; i++ {
		made = append([]string{e.order(t, "alice", domain.PayOnlineCard).ID}, made...) // newest first
		e.clock.Advance(time.Minute)
	}
	ids, p := e.listIDs(t, "alice", service.ListOrdersInput{Limit: intp(2)})
	if len(ids) != 2 || ids[0] != made[0] || ids[1] != made[1] || p.Total != 5 || p.Limit != 2 {
		t.Fatalf("page 1: %v total=%d", ids, p.Total)
	}
	ids, p = e.listIDs(t, "alice", service.ListOrdersInput{Limit: intp(2), Offset: intp(2)})
	if len(ids) != 2 || ids[0] != made[2] || ids[1] != made[3] || p.Offset != 2 {
		t.Fatalf("page 2: %v", ids)
	}
	ids, p = e.listIDs(t, "alice", service.ListOrdersInput{Limit: intp(2), Offset: intp(4)})
	if len(ids) != 1 || ids[0] != made[4] || p.Total != 5 {
		t.Fatalf("last page: %v", ids)
	}
	ids, p = e.listIDs(t, "alice", service.ListOrdersInput{Offset: intp(100)})
	if len(ids) != 0 || p.Total != 5 {
		t.Fatalf("offset past the end: empty page with the real total, got %v total=%d", ids, p.Total)
	}
}

// Заказы, созданные в одну и ту же секунду, не должны перемешиваться между страницами.
func TestListOrdersOrderIsStableForEqualTimestamps(t *testing.T) {
	e := newEnv(t) // часы не двигаем: все created_at равны
	for i := 0; i < 4; i++ {
		e.order(t, "alice", domain.PayOnlineCard)
	}
	all, _ := e.listIDs(t, "alice", service.ListOrdersInput{})
	var paged []string
	for off := 0; off < 4; off += 2 {
		ids, _ := e.listIDs(t, "alice", service.ListOrdersInput{Limit: intp(2), Offset: intp(off)})
		paged = append(paged, ids...)
	}
	if len(all) != 4 || len(paged) != 4 {
		t.Fatalf("%v %v", all, paged)
	}
	for i := range all {
		if all[i] != paged[i] {
			t.Fatalf("pages must reproduce the full order: %v vs %v", all, paged)
		}
	}
}

func TestListOrdersStatusFilter(t *testing.T) {
	e := newEnv(t)
	online := e.order(t, "alice", domain.PayOnlineCard) // pending_payment
	e.clock.Advance(time.Minute)
	pos := e.order(t, "alice", domain.PayPOS) // accepted
	e.clock.Advance(time.Minute)
	cancelled := e.order(t, "alice", domain.PayOnlineCard)
	if _, err := e.svc.CancelOrder(ctx, staff1, cancelled.ID, "other"); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		in   []string
		want []string
		tot  int
	}{
		{"single", []string{"accepted"}, []string{pos.ID}, 1},
		{"several, any of", []string{"pending_payment", "accepted"}, []string{pos.ID, online.ID}, 2},
		{"duplicates collapse", []string{"accepted", "accepted"}, []string{pos.ID}, 1},
		{"none match", []string{"ready"}, nil, 0},
		{"cancelled", []string{"cancelled"}, []string{cancelled.ID}, 1},
	}
	for _, c := range cases {
		ids, p := e.listIDs(t, "alice", service.ListOrdersInput{Statuses: c.in})
		if len(ids) != len(c.want) || p.Total != c.tot {
			t.Errorf("%s: got %v total=%d want %v total=%d", c.name, ids, p.Total, c.want, c.tot)
			continue
		}
		for i := range ids {
			if ids[i] != c.want[i] {
				t.Errorf("%s: got %v want %v", c.name, ids, c.want)
			}
		}
	}
	// total считается с учётом фильтра и не зависит от размера страницы
	if _, p := e.listIDs(t, "alice", service.ListOrdersInput{Statuses: []string{"pending_payment", "accepted"}, Limit: intp(1)}); p.Total != 2 || len(p.Items) != 1 {
		t.Fatalf("total with filter and limit: %+v", p)
	}
}

func TestListOrdersValidation(t *testing.T) {
	e := newEnv(t)
	bad := map[string]service.ListOrdersInput{
		"unknown status":  {Statuses: []string{"shipped"}},
		"empty status":    {Statuses: []string{""}},
		"one bad of many": {Statuses: []string{"accepted", "nope"}},
		"limit 0":         {Limit: intp(0)},
		"limit 51":        {Limit: intp(51)},
		"limit negative":  {Limit: intp(-1)},
		"offset negative": {Offset: intp(-1)},
		"too many status": {Statuses: []string{"accepted", "cooking", "ready", "closed", "cancelled", "delivered", "on_delivery", "handed_over", "not_received", "pending_payment", "accepted"}},
	}
	for name, in := range bad {
		_, err := e.svc.ListOrders(ctx, "alice", in)
		if err == nil {
			t.Errorf("%s: want error", name)
			continue
		}
		wantCode(t, err, "INVALID_PARAMETER")
	}
	if _, err := e.svc.ListOrders(ctx, "alice", service.ListOrdersInput{Limit: intp(50), Offset: intp(0)}); err != nil {
		t.Fatalf("limit 50 is allowed: %v", err)
	}
	// гость не получает «все заказы без владельца»
	_, err := e.svc.ListOrders(ctx, "", service.ListOrdersInput{})
	wantCode(t, err, "UNAUTHORIZED")
	_ = testkit.Restaurant1
}
