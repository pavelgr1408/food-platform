package authclient

import (
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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	testIssuer   = "restaurant-auth-service"
	testAudience = "restaurant-api"
)

type testKey struct {
	kid string
	key *rsa.PrivateKey
}

func newTestKey(t testing.TB, kid string) testKey {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return testKey{kid: kid, key: k}
}

func (k testKey) jwk() map[string]string {
	return map[string]string{"kty": "RSA", "use": "sig", "alg": "RS256", "kid": k.kid,
		"n": base64.RawURLEncoding.EncodeToString(k.key.N.Bytes()),
		"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(k.key.E)).Bytes())}
}

type claimOpt func(*tokenClaims)

func (k testKey) token(t testing.TB, now time.Time, opts ...claimOpt) string {
	t.Helper()
	c := tokenClaims{Phone: "+79990000000", Roles: []string{RoleCustomer}, RegisteredClaims: jwt.RegisteredClaims{
		Issuer: testIssuer, Subject: "11111111-1111-4111-8111-111111111111", Audience: jwt.ClaimStrings{testAudience},
		ExpiresAt: jwt.NewNumericDate(now.Add(15 * time.Minute)), IssuedAt: jwt.NewNumericDate(now), ID: "jti-1"}}
	for _, o := range opts {
		o(&c)
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, c)
	if k.kid != "" {
		tok.Header["kid"] = k.kid
	}
	s, err := tok.SignedString(k.key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// jwksServer — управляемый «auth»: набор ключей, режим отказа, счётчик обращений.
type jwksServer struct {
	*httptest.Server
	mu    sync.Mutex
	keys  []testKey
	fail  bool
	hits  atomic.Int32
	empty bool
}

func newJWKSServer(t testing.TB, keys ...testKey) *jwksServer {
	s := &jwksServer{keys: keys}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.hits.Add(1)
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.fail {
			http.Error(w, "down", http.StatusInternalServerError)
			return
		}
		list := []map[string]string{}
		if !s.empty {
			for _, k := range s.keys {
				list = append(list, k.jwk())
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": list})
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *jwksServer) set(f func(*jwksServer)) { s.mu.Lock(); f(s); s.mu.Unlock() }

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) Add(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }
func newClock() *fakeClock               { return &fakeClock{t: time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)} }
func quiet() *slog.Logger                { return slog.New(slog.NewTextHandler(io.Discard, nil)) }
func verifier(t testing.TB, srv *jwksServer, clk *fakeClock) *JWKSVerifier {
	t.Helper()
	v, err := NewJWKSVerifier(JWKSOptions{URL: srv.URL, Issuer: testIssuer, Audience: testAudience,
		CacheTTL: time.Minute, MinRefresh: 10 * time.Second, Now: clk.Now, Log: quiet()})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func authReq(token string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/x", nil)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	return r
}

func TestVerifyValidTokenAndPrincipal(t *testing.T) {
	k := newTestKey(t, "k1")
	srv := newJWKSServer(t, k)
	clk := newClock()
	v := verifier(t, srv, clk)
	tok := k.token(t, clk.Now(), func(c *tokenClaims) {
		c.Roles = []string{RoleStaff}
		c.Restaurants = []string{"a1000000-0000-4000-8000-000000000004"}
	})
	p, err := v.Authenticator().Authenticate(authReq(tok))
	if err != nil {
		t.Fatal(err)
	}
	if p.Guest || p.UserID != "11111111-1111-4111-8111-111111111111" || !p.HasRole(RoleStaff) ||
		!p.CanAccessRestaurant("a1000000-0000-4000-8000-000000000004") || p.CanAccessRestaurant("other") {
		t.Fatalf("unexpected principal %+v", p)
	}
	// последующие проверки не ходят в сеть
	for i := 0; i < 5; i++ {
		if _, err := v.Verify(context.Background(), tok); err != nil {
			t.Fatal(err)
		}
	}
	if srv.hits.Load() != 1 {
		t.Fatalf("jwks fetched %d times, want 1 (local verification)", srv.hits.Load())
	}
}

func TestGuestAndDevHeadersAreIgnored(t *testing.T) {
	k := newTestKey(t, "k1")
	srv := newJWKSServer(t, k)
	v := verifier(t, srv, newClock())
	a := v.Authenticator()

	p, err := a.Authenticate(authReq(""))
	if err != nil || !p.Guest {
		t.Fatalf("no token must be guest: %+v %v", p, err)
	}
	// Клиент подсовывает dev-заголовки: в режиме jwks они не дают личности.
	r := authReq("")
	r.Header.Set(HeaderUserID, "attacker")
	r.Header.Set(HeaderUserRoles, RoleHQStaff)
	r.Header.Set(HeaderRestaurants, "x")
	p, err = a.Authenticate(r)
	if err != nil || !p.Guest || p.UserID != "" || len(p.Roles) != 0 {
		t.Fatalf("dev headers must be ignored in jwks mode: %+v %v", p, err)
	}
	// А с битым токеном — 401, даже если заголовки «красивые».
	r = authReq("garbage")
	r.Header.Set(HeaderUserID, "attacker")
	if _, err := a.Authenticate(r); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("garbage token: %v", err)
	}
	r = httptest.NewRequest(http.MethodGet, "/x", nil)
	r.Header.Set("Authorization", "Basic abc")
	if _, err := a.Authenticate(r); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("non-bearer scheme must be rejected: %v", err)
	}
}

func TestRejectedTokens(t *testing.T) {
	k := newTestKey(t, "k1")
	other := newTestKey(t, "k1") // тот же kid, другой ключ
	srv := newJWKSServer(t, k)
	clk := newClock()
	v := verifier(t, srv, clk)
	now := clk.Now()
	cases := map[string]string{
		"expired":        k.token(t, now, func(c *tokenClaims) { c.ExpiresAt = jwt.NewNumericDate(now.Add(-time.Hour)) }),
		"wrong issuer":   k.token(t, now, func(c *tokenClaims) { c.Issuer = "evil" }),
		"wrong audience": k.token(t, now, func(c *tokenClaims) { c.Audience = jwt.ClaimStrings{"other"} }),
		"no expiration":  k.token(t, now, func(c *tokenClaims) { c.ExpiresAt = nil }),
		"future iat":     k.token(t, now, func(c *tokenClaims) { c.IssuedAt = jwt.NewNumericDate(now.Add(time.Hour)) }),
		"no subject":     k.token(t, now, func(c *tokenClaims) { c.Subject = "" }),
		"no roles":       k.token(t, now, func(c *tokenClaims) { c.Roles = nil }),
		"unknown role":   k.token(t, now, func(c *tokenClaims) { c.Roles = []string{"ADMIN"} }),
		"foreign key":    other.token(t, now),
		"no kid":         testKey{key: k.key}.token(t, now),
		"unknown kid":    testKey{kid: "nope", key: k.key}.token(t, now),
	}
	// HS256, подписанный публичным ключом как секретом (algorithm confusion), и alg=none.
	hs := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"sub": "x", "roles": []string{RoleHQStaff}, "iss": testIssuer, "aud": testAudience, "exp": now.Add(time.Hour).Unix()})
	hs.Header["kid"] = "k1"
	cases["hs256 confusion"], _ = hs.SignedString(k.key.N.Bytes())
	none := jwt.NewWithClaims(jwt.SigningMethodNone, jwt.MapClaims{"sub": "x", "roles": []string{RoleHQStaff}, "iss": testIssuer, "aud": testAudience, "exp": now.Add(time.Hour).Unix()})
	cases["alg none"], _ = none.SignedString(jwt.UnsafeAllowNoneSignatureType)
	for name, tok := range cases {
		if _, err := v.Verify(context.Background(), tok); !errors.Is(err, ErrInvalidToken) {
			t.Errorf("%s: want ErrInvalidToken, got %v", name, err)
		}
	}
	// допуск рассинхронизации: истёк 3 с назад — ещё принимается (leeway 5 с)
	soon := k.token(t, now, func(c *tokenClaims) { c.ExpiresAt = jwt.NewNumericDate(now.Add(-3 * time.Second)) })
	if _, err := v.Verify(context.Background(), soon); err != nil {
		t.Errorf("leeway: %v", err)
	}
}

