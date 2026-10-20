package gateway

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/pavelgr1408/food-platform/pkg/authclient"
	"github.com/pavelgr1408/food-platform/pkg/metrics"
	"github.com/pavelgr1408/food-platform/services/gateway/internal/clientip"
	"github.com/pavelgr1408/food-platform/services/gateway/internal/introspect"
	"github.com/pavelgr1408/food-platform/services/gateway/internal/routes"
)

const (
	issuer   = "restaurant-auth-service"
	audience = "restaurant-api"
	r4       = "a1000000-0000-4000-8000-000000000004"
)

// --- стенд ---

type recorded struct {
	Method, Path, Query, Body string
	Header                    http.Header
}

type upstream struct {
	*httptest.Server
	mu    sync.Mutex
	calls []recorded
	delay time.Duration
	code  int
}

func newUpstream(t *testing.T, name string) *upstream {
	u := &upstream{code: 200}
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			return // тело оборвано (напр. превышен лимит gateway): сервис запроса не получил
		}
		u.mu.Lock()
		u.calls = append(u.calls, recorded{r.Method, r.URL.Path, r.URL.RawQuery, string(b), r.Header.Clone()})
		delay, code := u.delay, u.code
		u.mu.Unlock()
		if delay > 0 {
			time.Sleep(delay)
		}
		w.Header().Set("X-From", name)
		w.Header().Set("X-Request-Id", r.Header.Get("X-Request-Id")) // как делают реальные сервисы
		w.Header().Set("ETag", `"v1"`)
		w.WriteHeader(code)
		_, _ = w.Write([]byte(`{"from":"` + name + `"}`))
	}))
	t.Cleanup(u.Close)
	return u
}

func (u *upstream) count() int { u.mu.Lock(); defer u.mu.Unlock(); return len(u.calls) }
func (u *upstream) last() recorded {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.calls[len(u.calls)-1]
}

type fakeIntro struct {
	mu      sync.Mutex
	active  bool
	err     error
	calls   int
	lastKey string
}

func (f *fakeIntro) Active(_ context.Context, key, _ string, _ time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.lastKey = key
	return f.active, f.err
}

type rig struct {
	t        *testing.T
	h        http.Handler
	key      *rsa.PrivateKey
	ups      map[string]*upstream
	intro    *fakeIntro
	reg      *metrics.Registry
	jwksHits atomic.Int32
}

type opt func(*Deps)

func newRig(t *testing.T, opts ...opt) *rig {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	rg := &rig{t: t, key: key, ups: map[string]*upstream{}, intro: &fakeIntro{active: true}}
	jwks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rg.jwksHits.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{"kty": "RSA", "kid": "k1", "use": "sig", "alg": "RS256",
			"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())}}})
	}))
	t.Cleanup(jwks.Close)
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	verifier, err := authclient.NewJWKSVerifier(authclient.JWKSOptions{URL: jwks.URL, Issuer: issuer, Audience: audience, Log: quiet})
	if err != nil {
		t.Fatal(err)
	}
	ups := map[string]*url.URL{}
	for _, n := range []string{"auth", "restaurant", "menu", "orders", "payment"} {
		rg.ups[n] = newUpstream(t, n)
		ups[n], _ = url.Parse(rg.ups[n].URL)
	}
	rs, err := routes.Load()
	if err != nil {
		t.Fatal(err)
	}
	rg.reg = metrics.New("gateway")
	d := Deps{Log: quiet, Metrics: rg.reg, Routes: rs, Upstreams: ups, Verifier: verifier, Introspector: rg.intro,
		MaxBody: 1 << 10, Timeout: 2 * time.Second,
		Limits: Limits{RPS: 1000, Burst: 1000, AuthPerMin: 600, AuthBurst: 1000}}
	for _, o := range opts {
		o(&d)
	}
	rg.h, err = New(d)
	if err != nil {
		t.Fatal(err)
	}
	return rg
}

