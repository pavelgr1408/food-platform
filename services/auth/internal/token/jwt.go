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

	"github.com/example/restaurant-auth-service/internal/domain"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

type Manager struct {
	privateKey *rsa.PrivateKey
	publicKey  *rsa.PublicKey
	issuer     string
	audience   string
	keyID      string
	accessTTL  time.Duration
	now        func() time.Time
}

type AccessClaims struct {
	Phone string            `json:"phone"`
	Roles []domain.UserRole `json:"roles"`
	jwt.RegisteredClaims
}

func New(privatePath, publicPath, issuer, audience, keyID string, ttl time.Duration) (*Manager, error) {
	privatePEM, err := os.ReadFile(privatePath)
	if err != nil {
		return nil, fmt.Errorf("read JWT private key: %w", err)
	}
	privateKey, err := jwt.ParseRSAPrivateKeyFromPEM(privatePEM)
	if err != nil {
		return nil, fmt.Errorf("parse JWT private key: %w", err)
	}
	publicPEM, err := os.ReadFile(publicPath)
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
	return &Manager{privateKey: privateKey, publicKey: publicKey, issuer: issuer, audience: audience, keyID: keyID, accessTTL: ttl, now: time.Now}, nil
}

func (m *Manager) Issue(user domain.User) (string, error) {
	now := m.now().UTC()
	roles := append([]domain.UserRole(nil), user.Roles...)
	sort.Slice(roles, func(i, j int) bool { return roles[i] < roles[j] })
	claims := AccessClaims{Phone: user.Phone, Roles: roles, RegisteredClaims: jwt.RegisteredClaims{
		Issuer: m.issuer, Subject: user.ID.String(), Audience: jwt.ClaimStrings{m.audience},
		ExpiresAt: jwt.NewNumericDate(now.Add(m.accessTTL)), IssuedAt: jwt.NewNumericDate(now), ID: uuid.NewString(),
	}}
	t := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	t.Header["kid"] = m.keyID
	return t.SignedString(m.privateKey)
}

func (m *Manager) Validate(raw string) (uuid.UUID, error) {
	claims := &AccessClaims{}
	t, err := jwt.ParseWithClaims(raw, claims, func(t *jwt.Token) (any, error) {
		if t.Method != jwt.SigningMethodRS256 {
			return nil, errors.New("unexpected signing method")
		}
		return m.publicKey, nil
	}, jwt.WithIssuer(m.issuer), jwt.WithAudience(m.audience), jwt.WithExpirationRequired(), jwt.WithIssuedAt(), jwt.WithValidMethods([]string{"RS256"}))
	if err != nil || !t.Valid || claims.Phone == "" || len(claims.Roles) == 0 {
		return uuid.Nil, domain.ErrInvalidAccessToken
	}
	id, err := uuid.Parse(claims.Subject)
	if err != nil {
		return uuid.Nil, domain.ErrInvalidAccessToken
	}
	for _, role := range claims.Roles {
		switch role {
		case domain.RoleCustomer, domain.RoleAdmin, domain.RoleCourier, domain.RoleKitchen:
		default:
			return uuid.Nil, domain.ErrInvalidAccessToken
		}
	}
	return id, nil
}

func (m *Manager) ExpiresIn() int64 { return int64(m.accessTTL.Seconds()) }

func (m *Manager) JWKS() map[string]any {
	e := big.NewInt(int64(m.publicKey.E)).Bytes()
	return map[string]any{"keys": []map[string]string{{
		"kty": "RSA", "e": base64.RawURLEncoding.EncodeToString(e), "use": "sig", "kid": m.keyID,
		"alg": "RS256", "n": base64.RawURLEncoding.EncodeToString(m.publicKey.N.Bytes()),
	}}}
}