func TestKeyRotationWithoutRestart(t *testing.T) {
	oldK, newK := newTestKey(t, "k-old"), newTestKey(t, "k-new")
	srv := newJWKSServer(t, oldK)
	clk := newClock()
	v := verifier(t, srv, clk)
	ctx := context.Background()

	if _, err := v.Verify(ctx, oldK.token(t, clk.Now())); err != nil {
		t.Fatal(err)
	}
	// auth выкатил новый ключ: JWKS = [новый, старый]; сервис ничего не знает про kid k-new
	srv.set(func(s *jwksServer) { s.keys = []testKey{newK, oldK} })
	clk.Add(11 * time.Second) // прошёл MinRefresh
	if _, err := v.Verify(ctx, newK.token(t, clk.Now())); err != nil {
		t.Fatalf("token signed by the new key must pass after rotation: %v", err)
	}
	if _, err := v.Verify(ctx, oldK.token(t, clk.Now())); err != nil {
		t.Fatalf("tokens of the previous key stay valid during overlap: %v", err)
	}
	// старый ключ выведен из набора — после очередного обновления его токены отвергаются
	srv.set(func(s *jwksServer) { s.keys = []testKey{newK} })
	clk.Add(2 * time.Minute) // набор устарел (CacheTTL 1 мин)
	if _, err := v.Verify(ctx, newK.token(t, clk.Now())); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Verify(ctx, oldK.token(t, clk.Now())); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("retired key must be rejected, got %v", err)
	}
}

