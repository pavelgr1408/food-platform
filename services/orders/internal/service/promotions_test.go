package service_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pavelgr1408/food-platform/pkg/kafka"
	"github.com/pavelgr1408/food-platform/pkg/pgxutil"
	"github.com/pavelgr1408/food-platform/services/orders/internal/domain"
	"github.com/pavelgr1408/food-platform/services/orders/internal/service"
	"github.com/pavelgr1408/food-platform/services/orders/internal/testkit"
)

const (
	promoA = "c1000000-0000-4000-8000-00000000000a"
	promoB = "c1000000-0000-4000-8000-00000000000b"
)

// promoEnv — окружение без кэша реплики: событие видно сразу.
func promoEnv(t *testing.T) *env {
	t.Helper()
	e := &env{pool: testkit.Pool(t), menu: testkit.NewMenu(), rest: testkit.NewRestaurants(), clock: testkit.NewClock(), m: service.NewMetrics()}
	e.svc = service.New(service.Deps{Pool: e.pool, Menu: e.menu, Restaurants: e.rest, Log: testkit.Log(),
		Clock: e.clock.Now, Metrics: e.m,
		Config: service.Config{PaymentTimeout: 15 * time.Minute, PromotionCacheTTL: -1}})
	return e
}

type promoEv map[string]any

// percent15 — национальная акция −15 % на всё.
func percent15(id string, version int, status string) promoEv {
	return promoEv{"promotionId": id, "version": version, "scope": "national", "title": "−15%", "currency": "RUB",
		"conditions": map[string]any{}, "discount": map[string]any{"type": "percent", "value": 1500},
		"priority": 10, "validFrom": "2026-10-01T00:00:00Z", "status": status}
}

func (e *env) feed(t *testing.T, p promoEv) error {
	t.Helper()
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	ev := kafka.Event{Type: "promotions.discount.v1", Topic: service.TopicPromotions, Key: p["promotionId"].(string), Payload: raw}
	return pgxutil.InTx(ctx, e.pool, func(tx pgx.Tx) error { return e.svc.HandlePromotion(ctx, tx, ev) })
}

func (e *env) mustFeed(t *testing.T, p promoEv) {
	t.Helper()
	if err := e.feed(t, p); err != nil {
		t.Fatal(err)
	}
}

