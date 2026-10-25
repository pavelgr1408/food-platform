package service_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pavelgr1408/food-platform/pkg/kafka"
	"github.com/pavelgr1408/food-platform/services/orders/internal/domain"
	"github.com/pavelgr1408/food-platform/services/orders/internal/service"
	"github.com/pavelgr1408/food-platform/services/orders/internal/testkit"
)

var (
	staff1 = service.Actor{UserID: "staff-1", Staff: true, Restaurants: []string{testkit.Restaurant1}}
	staff2 = service.Actor{UserID: "staff-2", Staff: true, Restaurants: []string{testkit.Restaurant2}}
	hq     = service.Actor{UserID: "hq-1", Staff: true, HQ: true}
	client = service.Actor{UserID: "user-1"} // CUSTOMER: роли персонала нет
	guest  = service.Actor{}
)

// faxOrder оформляет заказ по факсу от имени staff1 (самовывоз, оплата на кассе).
func (e *env) faxOrder(t *testing.T) domain.Order {
	t.Helper()
	v := e.cartWith(t, staff1.UserID)
	o, err := e.svc.CreateOrder(ctx, staff1.UserID, service.CreateOrderInput{CartID: v.Cart.ID, PaymentMethod: domain.PayPOS,
		Channel: "staff_fax", Actor: staff1, Contact: domain.Contact{Phone: "+79995550101", Name: "Клиент по факсу"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return o
}

// toReady проводит заказ accepted → cooking → ready (как это делает производство).
func (e *env) toReady(t *testing.T, id string) {
	t.Helper()
	for _, st := range []domain.Status{domain.StatusCooking, domain.StatusReady} {
		if _, _, err := e.svc.Transition(ctx, id, st, "", "system"); err != nil {
			t.Fatal(err)
		}
	}
}

func (e *env) payRefund(t *testing.T, orderID, refund string, at time.Time) {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"orderId": orderID, "paymentId": uuid.NewString(), "attempt": 1, "status": "paid",
		"method": "online", "updatedAt": at, "refundStatus": refund})
	ev := kafka.Event{ID: uuid.New(), Topic: service.TopicPaymentStatus, Key: orderID, Payload: b}
	if _, err := kafka.ProcessOnce(ctx, e.pool, "orders-service", ev, e.svc.HandlePaymentStatus); err != nil {
		t.Fatalf("refund event %s: %v", refund, err)
	}
}

func TestFaxOrderByStaff(t *testing.T) {
	e := newEnv(t)
	o := e.faxOrder(t)
	if o.Channel != "staff_fax" || o.OwnerUserID != "" || o.CreatedBy != "staff-1" || o.Status != domain.StatusAccepted ||
		o.PaymentMethod != domain.PayPOS || o.Contact.Phone != "+79995550101" {
		t.Fatalf("%+v", o)
	}
	// владельца нет: заказ — клиента-гостя, видим по знанию UUID; сотрудник — в журнале
	if _, err := e.svc.GetOrder(ctx, "", o.ID); err != nil {
		t.Fatal(err)
	}
	h, _ := e.svc.Store().History(ctx, e.pool, o.ID)
	if len(h) != 1 || h[0].Actor != "staff:staff-1" {
		t.Fatalf("history: %+v", h)
	}
	var st service.OrderStateEvent
	_ = json.Unmarshal(e.outbox(t)[0].Payload, &st)
	if st.Source != "fax" || !st.Customer.Guest || st.Customer.Phone != "+79995550101" || st.PaymentMethod != "restaurant" {
		t.Fatalf("state: %+v", st)
	}
}

func TestFaxOrderRules(t *testing.T) {
	e := newEnv(t)
	create := func(a service.Actor, cartOwner string, pm domain.PaymentMethod, c domain.Contact) error {
		v := e.cartWith(t, cartOwner)
		_, err := e.svc.CreateOrder(ctx, cartOwner, service.CreateOrderInput{CartID: v.Cart.ID, PaymentMethod: pm,
			Channel: "staff_fax", Actor: a, Contact: c}, nil)
		return err
	}
	phone := domain.Contact{Phone: "+79995550101"}
	wantCode(t, create(guest, "", domain.PayPOS, phone), "UNAUTHORIZED")
	wantCode(t, create(client, "user-1", domain.PayPOS, phone), "FORBIDDEN")
	wantCode(t, create(staff2, "staff-2", domain.PayPOS, phone), "FORBIDDEN") // корзина ресторана 1, сотрудник ресторана 2
	wantCode(t, create(staff1, "staff-1", domain.PayOnlineCard, phone), "PAYMENT_METHOD_NOT_ALLOWED")
	// у корзины есть телефон (cartWith), но контакт заказа по факсу — отдельная проверка только если телефона нет совсем
	v, _ := e.svc.CreateCart(ctx, "staff-1", service.CreateCartInput{RestaurantID: testkit.Restaurant1})
	e.svc.UpdateCartItems(ctx, "staff-1", v.Cart.ID, []service.ItemInput{{MenuItemID: testkit.Cola, Quantity: 1}})
	e.svc.SetFulfillment(ctx, "staff-1", v.Cart.ID, service.FulfillmentInput{Type: domain.FulfillmentPickup})
	_, err := e.svc.CreateOrder(ctx, "staff-1", service.CreateOrderInput{CartID: v.Cart.ID, PaymentMethod: domain.PayPOS, Channel: "staff_fax", Actor: staff1}, nil)
	wantCode(t, err, "CONTACT_REQUIRED")
	if err := create(hq, "hq-1", domain.PayPOS, phone); err != nil { // HQ_STAFF — любой ресторан
		t.Fatal(err)
	}
}

func TestSearchOrderScopedToRestaurant(t *testing.T) {
	e := newEnv(t)
	o := e.faxOrder(t)

	got, err := e.svc.SearchOrder(ctx, staff1, "  "+lower(o.Number)+" ")
	if err != nil || got.ID != o.ID || got.Contact.Phone != "+79995550101" {
		t.Fatalf("%+v %v", got, err)
	}
	if got, err = e.svc.SearchOrder(ctx, hq, o.Number); err != nil || got.ID != o.ID {
		t.Fatalf("hq: %+v %v", got, err)
	}
	_, err = e.svc.SearchOrder(ctx, staff2, o.Number) // чужой ресторан неотличим от несуществующего
	wantCode(t, err, "ORDER_NOT_FOUND")
	_, err = e.svc.SearchOrder(ctx, staff1, "R1-261018-9999")
	wantCode(t, err, "ORDER_NOT_FOUND")
	for _, bad := range []string{"1234", "R1-26101-0001", "DROP TABLE", "R1-261018-0001'; --"} {
		_, err = e.svc.SearchOrder(ctx, staff1, bad)
		wantCode(t, err, "INVALID_ORDER_NUMBER")
	}
	_, err = e.svc.SearchOrder(ctx, staff1, " ")
	wantCode(t, err, "INVALID_PARAMETER")
	_, err = e.svc.SearchOrder(ctx, guest, o.Number)
	wantCode(t, err, "UNAUTHORIZED")
	_, err = e.svc.SearchOrder(ctx, client, o.Number)
	wantCode(t, err, "FORBIDDEN")
}

func lower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}