func (rg *rig) token(sub string, roles []string, restaurants []string, mod ...func(*jwt.MapClaims)) string {
	now := time.Now()
	c := jwt.MapClaims{"sub": sub, "phone": "+79990000000", "roles": roles, "restaurants": restaurants, "iss": issuer, "aud": audience,
		"iat": now.Unix(), "exp": now.Add(15 * time.Minute).Unix(), "jti": "jti-" + sub}
	for _, m := range mod {
		m(&c)
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, c)
	tok.Header["kid"] = "k1"
	s, err := tok.SignedString(rg.key)
	if err != nil {
		rg.t.Fatal(err)
	}
	return s
}

type req struct {
	method, path, body string
	header             map[string]string
	remote             string
}

func (rg *rig) do(r req) *httptest.ResponseRecorder {
	var body io.Reader
	if r.body != "" {
		body = strings.NewReader(r.body)
	}
	rq := httptest.NewRequest(r.method, r.path, body)
	rq.RemoteAddr = "203.0.113.10:4321"
	if r.remote != "" {
		rq.RemoteAddr = r.remote
	}
	for k, v := range r.header {
		rq.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	rg.h.ServeHTTP(rec, rq)
	return rec
}

func bearer(tok string) map[string]string { return map[string]string{"Authorization": "Bearer " + tok} }

func problem(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	var p map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &p)
	if rec.Code != status || p["code"] != code {
		t.Fatalf("want %d/%s, got %d %s", status, code, rec.Code, rec.Body.String())
	}
	if !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/problem+json") {
		t.Fatalf("Content-Type = %q", rec.Header().Get("Content-Type"))
	}
}

// --- маршрутизация и проксирование ---

func TestPublicRoutePassesThroughUntouched(t *testing.T) {
	rg := newRig(t)
	rec := rg.do(req{method: "GET", path: "/restaurants/" + r4 + "/menu?lang=ru&x=1", header: map[string]string{"If-None-Match": `"v0"`, "X-Request-Id": "req-123"}})
	if rec.Code != 200 || rec.Header().Get("X-From") != "menu" || rec.Header().Get("ETag") != `"v1"` {
		t.Fatalf("response must be relayed as is: %d %v", rec.Code, rec.Header())
	}
	if vals := rec.Header().Values("X-Request-Id"); len(vals) != 1 || vals[0] != "req-123" {
		t.Fatalf("request id must be echoed exactly once, got %q", vals)
	}
	got := rg.ups["menu"].last()
	if got.Path != "/restaurants/"+r4+"/menu" || got.Query != "lang=ru&x=1" || got.Header.Get("If-None-Match") != `"v0"` ||
		got.Header.Get("X-Request-Id") != "req-123" || !strings.HasPrefix(got.Header.Get("Traceparent"), "00-") {
		t.Fatalf("upstream saw %+v", got)
	}
	// /restaurants и /restaurants/{id} — другой сервис, чем меню
	rg.do(req{method: "GET", path: "/restaurants?lat=1&lon=2"})
	rg.do(req{method: "GET", path: "/restaurants/" + r4})
	if rg.ups["restaurant"].count() != 2 || rg.ups["menu"].count() != 1 {
		t.Fatalf("routing: restaurant=%d menu=%d", rg.ups["restaurant"].count(), rg.ups["menu"].count())
	}
	// orders и payment делят префикс /orders/{id}/…
	rg.do(req{method: "GET", path: "/orders/" + r4 + "/eta"})
	rg.do(req{method: "GET", path: "/orders/" + r4 + "/payment-methods"})
	rg.do(req{method: "POST", path: "/orders/" + r4 + "/payments", body: "{}", header: map[string]string{"Idempotency-Key": "k"}})
	if rg.ups["orders"].count() != 1 || rg.ups["payment"].count() != 2 {
		t.Fatalf("routing: orders=%d payment=%d", rg.ups["orders"].count(), rg.ups["payment"].count())
	}
}

func TestBodyAndMethodAreForwarded(t *testing.T) {
	rg := newRig(t)
	rec := rg.do(req{method: "POST", path: "/restaurants/" + r4 + "/menu/validation", body: `{"items":[]}`, header: map[string]string{"Content-Type": "application/json"}})
	got := rg.ups["menu"].last()
	if rec.Code != 200 || got.Method != "POST" || got.Body != `{"items":[]}` || got.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("%d %+v", rec.Code, got)
	}
	rg.ups["orders"].code = 409
	rec = rg.do(req{method: "PUT", path: "/carts/abc/items", body: `{}`})
	if rec.Code != 409 {
		t.Fatalf("upstream status must be relayed, got %d", rec.Code)
	}
}