func TestPromotionAppliesToCartAndOrder(t *testing.T) {
	e := promoEnv(t)
	v := e.cartWith(t, "")
	if v.DiscountTotal != 0 || len(v.Applied) != 0 {
		t.Fatalf("no promotions yet: %+v", v)
	}
	e.mustFeed(t, percent15(promoA, 1, "active"))

	v = e.cartWith(t, "")
	// 15 % от 100500 = 15075
	if v.Subtotal != 100500 || v.DiscountTotal != 15075 || v.Total != 85425 {
		t.Fatalf("cart: sub=%d disc=%d total=%d", v.Subtotal, v.DiscountTotal, v.Total)
	}
	if len(v.Applied) != 1 || v.Applied[0].PromotionID != promoA || v.Applied[0].Discount != 15075 {
		t.Fatalf("applied: %+v", v.Applied)
	}

	o, err := e.svc.CreateOrder(ctx, "", service.CreateOrderInput{CartID: v.Cart.ID, PaymentMethod: domain.PayPOS}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if o.DiscountTotal != 15075 || o.Total != 85425 || len(o.Applied) != 1 || o.Applied[0].PromotionID != promoA {
		t.Fatalf("order: %+v", o)
	}
	// снимок скидки в заказе не меняется после завершения акции
	e.mustFeed(t, percent15(promoA, 2, "completed"))
	got, err := e.svc.GetOrder(ctx, "", o.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.DiscountTotal != 15075 || len(got.Applied) != 1 {
		t.Fatalf("snapshot must be immutable: %+v", got)
	}

	// событие orders.state содержит appliedPromotions
	var found bool
	for _, r := range e.outbox(t) {
		if r.Type != "orders.state.v1" {
			continue
		}
		var p struct {
			AppliedPromotions []string `json:"appliedPromotions"`
		}
		if err := json.Unmarshal(r.Payload, &p); err != nil {
			t.Fatal(err)
		}
		if len(p.AppliedPromotions) == 1 && p.AppliedPromotions[0] == promoA {
			found = true
		}
	}
	if !found {
		t.Error("orders.state.v1 must carry appliedPromotions")
	}
}

func TestPromotionVersionsStaleDuplicateCompleted(t *testing.T) {
	e := promoEnv(t)
	e.mustFeed(t, percent15(promoA, 2, "active"))
	stale := percent15(promoA, 1, "completed") // старая версия не должна отменить действующую
	e.mustFeed(t, stale)
	e.mustFeed(t, percent15(promoA, 2, "active")) // дубликат
	if v := e.cartWith(t, ""); v.DiscountTotal != 15075 {
		t.Fatalf("stale/duplicate must not change the replica: %d", v.DiscountTotal)
	}
	if e.count(t, "promotions_replica") != 1 {
		t.Fatal("one row per promotion")
	}
	e.mustFeed(t, percent15(promoA, 3, "completed"))
	if v := e.cartWith(t, ""); v.DiscountTotal != 0 || len(v.Applied) != 0 {
		t.Fatalf("completed promotion must not apply: %+v", v)
	}
}

func TestPromotionValidityWindow(t *testing.T) {
	e := promoEnv(t)
	p := percent15(promoA, 1, "active")
	p["validFrom"] = "2026-10-18T10:00:00Z" // через час после «сейчас»
	p["validTo"] = "2026-10-18T12:00:00Z"
	e.mustFeed(t, p)
	if v := e.cartWith(t, ""); v.DiscountTotal != 0 {
		t.Fatalf("not started yet: %d", v.DiscountTotal)
	}
	e.clock.Advance(90 * time.Minute)
	if v := e.cartWith(t, ""); v.DiscountTotal != 15075 {
		t.Fatalf("in window: %d", v.DiscountTotal)
	}
	e.clock.Advance(3 * time.Hour)
	if v := e.cartWith(t, ""); v.DiscountTotal != 0 {
		t.Fatalf("expired by validTo: %d", v.DiscountTotal)
	}
}

func TestPromotionLocalScopeOtherRestaurantIgnored(t *testing.T) {
	e := promoEnv(t)
	p := percent15(promoA, 1, "active")
	p["scope"] = "local"
	p["restaurantId"] = "a1000000-0000-4000-8000-0000000000ff"
	e.mustFeed(t, p)
	if v := e.cartWith(t, ""); v.DiscountTotal != 0 {
		t.Fatalf("local promotion of another restaurant: %d", v.DiscountTotal)
	}
	p2 := percent15(promoB, 1, "active")
	p2["scope"] = "local"
	p2["restaurantId"] = testkit.Restaurant1
	e.mustFeed(t, p2)
	if v := e.cartWith(t, ""); v.DiscountTotal != 15075 {
		t.Fatalf("local promotion of the cart's restaurant: %d", v.DiscountTotal)
	}
}

func TestPromotionInvalidPayloadIsError(t *testing.T) {
	e := promoEnv(t)
	bad := []promoEv{
		func() promoEv { p := percent15(promoA, 1, "active"); p["promotionId"] = "nope"; return p }(),
		func() promoEv { p := percent15(promoA, 0, "active"); return p }(),
		func() promoEv { p := percent15(promoA, 1, "paused"); return p }(),
		func() promoEv {
			p := percent15(promoA, 1, "active")
			p["discount"] = map[string]any{"type": "percent", "value": 10001}
			return p
		}(),
		func() promoEv {
			p := percent15(promoA, 1, "active")
			p["scope"] = "local"
			return p
		}(),
		func() promoEv { p := percent15(promoA, 1, "active"); delete(p, "validFrom"); return p }(),
		func() promoEv {
			p := percent15(promoA, 1, "active")
			p["discount"] = map[string]any{"type": "percent"}
			return p
		}(),
	}
	for i, p := range bad {
		if err := e.feed(t, p); err == nil {
			t.Errorf("case %d: invalid payload must fail (to retry → DLQ)", i)
		}
	}
	if e.count(t, "promotions_replica") != 0 {
		t.Fatal("invalid payloads must not be stored")
	}
	// мусор вместо JSON
	err := pgxutil.InTx(ctx, e.pool, func(tx pgx.Tx) error {
		return e.svc.HandlePromotion(ctx, tx, kafka.Event{Payload: []byte("{")})
	})
	if err == nil {
		t.Error("broken JSON must fail")
	}
}

func TestPromotionGiftStoredButIgnored(t *testing.T) {
	e := promoEnv(t)
	p := percent15(promoA, 1, "active")
	p["discount"] = map[string]any{"type": "gift", "value": 0}
	e.mustFeed(t, p)
	if e.count(t, "promotions_replica") != 1 {
		t.Fatal("gift is stored")
	}
	if v := e.cartWith(t, ""); v.DiscountTotal != 0 {
		t.Fatalf("gift gives no money discount: %d", v.DiscountTotal)
	}
}

func TestCreateOrderExpectedTotal(t *testing.T) {
	e := promoEnv(t)
	v := e.cartWith(t, "")
	exp := &service.Money{Amount: v.Total, Currency: "rub"}
	// скидка появилась между просмотром корзины и оформлением
	e.mustFeed(t, percent15(promoA, 1, "active"))
	_, err := e.svc.CreateOrder(ctx, "", service.CreateOrderInput{CartID: v.Cart.ID, PaymentMethod: domain.PayPOS, ExpectedTotal: exp}, nil)
	wantCode(t, err, "TOTAL_CHANGED")
	if e.count(t, "orders") != 0 {
		t.Fatal("no order on mismatch")
	}
	// клиент увидел новый итог → заказ создаётся
	v = e.cartWith(t, "")
	o, err := e.svc.CreateOrder(ctx, "", service.CreateOrderInput{CartID: v.Cart.ID, PaymentMethod: domain.PayPOS,
		ExpectedTotal: &service.Money{Amount: 85425, Currency: "RUB"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if o.Total != 85425 {
		t.Fatalf("total %d", o.Total)
	}
	// другая валюта — тоже расхождение
	v = e.cartWith(t, "")
	_, err = e.svc.CreateOrder(ctx, "", service.CreateOrderInput{CartID: v.Cart.ID, PaymentMethod: domain.PayPOS,
		ExpectedTotal: &service.Money{Amount: 85425, Currency: "USD"}}, nil)
	wantCode(t, err, "TOTAL_CHANGED")
}

func TestReplicaCacheAndStaleIfError(t *testing.T) {
	// с кэшем: событие, применённое в этом экземпляре, сбрасывает кэш
	e := newEnv(t)
	if v := e.cartWith(t, ""); v.DiscountTotal != 0 {
		t.Fatal("no discount")
	}
	e.mustFeed(t, percent15(promoA, 1, "active"))
	if v := e.cartWith(t, ""); v.DiscountTotal != 15075 {
		t.Fatalf("cache must be invalidated by the event: %d", v.DiscountTotal)
	}
	if err := e.svc.HandlePromotion(ctx, nil, kafka.Event{Payload: []byte("{")}); err == nil {
		t.Fatal("expected error")
	}

	// stale-if-error: после закрытия пула расчёт использует последний снимок, а не падает
	e2 := newEnv(t)
	e2.mustFeed(t, percent15(promoA, 1, "active"))
	v := e2.cartWith(t, "")
	if v.DiscountTotal != 15075 {
		t.Fatal("setup")
	}
	if _, err := e2.pool.Exec(ctx, `ALTER TABLE promotions_replica RENAME TO promotions_replica_x`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = e2.pool.Exec(ctx, `ALTER TABLE promotions_replica_x RENAME TO promotions_replica`) })
	e2.clock.Advance(time.Hour) // кэш протух → попытка перечитать → ошибка → прежний снимок
	got, err := e2.svc.GetCart(ctx, "", v.Cart.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.DiscountTotal != 15075 {
		t.Fatalf("stale snapshot must be used on error: %d", got.DiscountTotal)
	}
}