func TestIssueRequiresReadyAndPaid(t *testing.T) {
	e := newEnv(t)
	o := e.faxOrder(t) // pos: оплата на кассе при выдаче

	_, err := e.svc.IssueOrder(ctx, staff1, o.ID) // ещё accepted
	wantCode(t, err, "ORDER_STATE_CONFLICT")
	e.toReady(t, o.ID)
	_, err = e.svc.IssueOrder(ctx, staff1, o.ID) // готов, но касса оплату не приняла
	wantCode(t, err, "ORDER_NOT_PAID")

	// чужие и неправомочные получают отказ раньше любых деталей
	_, err = e.svc.IssueOrder(ctx, staff2, o.ID)
	wantCode(t, err, "ORDER_NOT_FOUND")
	_, err = e.svc.IssueOrder(ctx, client, o.ID)
	wantCode(t, err, "FORBIDDEN")
	_, err = e.svc.IssueOrder(ctx, guest, o.ID)
	wantCode(t, err, "UNAUTHORIZED")
	_, err = e.svc.IssueOrder(ctx, staff1, "not-a-uuid")
	wantCode(t, err, "ORDER_NOT_FOUND")

	e.mustPay(t, o.ID, "paid", e.clock.Now()) // вебхук кассы → payment → orders (как для онлайн-оплаты)
	n := len(e.outbox(t))
	got, err := e.svc.IssueOrder(ctx, staff1, o.ID)
	if err != nil || got.Status != domain.StatusClosed || got.PaymentStatus != domain.PaymentPaid {
		t.Fatalf("%+v %v", got, err)
	}
	// два шага журнала и два события orders.status, оба с reason=issued
	h, _ := e.svc.Store().History(ctx, e.pool, o.ID)
	last := h[len(h)-2:]
	if last[0].To != domain.StatusHandedOver || last[1].To != domain.StatusClosed || last[0].Actor != "staff:staff-1" || last[1].Reason != "issued" {
		t.Fatalf("history: %+v", h)
	}
	ob := e.outbox(t)[n:]
	if len(ob) != 2 || ob[0].Topic != "orders.status" || ob[1].Topic != "orders.status" {
		t.Fatalf("outbox: %+v", ob)
	}
	var s1, s2 service.OrderStatusEvent
	_ = json.Unmarshal(ob[0].Payload, &s1)
	_ = json.Unmarshal(ob[1].Payload, &s2)
	if s1.Status != "handed_over" || s2.Status != "closed" || s2.Reason != "issued" {
		t.Fatalf("%+v %+v", s1, s2)
	}

	// повтор — без эффекта
	again, err := e.svc.IssueOrder(ctx, staff1, o.ID)
	if err != nil || again.Status != domain.StatusClosed || len(e.outbox(t)) != n+2 {
		t.Fatalf("repeat: %+v %v outbox=%d", again, err, len(e.outbox(t))-n)
	}
}