func TestClientCannotInjectIdentityHeaders(t *testing.T) {
	rg := newRig(t)
	rg.do(req{method: "GET", path: "/orders/" + r4, header: map[string]string{
		"X-User-Id": "admin", "X-User-Roles": "HQ_STAFF", "X-Restaurant-Ids": "x", "X-Forwarded-For": "6.6.6.6", "X-Real-Ip": "6.6.6.6", "Forwarded": "for=6.6.6.6"}})
	got := rg.ups["orders"].last()
	for _, h := range []string{"X-User-Id", "X-User-Roles", "X-Restaurant-Ids", "X-Real-Ip", "Forwarded"} {
		if v := got.Header.Get(h); v != "" {
			t.Errorf("%s=%q reached the service", h, v)
		}
	}
	if got.Header.Get("X-Forwarded-For") != "203.0.113.10" {
		t.Fatalf("X-Forwarded-For = %q, want the real peer (untrusted proxy chain ignored)", got.Header.Get("X-Forwarded-For"))
	}
}

func TestUnknownAndInternalPathsAre404(t *testing.T) {
	rg := newRig(t)
	for _, c := range []struct{ method, path string }{
		{"POST", "/auth/introspect"}, {"POST", "/auth/authorize"}, {"GET", "/metrics"}, {"GET", "/actuator/prometheus"},
		{"POST", "/webhooks/payments/gateway"}, {"POST", "/webhooks/payments/pos"}, {"GET", "/nope"}, {"DELETE", "/orders/" + r4},
		{"GET", "/auth/login"}, {"GET", "/restaurants/"}, {"POST", "/restaurants"},
	} {
		rec := rg.do(req{method: c.method, path: c.path, body: "{}"})
		problem(t, rec, 404, CodeRouteNotFound)
	}
	for n, u := range rg.ups {
		if u.count() != 0 {
			t.Errorf("%s was reached by an unpublished path", n)
		}
	}
}

// --- аутентификация ---

func TestOptionalRouteGuestValidInvalidTokens(t *testing.T) {
	rg := newRig(t)
	if rec := rg.do(req{method: "POST", path: "/carts", body: "{}"}); rec.Code != 200 {
		t.Fatalf("guest must pass: %d", rec.Code)
	}
	if rg.ups["orders"].last().Header.Get("Authorization") != "" {
		t.Fatal("no token must reach the service as no token")
	}
	tok := rg.token("11111111-1111-4111-8111-111111111111", []string{"CUSTOMER"}, nil)
	if rec := rg.do(req{method: "POST", path: "/carts", body: "{}", header: bearer(tok)}); rec.Code != 200 {
		t.Fatalf("valid token: %d", rec.Code)
	}
	if rg.ups["orders"].last().Header.Get("Authorization") != "Bearer "+tok {
		t.Fatal("token must be passed to the service unchanged (services verify it themselves)")
	}
	before := rg.ups["orders"].count()
	forged := tok[:len(tok)-4] + "AAAA"
	expired := rg.token("11111111-1111-4111-8111-111111111111", []string{"CUSTOMER"}, nil, func(c *jwt.MapClaims) { (*c)["exp"] = time.Now().Add(-time.Hour).Unix() })
	wrongAud := rg.token("11111111-1111-4111-8111-111111111111", []string{"CUSTOMER"}, nil, func(c *jwt.MapClaims) { (*c)["aud"] = "other" })
	none := jwt.NewWithClaims(jwt.SigningMethodNone, jwt.MapClaims{"sub": "x", "roles": []string{"HQ_STAFF"}, "iss": issuer, "aud": audience, "exp": time.Now().Add(time.Hour).Unix()})
	noneTok, _ := none.SignedString(jwt.UnsafeAllowNoneSignatureType)
	for name, h := range map[string]map[string]string{
		"forged signature": bearer(forged), "expired": bearer(expired), "wrong audience": bearer(wrongAud), "alg none": bearer(noneTok),
		"garbage": bearer("abc.def.ghi"), "basic scheme": {"Authorization": "Basic Zm9vOmJhcg=="}, "empty bearer": {"Authorization": "Bearer "},
	} {
		rec := rg.do(req{method: "POST", path: "/carts", body: "{}", header: h})
		if rec.Code != 401 {
			t.Errorf("%s: got %d, want 401", name, rec.Code)
			continue
		}
		problem(t, rec, 401, CodeUnauthorized)
		if !strings.Contains(rec.Header().Get("WWW-Authenticate"), "Bearer") {
			t.Errorf("%s: missing WWW-Authenticate", name)
		}
	}
	if rg.ups["orders"].count() != before {
		t.Fatal("rejected requests must not reach the service")
	}
}

