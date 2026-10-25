package authz

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/pavelgr1408/food-platform/pkg/authclient"
	"github.com/pavelgr1408/food-platform/pkg/httpx"
)

const (
	r4 = "a1000000-0000-4000-8000-000000000004"
	r1 = "a1000000-0000-4000-8000-000000000001"
)

func principal(roles []string, rests ...string) authclient.Principal {
	return authclient.Principal{UserID: "u1", Roles: roles, Restaurants: rests}
}

// Таблица повторяет services/auth/internal/httpapi/api_test.go (TestAuthorize): политика не должна расходиться.
func TestClaimsMatchesAuthPolicy(t *testing.T) {
	tests := []struct {
		name   string
		p      authclient.Principal
		scope  string
		rest   string
		want   bool
		reason string
	}{
		{"owner local own", principal([]string{"FRANCHISE_OWNER"}, r4), "local", r4, true, ""},
		{"owner local other", principal([]string{"FRANCHISE_OWNER"}, r4), "local", r1, false, ReasonNotBoundToRestaurant},
		{"owner national", principal([]string{"FRANCHISE_OWNER"}, r4), "national", "", false, ReasonScopeNotAllowed},
		{"hq national", principal([]string{"HQ_STAFF"}), "national", "", true, ""},
		{"hq local any", principal([]string{"HQ_STAFF"}), "local", r1, true, ""},
		{"staff local", principal([]string{"STAFF"}, r4), "local", r4, false, ReasonRoleRequired},
		{"customer national", principal([]string{"CUSTOMER"}), "national", "", false, ReasonRoleRequired},
		{"guest", authclient.Principal{Guest: true}, "national", "", false, ReasonRoleRequired},
		{"owner local uppercase id", principal([]string{"FRANCHISE_OWNER"}, r4), "local", "A1000000-0000-4000-8000-000000000004", true, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d, err := Claims{}.CanManagePromotion(context.Background(), Request{Principal: tc.p, Scope: tc.scope, RestaurantID: tc.rest})
			if err != nil {
				t.Fatal(err)
			}
			if d.Allowed != tc.want || d.Reason != tc.reason {
				t.Fatalf("got %+v, want allowed=%v reason=%q", d, tc.want, tc.reason)
			}
		})
	}
	if _, err := (Claims{}).CanManagePromotion(context.Background(), Request{Principal: principal([]string{"HQ_STAFF"}), Scope: "x"}); err == nil {
		t.Fatal("unknown scope must be an error")
	}
}

func fakeAuth(t *testing.T, status int, body string, calls *atomic.Int32, got *map[string]string, hdr *http.Header) *HTTP {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/auth/authorize" || r.Method != http.MethodPost {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		m := map[string]string{}
		_ = json.NewDecoder(r.Body).Decode(&m)
		*got, *hdr = m, r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return NewHTTP(srv.URL+"/", httpx.NewClient(httpx.ClientConfig{MaxRetries: -1}))
}

func TestHTTPAllowedAndDenied(t *testing.T) {
	var calls atomic.Int32
	var got map[string]string
	var hdr http.Header
	h := fakeAuth(t, 200, `{"allowed":true}`, &calls, &got, &hdr)
	d, err := h.CanManagePromotion(context.Background(), Request{Authorization: "Bearer tok", Scope: "local", RestaurantID: r4})
	if err != nil || !d.Allowed {
		t.Fatalf("got %+v %v", d, err)
	}
	if got["action"] != "promotion.manage" || got["scope"] != "local" || got["restaurantId"] != r4 || hdr.Get("Authorization") != "Bearer tok" {
		t.Fatalf("request not forwarded: %v %v", got, hdr)
	}

	h = fakeAuth(t, 200, `{"allowed":false,"reason":"SCOPE_NOT_ALLOWED"}`, &calls, &got, &hdr)
	d, err = h.CanManagePromotion(context.Background(), Request{Authorization: "Bearer tok", Scope: "national"})
	if err != nil || d.Allowed || d.Reason != ReasonScopeNotAllowed {
		t.Fatalf("got %+v %v", d, err)
	}
	if _, ok := got["restaurantId"]; ok {
		t.Fatal("restaurantId must be omitted for national scope")
	}
}

func TestHTTPFailClosed(t *testing.T) {
	var calls atomic.Int32
	var got map[string]string
	var hdr http.Header
	for name, tc := range map[string]struct {
		status int
		body   string
	}{"500": {500, `{}`}, "503": {503, ``}, "bad json": {200, `not json`}, "400": {400, `{}`}} {
		t.Run(name, func(t *testing.T) {
			h := fakeAuth(t, tc.status, tc.body, &calls, &got, &hdr)
			_, err := h.CanManagePromotion(context.Background(), Request{Authorization: "Bearer t", Scope: "national"})
			if !errors.Is(err, ErrUnavailable) {
				t.Fatalf("want ErrUnavailable, got %v", err)
			}
		})
	}
	// auth вообще не отвечает
	dead := NewHTTP("http://127.0.0.1:1", httpx.NewClient(httpx.ClientConfig{MaxRetries: -1}))
	if _, err := dead.CanManagePromotion(context.Background(), Request{Authorization: "Bearer t", Scope: "national"}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("want ErrUnavailable, got %v", err)
	}
}

func TestHTTPNoTokenAndRejectedToken(t *testing.T) {
	var calls atomic.Int32
	var got map[string]string
	var hdr http.Header
	h := fakeAuth(t, 401, `{}`, &calls, &got, &hdr)
	d, err := h.CanManagePromotion(context.Background(), Request{Scope: "national"})
	if err != nil || d.Allowed || calls.Load() != 0 {
		t.Fatalf("no token must be denied without calling auth: %+v %v calls=%d", d, err, calls.Load())
	}
	d, err = h.CanManagePromotion(context.Background(), Request{Authorization: "Bearer revoked", Scope: "national"})
	if err != nil || d.Allowed || d.Reason != ReasonNotActive {
		t.Fatalf("rejected token must be denied: %+v %v", d, err)
	}
}
