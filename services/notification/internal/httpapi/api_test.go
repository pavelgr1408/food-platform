package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pavelgr1408/food-platform/pkg/authclient"
	"github.com/pavelgr1408/food-platform/pkg/httpx"
	"github.com/pavelgr1408/food-platform/pkg/metrics"
	"github.com/pavelgr1408/food-platform/services/notification/internal/domain"
	"github.com/pavelgr1408/food-platform/services/notification/internal/service"
)

type stub struct {
	in      service.RegisterInput
	created bool
	err     error
	list    []domain.DeviceToken
	delUser string
	delID   string
	calls   int
}

var regAt = time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)

func (s *stub) Register(_ context.Context, in service.RegisterInput) (domain.DeviceToken, bool, error) {
	s.in, s.calls = in, s.calls+1
	return domain.DeviceToken{ID: "11111111-1111-4111-8111-111111111111", UserID: in.UserID, Platform: in.Platform, Token: in.Token,
		PermissionsGranted: in.Permissions, Locale: in.Locale, RegisteredAt: regAt}, s.created, s.err
}
func (s *stub) List(_ context.Context, _ string) ([]domain.DeviceToken, error) {
	s.calls++
	return s.list, s.err
}
func (s *stub) Delete(_ context.Context, u, id string) error {
	s.delUser, s.delID, s.calls = u, id, s.calls+1
	return s.err
}

func newHandler(s *stub) http.Handler {
	return New(Deps{Log: slog.New(slog.NewJSONHandler(io.Discard, nil)), Metrics: metrics.New("notification"),
		Auth: authclient.DevAuthenticator{}, Tokens: s})
}

func do(h http.Handler, method, path, body string, hdr map[string]string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	h.ServeHTTP(rec, req)
	return rec
}

var alice = map[string]string{"X-User-Id": "alice", "X-User-Roles": "CUSTOMER"}

func problemCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var p httpx.Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("not a problem: %s", rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != httpx.ContentTypeProblem {
		t.Fatalf("content-type %q", ct)
	}
	return p.Code
}

func TestHooksPresent(t *testing.T) {
	h := newHandler(&stub{})
	if r := do(h, "GET", "/health/", "", nil); r.Code != 200 || strings.TrimSpace(r.Body.String()) != `{"status":"OK"}` {
		t.Fatalf("liveness: %d %s", r.Code, r.Body)
	}
	if r := do(h, "GET", "/actuator/health", "", nil); r.Code != 200 {
		t.Fatalf("readiness: %d", r.Code)
	}
	if r := do(h, "GET", "/metrics", "", nil); r.Code != 200 || !strings.Contains(r.Body.String(), "http_requests_total") {
		t.Fatalf("metrics: %d", r.Code)
	}
}

func TestGuestGets401OnEveryOperation(t *testing.T) {
	s := &stub{}
	h := newHandler(s)
	id := "11111111-1111-4111-8111-111111111111"
	for _, c := range [][3]string{
		{"POST", "/notifications/device-tokens", `{"platform":"fcm","token":"x"}`},
		{"GET", "/notifications/device-tokens", ""},
		{"DELETE", "/notifications/device-tokens/" + id, ""},
	} {
		r := do(h, c[0], c[1], c[2], nil)
		if r.Code != 401 || problemCode(t, r) != "UNAUTHORIZED" {
			t.Errorf("%s %s: %d %s", c[0], c[1], r.Code, r.Body)
		}
	}
	if s.calls != 0 {
		t.Fatal("service must not be called for guests")
	}
}

func TestRegisterCreatedAndLocation(t *testing.T) {
	s := &stub{created: true}
	r := do(newHandler(s), "POST", "/notifications/device-tokens", `{"platform":"apns","token":"abc"}`,
		map[string]string{"X-User-Id": "alice", "X-User-Roles": "STAFF", "Accept-Language": "en-US,en;q=0.9"})
	if r.Code != 201 || r.Header().Get("Location") != "/notifications/device-tokens/11111111-1111-4111-8111-111111111111" {
		t.Fatalf("%d %v %s", r.Code, r.Header(), r.Body)
	}
	if s.in.UserID != "alice" || s.in.Platform != domain.APNS || s.in.Token != "abc" || !s.in.Permissions || s.in.Locale != domain.EN {
		t.Fatalf("permissionsGranted defaults to true, language from Accept-Language, any role may register: %+v", s.in)
	}
	var out map[string]any
	_ = json.Unmarshal(r.Body.Bytes(), &out)
	if out["id"] == nil || out["platform"] != "apns" || out["permissionsGranted"] != true || out["locale"] != "en" || out["registeredAt"] != "2026-10-25T12:00:00Z" {
		t.Fatalf("%v", out)
	}
}