func TestPublicRouteIgnoresTokens(t *testing.T) {
	rg := newRig(t)
	rec := rg.do(req{method: "GET", path: "/restaurants", header: bearer("garbage")})
	if rec.Code != 200 {
		t.Fatalf("public route must not look at the token, got %d", rec.Code)
	}
}

func TestRequiredRouteAndRoles(t *testing.T) {
	rg := newRig(t)
	problem(t, rg.do(req{method: "GET", path: "/auth/me"}), 401, CodeUnauthorized)
	customer := rg.token("11111111-1111-4111-8111-111111111111", []string{"CUSTOMER"}, nil)
	staff := rg.token("22222222-2222-4222-8222-222222222222", []string{"STAFF"}, []string{r4})
	if rec := rg.do(req{method: "GET", path: "/auth/me", header: bearer(customer)}); rec.Code != 200 {
		t.Fatalf("me: %d", rec.Code)
	}
	// GET /orders/search (searchOrder) — только персонал
	problem(t, rg.do(req{method: "GET", path: "/orders/search", header: bearer(customer)}), 403, CodeForbidden)
	problem(t, rg.do(req{method: "GET", path: "/orders/search"}), 401, CodeUnauthorized)
	before := rg.ups["orders"].count()
	if rec := rg.do(req{method: "GET", path: "/orders/search?number=R4-1", header: bearer(staff)}); rec.Code != 200 {
		t.Fatalf("staff must pass the gate: %d", rec.Code)
	}
	if rg.ups["orders"].count() != before+1 {
		t.Fatal("staff request must reach orders")
	}
}

func TestSensitiveRoutesUseIntrospection(t *testing.T) {
	rg := newRig(t)
	tok := rg.token("11111111-1111-4111-8111-111111111111", []string{"CUSTOMER"}, nil)
	order := req{method: "POST", path: "/orders", body: "{}", header: bearer(tok)}

	if rec := rg.do(order); rec.Code != 200 || rg.intro.calls != 1 || rg.intro.lastKey != "jti-11111111-1111-4111-8111-111111111111" {
		t.Fatalf("active user: %d, introspections=%d", rec.Code, rg.intro.calls)
	}
	// заблокированный пользователь: токен подписан и не истёк, но auth говорит «неактивен»
	rg.intro.active = false
	problem(t, rg.do(order), 401, CodeUnauthorized)
	// auth лежит → fail-closed
	rg.intro.active, rg.intro.err = true, introspect.ErrUnavailable
	before := rg.ups["orders"].count()
	problem(t, rg.do(order), 503, CodeAuthDown)
	pay := req{method: "POST", path: "/orders/" + r4 + "/payments", body: "{}", header: map[string]string{"Authorization": "Bearer " + tok, "Idempotency-Key": "k1"}}
	problem(t, rg.do(pay), 503, CodeAuthDown)
	if rg.ups["orders"].count() != before || rg.ups["payment"].count() != 0 {
		t.Fatal("fail-closed: money operations must not reach the services while auth is down")
	}
	// гость не затронут недоступностью auth
	if rec := rg.do(req{method: "POST", path: "/orders", body: "{}"}); rec.Code != 200 {
		t.Fatalf("guest checkout must not depend on auth: %d", rec.Code)
	}
	// обычные (не sensitive) маршруты с токеном auth не трогают
	calls := rg.intro.calls
	rg.do(req{method: "GET", path: "/orders/" + r4, header: bearer(tok)})
	if rg.intro.calls != calls {
		t.Fatal("non-sensitive routes must verify the token locally")
	}
}

