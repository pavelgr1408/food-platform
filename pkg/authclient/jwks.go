package authclient

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Claims — проверенные claims access-токена auth-service (RS256).
type Claims struct {
	Subject     string // userId (sub)
	Phone       string
	Roles       []string
	Restaurants []string
	ExpiresAt   time.Time
	TokenID     string // jti
}

// ErrInvalidToken — подпись, срок, издатель/аудитория или структура claims не прошли проверку.
var ErrInvalidToken = errors.New("invalid token")

// JWKSOptions — настройки локальной проверки JWT по JWKS (ADR-11).
type JWKSOptions struct {
	URL      string // например http://auth:8081/auth/.well-known/jwks.json
	Issuer   string
	Audience string
	// CacheTTL — через сколько набор считается устаревшим и перечитывается при очередной проверке
	// (по умолчанию 10 мин). Ошибка перечитывания не ломает проверку: служит прежний набор.
	CacheTTL time.Duration
	// MinRefresh — минимальный интервал между обращениями к JWKS (по умолчанию 10 с). Ограничивает
	// нагрузку на auth при токенах с неизвестным kid, в том числе поддельных.
	MinRefresh time.Duration
	Leeway     time.Duration // допуск рассинхронизации часов (по умолчанию 5 с)
	HTTP       *http.Client  // по умолчанию таймаут 3 с
	Log        *slog.Logger
	Now        func() time.Time // тесты
}

// JWKSVerifier проверяет access-токены локально, без сетевого вызова на каждый запрос.
type JWKSVerifier struct {
	o JWKSOptions

	mu          sync.RWMutex
	keys        map[string]*rsa.PublicKey
	fetchedAt   time.Time // последнее успешное чтение набора
	lastAttempt time.Time // последняя попытка чтения (успешная или нет)

	refreshMu sync.Mutex // сериализует чтение: одна выборка на всех ожидающих
}

