package httpapi_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pavelgr1408/food-platform/pkg/authclient"
	"github.com/pavelgr1408/food-platform/pkg/metrics"
	"github.com/pavelgr1408/food-platform/pkg/pgxutil"
	"github.com/pavelgr1408/food-platform/pkg/pgxutil/pgtest"
	"github.com/pavelgr1408/food-platform/services/auth/internal/httpapi"
	"github.com/pavelgr1408/food-platform/services/auth/internal/seed"
	"github.com/pavelgr1408/food-platform/services/auth/internal/service"
	"github.com/pavelgr1408/food-platform/services/auth/internal/store"
	"github.com/pavelgr1408/food-platform/services/auth/internal/token"
	"github.com/pavelgr1408/food-platform/services/auth/migrations"
)

const (
	r1 = "a1000000-0000-4000-8000-000000000001"
	r4 = "a1000000-0000-4000-8000-000000000004"
)

type env struct {
	t    *testing.T
	srv  *httptest.Server
	exec func(sql string, args ...any)
}

func writeKeys(t *testing.T, dir, name string) (priv, pub string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
	priv, pub = filepath.Join(dir, name+".key"), filepath.Join(dir, name+".pub")
	_ = os.WriteFile(priv, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0600)
	_ = os.WriteFile(pub, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), 0600)
	return priv, pub
}

