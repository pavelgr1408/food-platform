package httpapi_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pavelgr1408/food-platform/pkg/authclient"
	"github.com/pavelgr1408/food-platform/pkg/idempotency"
	"github.com/pavelgr1408/food-platform/pkg/metrics"
	"github.com/pavelgr1408/food-platform/services/loyalty/internal/authz"
	"github.com/pavelgr1408/food-platform/services/loyalty/internal/httpapi"
	"github.com/pavelgr1408/food-platform/services/loyalty/internal/service"
	"github.com/pavelgr1408/food-platform/services/loyalty/internal/testkit"
)

const (
	r1 = "a1000000-0000-4000-8000-000000000001"
	r4 = "a1000000-0000-4000-8000-000000000004"
)

var (
	hq    = map[string]string{"X-User-Id": "hq", "X-User-Roles": "HQ_STAFF"}
	owner = map[string]string{"X-User-Id": "owner4", "X-User-Roles": "FRANCHISE_OWNER", "X-Restaurant-Ids": r4}
	staff = map[string]string{"X-User-Id": "s4", "X-User-Roles": "STAFF", "X-Restaurant-Ids": r4}
	cust  = map[string]string{"X-User-Id": "c1", "X-User-Roles": "CUSTOMER"}
)

func newHandler(t *testing.T) (http.Handler, *testkit.Clock) {
	t.Helper()
	pool := testkit.Pool(t)
	clock := testkit.NewClock()
	svc := service.New(service.Deps{Pool: pool, Authz: authz.Claims{}, Now: clock.Now})
	return httpapi.New(httpapi.Deps{Log: testkit.Log(), Metrics: metrics.New("loyalty"), Auth: authclient.DevAuthenticator{},
		Promotions: svc, Idempotency: idempotency.NewStore(pool)}), clock
}

func do(h http.Handler, method, path, body string, hdr ...map[string]string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, m := range hdr {
		for k, v := range m {
			req.Header.Set(k, v)
		}
	}
	h.ServeHTTP(rec, req)
	return rec
}

func key(k string) map[string]string { return map[string]string{"Idempotency-Key": k} }

func body(scope, rest string, prio int, extra string) string {
	r := ""
	if rest != "" {
		r = `"restaurantId":"` + rest + `",`
	}
	return `{"title":"Скидка","scope":"` + scope + `",` + r + `"priority":` + itoa(prio) +
		`,"startsAt":"2026-11-01T00:00:00Z","endsAt":"2026-12-01T00:00:00Z","discount":{"type":"percent","value":1500}` + extra + `}`
}

func itoa(i int) string { b, _ := json.Marshal(i); return string(b) }

func decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("body %q: %v", rec.Body.String(), err)
	}
	return m
}

func TestFlowAndJSONShape(t *testing.T) {
	h, _ := newHandler(t)
	rec := do(h, "POST", "/promotions", body("local", r4, 5, `,"conditions":{"minSubtotal":50000,"menuItemIds":["b2000000-0000-4000-8000-000000000001"],"stackable":false}`), owner, key("k1"))
	if rec.Code != 201 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	p := decode(t, rec)
	id := p["id"].(string)
	if rec.Header().Get("Location") != "/promotions/"+id || p["status"] != "draft" || p["version"].(float64) != 1 || p["currency"] != "RUB" {
		t.Fatalf("shape: %s", rec.Body)
	}
	if d := p["discount"].(map[string]any); d["type"] != "percent" || d["value"].(float64) != 1500 {
		t.Fatalf("discount: %v", d)
	}
	if c := p["conditions"].(map[string]any); c["minSubtotal"].(float64) != 50000 || c["stackable"] != false {
		t.Fatalf("conditions: %v", c)
	}

	// гость видит только действующие: черновик в списке не появляется
	rec = do(h, "GET", "/promotions?restaurantId="+r4, "")
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("draft must be hidden: %d %s", rec.Code, rec.Body)
	}

	if rec = do(h, "POST", "/promotions/"+id+"/activation", "", owner); rec.Code != 200 || decode(t, rec)["status"] != "active" {
		t.Fatalf("activate: %d %s", rec.Code, rec.Body)
	}
	rec = do(h, "GET", "/promotions?restaurantId="+r4, "")
	var list []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list) != 1 || list[0]["id"] != id {
		t.Fatalf("list: %s", rec.Body)
	}
	if _, leaked := list[0]["createdBy"]; leaked || list[0]["status"] != nil {
		t.Fatalf("public view must not expose internal fields: %v", list[0])
	}
	if rec = do(h, "GET", "/promotions", ""); strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("guest without restaurant sees national only: %s", rec.Body)
	}

	if rec = do(h, "GET", "/promotions/"+id+"/history", "", owner); rec.Code != 200 {
		t.Fatalf("history: %d", rec.Code)
	}
	var hist []map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &hist)
	if len(hist) != 2 || hist[0]["action"] != "created" || hist[1]["action"] != "activated" || hist[1]["snapshot"].(map[string]any)["status"] != "active" {
		t.Fatalf("history: %s", rec.Body)
	}

	if rec = do(h, "POST", "/promotions/"+id+"/completion", "", owner); rec.Code != 200 || decode(t, rec)["status"] != "completed" {
		t.Fatalf("complete: %d %s", rec.Code, rec.Body)
	}
	if rec = do(h, "PUT", "/promotions/"+id, body("local", r4, 5, ""), owner); rec.Code != 409 || decode(t, rec)["code"] != "PROMOTION_COMPLETED" {
		t.Fatalf("update completed: %d %s", rec.Code, rec.Body)
	}
}