func TestRegisterExistingIs200WithoutLocation(t *testing.T) {
	s := &stub{created: false}
	r := do(newHandler(s), "POST", "/notifications/device-tokens", `{"platform":"fcm","token":"abc","permissionsGranted":false}`, alice)
	if r.Code != 200 || r.Header().Get("Location") != "" || s.in.Permissions {
		t.Fatalf("%d %v perm=%v", r.Code, r.Header(), s.in.Permissions)
	}
}

func TestRegisterRejectsBadBodies(t *testing.T) {
	for name, body := range map[string]string{"unknown field": `{"platform":"fcm","token":"x","extra":1}`, "broken": `{`, "two values": `{} {}`} {
		s := &stub{}
		r := do(newHandler(s), "POST", "/notifications/device-tokens", body, alice)
		if r.Code != 400 || problemCode(t, r) != "INVALID_JSON" || s.calls != 0 {
			t.Errorf("%s: %d %s", name, r.Code, r.Body)
		}
	}
	// ошибка валидации сервиса доходит как 400 problem
	s := &stub{err: httpx.NewError(400, "VALIDATION_FAILED", "platform must be one of apns, fcm, huawei")}
	r := do(newHandler(s), "POST", "/notifications/device-tokens", `{"platform":"sms","token":"x"}`, alice)
	if r.Code != 400 || problemCode(t, r) != "VALIDATION_FAILED" {
		t.Fatalf("%d %s", r.Code, r.Body)
	}
}

func TestListReturnsArray(t *testing.T) {
	r := do(newHandler(&stub{}), "GET", "/notifications/device-tokens", "", alice)
	if r.Code != 200 || strings.TrimSpace(r.Body.String()) != "[]" {
		t.Fatalf("empty list must be [], got %d %q", r.Code, r.Body)
	}
	s := &stub{list: []domain.DeviceToken{{ID: "a", Platform: domain.FCM, Token: "t", Locale: domain.RU, PermissionsGranted: true, RegisteredAt: regAt}}}
	r = do(newHandler(s), "GET", "/notifications/device-tokens", "", alice)
	var out []map[string]any
	if err := json.Unmarshal(r.Body.Bytes(), &out); err != nil || len(out) != 1 || out[0]["token"] != "t" {
		t.Fatalf("%v %s", err, r.Body)
	}
}

func TestDelete(t *testing.T) {
	id := "11111111-1111-4111-8111-111111111111"
	s := &stub{}
	if r := do(newHandler(s), "DELETE", "/notifications/device-tokens/"+id, "", alice); r.Code != 204 || r.Body.Len() != 0 || s.delUser != "alice" || s.delID != id {
		t.Fatalf("%d %q user=%s id=%s", r.Code, r.Body, s.delUser, s.delID)
	}
	s = &stub{err: httpx.NewError(404, "DEVICE_TOKEN_NOT_FOUND", "device token not found")}
	r := do(newHandler(s), "DELETE", "/notifications/device-tokens/"+id, "", alice)
	if r.Code != 404 || problemCode(t, r) != "DEVICE_TOKEN_NOT_FOUND" {
		t.Fatalf("%d %s", r.Code, r.Body)
	}
}

func TestInternalErrorDoesNotLeak(t *testing.T) {
	s := &stub{err: io.ErrUnexpectedEOF}
	r := do(newHandler(s), "GET", "/notifications/device-tokens", "", alice)
	if r.Code != 500 || strings.Contains(r.Body.String(), "unexpected EOF") {
		t.Fatalf("%d %s", r.Code, r.Body)
	}
}