func newEnv(t *testing.T) *env {
	t.Helper()
	pool := pgtest.New(t)
	ctx := context.Background()
	if err := pgxutil.Migrate(ctx, pool, nil, pgxutil.Source{Module: "auth", FS: migrations.FS}); err != nil {
		t.Fatal(err)
	}
	priv, pub := writeKeys(t, t.TempDir(), "k")
	tm, err := token.New(token.Config{PrivatePath: priv, PublicPath: pub, Issuer: "restaurant-auth-service", Audience: "restaurant-api", KeyID: "test", TTL: 15 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	st := store.New(pool)
	svc := service.New(st, tm, time.Hour, 4)
	if err := seed.Apply(ctx, st, svc, log); err != nil {
		t.Fatal(err)
	}
	h := httpapi.New(httpapi.Deps{Log: log, Metrics: metrics.New("auth"), Service: svc,
		Checks: nil})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	e := &env{t: t, srv: srv}
	e.exec = func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	return e
}

type resp struct {
	status int
	header http.Header
	body   map[string]any
	raw    string
}

func (e *env) do(method, path, bearer string, body any) resp {
	e.t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rd = strings.NewReader(b)
	default:
		j, _ := json.Marshal(b)
		rd = bytes.NewReader(j)
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer r.Body.Close()
	raw, _ := io.ReadAll(r.Body)
	out := resp{status: r.StatusCode, header: r.Header, raw: string(raw)}
	_ = json.Unmarshal(raw, &out.body)
	return out
}

func (e *env) login(phone, password string) resp {
	return e.do("POST", "/auth/login", "", map[string]string{"phone": phone, "password": password})
}

func (e *env) accessToken(phone, password string) string {
	e.t.Helper()
	r := e.login(phone, password)
	if r.status != 200 {
		e.t.Fatalf("login %s: %d %s", phone, r.status, r.raw)
	}
	return r.body["accessToken"].(string)
}

func wantProblem(t *testing.T, r resp, status int, code string) {
	t.Helper()
	if r.status != status || r.body["code"] != code {
		t.Fatalf("want %d/%s, got %d %s", status, code, r.status, r.raw)
	}
	if ct := r.header.Get("Content-Type"); !strings.HasPrefix(ct, "application/problem+json") {
		t.Fatalf("Content-Type = %q, want problem+json", ct)
	}
	for _, k := range []string{"title", "status", "detail", "instance"} {
		if _, ok := r.body[k]; !ok {
			t.Fatalf("problem has no %q: %s", k, r.raw)
		}
	}
}

func TestRegisterLoginRefreshLogoutFlow(t *testing.T) {
	e := newEnv(t)
	phone, pass := "+79990001122", "Str0ng-Pass-123"
	r := e.do("POST", "/auth/register", "", map[string]string{"phone": phone, "password": pass})
	if r.status != 201 || r.body["status"] != "ACTIVE" {
		t.Fatalf("register: %d %s", r.status, r.raw)
	}
	wantProblem(t, e.do("POST", "/auth/register", "", map[string]string{"phone": phone, "password": pass}), 409, "PHONE_ALREADY_REGISTERED")

	l := e.login(phone, pass)
	if l.status != 200 || l.body["tokenType"] != "Bearer" {
		t.Fatalf("login: %d %s", l.status, l.raw)
	}
	access, refresh := l.body["accessToken"].(string), l.body["refreshToken"].(string)

	me := e.do("GET", "/auth/me", access, nil)
	roles := me.body["roles"].([]any)
	if me.status != 200 || len(roles) != 1 || roles[0] != "CUSTOMER" || len(me.body["restaurants"].([]any)) != 0 {
		t.Fatalf("me: %d %s", me.status, me.raw)
	}
	in := e.do("POST", "/auth/introspect", "", map[string]string{"token": access})
	if in.status != 200 || in.body["active"] != true {
		t.Fatalf("introspect: %s", in.raw)
	}

	rf := e.do("POST", "/auth/refresh", "", map[string]string{"refreshToken": refresh})
	if rf.status != 200 {
		t.Fatalf("refresh: %s", rf.raw)
	}
	wantProblem(t, e.do("POST", "/auth/refresh", "", map[string]string{"refreshToken": refresh}), 401, "INVALID_REFRESH_TOKEN") // повтор
	lo := e.do("POST", "/auth/logout", "", map[string]string{"refreshToken": rf.body["refreshToken"].(string)})
	if lo.status != 200 || lo.body["message"] != "Logged out successfully" {
		t.Fatalf("logout: %s", lo.raw)
	}
	wantProblem(t, e.do("POST", "/auth/refresh", "", map[string]string{"refreshToken": rf.body["refreshToken"].(string)}), 401, "INVALID_REFRESH_TOKEN")
}

func TestErrorsAreProblemJSONWithCodes(t *testing.T) {
	e := newEnv(t)
	wantProblem(t, e.login("+79990000000", "wrong-password"), 401, "INVALID_CREDENTIALS")
	wantProblem(t, e.login("+79998887766", "whatever1"), 401, "INVALID_CREDENTIALS") // тот же ответ: нет user enumeration
	wantProblem(t, e.login("+79992222222", "blocked123"), 403, "USER_NOT_ACTIVE")
	wantProblem(t, e.do("POST", "/auth/register", "", map[string]string{"phone": "89991112233", "password": "123456"}), 400, "VALIDATION_FAILED")
	wantProblem(t, e.do("POST", "/auth/register", "", map[string]string{"phone": "+79991112233", "password": "123"}), 400, "VALIDATION_FAILED")
	wantProblem(t, e.do("POST", "/auth/register", "", "{not json"), 400, "INVALID_JSON")
	wantProblem(t, e.do("POST", "/auth/refresh", "", map[string]string{"refreshToken": " "}), 400, "VALIDATION_FAILED")
	wantProblem(t, e.do("POST", "/auth/introspect", "", map[string]string{"token": ""}), 400, "VALIDATION_FAILED")
	wantProblem(t, e.do("GET", "/auth/me", "", nil), 401, "INVALID_TOKEN")
	wantProblem(t, e.do("GET", "/auth/me", "garbage", nil), 401, "INVALID_TOKEN")
	if r := e.do("GET", "/auth/me", "", nil); !strings.Contains(r.header.Get("WWW-Authenticate"), "Bearer") {
		t.Fatalf("401 must carry WWW-Authenticate, got %v", r.header)
	}
	in := e.do("POST", "/auth/introspect", "", map[string]string{"token": "garbage"})
	if in.status != 200 || in.body["active"] != false || len(in.body) != 1 {
		t.Fatalf("garbage introspect must be {active:false}: %s", in.raw)
	}
}

func TestStaffTokenCarriesRolesAndRestaurants(t *testing.T) {
	e := newEnv(t)
	tok := e.accessToken("+79993333333", "staff123")
	v, err := authclient.NewJWKSVerifier(authclient.JWKSOptions{URL: e.srv.URL + "/auth/.well-known/jwks.json",
		Issuer: "restaurant-auth-service", Audience: "restaurant-api", Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	// Токен проверяется тем же кодом, что на gateway и в сервисах, — по JWKS самого auth.
	c, err := v.Verify(context.Background(), tok)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Roles) != 1 || c.Roles[0] != "STAFF" || len(c.Restaurants) != 1 || c.Restaurants[0] != r4 {
		t.Fatalf("claims = %+v", c)
	}
	me := e.do("GET", "/auth/me", tok, nil)
	if got := me.body["restaurants"].([]any); len(got) != 1 || got[0] != r4 {
		t.Fatalf("me.restaurants = %v", got)
	}
	// JWKS: публично, ключ без приватных частей
	jw := e.do("GET", "/auth/.well-known/jwks.json", "", nil)
	if jw.status != 200 || strings.Contains(jw.raw, `"d"`) || !strings.Contains(jw.header.Get("Cache-Control"), "max-age") {
		t.Fatalf("jwks: %d %v %s", jw.status, jw.header, jw.raw)
	}
}

func TestAuthorizeUsesLiveDatabase(t *testing.T) {
	e := newEnv(t)
	staff := e.accessToken("+79993333333", "staff123")
	owner := e.accessToken("+79995555555", "owner123")
	hq := e.accessToken("+79991111111", "admin123")
	customer := e.accessToken("+79990000000", "123456")

	type c struct {
		name   string
		token  string
		body   map[string]string
		allow  bool
		reason string
	}
	for _, tc := range []c{
		{"staff manages own restaurant orders", staff, map[string]string{"action": "order.manage", "restaurantId": r4}, true, ""},
		{"staff of another restaurant denied", staff, map[string]string{"action": "order.manage", "restaurantId": r1}, false, "NOT_BOUND_TO_RESTAURANT"},
		{"owner manages local promo", owner, map[string]string{"action": "promotion.manage", "scope": "local", "restaurantId": r4}, true, ""},
		{"owner cannot manage other restaurant", owner, map[string]string{"action": "promotion.manage", "scope": "local", "restaurantId": r1}, false, "NOT_BOUND_TO_RESTAURANT"},
		{"owner cannot widen to national", owner, map[string]string{"action": "promotion.manage", "scope": "national"}, false, "SCOPE_NOT_ALLOWED"},
		{"hq national", hq, map[string]string{"action": "promotion.manage", "scope": "national"}, true, ""},
		{"customer denied", customer, map[string]string{"action": "order.manage", "restaurantId": r4}, false, "ROLE_REQUIRED"},
	} {
		r := e.do("POST", "/auth/authorize", tc.token, tc.body)
		if r.status != 200 || r.body["allowed"] != tc.allow {
			t.Errorf("%s: %d %s", tc.name, r.status, r.raw)
			continue
		}
		if reason, has := r.body["reason"]; tc.allow == has || (has && reason != tc.reason) {
			t.Errorf("%s: reason = %v, want %q (%s)", tc.name, reason, tc.reason, r.raw)
		}
	}

	// Привязку сняли в БД — выданный токен ещё действителен (claims), но authorize видит это сразу.
	e.exec(`DELETE FROM user_restaurants WHERE user_id='00000000-0000-0000-0000-000000000004'`)
	r := e.do("POST", "/auth/authorize", staff, map[string]string{"action": "order.manage", "restaurantId": r4})
	if r.body["allowed"] != false || r.body["reason"] != "NOT_BOUND_TO_RESTAURANT" {
		t.Fatalf("revoked binding must take effect at once: %s", r.raw)
	}
	// Пользователя заблокировали: токен формально жив, introspect и authorize отказывают.
	e.exec(`UPDATE users SET status='BLOCKED' WHERE id='00000000-0000-0000-0000-000000000002'`)
	if in := e.do("POST", "/auth/introspect", "", map[string]string{"token": hq}); in.body["active"] != false {
		t.Fatalf("blocked user must be inactive: %s", in.raw)
	}
	if r := e.do("POST", "/auth/authorize", hq, map[string]string{"action": "promotion.manage", "scope": "national"}); r.body["allowed"] != false || r.body["reason"] != "NOT_ACTIVE" {
		t.Fatalf("blocked hq must be denied: %s", r.raw)
	}
}

func TestAuthorizeBadRequests(t *testing.T) {
	e := newEnv(t)
	hq := e.accessToken("+79991111111", "admin123")
	wantProblem(t, e.do("POST", "/auth/authorize", "", map[string]string{"action": "order.manage", "restaurantId": r4}), 401, "INVALID_TOKEN")
	wantProblem(t, e.do("POST", "/auth/authorize", "garbage", map[string]string{"action": "order.manage", "restaurantId": r4}), 401, "INVALID_TOKEN")
	wantProblem(t, e.do("POST", "/auth/authorize", hq, map[string]string{"action": "launch.rocket"}), 400, "UNKNOWN_ACTION")
	wantProblem(t, e.do("POST", "/auth/authorize", hq, map[string]string{"action": "order.manage"}), 400, "VALIDATION_FAILED")
	wantProblem(t, e.do("POST", "/auth/authorize", hq, map[string]string{"action": "order.manage", "restaurantId": "not-a-uuid"}), 400, "VALIDATION_FAILED")
	wantProblem(t, e.do("POST", "/auth/authorize", hq, map[string]string{"action": "promotion.manage"}), 400, "VALIDATION_FAILED")
}

func TestMetricsRoutesAreTemplates(t *testing.T) {
	e := newEnv(t)
	e.login("+79990000000", "wrong")
	m := e.do("GET", "/metrics", "", nil)
	if m.status != 200 || !strings.Contains(m.raw, `route="POST /auth/login"`) || !strings.Contains(m.raw, `service="auth"`) {
		t.Fatalf("metrics: %d %.300s", m.status, m.raw)
	}
}
