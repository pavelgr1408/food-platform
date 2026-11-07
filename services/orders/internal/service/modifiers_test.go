package service_test

import (
	"reflect"
	"testing"

	"github.com/pavelgr1408/food-platform/services/orders/internal/domain"
	"github.com/pavelgr1408/food-platform/services/orders/internal/service"
	"github.com/pavelgr1408/food-platform/services/orders/internal/testkit"
)

var baconSnap = domain.LineModifier{ID: testkit.Bacon, Name: "Бекон", Price: 8900}

// Долг №81: корзина хранит снимок модификаторов (название и цена), строка без модификаторов — пустой, не nil.
func TestCartLinesCarryModifierSnapshot(t *testing.T) {
	e := newEnv(t)
	v := e.cartWith(t, "")
	if got := v.Cart.Items[0].Modifiers; !reflect.DeepEqual(got, []domain.LineModifier{baconSnap}) {
		t.Fatalf("burger snapshot: %#v", got)
	}
	if got := v.Cart.Items[1].Modifiers; got == nil || len(got) != 0 {
		t.Fatalf("cola without modifiers must have empty non-nil snapshot, got %#v", got)
	}
	again, err := e.svc.GetCart(ctx, "", v.Cart.ID) // из БД, а не из памяти
	if err != nil {
		t.Fatal(err)
	}
	if got := again.Cart.Items[0].Modifiers; !reflect.DeepEqual(got, []domain.LineModifier{baconSnap}) {
		t.Fatalf("snapshot must survive a round trip through the DB: %#v", got)
	}
}

// Снимок в заказе фиксируется при оформлении и не зависит от последующих правок меню.
func TestOrderModifierSnapshotIsFrozen(t *testing.T) {
	e := newEnv(t)
	o := e.order(t, "", domain.PayOnlineCard)
	if got := o.Items[0].Modifiers; !reflect.DeepEqual(got, []domain.LineModifier{baconSnap}) {
		t.Fatalf("order snapshot: %#v", got)
	}
	e.menu.SetModName(testkit.Burger, testkit.Bacon, "Хрустящий бекон")
	e.menu.SetPrice(testkit.Burger, 99900)
	got, err := e.svc.GetOrder(ctx, "", o.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Items[0].Modifiers, []domain.LineModifier{baconSnap}) {
		t.Fatalf("later menu edits must not change the order: %#v", got.Items[0].Modifiers)
	}
}

// createOrder берёт свежие названия из menu (как и свежие цены), а не из корзины двухдневной давности.
func TestCreateOrderRefreshesSnapshotFromMenu(t *testing.T) {
	e := newEnv(t)
	v := e.cartWith(t, "")
	e.menu.SetModName(testkit.Burger, testkit.Bacon, "Хрустящий бекон") // цена та же → PRICE_CHANGED не будет
	o, err := e.svc.CreateOrder(ctx, "", service.CreateOrderInput{CartID: v.Cart.ID, PaymentMethod: domain.PayOnlineCard}, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []domain.LineModifier{{ID: testkit.Bacon, Name: "Хрустящий бекон", Price: 8900}}
	if !reflect.DeepEqual(o.Items[0].Modifiers, want) {
		t.Fatalf("order must carry the name valid at checkout: %#v", o.Items[0].Modifiers)
	}
}

// Строки без снимка (созданные до миграции или при menu без поля) — nil, не пустой массив:
// иначе клиент решит, что модификаторов не было.
func TestLinesWithoutSnapshotStayNil(t *testing.T) {
	e := newEnv(t)
	e.menu.WithoutModifiersField(true)
	v := e.cartWith(t, "")
	if v.Cart.Items[0].Modifiers != nil {
		t.Fatalf("burger+bacon with menu lacking the field -> no snapshot, got %#v", v.Cart.Items[0].Modifiers)
	}
	if got := v.Cart.Items[1].Modifiers; got == nil || len(got) != 0 {
		t.Fatalf("no modifiers selected is an empty snapshot whatever menu returns, got %#v", got)
	}

	// строка, сохранённая до миграции 007 (modifiers IS NULL), читается как «снимка нет»
	e.menu.WithoutModifiersField(false)
	v = e.cartWith(t, "")
	if _, err := e.pool.Exec(ctx, `UPDATE cart_items SET modifiers = NULL WHERE cart_id = $1`, v.Cart.ID); err != nil {
		t.Fatal(err)
	}
	legacy, err := e.svc.GetCart(ctx, "", v.Cart.ID)
	if err != nil {
		t.Fatal(err)
	}
	if legacy.Cart.Items[0].Modifiers != nil || legacy.Cart.Items[1].Modifiers != nil {
		t.Fatalf("NULL column must read as nil: %#v", legacy.Cart.Items)
	}
	// ...а оформление такой корзины даёт заказ уже со снимком
	o, err := e.svc.CreateOrder(ctx, "", service.CreateOrderInput{CartID: v.Cart.ID, PaymentMethod: domain.PayOnlineCard}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(o.Items[0].Modifiers, []domain.LineModifier{baconSnap}) {
		t.Fatalf("order from a legacy cart must get a fresh snapshot: %#v", o.Items[0].Modifiers)
	}
}