func TestUnknownKidRefreshIsRateLimited(t *testing.T) {
	k := newTestKey(t, "k1")
	srv := newJWKSServer(t, k)
	clk := newClock()
	v := verifier(t, srv, clk)
	forged := testKey{kid: "random", key: k.key}
	for i := 0; i < 50; i++ {
		_, _ = v.Verify(context.Background(), forged.token(t, clk.Now()))
	}
	if h := srv.hits.Load(); h != 1 {
		t.Fatalf("50 tokens with unknown kid caused %d JWKS fetches, want 1 (MinRefresh)", h)
	}
	clk.Add(11 * time.Second)
	_, _ = v.Verify(context.Background(), forged.token(t, clk.Now()))
	if h := srv.hits.Load(); h != 2 {
		t.Fatalf("after MinRefresh one more fetch expected, got %d", h)
	}
}

func TestStaleKeysServeWhileAuthIsDown(t *testing.T) {
	k := newTestKey(t, "k1")
	srv := newJWKSServer(t, k)
	clk := newClock()
	v := verifier(t, srv, clk)
	if _, err := v.Verify(context.Background(), k.token(t, clk.Now())); err != nil {
		t.Fatal(err)
	}
	srv.set(func(s *jwksServer) { s.fail = true })
	clk.Add(30 * time.Minute) // кэш давно устарел, auth лежит
	tok := k.token(t, clk.Now())
	for i := 0; i < 3; i++ {
		if _, err := v.Verify(context.Background(), tok); err != nil {
			t.Fatalf("known key must keep working while auth is down: %v", err)
		}
		clk.Add(15 * time.Second)
	}
	// пустой набор не затирает рабочий кэш
	srv.set(func(s *jwksServer) { s.fail = false; s.empty = true })
	clk.Add(time.Minute)
	if _, err := v.Verify(context.Background(), k.token(t, clk.Now())); err != nil {
		t.Fatalf("empty jwks must not wipe cached keys: %v", err)
	}
}

func TestLazyStartWhenAuthIsLate(t *testing.T) {
	k := newTestKey(t, "k1")
	srv := newJWKSServer(t, k)
	srv.set(func(s *jwksServer) { s.fail = true })
	clk := newClock()
	v := verifier(t, srv, clk)
	if err := v.Warmup(context.Background()); err == nil || v.Ready() {
		t.Fatalf("warmup against a down auth must fail without keys, err=%v ready=%v", err, v.Ready())
	}
	tok := k.token(t, clk.Now())
	if _, err := v.Verify(context.Background(), tok); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("no keys yet: %v", err)
	}
	srv.set(func(s *jwksServer) { s.fail = false })
	clk.Add(11 * time.Second)
	if _, err := v.Verify(context.Background(), tok); err != nil {
		t.Fatalf("keys must be loaded once auth is back: %v", err)
	}
	if !v.Ready() {
		t.Fatal("verifier must be ready")
	}
}

func TestNewFromOptions(t *testing.T) {
	log := quiet()
	if _, err := New(Options{Mode: "jwks", Env: "prod"}, log); err == nil {
		t.Fatal("jwks without URL/issuer must fail")
	}
	if _, err := New(Options{Mode: "dev", Env: "prod"}, log); err == nil {
		t.Fatal("dev must be forbidden in prod")
	}
	if _, err := New(Options{Mode: "nope"}, log); err == nil {
		t.Fatal("unknown mode must fail")
	}
	k := newTestKey(t, "k1")
	srv := newJWKSServer(t, k)
	a, err := New(Options{Mode: "jwks", Env: "prod", JWKSURL: srv.URL, Issuer: testIssuer, Audience: testAudience}, log)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Authenticate(authReq(k.token(t, time.Now()))); err != nil {
		t.Fatal(err)
	}
	// auth недоступен при старте — сервис всё равно создаётся (ленивое чтение)
	down := newJWKSServer(t, k)
	down.set(func(s *jwksServer) { s.fail = true })
	if _, err := New(Options{Mode: "jwks", JWKSURL: down.URL, Issuer: testIssuer, Audience: testAudience}, log); err != nil {
		t.Fatalf("late auth must not block startup: %v", err)
	}
}