// --- лимиты ---

func TestAuthClassIsStrictPerIP(t *testing.T) {
	rg := newRig(t, func(d *Deps) {
		d.Limits.AuthPerMin, d.Limits.AuthBurst = 6, 3
		d.ClientIP = clientip.Resolver{Trusted: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}}
	})
	login := func(client string) *httptest.ResponseRecorder {
		return rg.do(req{method: "POST", path: "/auth/login", body: "{}", remote: "10.1.1.1:80", header: map[string]string{"X-Forwarded-For": client}})
	}
	for i := 0; i < 3; i++ {
		if rec := login("198.51.100.1"); rec.Code != 200 {
			t.Fatalf("attempt %d within burst: %d", i, rec.Code)
		}
	}
	rec := login("198.51.100.1")
	problem(t, rec, 429, CodeRateLimited)
	if ra := rec.Header().Get("Retry-After"); ra != "10" {
		t.Fatalf("Retry-After = %q, want 10 (6/min)", ra)
	}
	if rec := login("198.51.100.2"); rec.Code != 200 {
		t.Fatalf("another client behind the same ingress is independent: %d", rec.Code)
	}
	// «обход» подделкой X-Forwarded-For от НЕдоверенного соседа не работает
	for i := 0; i < 3; i++ {
		rg.do(req{method: "POST", path: "/auth/login", body: "{}", header: map[string]string{"X-Forwarded-For": "7.7.7." + string(rune('0'+i))}})
	}
	problem(t, rg.do(req{method: "POST", path: "/auth/login", body: "{}", header: map[string]string{"X-Forwarded-For": "7.7.7.9"}}), 429, CodeRateLimited)
	// остальные маршруты auth-класс не затрагивает
	if rec := rg.do(req{method: "GET", path: "/restaurants"}); rec.Code != 200 {
		t.Fatalf("catalog must stay open: %d", rec.Code)
	}
}

func TestDefaultLimitIsPerUser(t *testing.T) {
	rg := newRig(t, func(d *Deps) { d.Limits.RPS, d.Limits.Burst = 1, 3 })
	a := bearer(rg.token("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", []string{"CUSTOMER"}, nil))
	b := bearer(rg.token("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", []string{"CUSTOMER"}, nil))
	for i := 0; i < 3; i++ {
		if rec := rg.do(req{method: "GET", path: "/orders/" + r4, header: a}); rec.Code != 200 {
			t.Fatalf("request %d: %d", i, rec.Code)
		}
	}
	problem(t, rg.do(req{method: "GET", path: "/orders/" + r4, header: a}), 429, CodeRateLimited)
	// другой пользователь с того же IP (NAT) не страдает
	if rec := rg.do(req{method: "GET", path: "/orders/" + r4, header: b}); rec.Code != 200 {
		t.Fatalf("user B is limited because of user A: %d", rec.Code)
	}
	// поток мусорных токенов с одного IP упирается в лимит IP, а не нагружает проверку подписей бесконечно
	var limited bool
	for i := 0; i < 10; i++ {
		if rec := rg.do(req{method: "GET", path: "/orders/" + r4, header: bearer("garbage"), remote: "198.51.100.77:1"}); rec.Code == 429 {
			limited = true
		}
	}
	if !limited {
		t.Fatal("garbage tokens from one IP must be rate limited")
	}
}

func TestBodyLimit(t *testing.T) {
	rg := newRig(t) // MaxBody = 1 KiB
	big := strings.Repeat("x", 2048)
	problem(t, rg.do(req{method: "POST", path: "/carts", body: big}), 413, CodeTooLarge)
	// без Content-Length (chunked) предел тоже работает
	rq := httptest.NewRequest("POST", "/carts", io.NopCloser(strings.NewReader(big)))
	rq.ContentLength = -1
	rec := httptest.NewRecorder()
	rg.h.ServeHTTP(rec, rq)
	problem(t, rec, 413, CodeTooLarge)
	if rg.ups["orders"].count() != 0 {
		t.Fatal("oversized body must not be accepted by the service")
	}
	if rec := rg.do(req{method: "POST", path: "/carts", body: strings.Repeat("x", 1024)}); rec.Code != 200 {
		t.Fatalf("exactly at the limit must pass: %d", rec.Code)
	}
}

