package gateway

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

func TestCreateSendsKeyAmountAndAuth(t *testing.T) {
	var gotKey, gotAuth string
	var gotBody createDTO
	c, stop := client(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey, gotAuth = r.Header.Get("Idempotency-Key"), r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"id":"gw_1","status":"pending","confirmationUrl":"http://pay/gw_1"}`))
	}))
	defer stop()
	got, err := c.Create(context.Background(), CreateRequest{Key: "p1", Reference: "p1", Amount: 54990, Currency: "RUB"})
	if err != nil || got.Ref != "gw_1" || got.URL != "http://pay/gw_1" {
		t.Fatalf("%+v %v", got, err)
	}
	if gotKey != "p1" || gotAuth != "Bearer k" || gotBody.Amount.Amount != 54990 || gotBody.Reference != "p1" {
		t.Fatalf("key=%q auth=%q body=%+v", gotKey, gotAuth, gotBody)
	}
}

func TestErrorClassification(t *testing.T) {
	for _, tc := range []struct {
		code     int
		rejected bool
		notFound bool
	}{{400, true, false}, {422, true, false}, {404, false, true}, {500, false, false}, {503, false, false}, {429, false, false}} {
		c, stop := client(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(tc.code)
			_, _ = w.Write([]byte(`{"code":"X","message":"m"}`))
		}))
		_, err := c.Status(context.Background(), "gw")
		stop()
		if err == nil {
			t.Fatalf("%d: error expected", tc.code)
		}
		if IsRejected(err) != tc.rejected || (err == ErrNotFound) != tc.notFound {
			t.Errorf("%d: rejected=%v notFound=%v err=%v", tc.code, IsRejected(err), err == ErrNotFound, err)
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
	_, err := c.Create(context.Background(), CreateRequest{Key: "p", Amount: 1, Currency: "RUB"})
	if err == nil || IsRejected(err) {
		t.Fatalf("timeout must be a transient (unknown) error, got %v", err)
	}
}

func TestStatusAndCancel(t *testing.T) {
	c, stop := client(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"gw_1","status":"paid","amount":{"amount":100,"currency":"RUB"}}`))
	}))
	defer stop()
	for _, f := range []func(context.Context, string) (State, error){c.Status, c.Cancel} {
		s, err := f(context.Background(), "gw_1")
		if err != nil || s.Status != StatePaid || s.Amount != 100 || s.Currency != "RUB" {
			t.Fatalf("%+v %v", s, err)
		}
	}
}

func TestCreateSendsTender(t *testing.T) {
	var body createDTO
	c, stop := client(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"id":"pos_1","status":"pending"}`))
	}))
	defer stop()
	if _, err := c.Create(context.Background(), CreateRequest{Key: "p", Reference: "p", Amount: 1, Currency: "RUB", Tender: "cash"}); err != nil {
		t.Fatal(err)
	}
	if body.Tender != "cash" {
		t.Fatalf("%+v", body)
	}
}

func TestStatusReturnsOnlyFiscalObject(t *testing.T) {
	for body, want := range map[string]string{
		`{"id":"p","status":"paid","fiscalData":{"receiptNumber":"1"}}`: `{"receiptNumber":"1"}`,
		`{"id":"p","status":"paid","fiscalData":null}`:                  "",
		`{"id":"p","status":"paid","fiscalData":"junk"}`:                "",
		`{"id":"p","status":"paid"}`:                                    "",
	} {
		c, stop := client(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }))
		s, err := c.Status(context.Background(), "p")
		stop()
		if err != nil || string(s.Fiscal) != want {
			t.Errorf("%s: fiscal=%q err=%v", body, s.Fiscal, err)
		}
	}
}

func TestRefundIsIdempotentByKeyAndClassifiesErrors(t *testing.T) {
	var gotKey, gotPath string
	var gotBody struct {
		Amount moneyDTO `json:"amount"`
	}
	code := 200
	c, stop := client(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey, gotPath = r.Header.Get("Idempotency-Key"), r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(code)
		_, _ = w.Write([]byte(`{"id":"rf_1","status":"refunded","code":"nope","message":"m"}`))
	}))
	defer stop()
	got, err := c.Refund(context.Background(), "gw 1", "pay-1", 54990, "RUB")
	if err != nil || got.Ref != "rf_1" || got.Status != "refunded" {
		t.Fatalf("%+v %v", got, err)
	}
	if gotKey != "pay-1" || gotPath != "/v1/payments/gw 1/refund" || gotBody.Amount.Amount != 54990 || gotBody.Amount.Currency != "RUB" {
		t.Fatalf("key=%q path=%q body=%+v", gotKey, gotPath, gotBody)
	}
	code = 422
	if _, err := c.Refund(context.Background(), "gw_1", "pay-1", 1, "RUB"); !IsRejected(err) {
		t.Fatalf("422 is a definite refusal: %v", err)
	}
	code = 503
	if _, err := c.Refund(context.Background(), "gw_1", "pay-1", 1, "RUB"); err == nil || IsRejected(err) {
		t.Fatalf("503 is transient: %v", err)
	}
}
