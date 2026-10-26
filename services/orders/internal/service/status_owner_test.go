package service_test

import (
	"encoding/json"
	"testing"

	"github.com/pavelgr1408/food-platform/services/orders/internal/domain"
)

// S9: orders.status несёт владельца заказа (notification-service по нему находит устройства получателя).
// У гостевого заказа поля нет вовсе — push гостю не отправляется.
func TestOrdersStatusCarriesOwner(t *testing.T) {
	e := newEnv(t)
	owned := e.order(t, "alice", domain.PayPOS)
	guest := e.order(t, "", domain.PayPOS)

	ev := e.statusEvents(t, owned.ID)
	if len(ev) == 0 || ev[0].UserID != "alice" {
		t.Fatalf("owner must be in orders.status: %+v", ev)
	}
	ev = e.statusEvents(t, guest.ID)
	if len(ev) == 0 || ev[0].UserID != "" {
		t.Fatalf("guest order has no owner: %+v", ev)
	}
	for _, r := range e.outbox(t) {
		if r.Topic != "orders.status" || r.Key != guest.ID {
			continue
		}
		var raw map[string]any
		_ = json.Unmarshal(r.Payload, &raw)
		if _, has := raw["userId"]; has {
			t.Fatalf("userId must be omitted for guests: %s", r.Payload)
		}
	}
}