func TestIdempotentCreate(t *testing.T) {
	h, _ := newHandler(t)
	b := body("national", "", 1, "")
	first := do(h, "POST", "/promotions", b, hq, key("same"))
	replay := do(h, "POST", "/promotions", b, hq, key("same"))
	if first.Code != 201 || replay.Code != 201 || replay.Header().Get("Idempotent-Replay") != "true" || first.Body.String() != replay.Body.String() {
		t.Fatalf("replay: %d/%d %q\n%s\n%s", first.Code, replay.Code, replay.Header().Get("Idempotent-Replay"), first.Body, replay.Body)
	}
	if rec := do(h, "POST", "/promotions", body("national", "", 2, ""), hq, key("same")); rec.Code != 422 || decode(t, rec)["code"] != "IDEMPOTENCY_KEY_REUSED" {
		t.Fatalf("reuse: %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "POST", "/promotions", b, hq); rec.Code != 400 || decode(t, rec)["code"] != "IDEMPOTENCY_KEY_REQUIRED" {
		t.Fatalf("missing key: %d %s", rec.Code, rec.Body)
	}
	// ключ не «утекает» между пользователями
	if rec := do(h, "POST", "/promotions", body("local", r4, 1, ""), owner, key("same")); rec.Code != 201 {
		t.Fatalf("scope per user: %d %s", rec.Code, rec.Body)
	}
}

func TestAccess(t *testing.T) {
	h, _ := newHandler(t)
	b := body("local", r4, 1, "")
	for name, tc := range map[string]struct {
		hdr  map[string]string
		code int
	}{"guest": {nil, 401}, "customer": {cust, 403}, "staff": {staff, 403}} {
		for _, rt := range []struct{ m, p, b string }{
			{"POST", "/promotions", b}, {"GET", "/promotions/" + r1, ""}, {"PUT", "/promotions/" + r1, b},
			{"GET", "/promotions/" + r1 + "/history", ""}, {"POST", "/promotions/" + r1 + "/activation", ""}, {"POST", "/promotions/" + r1 + "/completion", ""},
		} {
			if rec := do(h, rt.m, rt.p, rt.b, tc.hdr, key("k")); rec.Code != tc.code {
				t.Errorf("%s %s %s: %d, want %d", name, rt.m, rt.p, rec.Code, tc.code)
			}
		}
	}
	if rec := do(h, "POST", "/promotions", body("national", "", 0, ""), owner, key("n")); rec.Code != 403 || decode(t, rec)["code"] != "SCOPE_NOT_ALLOWED" {
		t.Fatalf("owner national: %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "POST", "/promotions", body("local", r1, 0, ""), owner, key("o")); rec.Code != 403 || decode(t, rec)["code"] != "NOT_BOUND_TO_RESTAURANT" {
		t.Fatalf("owner other: %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "GET", "/promotions/"+r1, "", hq); rec.Code != 404 || decode(t, rec)["code"] != "PROMOTION_NOT_FOUND" {
		t.Fatalf("not found: %d %s", rec.Code, rec.Body)
	}
}

func TestConflictProblemHasIDs(t *testing.T) {
	h, _ := newHandler(t)
	a := decode(t, do(h, "POST", "/promotions", body("national", "", 1, ""), hq, key("a")))["id"].(string)
	if rec := do(h, "POST", "/promotions/"+a+"/activation", "", hq); rec.Code != 200 {
		t.Fatal(rec.Body)
	}
	b := decode(t, do(h, "POST", "/promotions", body("local", r4, 1, ""), owner, key("b")))["id"].(string)
	rec := do(h, "POST", "/promotions/"+b+"/activation", "", owner)
	m := decode(t, rec)
	ids, _ := m["conflictingPromotionIds"].([]any)
	if rec.Code != 409 || m["code"] != "PROMOTION_CONFLICT" || len(ids) != 1 || ids[0] != a || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/problem+json") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestInputValidation(t *testing.T) {
	h, _ := newHandler(t)
	cases := map[string]struct {
		body string
		code int
		err  string
	}{
		"malformed json":        {`{`, 400, "INVALID_JSON"},
		"unknown field":         {`{"title":"x","scope":"national","bogus":1}`, 400, "INVALID_JSON"},
		"unknown condition":     {body("national", "", 0, `,"conditions":{"coupon":"X"}`), 400, "INVALID_JSON"},
		"float discount":        {strings.Replace(body("national", "", 0, ""), `"value":1500`, `"value":15.5`, 1), 400, "INVALID_JSON"},
		"missing discount":      {`{"title":"x","scope":"national","startsAt":"2026-11-01T00:00:00Z","endsAt":"2026-12-01T00:00:00Z"}`, 400, "VALIDATION_FAILED"},
		"percent over 100":      {strings.Replace(body("national", "", 0, ""), `"value":1500`, `"value":10001`, 1), 422, "PROMOTION_INVALID"},
		"local without rest":    {body("local", "", 0, ""), 422, "PROMOTION_INVALID"},
		"ends before starts":    {strings.Replace(body("national", "", 0, ""), "2026-12-01", "2026-10-01", 1), 422, "PROMOTION_INVALID"},
		"gift is not supported": {strings.Replace(body("national", "", 0, ""), `"percent"`, `"gift"`, 1), 422, "PROMOTION_INVALID"},
	}
	n := 0
	for name, tc := range cases {
		n++
		rec := do(h, "POST", "/promotions", tc.body, hq, key("v"+itoa(n)))
		if rec.Code != tc.code || decode(t, rec)["code"] != tc.err {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	if rec := do(h, "GET", "/promotions?restaurantId=zzz", ""); rec.Code != 400 {
		t.Errorf("bad restaurantId: %d", rec.Code)
	}
}
