package mock

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type pm struct {
	t   *testing.T
	srv *httptest.Server
}

func newPush(t *testing.T) *pm {
	t.Helper()
	t.Setenv("MOCK_PUSH_HOLD", "300ms")
	m := &pushMock{}
	mux := http.NewServeMux()
	m.Register(mux, newEnv())
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &pm{t: t, srv: srv}
}

func (p *pm) call(method, path, body, key string, hdr map[string]string) (int, string) {
	p.t.Helper()
	req, _ := http.NewRequest(method, p.srv.URL+path, strings.NewReader(body))
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		p.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

const order = "5b0b4a0e-3f55-4a0c-9b7e-1d0d6f2b7a11"

func (p *pm) apns(token, key string) (int, string) {
	body := `{"aps":{"alert":{"title":"Заказ готов","body":"Заказ №R1 готов."}},"orderId":"` + order + `","orderNumber":"R1","type":"order_ready"}`
	return p.call("POST", "/3/device/"+token, body, key, map[string]string{"apns-priority": "10", "apns-id": "id-1"})
}
func (p *pm) fcm(token, key string) (int, string) {
	body := `{"message":{"token":"` + token + `","notification":{"title":"T","body":"B"},"data":{"orderId":"` + order + `","orderNumber":"R1","type":"order_ready"},"android":{"priority":"HIGH"}}}`
	return p.call("POST", "/v1/projects/food/messages:send", body, key, nil)
}
func (p *pm) hms(token, key string) (int, string) {
	body := `{"validate_only":false,"message":{"token":["` + token + `"],"data":"{\"orderId\":\"` + order + `\",\"orderNumber\":\"R1\",\"type\":\"order_ready\"}","android":{"urgency":"HIGH","notification":{"title":"T","body":"B"}}}}`
	return p.call("POST", "/v1/app1/messages:send", body, key, nil)
}

func (p *pm) pushes(q string) []pushRecord {
	p.t.Helper()
	_, b := p.call("GET", "/_inspect/pushes"+q, "", "", nil)
	var out []pushRecord
	if err := json.Unmarshal([]byte(b), &out); err != nil {
		p.t.Fatalf("%v: %s", err, b)
	}
	return out
}

func TestPushAcceptsAllProvidersAndRecords(t *testing.T) {
	p := newPush(t)
	for name, f := range map[string]func() (int, string){
		"apns": func() (int, string) { return p.apns("tok-a", "dev-push-apns-key") },
		"fcm":  func() (int, string) { return p.fcm("tok-f", "dev-push-fcm-key") },
		"hms":  func() (int, string) { return p.hms("tok-h", "dev-push-hms-key") },
	} {
		if code, body := f(); code != 200 {
			t.Fatalf("%s: %d %s", name, code, body)
		}
	}
	all := p.pushes("")
	if len(all) != 3 {
		t.Fatalf("%+v", all)
	}
	byProv := map[string]pushRecord{}
	for _, r := range all {
		byProv[r.Provider] = r
	}
	for prov, tok := range map[string]string{"apns": "tok-a", "fcm": "tok-f", "hms": "tok-h"} {
		r := byProv[prov]
		if r.Token != tok || r.OrderID != order || r.Type != "order_ready" || r.Priority != "high" || r.OrderNumber != "R1" {
			t.Fatalf("%s: %+v", prov, r)
		}
	}
	if byProv["apns"].Title != "Заказ готов" {
		t.Fatalf("title: %+v", byProv["apns"])
	}
	if n := len(p.pushes("?token=tok-f")); n != 1 {
		t.Fatalf("token filter: %d", n)
	}
	if n := len(p.pushes("?orderId=" + order)); n != 3 {
		t.Fatalf("order filter: %d", n)
	}
	if n := len(p.pushes("?orderId=other")); n != 0 {
		t.Fatalf("order filter: %d", n)
	}
}

func TestPushRequiresProviderKey(t *testing.T) {
	p := newPush(t)
	if code, _ := p.apns("t", ""); code != 401 {
		t.Fatalf("no key: %d", code)
	}
	if code, _ := p.fcm("t", "dev-push-apns-key"); code != 401 {
		t.Fatalf("key of another provider: %d", code)
	}
	if len(p.pushes("")) != 0 {
		t.Fatal("nothing must be recorded")
	}
}

func TestPushInvalidTokenInProviderFormats(t *testing.T) {
	p := newPush(t)
	if code, body := p.apns("dead-invalid", "dev-push-apns-key"); code != 410 || !strings.Contains(body, "Unregistered") {
		t.Fatalf("apns: %d %s", code, body)
	}
	if code, body := p.fcm("dead-invalid", "dev-push-fcm-key"); code != 404 || !strings.Contains(body, "UNREGISTERED") {
		t.Fatalf("fcm: %d %s", code, body)
	}
	if code, body := p.hms("dead-invalid", "dev-push-hms-key"); code != 400 || !strings.Contains(body, "80300007") {
		t.Fatalf("hms: %d %s", code, body)
	}
	if code, _ := p.fcm("x-reject", "dev-push-fcm-key"); code != 400 {
		t.Fatalf("reject: %d", code)
	}
	if len(p.pushes("")) != 0 {
		t.Fatal("refused pushes must not be recorded")
	}
}

func TestPushFlakyTokenFailsOnce(t *testing.T) {
	p := newPush(t)
	if code, _ := p.fcm("flaky-1", "dev-push-fcm-key"); code != 503 {
		t.Fatalf("first: %d", code)
	}
	if code, _ := p.fcm("flaky-1", "dev-push-fcm-key"); code != 200 {
		t.Fatalf("second: %d", code)
	}
}

func TestPushModesAndNext(t *testing.T) {
	p := newPush(t)
	set := func(path string) {
		t.Helper()
		if code, b := p.call("POST", path, "", "", nil); code != 200 {
			t.Fatalf("%s: %d %s", path, code, b)
		}
	}
	set("/_admin/push/mode?mode=error")
	if code, _ := p.fcm("t", "dev-push-fcm-key"); code != 503 {
		t.Fatalf("error mode: %d", code)
	}
	set("/_admin/push/mode?mode=ratelimit")
	if code, _ := p.fcm("t", "dev-push-fcm-key"); code != 429 {
		t.Fatalf("ratelimit mode: %d", code)
	}
	set("/_admin/push/mode?mode=ok")
	set("/_admin/push/next?outcome=invalid")
	if code, _ := p.fcm("t", "dev-push-fcm-key"); code != 404 {
		t.Fatalf("next=invalid: %d", code)
	}
	if code, _ := p.fcm("t", "dev-push-fcm-key"); code != 200 {
		t.Fatalf("next is one-shot: %d", code)
	}
	if code, _ := p.call("POST", "/_admin/push/mode?mode=bogus", "", "", nil); code != 400 {
		t.Fatal("bad mode must be 400")
	}
	if code, _ := p.call("POST", "/_admin/push/next?outcome=bogus", "", "", nil); code != 400 {
		t.Fatal("bad outcome must be 400")
	}
	if code, _ := p.call("POST", "/_admin/push/reset", "", "", nil); code != 204 || len(p.pushes("")) != 0 {
		t.Fatal("reset clears pushes")
	}
	_, st := p.call("GET", "/_inspect/push", "", "", nil)
	if !strings.Contains(st, `"mode":"ok"`) {
		t.Fatalf("reset restores the mode: %s", st)
	}
}

func TestPushTimeoutModeHoldsRequest(t *testing.T) {
	p := newPush(t)
	if code, _ := p.call("POST", "/_admin/push/mode?mode=timeout", "", "", nil); code != 200 {
		t.Fatal("mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "POST", p.srv.URL+"/3/device/t", strings.NewReader(`{"aps":{"alert":{}}}`))
	req.Header.Set("Authorization", "Bearer dev-push-apns-key")
	if _, err := http.DefaultClient.Do(req); err == nil {
		t.Fatal("client must time out: the mock gives no answer")
	}
}
