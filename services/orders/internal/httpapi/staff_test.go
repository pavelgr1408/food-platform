package httpapi_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/pavelgr1408/food-platform/services/orders/internal/testkit"
)

// Субъекты матрицы прав (dev-аутентификация: заголовки X-User-*).
type subject struct {
	name string
	hdr  map[string]string
}

func subjects() []subject {
	h := func(id, roles, rests string) map[string]string {
		m := map[string]string{"X-User-Id": id, "X-User-Roles": roles}
		if rests != "" {
			m["X-Restaurant-Ids"] = rests
		}
		return m
	}
	return []subject{
		{"guest", nil},
		{"customer", h("u-1", "CUSTOMER", "")},
		{"staff-own", h("s-1", "STAFF", testkit.Restaurant1)},
		{"staff-other", h("s-2", "STAFF", testkit.Restaurant2)},
		{"owner-own", h("o-1", "FRANCHISE_OWNER", testkit.Restaurant1)},
		{"owner-other", h("o-2", "FRANCHISE_OWNER", testkit.Restaurant2)},
		{"hq", h("h-1", "HQ_STAFF", "")},
	}
}

func authz(name string) (status int, code string) {
	switch name {
	case "guest":
		return 401, "UNAUTHORIZED"
	case "customer":
		return 403, "FORBIDDEN"
	case "staff-other", "owner-other":
		return 404, "ORDER_NOT_FOUND" // чужой ресторан: существование не раскрываем
	}
	return 200, ""
}

// faxOrder оформляет заказ по факсу от имени сотрудника ресторана 1 и возвращает (id, number).
func faxOrder(t *testing.T, srv *httptest.Server, key string) (string, string) {
	t.Helper()
	staff := map[string]string{"X-User-Id": "s-1", "X-User-Roles": "STAFF", "X-Restaurant-Ids": testkit.Restaurant1}
	cart := readyCart(t, srv, staff)
	r := do(t, srv, "POST", "/orders", map[string]any{"cartId": cart, "paymentMethod": "pos", "channel": "staff_fax",
		"contact": map[string]any{"phone": "+79995550101", "name": "Клиент"}}, map[string]string{
		"X-User-Id": "s-1", "X-User-Roles": "STAFF", "X-Restaurant-Ids": testkit.Restaurant1, "Idempotency-Key": key})
	if r.Status != 201 {
		t.Fatalf("fax order: %d %s", r.Status, r.Body)
	}
	b := r.json(t)
	if b["channel"] != "staff_fax" || b["status"] != "accepted" {
		t.Fatalf("%s", r.Body)
	}
	return b["id"].(string), b["number"].(string)
}

func TestStaffRightsMatrix(t *testing.T) {
	srv, _, _, _ := newServer(t)
	id, number := faxOrder(t, srv, "k-matrix-0")

	type op struct {
		name string
		call func(h map[string]string, id string) resp
	}
	ops := []op{
		{"searchOrder", func(h map[string]string, _ string) resp {
			return do(t, srv, "GET", "/orders/search?number="+number, nil, h)
		}},
		{"markOrderIssued", func(h map[string]string, id string) resp {
			return do(t, srv, "POST", "/orders/"+id+"/issuance", nil, h)
		}},
		{"cancelOrder", func(h map[string]string, id string) resp {
			return do(t, srv, "POST", "/orders/"+id+"/cancellation", map[string]any{"reason": "other"}, h)
		}},
	}
	for _, o := range ops {
		for _, s := range subjects() {
			t.Run(fmt.Sprintf("%s/%s", o.name, s.name), func(t *testing.T) {
				// свежий заказ на каждую проверку: issuance/cancel меняют состояние
				fid, _ := faxOrder(t, srv, "k-"+o.name+"-"+s.name)
				r := o.call(s.hdr, fid)
				if o.name == "searchOrder" { // поиск по номеру одного и того же заказа
					r = o.call(s.hdr, id)
				}
				status, code := authz(s.name)
				if status != 200 {
					wantProblem(t, r, status, code)
					return
				}
				switch o.name {
				case "searchOrder":
					b := r.json(t)
					if r.Status != 200 || b["id"] != id || b["contact"].(map[string]any)["phone"] != "+79995550101" || b["createdBy"] != "s-1" {
						t.Fatalf("%d %s", r.Status, r.Body)
					}
				case "markOrderIssued": // допустимый субъект, но заказ ещё accepted/не оплачен
					wantProblem(t, r, 409, "ORDER_STATE_CONFLICT")
				case "cancelOrder":
					if r.Status != 200 || r.json(t)["status"] != "cancelled" {
						t.Fatalf("%d %s", r.Status, r.Body)
					}
				}
			})
		}
	}
}

