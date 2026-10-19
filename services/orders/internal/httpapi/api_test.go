package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pavelgr1408/food-platform/pkg/authclient"
	"github.com/pavelgr1408/food-platform/pkg/idempotency"
	"github.com/pavelgr1408/food-platform/pkg/metrics"
	"github.com/pavelgr1408/food-platform/services/orders/internal/httpapi"
	"github.com/pavelgr1408/food-platform/services/orders/internal/service"
	"github.com/pavelgr1408/food-platform/services/orders/internal/testkit"
)

func newServer(t *testing.T) (*httptest.Server, *testkit.FakeMenu, *testkit.FakeRestaurants, func(string) int) {
	t.Helper()
	pool := testkit.Pool(t)
	menu, rest := testkit.NewMenu(), testkit.NewRestaurants()
	svc := service.New(service.Deps{Pool: pool, Menu: menu, Restaurants: rest, Log: testkit.Log()})
	h := httpapi.New(httpapi.Deps{Log: testkit.Log(), Metrics: metrics.New("orders"), Auth: authclient.DevAuthenticator{},
		Idempotency: idempotency.NewStore(pool), Orders: svc})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	count := func(table string) int {
		var n int
		if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM `+table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	return srv, menu, rest, count
}

type resp struct {
	Status int
	Header http.Header
	Body   []byte
}

func (r resp) json(t *testing.T) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(r.Body, &m); err != nil {
		t.Fatalf("body is not JSON: %v\n%s", err, r.Body)
	}
	return m
}

func do(t *testing.T, srv *httptest.Server, method, path string, body any, hdr map[string]string) resp {
	t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rd = strings.NewReader(b)
	default:
		raw, _ := json.Marshal(b)
		rd = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest(method, srv.URL+path, rd)
	if rd != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return resp{res.StatusCode, res.Header, b}
}

func wantProblem(t *testing.T, r resp, status int, code string) {
	t.Helper()
	if r.Status != status || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/problem+json") {
		t.Fatalf("want %d problem, got %d %s: %s", status, r.Status, r.Header.Get("Content-Type"), r.Body)
	}
	if got := r.json(t)["code"]; got != code {
		t.Fatalf("problem code: got %v want %s: %s", got, code, r.Body)
	}
}

// readyCart: гостевая корзина с бургером×2+беконом и колой, самовывоз.
func readyCart(t *testing.T, srv *httptest.Server, hdr map[string]string) string {
	t.Helper()
	guest := hdr["X-User-Id"] == ""
	r := do(t, srv, "POST", "/carts", map[string]any{"restaurantId": testkit.Restaurant1, "guest": guest,
		"contact": map[string]any{"name": "Иван", "phone": "+79991234567"}}, hdr)
	if r.Status != 201 {
		t.Fatalf("create cart: %d %s", r.Status, r.Body)
	}
	id := r.json(t)["id"].(string)
	if loc := r.Header.Get("Location"); loc != "/carts/"+id {
		t.Fatalf("Location: %q", loc)
	}
	r = do(t, srv, "PUT", "/carts/"+id+"/items", map[string]any{"items": []any{
		map[string]any{"menuItemId": testkit.Burger, "quantity": 2, "modifierIds": []string{testkit.Bacon}},
		map[string]any{"menuItemId": testkit.Cola, "quantity": 1},
	}}, hdr)
	if r.Status != 200 {
		t.Fatalf("items: %d %s", r.Status, r.Body)
	}
	r = do(t, srv, "PUT", "/carts/"+id+"/fulfillment", map[string]any{"type": "pickup"}, hdr)
	if r.Status != 200 {
		t.Fatalf("fulfillment: %d %s", r.Status, r.Body)
	}
	return id
}

func TestCartContractShapeAndMoneyAreIntegers(t *testing.T) {
	srv, _, _, _ := newServer(t)
	id := readyCart(t, srv, nil)
	r := do(t, srv, "GET", "/carts/"+id, nil, nil)
	if r.Status != 200 || r.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("%d %s", r.Status, r.Header.Get("Content-Type"))
	}
	c := r.json(t)
	total := c["total"].(map[string]any)
	// float64 в JSON-декодере: целое проверяем по отсутствию дробной части в сыром теле
	if total["amount"].(float64) != 100500 || total["currency"] != "RUB" || !strings.Contains(string(r.Body), `"amount":100500`) {
		t.Fatalf("total: %v / %s", total, r.Body)
	}
	if c["status"] != "open" || c["subtotal"].(map[string]any)["amount"].(float64) != 100500 || c["discountTotal"].(map[string]any)["amount"].(float64) != 0 {
		t.Fatalf("cart: %s", r.Body)
	}
	items := c["items"].([]any)
	first := items[0].(map[string]any)
	if first["name"] != "Бургер" || first["available"] != true || first["unitPrice"].(map[string]any)["amount"].(float64) != 43800 {
		t.Fatalf("item: %v", first)
	}
	if c["appliedPromotions"] == nil || c["fulfillment"].(map[string]any)["type"] != "pickup" {
		t.Fatalf("cart: %s", r.Body)
	}
}

func TestRequestValidationErrors(t *testing.T) {
	srv, _, _, _ := newServer(t)
	wantProblem(t, do(t, srv, "POST", "/carts", `{"restaurantId":`, nil), 400, "INVALID_JSON")
	wantProblem(t, do(t, srv, "POST", "/carts", map[string]any{"restaurantId": testkit.Restaurant1, "surprise": 1}, nil), 400, "INVALID_JSON")
	wantProblem(t, do(t, srv, "POST", "/carts", map[string]any{"restaurantId": testkit.Restaurant1}, nil), 401, "UNAUTHENTICATED")
	wantProblem(t, do(t, srv, "POST", "/carts", map[string]any{"restaurantId": "a1000000-0000-4000-8000-0000000000ff", "guest": true}, nil), 404, "RESTAURANT_NOT_FOUND")
	wantProblem(t, do(t, srv, "GET", "/carts/not-a-uuid", nil, nil), 404, "CART_NOT_FOUND")
	id := readyCart(t, srv, nil)
	wantProblem(t, do(t, srv, "PUT", "/carts/"+id+"/items", `{}`, nil), 400, "INVALID_PARAMETER")
	wantProblem(t, do(t, srv, "PUT", "/carts/"+id+"/items", map[string]any{"items": []any{map[string]any{"menuItemId": testkit.Cola, "quantity": 0}}}, nil), 400, "INVALID_PARAMETER")
	wantProblem(t, do(t, srv, "PUT", "/carts/"+id+"/fulfillment", map[string]any{"type": "delivery"}, nil), 422, "ADDRESS_REQUIRED")
	wantProblem(t, do(t, srv, "PUT", "/carts/"+id+"/fulfillment", map[string]any{"type": "delivery", "address": map[string]any{"raw": "x", "location": map[string]any{"lat": 1}}}, nil), 400, "INVALID_PARAMETER")
	wantProblem(t, do(t, srv, "GET", "/orders/"+id, nil, nil), 404, "ORDER_NOT_FOUND")
	wantProblem(t, do(t, srv, "PUT", "/orders/"+id+"/pickup-slot", `{"from":"2026-10-18T10:00:00Z"}`, nil), 400, "INVALID_PARAMETER")
}

func TestUnavailableItemsProblemExtension(t *testing.T) {
	srv, _, _, _ := newServer(t)
	r := do(t, srv, "POST", "/carts", map[string]any{"restaurantId": testkit.Restaurant1, "guest": true}, nil)
	id := r.json(t)["id"].(string)
	r = do(t, srv, "PUT", "/carts/"+id+"/items", map[string]any{"items": []any{
		map[string]any{"menuItemId": testkit.Cola, "quantity": 1},
		map[string]any{"menuItemId": testkit.Cake, "quantity": 1},
	}}, nil)
	wantProblem(t, r, 409, "ITEMS_UNAVAILABLE")
	un := r.json(t)["unavailableItems"].([]any)
	if len(un) != 1 || un[0].(map[string]any)["menuItemId"] != testkit.Cake || un[0].(map[string]any)["reasonCode"] != "ITEM_UNAVAILABLE" {
		t.Fatalf("unavailableItems: %s", r.Body)
	}
	cart := do(t, srv, "GET", "/carts/"+id, nil, nil).json(t)
	if len(cart["items"].([]any)) != 1 {
		t.Fatalf("cart keeps the available line: %v", cart["items"])
	}
}

func orderBody(cartID string) map[string]any {
	return map[string]any{"cartId": cartID, "paymentMethod": "online_card"}
}

func TestCreateOrderRequiresIdempotencyKey(t *testing.T) {
	srv, _, _, count := newServer(t)
	id := readyCart(t, srv, nil)
	wantProblem(t, do(t, srv, "POST", "/orders", orderBody(id), nil), 400, "IDEMPOTENCY_KEY_REQUIRED")
	wantProblem(t, do(t, srv, "POST", "/orders", orderBody(id), map[string]string{"Idempotency-Key": strings.Repeat("k", 129)}), 400, "IDEMPOTENCY_KEY_INVALID")
	if count("orders") != 0 {
		t.Fatal("no order without a valid key")
	}
}

// номер зависит от текущей даты (часы в HTTP-тестах реальные)
var numberRe = regexp.MustCompile(`^R1-\d{6}-0001$`)

// DoD S2: повтор того же запроса возвращает тот же 201 без нового заказа (MG2).
func TestIdempotentReplayReturnsSameOrder(t *testing.T) {
	srv, _, _, count := newServer(t)
	id := readyCart(t, srv, nil)
	h := map[string]string{"Idempotency-Key": "order-attempt-1"}
	first := do(t, srv, "POST", "/orders", orderBody(id), h)
	if first.Status != 201 || first.Header.Get("Idempotent-Replay") != "" {
		t.Fatalf("first: %d %s", first.Status, first.Body)
	}
	o := first.json(t)
	if o["status"] != "pending_payment" || o["paymentStatus"] != "pending" || o["paymentMethod"] != "online_card" ||
		!numberRe.MatchString(o["number"].(string)) {
		t.Fatalf("order: %s", first.Body)
	}
	if first.Header.Get("Location") != "/orders/"+o["id"].(string) {
		t.Fatalf("Location: %q", first.Header.Get("Location"))
	}

	again := do(t, srv, "POST", "/orders", orderBody(id), h)
	if again.Status != 201 || again.Header.Get("Idempotent-Replay") != "true" || !bytes.Equal(again.Body, first.Body) ||
		again.Header.Get("Location") != first.Header.Get("Location") {
		t.Fatalf("replay differs: %d %q\n%s\nvs\n%s", again.Status, again.Header.Get("Idempotent-Replay"), again.Body, first.Body)
	}
	// тот же ключ, другое тело — ошибка; новая корзина по тому же ключу не создаёт заказ
	other := readyCart(t, srv, nil)
	wantProblem(t, do(t, srv, "POST", "/orders", orderBody(other), h), 422, "IDEMPOTENCY_KEY_REUSED")
	if count("orders") != 1 || count("outbox") != 2 {
		t.Fatalf("orders=%d outbox=%d", count("orders"), count("outbox"))
	}
	// новый ключ на уже оформленную корзину — 409, а не второй заказ
	wantProblem(t, do(t, srv, "POST", "/orders", orderBody(id), map[string]string{"Idempotency-Key": "order-attempt-2"}), 409, "CART_CLOSED")
	if count("orders") != 1 {
		t.Fatal("second order created")
	}
}

// DoD S2: параллельные одинаковые Idempotency-Key дают один заказ.
func TestParallelSameKeyMakesOneOrder(t *testing.T) {
	srv, _, _, count := newServer(t)
	id := readyCart(t, srv, nil)
	const n = 24
	var wg sync.WaitGroup
	results := make(chan resp, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results <- do(t, srv, "POST", "/orders", orderBody(id), map[string]string{"Idempotency-Key": "same-key"})
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	ids := map[string]bool{}
	created, inProgress := 0, 0
	for r := range results {
		switch r.Status {
		case 201:
			created++
			ids[r.json(t)["id"].(string)] = true
		case 409:
			wantProblem(t, r, 409, "IDEMPOTENCY_IN_PROGRESS")
			inProgress++
		default:
			t.Fatalf("unexpected %d: %s", r.Status, r.Body)
		}
	}
	if len(ids) != 1 || created < 1 {
		t.Fatalf("distinct order ids: %v (201s=%d, 409s=%d)", ids, created, inProgress)
	}
	if count("orders") != 1 || count("outbox") != 2 || count("order_status_history") != 1 {
		t.Fatalf("orders=%d outbox=%d history=%d", count("orders"), count("outbox"), count("order_status_history"))
	}
}

func TestOrderBusinessErrorsOverHTTP(t *testing.T) {
	srv, menu, rest, count := newServer(t)
	id := readyCart(t, srv, nil)
	key := func(s string) map[string]string { return map[string]string{"Idempotency-Key": s} }

	wantProblem(t, do(t, srv, "POST", "/orders", map[string]any{"cartId": id, "paymentMethod": "courier_card"}, key("k1")), 422, "PAYMENT_METHOD_NOT_ALLOWED")
	wantProblem(t, do(t, srv, "POST", "/orders", map[string]any{"cartId": id, "paymentMethod": "online_card", "channel": "staff_fax"}, key("k2")), 422, "CHANNEL_NOT_AVAILABLE")

	menu.SetAvailable(testkit.Cola, false)
	r := do(t, srv, "POST", "/orders", orderBody(id), key("k3"))
	wantProblem(t, r, 409, "ITEMS_UNAVAILABLE")
	if len(r.json(t)["unavailableItems"].([]any)) != 1 {
		t.Fatalf("%s", r.Body)
	}
	menu.SetAvailable(testkit.Cola, true)

	menu.SetPrice(testkit.Cola, 20000)
	wantProblem(t, do(t, srv, "POST", "/orders", orderBody(id), key("k4")), 409, "PRICE_CHANGED")
	// после подтверждения новой суммы — тем же телом, но НОВЫМ ключом
	if r := do(t, srv, "POST", "/orders", orderBody(id), key("k5")); r.Status != 201 || r.json(t)["total"].(map[string]any)["amount"].(float64) != 107600 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}

	id2 := readyCart(t, srv, nil)
	rest.Update(testkit.Restaurant1, func(i *clients_info) { i.OpenNow = false })
	wantProblem(t, do(t, srv, "POST", "/orders", orderBody(id2), key("k6")), 409, "RESTAURANT_CLOSED")
	rest.Update(testkit.Restaurant1, func(i *clients_info) { i.OpenNow = true })
	if count("orders") != 1 {
		t.Fatalf("only the confirmed order must exist, got %d", count("orders"))
	}
	// 5xx (menu недоступен) не запоминается под ключом: после восстановления тот же ключ работает
	menu.Fail(errUnavailable)
	wantProblem(t, do(t, srv, "POST", "/orders", orderBody(id2), key("k7")), 503, "MENU_UNAVAILABLE")
	menu.Fail(nil)
	if r := do(t, srv, "POST", "/orders", orderBody(id2), key("k7")); r.Status != 201 {
		t.Fatalf("retry with the same key after 5xx must succeed: %d %s", r.Status, r.Body)
	}
}

func TestOwnershipOverHTTP(t *testing.T) {
	srv, _, _, _ := newServer(t)
	alice := map[string]string{"X-User-Id": "alice", "X-User-Roles": "CUSTOMER"}
	bob := map[string]string{"X-User-Id": "bob", "X-User-Roles": "CUSTOMER"}
	id := readyCart(t, srv, alice)
	wantProblem(t, do(t, srv, "GET", "/carts/"+id, nil, bob), 404, "CART_NOT_FOUND")
	wantProblem(t, do(t, srv, "GET", "/carts/"+id, nil, nil), 404, "CART_NOT_FOUND")
	h := map[string]string{"Idempotency-Key": "alice-1", "X-User-Id": "alice", "X-User-Roles": "CUSTOMER"}
	r := do(t, srv, "POST", "/orders", map[string]any{"cartId": id, "paymentMethod": "pos"}, h)
	if r.Status != 201 || r.json(t)["status"] != "accepted" {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	oid := r.json(t)["id"].(string)
	for _, hdr := range []map[string]string{bob, nil} {
		wantProblem(t, do(t, srv, "GET", "/orders/"+oid, nil, hdr), 404, "ORDER_NOT_FOUND")
		wantProblem(t, do(t, srv, "GET", "/orders/"+oid+"/eta", nil, hdr), 404, "ORDER_NOT_FOUND")
	}
	if r := do(t, srv, "GET", "/orders/"+oid, nil, alice); r.Status != 200 {
		t.Fatalf("%d", r.Status)
	}
	// ключ идемпотентности принадлежит пользователю: тот же ключ у Боба — другая область
	bobCart := readyCart(t, srv, bob)
	hb := map[string]string{"Idempotency-Key": "alice-1", "X-User-Id": "bob", "X-User-Roles": "CUSTOMER"}
	if r := do(t, srv, "POST", "/orders", map[string]any{"cartId": bobCart, "paymentMethod": "pos"}, hb); r.Status != 201 {
		t.Fatalf("bob with the same key string must create his own order: %d %s", r.Status, r.Body)
	}
}

func TestPickupSlotAndEtaOverHTTP(t *testing.T) {
	srv, _, _, _ := newServer(t)
	id := readyCart(t, srv, nil)
	o := do(t, srv, "POST", "/orders", orderBody(id), map[string]string{"Idempotency-Key": "s1"}).json(t)
	oid := o["id"].(string)

	eta := do(t, srv, "GET", "/orders/"+oid+"/eta", nil, nil)
	if eta.Status != 200 || eta.json(t)["approximate"] != true || eta.json(t)["readyAt"] == nil {
		t.Fatalf("eta: %d %s", eta.Status, eta.Body)
	}
	now := time.Now().UTC()
	from, to := now.Add(2*time.Hour).Format(time.RFC3339), now.Add(150*time.Minute).Format(time.RFC3339)
	r := do(t, srv, "PUT", "/orders/"+oid+"/pickup-slot", map[string]any{"from": from, "to": to}, nil)
	if r.Status != 200 || r.json(t)["pickupSlot"] == nil {
		t.Fatalf("slot: %d %s", r.Status, r.Body)
	}
	wantProblem(t, do(t, srv, "PUT", "/orders/"+oid+"/pickup-slot", map[string]any{"from": to, "to": from}, nil), 422, "INVALID_SLOT")
	got := do(t, srv, "GET", "/orders/"+oid, nil, nil).json(t)
	if got["pickupSlot"] == nil {
		t.Fatal("slot must be persisted")
	}
}

func TestHealthAndMetricsExposed(t *testing.T) {
	srv, _, _, _ := newServer(t)
	if r := do(t, srv, "GET", "/health/", nil, nil); r.Status != 200 || !strings.Contains(string(r.Body), "OK") {
		t.Fatalf("health: %d %s", r.Status, r.Body)
	}
	readyCart(t, srv, nil)
	m := do(t, srv, "GET", "/metrics", nil, nil)
	if m.Status != 200 || !strings.Contains(string(m.Body), `route="PUT /carts/{cartId}/items"`) {
		t.Fatalf("RED metrics must carry the route template: %d", m.Status)
	}
}
