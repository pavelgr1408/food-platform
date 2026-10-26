package push

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pavelgr1408/food-platform/services/notification/internal/domain"
)

type seen struct {
	method, path string
	hdr          http.Header
	body         map[string]any
}

func server(t *testing.T, status int, resp string) (*httptest.Server, *seen) {
	t.Helper()
	s := &seen{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		s.method, s.path, s.hdr = r.Method, r.URL.EscapedPath(), r.Header.Clone()
		_ = json.Unmarshal(b, &s.body)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(resp))
	}))
	t.Cleanup(srv.Close)
	return srv, s
}

func msg(p domain.Platform) Message {
	return Message{ID: "n-1", Platform: p, Token: "tok/en:1", Title: "T", Body: "B", Priority: domain.PriorityHigh,
		OrderID: "o-1", OrderNumber: "R1-1", Type: domain.OrderReady}
}

func router(url string) *Router {
	return NewRouter(Config{APNsURL: url, APNsKey: "ak", APNsTopic: "app.id", FCMURL: url, FCMKey: "fk", FCMProject: "proj",
		HMSURL: url, HMSKey: "hk", HMSAppID: "app1", Timeout: 2 * time.Second})
}

func TestAPNsRequest(t *testing.T) {
	srv, s := server(t, 200, "{}")
	res, err := router(srv.URL).Send(context.Background(), msg(domain.APNS))
	if res != Sent || err != nil {
		t.Fatalf("%v %v", res, err)
	}
	if s.method != "POST" || s.path != "/3/device/tok%2Fen:1" {
		t.Fatalf("%s %s", s.method, s.path)
	}
	if s.hdr.Get("apns-priority") != "10" || s.hdr.Get("apns-topic") != "app.id" || s.hdr.Get("apns-id") != "n-1" || s.hdr.Get("Authorization") != "Bearer ak" {
		t.Fatalf("%v", s.hdr)
	}
	aps := s.body["aps"].(map[string]any)["alert"].(map[string]any)
	if aps["title"] != "T" || aps["body"] != "B" || s.body["orderId"] != "o-1" || s.body["type"] != "order_ready" {
		t.Fatalf("%v", s.body)
	}
}

func TestFCMRequest(t *testing.T) {
	srv, s := server(t, 200, "{}")
	m := msg(domain.FCM)
	m.Priority = domain.PriorityNormal
	if res, err := router(srv.URL).Send(context.Background(), m); res != Sent || err != nil {
		t.Fatalf("%v %v", res, err)
	}
	if s.path != "/v1/projects/proj/messages:send" || s.hdr.Get("Authorization") != "Bearer fk" {
		t.Fatalf("%s %v", s.path, s.hdr)
	}
	mm := s.body["message"].(map[string]any)
	if mm["token"] != "tok/en:1" || mm["android"].(map[string]any)["priority"] != "NORMAL" ||
		mm["data"].(map[string]any)["orderNumber"] != "R1-1" || mm["notification"].(map[string]any)["title"] != "T" {
		t.Fatalf("%v", mm)
	}
}

func TestHMSRequest(t *testing.T) {
	srv, s := server(t, 200, "{}")
	if res, err := router(srv.URL).Send(context.Background(), msg(domain.Huawei)); res != Sent || err != nil {
		t.Fatalf("%v %v", res, err)
	}
	if s.path != "/v1/app1/messages:send" || s.hdr.Get("Authorization") != "Bearer hk" {
		t.Fatalf("%s %v", s.path, s.hdr)
	}
	mm := s.body["message"].(map[string]any)
	if mm["token"].([]any)[0] != "tok/en:1" || mm["android"].(map[string]any)["urgency"] != "HIGH" {
		t.Fatalf("%v", mm)
	}
	var d map[string]string
	if err := json.Unmarshal([]byte(mm["data"].(string)), &d); err != nil || d["type"] != "order_ready" {
		t.Fatalf("data must be a JSON string: %v %v", mm["data"], err)
	}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		code int
		body string
		want Result
	}{
		{200, "", Sent}, {201, "", Sent}, {204, "", Sent},
		{404, "", InvalidToken}, {410, `{"reason":"Unregistered"}`, InvalidToken},
		{400, `{"reason":"BadDeviceToken"}`, InvalidToken}, {400, `{"error":{"status":"UNREGISTERED"}}`, InvalidToken}, {400, `{"code":"80300007"}`, InvalidToken},
		{400, `{"reason":"PayloadEmpty"}`, Failed}, {413, "", Failed}, {422, "", Failed},
		{401, "", Retry}, {403, "", Retry}, {408, "", Retry}, {429, "", Retry}, {500, "", Retry}, {503, "", Retry},
	}
	for _, c := range cases {
		if got := classify(c.code, c.body); got != c.want {
			t.Errorf("%d %q: got %v want %v", c.code, c.body, got, c.want)
		}
	}
}

func TestSendClassifiesStatuses(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   Result
	}{{200, Sent}, {410, InvalidToken}, {503, Retry}, {400, Failed}} {
		srv, _ := server(t, tc.status, `{"reason":"x"}`)
		res, err := router(srv.URL).Send(context.Background(), msg(domain.FCM))
		if res != tc.want {
			t.Errorf("status %d: got %v", tc.status, res)
		}
		if (tc.want == Sent) != (err == nil) {
			t.Errorf("status %d: err=%v", tc.status, err)
		}
	}
}

func TestNetworkErrorIsRetryAndDoesNotLeakToken(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close() // соединение будет отвергнуто
	m := msg(domain.APNS)
	m.Token = "SECRET-DEVICE-TOKEN"
	res, err := router(url).Send(context.Background(), m)
	if res != Retry || err == nil {
		t.Fatalf("%v %v", res, err)
	}
	if strings.Contains(err.Error(), "SECRET-DEVICE-TOKEN") {
		t.Fatalf("error leaks push token: %v", err)
	}
}

func TestTimeoutIsRetry(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(2 * time.Second):
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	r := NewRouter(Config{FCMURL: srv.URL, FCMProject: "p", Timeout: 100 * time.Millisecond})
	res, err := r.Send(context.Background(), msg(domain.FCM))
	if res != Retry || err == nil {
		t.Fatalf("%v %v", res, err)
	}
}

func TestUnsupportedPlatform(t *testing.T) {
	res, err := router("http://x").Send(context.Background(), Message{Platform: "sms"})
	if res != Failed || err == nil {
		t.Fatalf("%v %v", res, err)
	}
}
