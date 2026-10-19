package service_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pavelgr1408/food-platform/pkg/httpx"
	"github.com/pavelgr1408/food-platform/services/orders/internal/clients"
	"github.com/pavelgr1408/food-platform/services/orders/internal/domain"
	"github.com/pavelgr1408/food-platform/services/orders/internal/service"
	"github.com/pavelgr1408/food-platform/services/orders/internal/testkit"
)

type env struct {
	svc   *service.Service
	pool  *pgxpool.Pool
	menu  *testkit.FakeMenu
	rest  *testkit.FakeRestaurants
	clock *testkit.Clock
	m     *service.Metrics
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{pool: testkit.Pool(t), menu: testkit.NewMenu(), rest: testkit.NewRestaurants(), clock: testkit.NewClock(), m: service.NewMetrics()}
	e.svc = service.New(service.Deps{Pool: e.pool, Menu: e.menu, Restaurants: e.rest, Log: testkit.Log(),
		Clock: e.clock.Now, Metrics: e.m,
		Config: service.Config{PaymentTimeout: 15 * time.Minute}})
	return e
}

var ctx = context.Background()

func code(t *testing.T, err error) string {
	t.Helper()
	var he *httpx.Error
	if !errors.As(err, &he) {
		var ue *service.ItemsUnavailableError
		if errors.As(err, &ue) {
			return "ITEMS_UNAVAILABLE"
		}
		t.Fatalf("not an httpx.Error: %T %v", err, err)
	}
	return he.Code
}

func wantCode(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("want error %s, got nil", want)
	}
	if got := code(t, err); got != want {
		t.Fatalf("code: got %s want %s (%v)", got, want, err)
	}
}

