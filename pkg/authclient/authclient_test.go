package authclient

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/pavelgr1408/food-platform/pkg/httpx"
)

func serve(h http.Handler, hdr map[string]string) int {
	req := httptest.NewRequest("GET", "/x", nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

func TestRoleGuard(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	h := httpx.Chain(ok, Middleware(DevAuthenticator{}), RequireRole(RoleStaff, RoleHQStaff))
	if c := serve(h, nil); c != 401 {
		t.Errorf("guest: %d", c)
	}
	if c := serve(h, map[string]string{HeaderUserID: "u1", HeaderUserRoles: "CUSTOMER"}); c != 403 {
		t.Errorf("customer: %d", c)
	}
	if c := serve(h, map[string]string{HeaderUserID: "u2", HeaderUserRoles: "STAFF, CUSTOMER"}); c != 200 {
		t.Errorf("staff: %d", c)
	}
}

func TestRestaurantScope(t *testing.T) {
	staff := Principal{UserID: "u", Roles: []string{RoleStaff}, Restaurants: []string{"r1"}}
	if !staff.CanAccessRestaurant("r1") || staff.CanAccessRestaurant("r2") {
		t.Error("staff scope")
	}
	if !(Principal{Roles: []string{RoleHQStaff}}).CanAccessRestaurant("any") {
		t.Error("HQ sees all")
	}
}

func TestNewFromModeRefusesDevInProd(t *testing.T) {
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	if _, err := NewFromMode("dev", "prod", log); err == nil {
		t.Fatal("dev auth must be rejected in prod")
	}
	if _, err := NewFromMode("dev", "dev", log); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFromMode("", "dev", log); err == nil {
		t.Fatal("mode required")
	}
}

func TestFromContextDefaultsToGuest(t *testing.T) {
	if !FromContext(httptest.NewRequest("GET", "/", nil).Context()).Guest {
		t.Fatal("default must be guest")
	}
}
