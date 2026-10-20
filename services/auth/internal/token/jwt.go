package token

import (
	"crypto/rsa"
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"
	"os"
	"sort"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/pavelgr1408/food-platform/services/auth/internal/domain"
)

// Config — параметры выпуска и проверки access-токенов.
type Config struct {
	PrivatePath string
	PublicPath  string
	Issuer      string
	Audience    string
	KeyID       string
	TTL         time.Duration
	// Previous — публичные ключи предыдущих подписей (kid → путь к PEM). Остаются в JWKS и принимаются при
	// проверке, пока живут выданные ими токены, — ротация без простоя: новый ключ подписывает, потребители
	// (gateway, сервисы) подхватывают его по неизвестному kid, а токены старого ключа дорабатывают TTL.
	Previous map[string]string
}

type Manager struct {
	privateKey *rsa.PrivateKey
	keys       map[string]*rsa.PublicKey // kid → публичный ключ (текущий и предыдущие)
	issuer     string
	audience   string
	keyID      string
	accessTTL  time.Duration
	now        func() time.Time
}

type AccessClaims struct {
	Phone       string            `json:"phone"`
	Roles       []domain.UserRole `json:"roles"`
	Restaurants []string          `json:"restaurants"`
	jwt.RegisteredClaims
}

func New(c Config) (*Manager, error) {
	privatePEM, err := os.ReadFile(c.PrivatePath)
	if err != nil {
		return nil, fmt.Errorf("read JWT private key: %w", err)
	}
	privateKey, err := jwt.ParseRSAPrivateKeyFromPEM(privatePEM)
	if err != nil {
		return nil, fmt.Errorf("parse JWT private key: %w", err)
	}
	publicPEM, err := os.ReadFile(c.PublicPath)
	if err != nil {
		return nil, fmt.Errorf("read JWT public key: %w", err)
	}
	publicKey, err := jwt.ParseRSAPublicKeyFromPEM(publicPEM)
	if err != nil {
		return nil, fmt.Errorf("parse JWT public key: %w", err)
	}
	if privateKey.PublicKey.N.Cmp(publicKey.N) != 0 || privateKey.PublicKey.E != publicKey.E {
		return nil, errors.New("JWT private and public keys do not match")
	}
	keys := map[string]*rsa.PublicKey{c.KeyID: publicKey}
	for kid, path := range c.Previous {
		if kid == "" || kid == c.KeyID {
			return nil, fmt.Errorf("previous JWT key id %q is empty or equals the current one", kid)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read previous JWT public key %s: %w", kid, err)
		}
		pk, err := jwt.ParseRSAPublicKeyFromPEM(b)
		if err != nil {
			return nil, fmt.Errorf("parse previous JWT public key %s: %w", kid, err)
		}
		keys[kid] = pk
	}
	return &Manager{privateKey: privateKey, keys: keys, issuer: c.Issuer, audience: c.Audience, keyID: c.KeyID, accessTTL: c.TTL, now: time.Now}, nil
}

func (m *Manager) Issue(user domain.User) (string, error) {
	now := m.now().UTC()
	roles := append([]domain.UserRole(nil), user.Roles...)
	sort.Slice(roles, func(i, j int) bool { return roles[i] < roles[j] })
	restaurants := make([]string, 0, len(user.Restaurants))
	for _, id := range user.Restaurants {
		restaurants = append(restaurants, id.String())
	}
	sort.Strings(restaurants)
	claims := AccessClaims{Phone: user.Phone, Roles: roles, Restaurants: restaurants, RegisteredClaims: jwt.RegisteredClaims{
		Issuer: m.issuer, Subject: user.ID.String(), Audience: jwt.ClaimStrings{m.audience},
		ExpiresAt: jwt.NewNumericDate(now.Add(m.accessTTL)), IssuedAt: jwt.NewNumericDate(now), ID: uuid.NewString(),
	}}
	t := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	t.Header["kid"] = m.keyID
	return t.SignedString(m.privateKey)
}

// Validate проверяет подпись (текущим или предыдущим ключом по kid), срок, iss/aud и роли.
func (m *Manager) Validate(raw string) (uuid.UUID, error) {
	claims := &AccessClaims{}
	t, err := jwt.ParseWithClaims(raw, claims, func(t *jwt.Token) (any, error) {
		if t.Method != jwt.SigningMethodRS256 {
			return nil, errors.New("unexpected signing method")
		}
		kid, _ := t.Header["kid"].(string)
		if k, ok := m.keys[kid]; ok {
			return k, nil
		}
		return nil, errors.New("unknown key id")
	}, jwt.WithIssuer(m.issuer), jwt.WithAudience(m.audience), jwt.WithExpirationRequired(), jwt.WithIssuedAt(),
		jwt.WithValidMethods([]string{"RS256"}), jwt.WithTimeFunc(m.now))
	if err != nil || !t.Valid || claims.Phone == "" || len(claims.Roles) == 0 {
		return uuid.Nil, domain.ErrInvalidAccessToken
	}
	id, err := uuid.Parse(claims.Subject)
	if err != nil {
		return uuid.Nil, domain.ErrInvalidAccessToken
	}
	for _, role := range claims.Roles {
		if !role.Valid() {
			return uuid.Nil, domain.ErrInvalidAccessToken
		}
	}
	return id, nil
}

func (m *Manager) ExpiresIn() int64 { return int64(m.accessTTL.Seconds()) }

// JWKS — текущий ключ первым, затем предыдущие по kid.
func (m *Manager) JWKS() map[string]any {
	jwk := func(kid string, pk *rsa.PublicKey) map[string]string {
		return map[string]string{
			"kty": "RSA", "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pk.E)).Bytes()), "use": "sig", "kid": kid,
			"alg": "RS256", "n": base64.RawURLEncoding.EncodeToString(pk.N.Bytes()),
		}
	}
	out := []map[string]string{jwk(m.keyID, m.keys[m.keyID])}
	var prev []string
	for kid := range m.keys {
		if kid != m.keyID {
			prev = append(prev, kid)
		}
	}
	sort.Strings(prev)
	for _, kid := range prev {
		out = append(out, jwk(kid, m.keys[kid]))
	}
	return map[string]any{"keys": out}
}