// --- отказы сервисов ---

func TestUpstreamFailures(t *testing.T) {
	rg := newRig(t, func(d *Deps) { d.Timeout = 150 * time.Millisecond })
	rg.ups["menu"].delay = 600 * time.Millisecond
	problem(t, rg.do(req{method: "GET", path: "/restaurants/" + r4 + "/menu"}), 504, CodeUpstreamTimout)
	rg.ups["restaurant"].Close()
	problem(t, rg.do(req{method: "GET", path: "/restaurants"}), 502, CodeUpstreamDown)
	// отказ одного сервиса не ломает остальные
	if rec := rg.do(req{method: "GET", path: "/orders/" + r4}); rec.Code != 200 {
		t.Fatalf("orders: %d", rec.Code)
	}
}

// --- CORS ---

func TestCORS(t *testing.T) {
	rg := newRig(t, func(d *Deps) { d.CORSOrigins = []string{"https://web.example"} })
	pre := rg.do(req{method: "OPTIONS", path: "/orders", header: map[string]string{
		"Origin": "https://web.example", "Access-Control-Request-Method": "POST", "Access-Control-Request-Headers": "idempotency-key"}})
	if pre.Code != 204 || pre.Header().Get("Access-Control-Allow-Origin") != "https://web.example" ||
		!strings.Contains(pre.Header().Get("Access-Control-Allow-Headers"), "Idempotency-Key") ||
		!strings.Contains(pre.Header().Get("Access-Control-Allow-Methods"), "PUT") {
		t.Fatalf("preflight: %d %v", pre.Code, pre.Header())
	}
	if rg.ups["orders"].count() != 0 {
		t.Fatal("preflight must not reach the service")
	}
	ok := rg.do(req{method: "GET", path: "/restaurants", header: map[string]string{"Origin": "https://web.example"}})
	if ok.Header().Get("Access-Control-Allow-Origin") != "https://web.example" || !strings.Contains(ok.Header().Get("Access-Control-Expose-Headers"), "ETag") ||
		!strings.Contains(ok.Header().Get("Vary"), "Origin") {
		t.Fatalf("actual response: %v", ok.Header())
	}
	// ошибки периметра тоже несут CORS (иначе браузер скроет причину)
	bad := rg.do(req{method: "GET", path: "/auth/me", header: map[string]string{"Origin": "https://web.example"}})
	if bad.Code != 401 || bad.Header().Get("Access-Control-Allow-Origin") != "https://web.example" {
		t.Fatalf("401 must carry CORS: %d %v", bad.Code, bad.Header())
	}
	evil := rg.do(req{method: "GET", path: "/restaurants", header: map[string]string{"Origin": "https://evil.example"}})
	if evil.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("foreign origin must not be allowed")
	}
	evilPre := rg.do(req{method: "OPTIONS", path: "/orders", header: map[string]string{"Origin": "https://evil.example", "Access-Control-Request-Method": "POST"}})
	if evilPre.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("foreign preflight must not be allowed")
	}
}

// --- конструктор и метрики ---

func TestNewRejectsRouteToUnknownUpstream(t *testing.T) {
	rs := []routes.Route{{Pattern: "GET /x", Upstream: "ghost", Access: routes.Public}}
	_, err := New(Deps{Routes: rs, Upstreams: map[string]*url.URL{}, Log: slog.Default()})
	if err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("got %v", err)
	}
}

func TestMetricsExposeRejectionsAndRoutes(t *testing.T) {
	rg := newRig(t)
	rg.do(req{method: "GET", path: "/auth/me"})
	rg.do(req{method: "GET", path: "/nope"})
	rec := httptest.NewRecorder()
	rg.reg.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	body := rec.Body.String()
	for _, want := range []string{`gateway_auth_rejected_total{reason="missing",service="gateway"} 1`, `route="GET /auth/me"`, `route="/"`} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics lack %q", want)
		}
	}
}

var _ = bytes.NewReader
var _ = errors.New