func TestFaxOrderRightsOverHTTP(t *testing.T) {
	srv, _, _, _ := newServer(t)
	for _, s := range subjects() {
		t.Run(s.name, func(t *testing.T) {
			hdr := map[string]string{}
			for k, v := range s.hdr {
				hdr[k] = v
			}
			// корзину создаёт сам субъект (гость — как гость)
			cart := readyCart(t, srv, hdr)
			hdr["Idempotency-Key"] = "fax-" + s.name
			r := do(t, srv, "POST", "/orders", map[string]any{"cartId": cart, "paymentMethod": "pos", "channel": "staff_fax",
				"contact": map[string]any{"phone": "+79995550101"}}, hdr)
			switch s.name {
			case "guest":
				wantProblem(t, r, 401, "UNAUTHORIZED")
			case "customer", "staff-other", "owner-other":
				wantProblem(t, r, 403, "FORBIDDEN")
			default:
				if r.Status != http.StatusCreated || r.json(t)["channel"] != "staff_fax" {
					t.Fatalf("%d %s", r.Status, r.Body)
				}
			}
		})
	}
}

func TestSearchOrderValidationOverHTTP(t *testing.T) {
	srv, _, _, _ := newServer(t)
	staff := map[string]string{"X-User-Id": "s-1", "X-User-Roles": "STAFF", "X-Restaurant-Ids": testkit.Restaurant1}
	wantProblem(t, do(t, srv, "GET", "/orders/search", nil, staff), 400, "INVALID_PARAMETER")
	wantProblem(t, do(t, srv, "GET", "/orders/search?number=abc", nil, staff), 400, "INVALID_ORDER_NUMBER")
	wantProblem(t, do(t, srv, "GET", "/orders/search?number=R1-261018-0042", nil, staff), 404, "ORDER_NOT_FOUND")
	wantProblem(t, do(t, srv, "POST", "/orders/"+testkit.Restaurant1+"/cancellation", map[string]any{"reason": "bogus"}, staff), 400, "INVALID_PARAMETER")
	wantProblem(t, do(t, srv, "POST", "/orders/"+testkit.Restaurant1+"/cancellation", `{"reason":"other","x":1}`, staff), 400, "INVALID_JSON")
}

// Долг №89: очередь ресторана — права, фильтры и форма ответа поверх HTTP.
func TestRestaurantQueueOverHTTP(t *testing.T) {
	srv, _, _, _ := newServer(t)
	id, number := faxOrder(t, srv, "k-queue-0")

	for _, s := range subjects() {
		t.Run(s.name, func(t *testing.T) {
			r := do(t, srv, "GET", "/orders/queue", nil, s.hdr)
			switch s.name {
			case "guest":
				wantProblem(t, r, 401, "UNAUTHORIZED")
				return
			case "customer":
				wantProblem(t, r, 403, "FORBIDDEN")
				return
			case "staff-other", "owner-other": // другой ресторан: своя (пустая) очередь, чужих заказов не видно
				if b := r.json(t); r.Status != 200 || len(b["items"].([]any)) != 0 || b["total"].(float64) != 0 {
					t.Fatalf("%d %s", r.Status, r.Body)
				}
				return
			}
			b := r.json(t)
			items := b["items"].([]any)
			if r.Status != 200 || len(items) != 1 || b["total"].(float64) != 1 || b["limit"].(float64) != 20 || b["offset"].(float64) != 0 {
				t.Fatalf("%d %s", r.Status, r.Body)
			}
			it := items[0].(map[string]any)
			if it["id"] != id || it["number"] != number || it["contact"].(map[string]any)["phone"] != "+79995550101" {
				t.Fatalf("%s", r.Body)
			}
		})
	}
	staff := map[string]string{"X-User-Id": "s-1", "X-User-Roles": "STAFF", "X-Restaurant-Ids": testkit.Restaurant1}
	wantProblem(t, do(t, srv, "GET", "/orders/queue?restaurantId="+testkit.Restaurant2, nil, staff), 403, "FORBIDDEN")
	wantProblem(t, do(t, srv, "GET", "/orders/queue?limit=abc", nil, staff), 400, "INVALID_PARAMETER")
	wantProblem(t, do(t, srv, "GET", "/orders/queue?status=shipped", nil, staff), 400, "INVALID_PARAMETER")
	wantProblem(t, do(t, srv, "GET", "/orders/queue?sort=random", nil, staff), 400, "INVALID_PARAMETER")
	r := do(t, srv, "GET", "/orders/queue?status=cancelled&status=closed", nil, staff)
	if r.Status != 200 || len(r.json(t)["items"].([]any)) != 0 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	// не путается с /orders/{orderId}: queue — литеральный маршрут
	if r := do(t, srv, "GET", "/orders/queue", nil, nil); r.Status != 401 {
		t.Fatalf("guest: %d %s", r.Status, r.Body)
	}
}