// NewJWKSVerifier проверяет настройки. Сеть не трогает: auth может стартовать позже сервиса —
// ключи читаются лениво при первой проверке или явным Warmup.
func NewJWKSVerifier(o JWKSOptions) (*JWKSVerifier, error) {
	var errs []error
	if o.URL == "" {
		errs = append(errs, errors.New("JWKS URL is required"))
	}
	if o.Issuer == "" {
		errs = append(errs, errors.New("token issuer is required"))
	}
	if o.Audience == "" {
		errs = append(errs, errors.New("token audience is required"))
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	if o.CacheTTL <= 0 {
		o.CacheTTL = 10 * time.Minute
	}
	if o.MinRefresh <= 0 {
		o.MinRefresh = 10 * time.Second
	}
	if o.Leeway <= 0 {
		o.Leeway = 5 * time.Second
	}
	if o.HTTP == nil {
		o.HTTP = &http.Client{Timeout: 3 * time.Second}
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &JWKSVerifier{o: o, keys: map[string]*rsa.PublicKey{}}, nil
}

// Warmup читает набор ключей сразу (старт сервиса, проба готовности). Ошибка не фатальна для вызывающего.
func (v *JWKSVerifier) Warmup(ctx context.Context) error {
	v.refreshMu.Lock()
	defer v.refreshMu.Unlock()
	return v.fetch(ctx)
}

// Ready — есть ли хотя бы один ключ (для readiness).
func (v *JWKSVerifier) Ready() bool {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return len(v.keys) > 0
}

// Verify проверяет подпись (только RS256), срок, iss/aud и структуру claims.
func (v *JWKSVerifier) Verify(ctx context.Context, raw string) (Claims, error) {
	var c tokenClaims
	tok, err := jwt.ParseWithClaims(raw, &c, func(t *jwt.Token) (any, error) {
		kid, _ := t.Header["kid"].(string)
		if kid == "" {
			return nil, errors.New("token has no kid")
		}
		return v.key(ctx, kid)
	},
		jwt.WithValidMethods([]string{"RS256"}),
		jwt.WithIssuer(v.o.Issuer), jwt.WithAudience(v.o.Audience),
		jwt.WithExpirationRequired(), jwt.WithIssuedAt(), jwt.WithLeeway(v.o.Leeway),
		jwt.WithTimeFunc(v.o.Now))
	if err != nil || !tok.Valid {
		return Claims{}, fmt.Errorf("%w: %v", ErrInvalidToken, err)
	}
	if c.Subject == "" || len(c.Roles) == 0 {
		return Claims{}, fmt.Errorf("%w: subject and roles are required", ErrInvalidToken)
	}
	for _, r := range c.Roles {
		if !slices.Contains(knownRoles, r) {
			return Claims{}, fmt.Errorf("%w: unknown role %q", ErrInvalidToken, r)
		}
	}
	return Claims{Subject: c.Subject, Phone: c.Phone, Roles: c.Roles, Restaurants: c.Restaurants,
		ExpiresAt: c.ExpiresAt.Time, TokenID: c.ID}, nil
}

var knownRoles = []string{RoleCustomer, RoleStaff, RoleFranchiseOwner, RoleHQStaff}

type tokenClaims struct {
	Phone       string   `json:"phone"`
	Roles       []string `json:"roles"`
	Restaurants []string `json:"restaurants"`
	jwt.RegisteredClaims
}

// key возвращает ключ по kid. Набор перечитывается: (а) если он старше CacheTTL — при ошибке служит
// прежний; (б) если kid неизвестен — не чаще MinRefresh (ротация ключа без простоя).
func (v *JWKSVerifier) key(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	now := v.o.Now()
	k, stale := v.lookup(kid, now)
	if k != nil && !stale {
		return k, nil
	}
	v.refreshMu.Lock()
	defer v.refreshMu.Unlock()
	// Пока ждали очередь, набор мог обновить другой запрос.
	if k2, stale2 := v.lookup(kid, v.o.Now()); k2 != nil && !stale2 {
		return k2, nil
	}
	v.mu.RLock()
	since := v.o.Now().Sub(v.lastAttempt)
	v.mu.RUnlock()
	if since >= v.o.MinRefresh {
		if err := v.fetch(ctx); err != nil {
			v.o.Log.WarnContext(ctx, "jwks refresh failed", "error", err, "url", v.o.URL)
		}
	}
	if k, _ := v.lookup(kid, v.o.Now()); k != nil {
		return k, nil // свежий либо (при недоступном auth) устаревший, но известный ключ
	}
	return nil, fmt.Errorf("unknown signing key %q", kid)
}

func (v *JWKSVerifier) lookup(kid string, now time.Time) (key *rsa.PublicKey, stale bool) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	k := v.keys[kid]
	return k, now.Sub(v.fetchedAt) > v.o.CacheTTL
}

// fetch читает набор; вызывать под refreshMu. Пустой или нечитаемый набор прежний кэш не затирает.
func (v *JWKSVerifier) fetch(ctx context.Context) error {
	v.mu.Lock()
	v.lastAttempt = v.o.Now()
	v.mu.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.o.URL, nil)
	if err != nil {
		return err
	}
	resp, err := v.o.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("jwks endpoint returned %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	keys, err := parseJWKS(body, v.o.Log)
	if err != nil {
		return err
	}
	v.mu.Lock()
	v.keys, v.fetchedAt = keys, v.o.Now()
	v.mu.Unlock()
	return nil
}

type jwkSet struct {
	Keys []struct {
		Kty string `json:"kty"`
		Kid string `json:"kid"`
		Use string `json:"use"`
		Alg string `json:"alg"`
		N   string `json:"n"`
		E   string `json:"e"`
	} `json:"keys"`
}

func parseJWKS(b []byte, log *slog.Logger) (map[string]*rsa.PublicKey, error) {
	var set jwkSet
	if err := json.Unmarshal(b, &set); err != nil {
		return nil, fmt.Errorf("decode jwks: %w", err)
	}
	out := map[string]*rsa.PublicKey{}
	for _, k := range set.Keys {
		if k.Kty != "RSA" || k.Kid == "" || (k.Alg != "" && k.Alg != "RS256") || (k.Use != "" && k.Use != "sig") {
			log.Warn("jwks key skipped", "kid", k.Kid, "kty", k.Kty, "alg", k.Alg)
			continue
		}
		n, err1 := base64.RawURLEncoding.DecodeString(k.N)
		e, err2 := base64.RawURLEncoding.DecodeString(k.E)
		if err1 != nil || err2 != nil || len(n) < 256 || len(e) == 0 || len(e) > 4 {
			log.Warn("jwks key malformed", "kid", k.Kid)
			continue
		}
		out[k.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
	}
	if len(out) == 0 {
		return nil, errors.New("jwks contains no usable RSA keys")
	}
	return out, nil
}

// jwksAuthenticator — Authenticator поверх JWKSVerifier: нет заголовка Authorization → гость,
// есть, но недействителен → ErrUnauthenticated. Заголовки X-User-* полностью игнорируются.
type jwksAuthenticator struct{ v *JWKSVerifier }

// Authenticator возвращает Authenticator для сервисов (режим AUTH_MODE=jwks).
func (v *JWKSVerifier) Authenticator() Authenticator { return jwksAuthenticator{v} }

// Authenticate реализует Authenticator.
func (a jwksAuthenticator) Authenticate(r *http.Request) (Principal, error) {
	h := strings.TrimSpace(r.Header.Get("Authorization"))
	if h == "" {
		return Principal{Guest: true}, nil
	}
	raw, ok := BearerToken(h)
	if !ok {
		return Principal{}, ErrUnauthenticated
	}
	c, err := a.v.Verify(r.Context(), raw)
	if err != nil {
		return Principal{}, ErrUnauthenticated
	}
	return Principal{UserID: c.Subject, Roles: c.Roles, Restaurants: c.Restaurants}, nil
}

// BearerToken извлекает токен из значения заголовка Authorization (схема без учёта регистра).
func BearerToken(header string) (string, bool) {
	scheme, tok, ok := strings.Cut(strings.TrimSpace(header), " ")
	tok = strings.TrimSpace(tok)
	if !ok || !strings.EqualFold(scheme, "Bearer") || tok == "" {
		return "", false
	}
	return tok, true
}
