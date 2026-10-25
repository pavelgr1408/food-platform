package courier

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/pavelgr1408/food-platform/pkg/httpx"
)

func client(h http.Handler) (*Client, func()) {
	srv := httptest.NewServer(h)
	return &Client{BaseURL: srv.URL, APIKey: "k", HTTP: httpx.NewClient(httpx.ClientConfig{Timeout: 300 * time.Millisecond, MaxRetries: -1})}, srv.Close
}

func TestCreateSendsKeyAuthAndPaymentAndParsesState(t *testing.T) {
	var gotKey, gotAuth string
	var gotBody map[string]any
	c, stop := client(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey, gotAuth = r.Header.Get("Idempotency-Key"), r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"id":"cr_1","status":"assigned","courier":{"id":"c01","name":"Анна","phone":"+79990000001"},
			"location":{"lat":59.9,"lon":30.3,"updatedAt":"2026-10-25T12:00:00Z"},"etaAt":"2026-10-25T12:20:00Z","paid":false}`))
	}))
	defer stop()
	st, err := c.Create(context.Background(), CreateRequest{Key: "d1", OrderID: "o1", OrderNumber: "R1-1",
		Pickup: Place{Point{59.93, 30.32}, "ресторан"}, Dropoff: Place{Point{59.94, 30.33}, "адрес"},
		Payment: &Payment{URL: "http://pay/1", Amount: 47800, Currency: "RUB"}})
	if err != nil || st.Ref != "cr_1" || st.Status != "assigned" || st.CourierName != "Анна" || st.Location == nil || st.EtaAt == nil {
		t.Fatalf("%+v %v", st, err)
	}
	pay, _ := gotBody["payment"].(map[string]any)
	if gotKey != "d1" || gotAuth != "Bearer k" || pay["url"] != "http://pay/1" {
		t.Fatalf("key=%q auth=%q body=%v", gotKey, gotAuth, gotBody)
	}
	amount, _ := pay["amount"].(map[string]any)
	if amount["amount"] != float64(47800) { // деньги — целые копейки
		t.Fatalf("amount: %v", amount)
	}
}

func TestErrorClassification(t *testing.T) {
	for _, tc := range []struct {
		code             int
		rejected, absent bool
	}{{400, true, false}, {422, true, false}, {404, false, true}, {500, false, false}, {503, false, false}, {429, false, false}} {
		c, stop := client(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(tc.code)
			_, _ = w.Write([]byte(`{"code":"X","message":"m"}`))
		}))
		_, err := c.Status(context.Background(), "cr")
		stop()
		if err == nil {
			t.Fatalf("%d: error expected", tc.code)
		}
		if IsRejected(err) != tc.rejected || (err == ErrNotFound) != tc.absent {
			t.Errorf("%d: rejected=%v absent=%v err=%v", tc.code, IsRejected(err), err == ErrNotFound, err)
		}
	}
}

func TestTimeoutIsNotRejection(t *testing.T) {
	c, stop := client(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer stop()
	_, err := c.Create(context.Background(), CreateRequest{Key: "d1"})
	if err == nil || IsRejected(err) {
		t.Fatalf("timeout must be an unknown outcome, not a rejection: %v", err)
	}
}

func TestSetPaymentAndCancel(t *testing.T) {
	var method, path string
	c, stop := client(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		if r.URL.Path == "/v1/deliveries/cr_1/payment" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_, _ = w.Write([]byte(`{"id":"cr_1","status":"en_route"}`))
	}))
	defer stop()
	if err := c.SetPayment(context.Background(), "cr_1", Payment{URL: "http://pay/1", Amount: 100, Currency: "RUB"}); err != nil || method != http.MethodPut {
		t.Fatalf("%s %s %v", method, path, err)
	}
	st, err := c.Cancel(context.Background(), "cr_1")
	if err != nil || st.Status != "en_route" || method != http.MethodPost || path != "/v1/deliveries/cr_1/cancel" {
		t.Fatalf("cancel after pickup returns the current state: %+v %s %s %v", st, method, path, err)
	}
}