func TestIssueOnlyPickupAndNotAfterCancel(t *testing.T) {
	e := newEnv(t)
	// доставка: выдаёт курьер (S7), не касса
	v := e.cartWith(t, "staff-1")
	e.svc.SetFulfillment(ctx, "staff-1", v.Cart.ID, service.FulfillmentInput{Type: domain.FulfillmentDelivery, Address: nearAddr("Москва, Тверская 1")})
	d, err := e.svc.CreateOrder(ctx, "staff-1", service.CreateOrderInput{CartID: v.Cart.ID, PaymentMethod: domain.PayCourierCard,
		Channel: "staff_fax", Actor: staff1, Contact: domain.Contact{Phone: "+79995550101"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	e.toReady(t, d.ID)
	_, err = e.svc.IssueOrder(ctx, staff1, d.ID)
	wantCode(t, err, "NOT_PICKUP_ORDER")

	// отменённый заказ не выдаётся
	c := e.faxOrder(t)
	if _, err := e.svc.CancelOrder(ctx, staff1, c.ID, "customer_request"); err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.IssueOrder(ctx, staff1, c.ID)
	wantCode(t, err, "ORDER_STATE_CONFLICT")
}

func TestNotReceivedTimer(t *testing.T) {
	e := newEnv(t)
	ready := e.faxOrder(t)
	e.toReady(t, ready.ID)
	issued := e.faxOrder(t)
	e.toReady(t, issued.ID)
	e.mustPay(t, issued.ID, "paid", e.clock.Now())
	if _, err := e.svc.IssueOrder(ctx, staff1, issued.ID); err != nil {
		t.Fatal(err)
	}
	cooking := e.faxOrder(t)
	if _, _, err := e.svc.Transition(ctx, cooking.ID, domain.StatusCooking, "", "system"); err != nil {
		t.Fatal(err)
	}

	if n, err := e.svc.ExpireNotReceived(ctx); err != nil || n != 0 {
		t.Fatalf("too early: n=%d err=%v", n, err)
	}
	e.clock.Advance(2*time.Hour + time.Minute)
	n := len(e.outbox(t))
	if got, err := e.svc.ExpireNotReceived(ctx); err != nil || got != 1 {
		t.Fatalf("n=%d err=%v", got, err)
	}
	if got, _ := e.svc.ExpireNotReceived(ctx); got != 0 { // повторный запуск ничего не делает
		t.Fatalf("second run: %d", got)
	}
	o, _ := e.svc.GetOrder(ctx, "", ready.ID)
	if o.Status != domain.StatusNotReceived || o.StatusReason != "pickup_timeout" {
		t.Fatalf("%+v", o)
	}
	if i, _ := e.svc.GetOrder(ctx, "", issued.ID); i.Status != domain.StatusClosed {
		t.Fatalf("issued order touched: %+v", i)
	}
	if c, _ := e.svc.GetOrder(ctx, "", cooking.ID); c.Status != domain.StatusCooking {
		t.Fatalf("cooking order touched: %+v", c)
	}
	var ss service.OrderStatusEvent
	ob := e.outbox(t)[n:]
	_ = json.Unmarshal(ob[len(ob)-1].Payload, &ss)
	if len(ob) != 1 || ss.Status != "not_received" || ss.Reason != "pickup_timeout" {
		t.Fatalf("%+v", ob)
	}
	_, err := e.svc.IssueOrder(ctx, staff1, ready.ID) // поздно
	wantCode(t, err, "ORDER_STATE_CONFLICT")
}

func TestCancelByStaff(t *testing.T) {
	e := newEnv(t)
	o := e.faxOrder(t)
	_, err := e.svc.CancelOrder(ctx, staff1, o.ID, "because")
	wantCode(t, err, "INVALID_PARAMETER")
	_, err = e.svc.CancelOrder(ctx, staff2, o.ID, "other")
	wantCode(t, err, "ORDER_NOT_FOUND")
	_, err = e.svc.CancelOrder(ctx, client, o.ID, "other")
	wantCode(t, err, "FORBIDDEN")

	n := len(e.outbox(t))
	got, err := e.svc.CancelOrder(ctx, staff1, o.ID, "out_of_stock")
	if err != nil || got.Status != domain.StatusCancelled || got.StatusReason != "out_of_stock" || got.RefundStatus != "" {
		t.Fatalf("%+v %v", got, err)
	}
	ob := e.outbox(t)[n:]
	var ss service.OrderStatusEvent
	_ = json.Unmarshal(ob[0].Payload, &ss)
	if len(ob) != 1 || ss.Status != "cancelled" || ss.Reason != "out_of_stock" {
		t.Fatalf("%+v", ob)
	}
	again, err := e.svc.CancelOrder(ctx, staff1, o.ID, "other") // повтор: уже отменён, причина прежняя
	if err != nil || again.StatusReason != "out_of_stock" || len(e.outbox(t)) != n+1 {
		t.Fatalf("repeat: %+v %v", again, err)
	}

	// после начала приготовления отменить нельзя
	c := e.faxOrder(t)
	if _, _, err := e.svc.Transition(ctx, c.ID, domain.StatusCooking, "", "system"); err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.CancelOrder(ctx, staff1, c.ID, "other")
	wantCode(t, err, "ORDER_STATE_CONFLICT")
}

func TestCancelPaidOrderStartsRefund(t *testing.T) {
	e := newEnv(t)
	v := e.cartWith(t, "")
	o, err := e.svc.CreateOrder(ctx, "", service.CreateOrderInput{CartID: v.Cart.ID, PaymentMethod: domain.PayOnlineCard}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// неоплаченный онлайн-заказ: отмена без возврата
	u, err := e.svc.CancelOrder(ctx, hq, o.ID, "customer_request")
	if err != nil || u.Status != domain.StatusCancelled || u.RefundStatus != "" {
		t.Fatalf("%+v %v", u, err)
	}

	paid := e.order(t, "", domain.PayOnlineCard)
	e.mustPay(t, paid.ID, "paid", e.clock.Now())
	got, err := e.svc.CancelOrder(ctx, hq, paid.ID, "restaurant_closed")
	if err != nil || got.Status != domain.StatusCancelled || got.RefundStatus != domain.RefundPending || got.PaymentStatus != domain.PaymentPaid {
		t.Fatalf("%+v %v", got, err)
	}
	// payment сообщает ход возврата: pending → refunded; назад не откатывается
	at := e.clock.Now().Add(time.Second)
	e.payRefund(t, paid.ID, "pending", at)
	e.payRefund(t, paid.ID, "refunded", at.Add(time.Second))
	e.payRefund(t, paid.ID, "pending", at.Add(2*time.Second))
	e.payRefund(t, paid.ID, "failed", at.Add(3*time.Second))
	r, _ := e.svc.GetOrder(ctx, "", paid.ID)
	if r.RefundStatus != domain.RefundRefunded {
		t.Fatalf("refund: %s", r.RefundStatus)
	}
}

func TestRefundFailedThenRefunded(t *testing.T) {
	e := newEnv(t)
	o := e.order(t, "", domain.PayOnlineCard)
	e.mustPay(t, o.ID, "paid", e.clock.Now())
	if _, err := e.svc.CancelOrder(ctx, hq, o.ID, "other"); err != nil {
		t.Fatal(err)
	}
	at := e.clock.Now()
	e.payRefund(t, o.ID, "failed", at.Add(time.Second))
	if g, _ := e.svc.GetOrder(ctx, "", o.ID); g.RefundStatus != domain.RefundFailed {
		t.Fatalf("%s", g.RefundStatus)
	}
	e.payRefund(t, o.ID, "refunded", at.Add(time.Minute)) // ручной разбор: деньги вернули
	if g, _ := e.svc.GetOrder(ctx, "", o.ID); g.RefundStatus != domain.RefundRefunded {
		t.Fatalf("%s", g.RefundStatus)
	}
}

func TestPaidAfterCancelMarksRefundPending(t *testing.T) {
	e := newEnv(t)
	o := e.order(t, "", domain.PayOnlineCard)
	if _, err := e.svc.CancelOrder(ctx, hq, o.ID, "other"); err != nil {
		t.Fatal(err)
	}
	e.mustPay(t, o.ID, "paid", e.clock.Now()) // деньги пришли после отмены
	g, _ := e.svc.GetOrder(ctx, "", o.ID)
	if g.Status != domain.StatusCancelled || g.PaymentStatus != domain.PaymentPaid || g.RefundStatus != domain.RefundPending {
		t.Fatalf("%+v", g)
	}
	e.payRefund(t, o.ID, "refunded", e.clock.Now().Add(time.Second))
	if g, _ = e.svc.GetOrder(ctx, "", o.ID); g.RefundStatus != domain.RefundRefunded {
		t.Fatalf("%s", g.RefundStatus)
	}
}

func TestIssueRaceWithTimer(t *testing.T) {
	// выдача и таймер неполучения одновременно: выигрывает один, заказ не ломается
	e := newEnv(t)
	o := e.faxOrder(t)
	e.toReady(t, o.ID)
	e.mustPay(t, o.ID, "paid", e.clock.Now())
	e.clock.Advance(3 * time.Hour)
	done := make(chan error, 2)
	go func() { _, err := e.svc.IssueOrder(ctx, staff1, o.ID); done <- err }()
	go func() { _, err := e.svc.ExpireNotReceived(ctx); done <- err }()
	var failures int
	for i := 0; i < 2; i++ {
		if err := <-done; err != nil {
			wantCode(t, err, "ORDER_STATE_CONFLICT")
			failures++
		}
	}
	g, _ := e.svc.GetOrder(ctx, "", o.ID)
	if (g.Status == domain.StatusClosed && failures != 0) || (g.Status == domain.StatusNotReceived && failures != 1) ||
		(g.Status != domain.StatusClosed && g.Status != domain.StatusNotReceived) {
		t.Fatalf("status=%s failures=%d", g.Status, failures)
	}
}
