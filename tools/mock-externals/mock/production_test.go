package mock

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pavelgr1408/food-platform/pkg/kafka"
)

type capture struct {
	mu  sync.Mutex
	got []kafka.Event
}

func (c *capture) Publish(_ context.Context, evs ...kafka.Event) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.got = append(c.got, evs...)
	return nil
}

func (c *capture) topic(t string) []kafka.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []kafka.Event
	for _, e := range c.got {
		if e.Topic == t {
			out = append(out, e)
		}
	}
	return out
}

type dishSnap struct {
	OrderID string `json:"orderId"`
	Dishes  []struct {
		DishID string `json:"dishId"`
		Status string `json:"status"`
	} `json:"dishes"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func (d dishSnap) statuses() string {
	var b []string
	for _, x := range d.Dishes {
		b = append(b, x.Status)
	}
	return strings.Join(b, ",")
}

func (c *capture) snaps(t *testing.T, orderID string) []dishSnap {
	var out []dishSnap
	for _, e := range c.topic(topicDishStatus) {
		var s dishSnap
		if err := json.Unmarshal(e.Payload, &s); err != nil {
			t.Fatal(err)
		}
		if s.OrderID == orderID {
			out = append(out, s)
		}
	}
	return out
}

func newProd(t *testing.T, queue, cook, stagger string) (*productionMock, *capture, *httptest.Server) {
	t.Helper()
	t.Setenv("MOCK_PRODUCTION_QUEUE_DELAY", queue)
	t.Setenv("MOCK_PRODUCTION_COOK_TIME", cook)
	t.Setenv("MOCK_PRODUCTION_STAGGER", stagger)
	ctx, cancel := context.WithCancel(context.Background())
	env := NewEnv(slog.New(slog.NewTextHandler(io.Discard, nil)), nil, time.Millisecond)
	m := &productionMock{}
	mux := http.NewServeMux()
	m.Register(mux, env)
	m.ctx = ctx
	cap := &capture{}
	m.pub = cap
	srv := httptest.NewServer(mux)
	t.Cleanup(func() { cancel(); env.Wait(); srv.Close() })
	return m, cap, srv
}

func state(orderID, rid, method, pay string, dishes ...string) []byte {
	items := make([]map[string]any, 0, len(dishes))
	for _, d := range dishes {
		items = append(items, map[string]any{"dishId": d, "quantity": 1})
	}
	b, _ := json.Marshal(map[string]any{"orderId": orderID, "revision": 1, "restaurantId": rid,
		"paymentMethod": method, "paymentStatus": pay, "items": items})
	return b
}

func waitUntil(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for i := 0; i < 300; i++ {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timeout: %s", what)
}

func TestProductionRuleADR10(t *testing.T) {
	m, cap, _ := newProd(t, "10ms", "10ms", "0s")
	m.onState(state("o-unpaid", "r1", "online", "pending", "d1"))
	m.onState(state("o-courier", "r1", "courier", "pending", "d1"))
	m.onState(state("o-pos", "r1", "restaurant", "pending", "d1"))
	m.onState(state("o-paid", "r1", "online", "paid", "d1"))

	if got := cap.snaps(t, "o-unpaid"); len(got) != 0 {
		t.Fatalf("unpaid online order must not go to production, got %d snapshots", len(got))
	}
	for _, id := range []string{"o-courier", "o-pos", "o-paid"} {
		id := id
		waitUntil(t, id+" ready", func() bool {
			s := cap.snaps(t, id)
			return len(s) > 0 && s[len(s)-1].statuses() == "ready"
		})
	}
	// оплата пришла вторым состоянием (revision 2) — теперь заказ берётся в работу
	m.onState(state("o-unpaid", "r1", "online", "paid", "d1"))
	waitUntil(t, "late paid order", func() bool { return len(cap.snaps(t, "o-unpaid")) > 0 })
}

func TestProductionDedupAndTimeline(t *testing.T) {
	m, cap, _ := newProd(t, "20ms", "20ms", "0s")
	st := state("o1", "r1", "online", "paid", "d1", "d2", "d1") // дубликат блюда схлопывается
	m.onState(st)
	m.onState(st) // повтор/второй revision — в работу не берётся
	waitUntil(t, "ready", func() bool {
		s := cap.snaps(t, "o1")
		return len(s) > 0 && s[len(s)-1].statuses() == "ready,ready"
	})
	time.Sleep(60 * time.Millisecond)
	s := cap.snaps(t, "o1")
	var seq []string
	for _, x := range s {
		seq = append(seq, x.statuses())
	}
	want := "queued,queued|cooking,cooking|ready,ready"
	if strings.Join(seq, "|") != want {
		t.Fatalf("timeline = %v, want %s", seq, want)
	}
	for i := 1; i < len(s); i++ {
		if s[i].UpdatedAt.Before(s[i-1].UpdatedAt) {
			t.Fatalf("updatedAt went backwards: %v", s)
		}
	}
}

func TestProductionStaggerGivesPartialSnapshots(t *testing.T) {
	m, cap, _ := newProd(t, "10ms", "10ms", "40ms")
	m.onState(state("o1", "r1", "online", "paid", "a", "b"))
	waitUntil(t, "all ready", func() bool {
		s := cap.snaps(t, "o1")
		return len(s) > 0 && s[len(s)-1].statuses() == "ready,ready"
	})
	var seq []string
	for _, x := range cap.snaps(t, "o1") {
		seq = append(seq, x.statuses())
	}
	if got := strings.Join(seq, "|"); got != "queued,queued|cooking,cooking|ready,cooking|ready,ready" {
		t.Fatalf("timeline = %s", got)
	}
}

func leadMinutes(t *testing.T, cap *capture, rid string) []int {
	var out []int
	for _, e := range cap.topic(topicLeadTime) {
		var p struct {
			RestaurantID    string `json:"restaurantId"`
			LeadTimeMinutes int    `json:"leadTimeMinutes"`
		}
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatal(err)
		}
		if p.RestaurantID == rid {
			out = append(out, p.LeadTimeMinutes)
		}
	}
	return out
}

func TestProductionLeadTimeLoadAndOverride(t *testing.T) {
	m, cap, srv := newProd(t, "50ms", "50ms", "0s")
	m.onState(state("o-unpaid", "r1", "online", "pending", "d1")) // ресторан виден по любому состоянию
	if got := leadMinutes(t, cap, "r1"); len(got) != 1 || got[0] != 15 {
		t.Fatalf("first lead time = %v, want [15]", got)
	}
	m.onState(state("o1", "r1", "online", "paid", "d1"))
	got := leadMinutes(t, cap, "r1")
	if got[len(got)-1] != 17 { // 15 + 1 активный заказ × 2
		t.Fatalf("lead time under load = %v", got)
	}
	waitUntil(t, "load released", func() bool {
		g := leadMinutes(t, cap, "r1")
		return g[len(g)-1] == 15
	})

	resp, err := http.Post(srv.URL+"/_admin/production/leadtime?restaurantId=r1&minutes=40", "", nil)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("leadtime override: %v %v", err, resp)
	}
	g := leadMinutes(t, cap, "r1")
	if g[len(g)-1] != 40 {
		t.Fatalf("override not published: %v", g)
	}
	for _, bad := range []string{"restaurantId=r1&minutes=-1", "restaurantId=r1&minutes=x", "minutes=5"} {
		resp, _ := http.Post(srv.URL+"/_admin/production/leadtime?"+bad, "", nil)
		if resp.StatusCode != 400 {
			t.Fatalf("%s → %d, want 400", bad, resp.StatusCode)
		}
	}
	// снять фиксацию
	_, _ = http.Post(srv.URL+"/_admin/production/leadtime?restaurantId=r1", "", nil)
	g = leadMinutes(t, cap, "r1")
	if g[len(g)-1] != 15 {
		t.Fatalf("override not cleared: %v", g)
	}
}

func TestProductionResetStopsOldTimers(t *testing.T) {
	m, cap, srv := newProd(t, "80ms", "80ms", "0s")
	m.onState(state("o1", "r1", "online", "paid", "d1"))
	resp, _ := http.Post(srv.URL+"/_admin/production/reset", "", nil)
	if resp.StatusCode != 204 {
		t.Fatalf("reset → %d", resp.StatusCode)
	}
	time.Sleep(300 * time.Millisecond)
	if s := cap.snaps(t, "o1"); len(s) != 1 {
		t.Fatalf("after reset old timers must be silent, got %d snapshots", len(s))
	}
	resp, _ = http.Get(srv.URL + "/_inspect/production")
	var v map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&v)
	if len(v["orders"].([]any)) != 0 {
		t.Fatalf("orders not cleared: %v", v)
	}
}

func TestProductionAdminValidation(t *testing.T) {
	_, _, srv := newProd(t, "1s", "1s", "0s")
	for _, c := range []struct{ path string }{
		{"/_admin/production/mode?queue=maybe"},
		{"/_admin/production/timing?queue=abc"},
		{"/_admin/production/timing?cook=-1s"},
		{"/_admin/production/timing?stagger=48h"},
	} {
		resp, _ := http.Post(srv.URL+c.path, "", nil)
		if resp.StatusCode != 400 {
			t.Fatalf("%s → %d, want 400", c.path, resp.StatusCode)
		}
	}
	resp, _ := http.Post(srv.URL+"/_admin/production/timing?queue=5ms&cook=7ms", "", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("timing → %d", resp.StatusCode)
	}
}

func TestProductionIgnoresGarbage(t *testing.T) {
	m, cap, _ := newProd(t, "5ms", "5ms", "0s")
	m.onState([]byte("not json"))
	m.onState([]byte(`{"orderId":"","restaurantId":"r"}`))
	m.onState(state("o-empty", "r1", "online", "paid")) // без позиций
	time.Sleep(30 * time.Millisecond)
	if n := len(cap.topic(topicDishStatus)); n != 0 {
		t.Fatalf("garbage produced %d dish events", n)
	}
}