// cartWith создаёт гостевую корзину ресторана 1 с бургером×2 (+бекон) и колой×1, способ получения — самовывоз.
func (e *env) cartWith(t *testing.T, userID string) service.CartView {
	t.Helper()
	v, err := e.svc.CreateCart(ctx, userID, service.CreateCartInput{RestaurantID: testkit.Restaurant1, Guest: userID == "",
		Contact: domain.Contact{Name: "Иван", Phone: "+7 (999) 123-45-67"}})
	if err != nil {
		t.Fatal(err)
	}
	v, err = e.svc.UpdateCartItems(ctx, userID, v.Cart.ID, []service.ItemInput{
		{MenuItemID: testkit.Burger, Quantity: 2, ModifierIDs: []string{testkit.Bacon}},
		{MenuItemID: testkit.Cola, Quantity: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	v, err = e.svc.SetFulfillment(ctx, userID, v.Cart.ID, service.FulfillmentInput{Type: domain.FulfillmentPickup})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func (e *env) order(t *testing.T, userID string, pm domain.PaymentMethod) domain.Order {
	t.Helper()
	v := e.cartWith(t, userID)
	o, err := e.svc.CreateOrder(ctx, userID, service.CreateOrderInput{CartID: v.Cart.ID, PaymentMethod: pm}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return o
}

type outboxRow struct {
	Topic, Type, Key string
	Payload          []byte
}

func (e *env) outbox(t *testing.T) []outboxRow {
	t.Helper()
	rows, err := e.pool.Query(ctx, `SELECT topic, event_type, key, payload FROM outbox ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []outboxRow
	for rows.Next() {
		var r outboxRow
		if err := rows.Scan(&r.Topic, &r.Type, &r.Key, &r.Payload); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func (e *env) count(t *testing.T, table string) int {
	t.Helper()
	var n int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestCartPricingInKopecksAndSnapshot(t *testing.T) {
	e := newEnv(t)
	v := e.cartWith(t, "")
	// 2×(349,00+89,00) + 129,00 = 1005,00 ₽ = 100500 копеек
	if v.Subtotal != 100500 || v.Total != 100500 || v.DiscountTotal != 0 {
		t.Fatalf("totals: %+v", v)
	}
	if len(v.Cart.Items) != 2 || v.Cart.Items[0].Name != "Бургер" || v.Cart.Items[0].UnitPrice != 43800 || v.Cart.Items[0].LineTotal != 87600 {
		t.Fatalf("items: %+v", v.Cart.Items)
	}
	if v.Cart.Currency != "RUB" || v.Cart.Fulfillment != domain.FulfillmentPickup || v.Cart.Contact.Phone != "+79991234567" {
		t.Fatalf("cart: %+v", v.Cart)
	}
}

func TestCreateCartRules(t *testing.T) {
	e := newEnv(t)
	_, err := e.svc.CreateCart(ctx, "", service.CreateCartInput{RestaurantID: testkit.Restaurant1}) // не гость и не вошёл
	wantCode(t, err, "UNAUTHENTICATED")
	_, err = e.svc.CreateCart(ctx, "", service.CreateCartInput{RestaurantID: "nope", Guest: true})
	wantCode(t, err, "INVALID_PARAMETER")
	_, err = e.svc.CreateCart(ctx, "", service.CreateCartInput{RestaurantID: "a1000000-0000-4000-8000-0000000000ff", Guest: true})
	wantCode(t, err, "RESTAURANT_NOT_FOUND")
	_, err = e.svc.CreateCart(ctx, "", service.CreateCartInput{RestaurantID: testkit.Restaurant1, Guest: true, Contact: domain.Contact{Phone: "89991234567"}})
	wantCode(t, err, "INVALID_PARAMETER")
	e.rest.Fail(fmt.Errorf("%w: boom", clients.ErrUnavailable))
	_, err = e.svc.CreateCart(ctx, "", service.CreateCartInput{RestaurantID: testkit.Restaurant1, Guest: true})
	wantCode(t, err, "RESTAURANT_UNAVAILABLE")
	if n := e.count(t, "carts"); n != 0 {
		t.Fatalf("no carts must be created, got %d", n)
	}
	if _, err := e.svc.CreateCart(ctx, "user-1", service.CreateCartInput{RestaurantID: "x"}); err == nil {
		t.Fatal("bad restaurant id must fail even for user")
	}
}

func TestUpdateItemsValidation(t *testing.T) {
	e := newEnv(t)
	v, _ := e.svc.CreateCart(ctx, "", service.CreateCartInput{RestaurantID: testkit.Restaurant1, Guest: true})
	id := v.Cart.ID
	bad := map[string][]service.ItemInput{
		"quantity 0":     {{MenuItemID: testkit.Burger, Quantity: 0}},
		"quantity 100":   {{MenuItemID: testkit.Burger, Quantity: 100}},
		"not uuid item":  {{MenuItemID: "burger", Quantity: 1}},
		"not uuid mod":   {{MenuItemID: testkit.Burger, Quantity: 1, ModifierIDs: []string{"x"}}},
		"dup modifier":   {{MenuItemID: testkit.Burger, Quantity: 1, ModifierIDs: []string{testkit.Bacon, testkit.Bacon}}},
		"duplicate line": {{MenuItemID: testkit.Burger, Quantity: 1}, {MenuItemID: testkit.Burger, Quantity: 2}},
	}
	for name, items := range bad {
		_, err := e.svc.UpdateCartItems(ctx, "", id, items)
		if err == nil || code(t, err) != "INVALID_PARAMETER" {
			t.Errorf("%s: want INVALID_PARAMETER, got %v", name, err)
		}
	}
	var many []service.ItemInput
	for i := 0; i < 51; i++ {
		many = append(many, service.ItemInput{MenuItemID: fmt.Sprintf("b1000000-0000-4000-8000-%012d", i), Quantity: 1})
	}
	_, err := e.svc.UpdateCartItems(ctx, "", id, many)
	wantCode(t, err, "INVALID_PARAMETER")
	if e.menu.Calls() != 0 {
		t.Errorf("menu must not be called for invalid input, calls=%d", e.menu.Calls())
	}
	// Пустой список очищает корзину без обращения к menu.
	if _, err := e.svc.UpdateCartItems(ctx, "", id, nil); err != nil {
		t.Fatal(err)
	}
	if e.menu.Calls() != 0 {
		t.Errorf("empty update must not call menu")
	}
}

func TestUnavailableItemsAreDroppedAndReported(t *testing.T) {
	e := newEnv(t)
	v, _ := e.svc.CreateCart(ctx, "", service.CreateCartInput{RestaurantID: testkit.Restaurant1, Guest: true})
	got, err := e.svc.UpdateCartItems(ctx, "", v.Cart.ID, []service.ItemInput{
		{MenuItemID: testkit.Cola, Quantity: 2},
		{MenuItemID: testkit.Cake, Quantity: 1},
		{MenuItemID: testkit.Burger, Quantity: 1, ModifierIDs: []string{testkit.Cheese}},
	})
	var ue *service.ItemsUnavailableError
	if !errors.As(err, &ue) || len(ue.Items) != 2 {
		t.Fatalf("want ItemsUnavailableError with 2 items, got %v", err)
	}
	if ue.Items[0].ReasonCode != "ITEM_UNAVAILABLE" || ue.Items[1].ReasonCode != "MODIFIER_UNAVAILABLE" {
		t.Fatalf("reasons: %+v", ue.Items)
	}
	// остальное сохранено, сумма пересчитана (E1)
	if len(got.Cart.Items) != 1 || got.Total != 25800 {
		t.Fatalf("cart after drop: %+v total=%d", got.Cart.Items, got.Total)
	}
	again, _ := e.svc.GetCart(ctx, "", v.Cart.ID)
	if len(again.Cart.Items) != 1 {
		t.Fatal("the dropped state must be persisted")
	}
}

func TestMenuOutageKeepsCart(t *testing.T) {
	e := newEnv(t)
	v := e.cartWith(t, "")
	e.menu.Fail(fmt.Errorf("%w: menu: timeout", clients.ErrUnavailable))
	_, err := e.svc.UpdateCartItems(ctx, "", v.Cart.ID, []service.ItemInput{{MenuItemID: testkit.Cola, Quantity: 5}})
	wantCode(t, err, "MENU_UNAVAILABLE")
	after, _ := e.svc.GetCart(ctx, "", v.Cart.ID)
	if after.Total != v.Total || len(after.Cart.Items) != 2 {
		t.Fatalf("cart must stay intact (MG2): %+v", after.Cart.Items)
	}
	// createOrder при недоступном menu: 503, заказа и событий нет, корзина открыта
	_, err = e.svc.CreateOrder(ctx, "", service.CreateOrderInput{CartID: v.Cart.ID, PaymentMethod: domain.PayOnlineCard}, nil)
	wantCode(t, err, "MENU_UNAVAILABLE")
	if e.count(t, "orders") != 0 || len(e.outbox(t)) != 0 {
		t.Fatal("nothing must be written on menu outage")
	}
	e.menu.Fail(nil)
	if _, err := e.svc.CreateOrder(ctx, "", service.CreateOrderInput{CartID: v.Cart.ID, PaymentMethod: domain.PayOnlineCard}, nil); err != nil {
		t.Fatalf("retry after recovery: %v", err)
	}
}

func TestFulfillmentRules(t *testing.T) {
	e := newEnv(t)
	v, _ := e.svc.CreateCart(ctx, "", service.CreateCartInput{RestaurantID: testkit.Restaurant1, Guest: true})
	id := v.Cart.ID
	_, err := e.svc.SetFulfillment(ctx, "", id, service.FulfillmentInput{Type: "teleport"})
	wantCode(t, err, "INVALID_PARAMETER")
	_, err = e.svc.SetFulfillment(ctx, "", id, service.FulfillmentInput{Type: domain.FulfillmentDelivery})
	wantCode(t, err, "ADDRESS_REQUIRED")
	lat := 95.0
	lon := 10.0
	_, err = e.svc.SetFulfillment(ctx, "", id, service.FulfillmentInput{Type: domain.FulfillmentDelivery, Address: domain.Address{Raw: "ул. Ленина, 1", Lat: &lat, Lon: &lon}})
	wantCode(t, err, "INVALID_PARAMETER")
	lat = 55.75
	lon = 37.6
	got, err := e.svc.SetFulfillment(ctx, "", id, service.FulfillmentInput{Type: domain.FulfillmentDelivery, Address: domain.Address{Raw: " ул. Тверская, 1 ", Lat: &lat, Lon: &lon}})
	if err != nil || got.Cart.Address.Raw != "ул. Тверская, 1" || *got.Cart.Address.Lat != 55.75 {
		t.Fatalf("delivery: %v %+v", err, got.Cart.Address)
	}
	// переход на самовывоз очищает адрес (A4)
	got, err = e.svc.SetFulfillment(ctx, "", id, service.FulfillmentInput{Type: domain.FulfillmentPickup, Address: domain.Address{Raw: "игнорируется"}})
	if err != nil || got.Cart.Address.Raw != "" || got.Cart.Address.Lat != nil {
		t.Fatalf("pickup: %v %+v", err, got.Cart.Address)
	}
	// ресторан без доставки
	v2, _ := e.svc.CreateCart(ctx, "", service.CreateCartInput{RestaurantID: testkit.Restaurant2, Guest: true})
	_, err = e.svc.SetFulfillment(ctx, "", v2.Cart.ID, service.FulfillmentInput{Type: domain.FulfillmentDelivery, Address: domain.Address{Raw: "x"}})
	wantCode(t, err, "DELIVERY_NOT_AVAILABLE")
}

func TestCreateOrderHappyPathOnlinePickup(t *testing.T) {
	e := newEnv(t)
	o := e.order(t, "", domain.PayOnlineCard)
	if o.Status != domain.StatusPendingPayment || o.PaymentStatus != domain.PaymentPending || o.Revision != 1 {
		t.Fatalf("order: %+v", o)
	}
	// 12:00 MSK 2026-10-18 → R1 (первый ресторан), seq 1
	if o.Number != "R1-261018-0001" {
		t.Fatalf("number: %s", o.Number)
	}
	if o.Subtotal != 100500 || o.Total != 100500 || o.DiscountTotal != 0 || o.Currency != "RUB" {
		t.Fatalf("money: %+v", o)
	}
	got, err := e.svc.GetOrder(ctx, "", o.ID)
	if err != nil || got.Number != o.Number || len(got.Items) != 2 || got.Contact.Phone != "+79991234567" {
		t.Fatalf("get: %v %+v", err, got)
	}
	// корзина закрыта и ссылается на заказ; повторное оформление и правки запрещены
	cv, _ := e.svc.GetCart(ctx, "", o.CartID)
	if cv.Cart.Status != domain.CartOrdered || cv.Cart.OrderID != o.ID {
		t.Fatalf("cart: %+v", cv.Cart)
	}
	_, err = e.svc.CreateOrder(ctx, "", service.CreateOrderInput{CartID: o.CartID, PaymentMethod: domain.PayOnlineCard}, nil)
	wantCode(t, err, "CART_CLOSED")
	_, err = e.svc.UpdateCartItems(ctx, "", o.CartID, nil)
	wantCode(t, err, "CART_CLOSED")
	_, err = e.svc.SetFulfillment(ctx, "", o.CartID, service.FulfillmentInput{Type: domain.FulfillmentPickup})
	wantCode(t, err, "CART_CLOSED")

	// ADR-10: два события в одной транзакции с заказом
	ob := e.outbox(t)
	if len(ob) != 2 || ob[0].Topic != "orders.state" || ob[1].Topic != "orders.status" || ob[0].Key != o.ID || ob[1].Key != o.ID {
		t.Fatalf("outbox: %+v", ob)
	}
	var st service.OrderStateEvent
	if err := json.Unmarshal(ob[0].Payload, &st); err != nil {
		t.Fatal(err)
	}
	if st.Revision != 1 || st.PaymentStatus != "pending" || st.PaymentMethod != "online" || st.TotalAmount.Amount != 100500 ||
		st.FulfillmentType != "pickup" || st.Customer.Guest != true || st.Customer.Phone != "+79991234567" ||
		len(st.Items) != 2 || st.Items[0].DishID != testkit.Burger || st.Items[0].Price.Amount != 43800 || st.Items[0].Modifiers[0] != testkit.Bacon {
		t.Fatalf("state event: %+v", st)
	}
	var ss service.OrderStatusEvent
	_ = json.Unmarshal(ob[1].Payload, &ss)
	if ss.Status != "pending_payment" || ss.OrderNumber != o.Number {
		t.Fatalf("status event: %+v", ss)
	}
	h, _ := e.svc.Store().History(ctx, e.pool, o.ID)
	if len(h) != 1 || h[0].To != domain.StatusPendingPayment || h[0].From != "" || h[0].Actor != "customer" {
		t.Fatalf("history: %+v", h)
	}
}

func TestNonPrepaidOrderIsAcceptedImmediately(t *testing.T) {
	e := newEnv(t)
	o := e.order(t, "", domain.PayPOS) // оплата на кассе при выдаче
	if o.Status != domain.StatusAccepted || o.PaymentStatus != domain.PaymentPending {
		t.Fatalf("order: %+v", o)
	}
	ob := e.outbox(t)
	var st service.OrderStateEvent
	_ = json.Unmarshal(ob[0].Payload, &st)
	if st.PaymentMethod != "restaurant" || st.PaymentStatus != "pending" || st.Revision != 1 {
		t.Fatalf("state: %+v", st)
	}
	var ss service.OrderStatusEvent
	_ = json.Unmarshal(ob[1].Payload, &ss)
	if ss.Status != "accepted" {
		t.Fatalf("status: %+v", ss)
	}
}

func TestCreateOrderValidation(t *testing.T) {
	e := newEnv(t)
	empty, _ := e.svc.CreateCart(ctx, "", service.CreateCartInput{RestaurantID: testkit.Restaurant1, Guest: true, Contact: domain.Contact{Phone: "+79990000001"}})
	_, err := e.svc.CreateOrder(ctx, "", service.CreateOrderInput{CartID: empty.Cart.ID, PaymentMethod: domain.PayOnlineCard}, nil)
	wantCode(t, err, "CART_EMPTY")

	e.svc.UpdateCartItems(ctx, "", empty.Cart.ID, []service.ItemInput{{MenuItemID: testkit.Cola, Quantity: 1}})
	_, err = e.svc.CreateOrder(ctx, "", service.CreateOrderInput{CartID: empty.Cart.ID, PaymentMethod: domain.PayOnlineCard}, nil)
	wantCode(t, err, "FULFILLMENT_REQUIRED")

	v := e.cartWith(t, "")
	for pm, want := range map[domain.PaymentMethod]string{domain.PayCourierCard: "PAYMENT_METHOD_NOT_ALLOWED", "cash": "PAYMENT_METHOD_NOT_ALLOWED", "": "PAYMENT_METHOD_NOT_ALLOWED"} {
		_, err = e.svc.CreateOrder(ctx, "", service.CreateOrderInput{CartID: v.Cart.ID, PaymentMethod: pm}, nil)
		wantCode(t, err, want)
	}
	_, err = e.svc.CreateOrder(ctx, "", service.CreateOrderInput{CartID: v.Cart.ID, PaymentMethod: domain.PayOnlineCard, Channel: "staff_fax"}, nil)
	wantCode(t, err, "CHANNEL_NOT_AVAILABLE")
	_, err = e.svc.CreateOrder(ctx, "", service.CreateOrderInput{CartID: v.Cart.ID, PaymentMethod: domain.PayOnlineCard, Channel: "carrier-pigeon"}, nil)
	wantCode(t, err, "INVALID_PARAMETER")
	_, err = e.svc.CreateOrder(ctx, "", service.CreateOrderInput{CartID: "nope", PaymentMethod: domain.PayOnlineCard}, nil)
	wantCode(t, err, "INVALID_PARAMETER")
	_, err = e.svc.CreateOrder(ctx, "", service.CreateOrderInput{CartID: "a1000000-0000-4000-8000-0000000000ff", PaymentMethod: domain.PayOnlineCard}, nil)
	wantCode(t, err, "CART_NOT_FOUND")

	// гость без телефона
	g, _ := e.svc.CreateCart(ctx, "", service.CreateCartInput{RestaurantID: testkit.Restaurant1, Guest: true})
	e.svc.UpdateCartItems(ctx, "", g.Cart.ID, []service.ItemInput{{MenuItemID: testkit.Cola, Quantity: 1}})
	e.svc.SetFulfillment(ctx, "", g.Cart.ID, service.FulfillmentInput{Type: domain.FulfillmentPickup})
	_, err = e.svc.CreateOrder(ctx, "", service.CreateOrderInput{CartID: g.Cart.ID, PaymentMethod: domain.PayOnlineCard}, nil)
	wantCode(t, err, "CONTACT_REQUIRED")
	// телефон можно передать в самом запросе
	if _, err = e.svc.CreateOrder(ctx, "", service.CreateOrderInput{CartID: g.Cart.ID, PaymentMethod: domain.PayOnlineCard,
		Contact: domain.Contact{Phone: "+79990000002"}}, nil); err != nil {
		t.Fatal(err)
	}
	// авторизованному телефон не обязателен
	u, _ := e.svc.CreateCart(ctx, "user-1", service.CreateCartInput{RestaurantID: testkit.Restaurant1})
	e.svc.UpdateCartItems(ctx, "user-1", u.Cart.ID, []service.ItemInput{{MenuItemID: testkit.Cola, Quantity: 1}})
	e.svc.SetFulfillment(ctx, "user-1", u.Cart.ID, service.FulfillmentInput{Type: domain.FulfillmentPickup})
	if _, err = e.svc.CreateOrder(ctx, "user-1", service.CreateOrderInput{CartID: u.Cart.ID, PaymentMethod: domain.PayPOS}, nil); err != nil {
		t.Fatal(err)
	}
	if e.count(t, "orders") != 2 {
		t.Fatalf("orders: %d", e.count(t, "orders"))
	}
}

func TestRestaurantClosedRejectsOrderAndKeepsCart(t *testing.T) {
	e := newEnv(t)
	v := e.cartWith(t, "")
	e.rest.Update(testkit.Restaurant1, func(i *clients.RestaurantInfo) { i.OpenNow = false })
	_, err := e.svc.CreateOrder(ctx, "", service.CreateOrderInput{CartID: v.Cart.ID, PaymentMethod: domain.PayOnlineCard}, nil)
	wantCode(t, err, "RESTAURANT_CLOSED")
	if e.count(t, "orders") != 0 || len(e.outbox(t)) != 0 {
		t.Fatal("no order and no events for a closed restaurant")
	}
	cv, _ := e.svc.GetCart(ctx, "", v.Cart.ID)
	if cv.Cart.Status != domain.CartOpen {
		t.Fatal("cart must stay open")
	}
	e.rest.Fail(fmt.Errorf("%w: down", clients.ErrUnavailable))
	_, err = e.svc.CreateOrder(ctx, "", service.CreateOrderInput{CartID: v.Cart.ID, PaymentMethod: domain.PayOnlineCard}, nil)
	wantCode(t, err, "RESTAURANT_UNAVAILABLE") // fail-closed: не можем убедиться, что открыт
}

func TestStopListAndPriceChangeAtOrderTime(t *testing.T) {
	e := newEnv(t)
	v := e.cartWith(t, "")
	// E1: позиция ушла в стоп-лист после расчёта корзины
	e.menu.SetAvailable(testkit.Cola, false)
	_, err := e.svc.CreateOrder(ctx, "", service.CreateOrderInput{CartID: v.Cart.ID, PaymentMethod: domain.PayOnlineCard}, nil)
	var ue *service.ItemsUnavailableError
	if !errors.As(err, &ue) || len(ue.Items) != 1 || ue.Items[0].MenuItemID != testkit.Cola {
		t.Fatalf("want unavailable cola, got %v", err)
	}
	if e.count(t, "orders") != 0 {
		t.Fatal("no order on unavailable items")
	}
	e.menu.SetAvailable(testkit.Cola, true)

	// цена выросла: клиент не должен заплатить больше, чем видел
	e.menu.SetPrice(testkit.Cola, 15900)
	_, err = e.svc.CreateOrder(ctx, "", service.CreateOrderInput{CartID: v.Cart.ID, PaymentMethod: domain.PayOnlineCard}, nil)
	wantCode(t, err, "PRICE_CHANGED")
	cv, _ := e.svc.GetCart(ctx, "", v.Cart.ID)
	if cv.Total != 103500 { // 2×438,00 + 159,00
		t.Fatalf("cart must show the new price, total=%d", cv.Total)
	}
	if e.count(t, "orders") != 0 {
		t.Fatal("no order on price change")
	}
	// клиент подтвердил новую сумму — заказ создаётся по новой цене
	o, err := e.svc.CreateOrder(ctx, "", service.CreateOrderInput{CartID: v.Cart.ID, PaymentMethod: domain.PayOnlineCard}, nil)
	if err != nil || o.Total != 103500 {
		t.Fatalf("order after confirm: %v %+v", err, o.Total)
	}
}

func TestCommitFailureRollsEverythingBack(t *testing.T) {
	e := newEnv(t)
	v := e.cartWith(t, "")
	boom := errors.New("boom")
	_, err := e.svc.CreateOrder(ctx, "", service.CreateOrderInput{CartID: v.Cart.ID, PaymentMethod: domain.PayOnlineCard},
		func(context.Context, pgx.Tx, domain.Order) error { return boom })
	if !errors.Is(err, boom) {
		t.Fatalf("got %v", err)
	}
	for _, tbl := range []string{"orders", "order_items", "order_status_history", "outbox", "order_counters"} {
		if n := e.count(t, tbl); n != 0 {
			t.Errorf("%s must be empty after rollback, has %d", tbl, n)
		}
	}
	cv, _ := e.svc.GetCart(ctx, "", v.Cart.ID)
	if cv.Cart.Status != domain.CartOpen || cv.Cart.OrderID != "" {
		t.Fatalf("cart must be open: %+v", cv.Cart)
	}
	// после сбоя номер не «сгорает»
	o, err := e.svc.CreateOrder(ctx, "", service.CreateOrderInput{CartID: v.Cart.ID, PaymentMethod: domain.PayOnlineCard}, nil)
	if err != nil || o.Number != "R1-261018-0001" {
		t.Fatalf("%v %s", err, o.Number)
	}
}

func TestOrderNumbering(t *testing.T) {
	e := newEnv(t)
	n1 := e.order(t, "", domain.PayOnlineCard).Number
	n2 := e.order(t, "", domain.PayOnlineCard).Number
	if n1 != "R1-261018-0001" || n2 != "R1-261018-0002" {
		t.Fatalf("%s %s", n1, n2)
	}
	// другой день — счётчик с единицы; граница суток по часовому поясу ресторана (МСК = UTC+3)
	e.clock.Advance(14 * time.Hour) // 23:00 UTC = 02:00 MSK 19-го
	if n := e.order(t, "", domain.PayOnlineCard).Number; n != "R1-261019-0001" {
		t.Fatalf("next local day: %s", n)
	}
	// другой ресторан — свой код, номера не пересекаются
	v, _ := e.svc.CreateCart(ctx, "", service.CreateCartInput{RestaurantID: testkit.Restaurant2, Guest: true, Contact: domain.Contact{Phone: "+79990000003"}})
	e.svc.UpdateCartItems(ctx, "", v.Cart.ID, []service.ItemInput{{MenuItemID: testkit.Cola, Quantity: 1}})
	e.svc.SetFulfillment(ctx, "", v.Cart.ID, service.FulfillmentInput{Type: domain.FulfillmentPickup})
	o, err := e.svc.CreateOrder(ctx, "", service.CreateOrderInput{CartID: v.Cart.ID, PaymentMethod: domain.PayOnlineCard}, nil)
	// Владивосток (UTC+10): 23:00 UTC 18-го — уже 09:00 19-го
	if err != nil || o.Number != "R2-261019-0001" {
		t.Fatalf("%v %s", err, o.Number)
	}
}

func TestParallelOrdersGetUniqueContiguousNumbers(t *testing.T) {
	e := newEnv(t)
	const n = 20
	ids := make([]string, n)
	for i := range ids {
		ids[i] = e.cartWith(t, "").Cart.ID
	}
	var wg sync.WaitGroup
	nums := make(chan string, n)
	for _, id := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			o, err := e.svc.CreateOrder(ctx, "", service.CreateOrderInput{CartID: id, PaymentMethod: domain.PayOnlineCard}, nil)
			if err != nil {
				t.Errorf("create: %v", err)
				return
			}
			nums <- o.Number
		}()
	}
	wg.Wait()
	close(nums)
	seen := map[string]bool{}
	for num := range nums {
		if seen[num] {
			t.Fatalf("duplicate number %s", num)
		}
		seen[num] = true
	}
	for i := 1; i <= n; i++ {
		if !seen[fmt.Sprintf("R1-261018-%04d", i)] {
			t.Fatalf("gap: %04d missing in %v", i, seen)
		}
	}
	if len(e.outbox(t)) != 2*n {
		t.Fatalf("outbox: %d", len(e.outbox(t)))
	}
}

// DoD S2: параллельные оформления ОДНОЙ корзины (разные ключи) дают ровно один заказ.
func TestConcurrentCreateOnSameCartMakesOneOrder(t *testing.T) {
	e := newEnv(t)
	v := e.cartWith(t, "")
	const n = 12
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok, closed := 0, 0
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := e.svc.CreateOrder(ctx, "", service.CreateOrderInput{CartID: v.Cart.ID, PaymentMethod: domain.PayOnlineCard}, nil)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				ok++
			case code(t, err) == "CART_CLOSED" || code(t, err) == "CART_CHANGED":
				closed++
			default:
				t.Errorf("unexpected: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()
	if ok != 1 || closed != n-1 {
		t.Fatalf("ok=%d closed=%d", ok, closed)
	}
	if e.count(t, "orders") != 1 || len(e.outbox(t)) != 2 || e.count(t, "order_status_history") != 1 {
		t.Fatalf("orders=%d outbox=%d", e.count(t, "orders"), len(e.outbox(t)))
	}
}

func TestAccessControl(t *testing.T) {
	e := newEnv(t)
	uc := e.cartWith(t, "alice")
	for name, f := range map[string]func() error{
		"get cart":    func() error { _, err := e.svc.GetCart(ctx, "bob", uc.Cart.ID); return err },
		"guest read":  func() error { _, err := e.svc.GetCart(ctx, "", uc.Cart.ID); return err },
		"update cart": func() error { _, err := e.svc.UpdateCartItems(ctx, "bob", uc.Cart.ID, nil); return err },
		"fulfillment": func() error {
			_, err := e.svc.SetFulfillment(ctx, "bob", uc.Cart.ID, service.FulfillmentInput{Type: domain.FulfillmentPickup})
			return err
		},
		"order cart": func() error {
			_, err := e.svc.CreateOrder(ctx, "bob", service.CreateOrderInput{CartID: uc.Cart.ID, PaymentMethod: domain.PayPOS}, nil)
			return err
		},
	} {
		if err := f(); err == nil || code(t, err) != "CART_NOT_FOUND" {
			t.Errorf("%s: want CART_NOT_FOUND (existence must not leak), got %v", name, err)
		}
	}
	o, err := e.svc.CreateOrder(ctx, "alice", service.CreateOrderInput{CartID: uc.Cart.ID, PaymentMethod: domain.PayPOS}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, f := range map[string]func() error{
		"get order": func() error { _, err := e.svc.GetOrder(ctx, "bob", o.ID); return err },
		"guest get": func() error { _, err := e.svc.GetOrder(ctx, "", o.ID); return err },
		"eta":       func() error { _, err := e.svc.GetEta(ctx, "bob", o.ID); return err },
		"slot": func() error {
			_, err := e.svc.SetPickupSlot(ctx, "bob", o.ID, e.clock.Now().Add(time.Hour), e.clock.Now().Add(90*time.Minute))
			return err
		},
	} {
		if err := f(); err == nil || code(t, err) != "ORDER_NOT_FOUND" {
			t.Errorf("%s: want ORDER_NOT_FOUND, got %v", name, err)
		}
	}
	if _, err := e.svc.GetOrder(ctx, "alice", o.ID); err != nil {
		t.Fatal(err)
	}
	// гость владеет заказом по знанию id (UUIDv4 — ссылочный токен); другой «гость» без id ничего не найдёт
	g := e.order(t, "", domain.PayPOS)
	if _, err := e.svc.GetOrder(ctx, "", g.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.GetOrder(ctx, "", "a1000000-0000-4000-8000-0000000000ff"); err == nil || code(t, err) != "ORDER_NOT_FOUND" {
		t.Fatalf("unknown id: %v", err)
	}
}

func TestStateMachineViaTransition(t *testing.T) {
	e := newEnv(t)
	o := e.order(t, "", domain.PayOnlineCard)
	// MG1: неоплаченный заказ нельзя отправить в производство
	_, _, err := e.svc.Transition(ctx, o.ID, domain.StatusCooking, "", "test")
	if !errors.Is(err, service.ErrInvalidTransition) {
		t.Fatalf("pending_payment → cooking must be forbidden, got %v", err)
	}
	for _, step := range []domain.Status{domain.StatusAccepted, domain.StatusCooking, domain.StatusReady, domain.StatusHandedOver, domain.StatusClosed} {
		got, changed, err := e.svc.Transition(ctx, o.ID, step, "", "test")
		if err != nil || !changed || got.Status != step {
			t.Fatalf("→ %s: %v changed=%v", step, err, changed)
		}
	}
	// идемпотентность: повтор последнего перехода — не ошибка и не изменение
	_, changed, err := e.svc.Transition(ctx, o.ID, domain.StatusClosed, "", "test")
	if err != nil || changed {
		t.Fatalf("repeat must be a no-op: %v %v", err, changed)
	}
	_, _, err = e.svc.Transition(ctx, o.ID, domain.StatusCancelled, "", "test")
	if !errors.Is(err, service.ErrInvalidTransition) {
		t.Fatalf("closed is terminal: %v", err)
	}
	h, _ := e.svc.Store().History(ctx, e.pool, o.ID)
	if len(h) != 6 || h[5].To != domain.StatusClosed || h[5].From != domain.StatusHandedOver {
		t.Fatalf("history (MG3): %+v", h)
	}
	// 2 события создания + 5 переходов
	if n := len(e.outbox(t)); n != 7 {
		t.Fatalf("outbox: %d", n)
	}
	if _, _, err := e.svc.Transition(ctx, "a1000000-0000-4000-8000-0000000000ff", domain.StatusAccepted, "", "t"); err == nil {
		t.Fatal("unknown order")
	}
}

func TestExpireUnpaid(t *testing.T) {
	e := newEnv(t)
	old := e.order(t, "", domain.PayOnlineCard)
	pos := e.order(t, "", domain.PayPOS) // accepted: таймаут неоплаты его не касается
	e.clock.Advance(10 * time.Minute)
	fresh := e.order(t, "", domain.PayOnlineCard)
	e.clock.Advance(6 * time.Minute) // old: 16 мин > 15; fresh: 6 мин

	n, err := e.svc.ExpireUnpaid(ctx)
	if err != nil || n != 1 {
		t.Fatalf("expired=%d err=%v", n, err)
	}
	got, _ := e.svc.GetOrder(ctx, "", old.ID)
	if got.Status != domain.StatusCancelled || got.StatusReason != service.ReasonPaymentTimeout {
		t.Fatalf("old: %+v", got)
	}
	for _, id := range []string{fresh.ID, pos.ID} {
		g, _ := e.svc.GetOrder(ctx, "", id)
		if g.Status == domain.StatusCancelled {
			t.Fatalf("%s must not be cancelled", g.Number)
		}
	}
	h, _ := e.svc.Store().History(ctx, e.pool, old.ID)
	if len(h) != 2 || h[1].Actor != "system" || h[1].Reason != "payment_timeout" {
		t.Fatalf("history: %+v", h)
	}
	last := e.outbox(t)[len(e.outbox(t))-1]
	var ss service.OrderStatusEvent
	_ = json.Unmarshal(last.Payload, &ss)
	if last.Topic != "orders.status" || ss.Status != "cancelled" || ss.Reason != "payment_timeout" {
		t.Fatalf("cancel event: %+v", ss)
	}
	// повторный проход ничего не делает
	if n, err := e.svc.ExpireUnpaid(ctx); err != nil || n != 0 {
		t.Fatalf("second pass: %d %v", n, err)
	}
}

func TestExpireRacingWithPaymentSkipsOrder(t *testing.T) {
	e := newEnv(t)
	o := e.order(t, "", domain.PayOnlineCard)
	e.clock.Advance(20 * time.Minute)
	// оплата успела раньше таймаута: заказ принят
	if _, _, err := e.svc.Transition(ctx, o.ID, domain.StatusAccepted, "", "payment"); err != nil {
		t.Fatal(err)
	}
	if n, err := e.svc.ExpireUnpaid(ctx); err != nil || n != 0 {
		t.Fatalf("paid order must survive: %d %v", n, err)
	}
	g, _ := e.svc.GetOrder(ctx, "", o.ID)
	if g.Status != domain.StatusAccepted {
		t.Fatalf("status: %s", g.Status)
	}
}

func TestPurgeStaleCarts(t *testing.T) {
	e := newEnv(t)
	stale := e.cartWith(t, "")
	ordered := e.order(t, "", domain.PayOnlineCard)
	// carts.updated_at ставится БД (now()), поэтому «стареем» корзины прямо в таблице
	_, err := e.pool.Exec(ctx, `UPDATE carts SET updated_at = now() - interval '8 days' WHERE id = ANY($1)`, []string{stale.Cart.ID, ordered.CartID})
	if err != nil {
		t.Fatal(err)
	}
	fresh := e.cartWith(t, "")
	e.clock = testkit.NewClock()
	svc := service.New(service.Deps{Pool: e.pool, Menu: e.menu, Restaurants: e.rest, Log: testkit.Log(),
		Clock: func() time.Time { return time.Now().UTC() }, Config: service.Config{CartTTL: 7 * 24 * time.Hour}})
	n, err := svc.PurgeCarts(ctx)
	if err != nil || n != 1 {
		t.Fatalf("purged=%d err=%v", n, err)
	}
	if _, err := svc.GetCart(ctx, "", stale.Cart.ID); err == nil {
		t.Fatal("stale open cart must be gone")
	}
	if _, err := svc.GetCart(ctx, "", ordered.CartID); err != nil {
		t.Fatalf("ordered cart is kept for audit: %v", err)
	}
	if _, err := svc.GetCart(ctx, "", fresh.Cart.ID); err != nil {
		t.Fatal(err)
	}
}

func TestPickupSlot(t *testing.T) {
	e := newEnv(t)
	o := e.order(t, "", domain.PayOnlineCard)
	from := e.clock.Now().Add(2 * time.Hour)
	to := from.Add(30 * time.Minute)

	_, err := e.svc.SetPickupSlot(ctx, "", o.ID, to, from)
	wantCode(t, err, "INVALID_SLOT")
	_, err = e.svc.SetPickupSlot(ctx, "", o.ID, e.clock.Now().Add(-time.Hour), to)
	wantCode(t, err, "INVALID_SLOT")

	got, err := e.svc.SetPickupSlot(ctx, "", o.ID, from, to)
	if err != nil || got.Revision != 2 || got.PickupFrom == nil || !got.PickupFrom.Equal(from) {
		t.Fatalf("slot: %v %+v", err, got)
	}
	// ADR-10: каждое изменение состояния — orders.state с revision+1
	ob := e.outbox(t)
	if len(ob) != 3 || ob[2].Topic != "orders.state" {
		t.Fatalf("outbox: %d", len(ob))
	}
	var st service.OrderStateEvent
	_ = json.Unmarshal(ob[2].Payload, &st)
	want := from.UTC().Format(time.RFC3339) + "/" + to.UTC().Format(time.RFC3339)
	if st.Revision != 2 || st.RequestedPickupInterval != want || st.PaymentStatus != "pending" {
		t.Fatalf("state: %+v want interval %s", st, want)
	}
	// слот влияет на ETA
	eta, _ := e.svc.GetEta(ctx, "", o.ID)
	if !eta.ReadyAt.Equal(from) {
		t.Fatalf("eta ready: %v", eta.ReadyAt)
	}
	// после начала приготовления слот менять нельзя
	e.svc.Transition(ctx, o.ID, domain.StatusAccepted, "", "p")
	e.svc.Transition(ctx, o.ID, domain.StatusCooking, "", "p")
	_, err = e.svc.SetPickupSlot(ctx, "", o.ID, from, to)
	wantCode(t, err, "ORDER_STATE_CONFLICT")

	// доставке слот не нужен
	cv := e.cartWith(t, "")
	e.svc.SetFulfillment(ctx, "", cv.Cart.ID, service.FulfillmentInput{Type: domain.FulfillmentDelivery, Address: domain.Address{Raw: "ул. Тверская, 1"}})
	d, err := e.svc.CreateOrder(ctx, "", service.CreateOrderInput{CartID: cv.Cart.ID, PaymentMethod: domain.PayCourierCard}, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.SetPickupSlot(ctx, "", d.ID, from, to)
	wantCode(t, err, "INVALID_SLOT")
}

func TestEtaIsApproximate(t *testing.T) {
	e := newEnv(t)
	o := e.order(t, "", domain.PayOnlineCard)
	eta, err := e.svc.GetEta(ctx, "", o.ID)
	if err != nil || !eta.Approximate || eta.DeliveryAt != nil || eta.EstimateMinutes != 20 || !eta.ReadyAt.Equal(o.CreatedAt.Add(20*time.Minute)) {
		t.Fatalf("pickup eta: %v %+v", err, eta)
	}
	cv := e.cartWith(t, "")
	e.svc.SetFulfillment(ctx, "", cv.Cart.ID, service.FulfillmentInput{Type: domain.FulfillmentDelivery, Address: domain.Address{Raw: "ул. Тверская, 1"}})
	d, _ := e.svc.CreateOrder(ctx, "", service.CreateOrderInput{CartID: cv.Cart.ID, PaymentMethod: domain.PayCourierCard}, nil)
	de, _ := e.svc.GetEta(ctx, "", d.ID)
	if de.DeliveryAt == nil || !de.Approximate || de.EstimateMinutes != 50 {
		t.Fatalf("delivery eta: %+v", de)
	}
	if d.Status != domain.StatusAccepted {
		t.Fatalf("courier payment → accepted, got %s", d.Status)
	}
}

func testkitLog() *slog.Logger { return testkit.Log() }
