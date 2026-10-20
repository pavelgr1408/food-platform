package token

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/pavelgr1408/food-platform/services/auth/internal/domain"
)

// keyFiles пишет пару ключей в t.TempDir() и возвращает пути.
func keyFiles(t *testing.T, name string) (priv, pub string, key *rsa.PrivateKey) {
	t.Helper()
	dir := t.TempDir()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	priv, pub = filepath.Join(dir, name+"-private.pem"), filepath.Join(dir, name+"-public.pem")
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(priv, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pub, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	return priv, pub, key
}

func cfg(priv, pub, kid string) Config {
	return Config{PrivatePath: priv, PublicPath: pub, Issuer: "restaurant-auth-service", Audience: "restaurant-api", KeyID: kid, TTL: 15 * time.Minute}
}

func TestIssueValidateAndJWKS(t *testing.T) {
	priv, pub, _ := keyFiles(t, "a")
	m, err := New(cfg(priv, pub, "test-key"))
	if err != nil {
		t.Fatal(err)
	}
	id, r4 := uuid.New(), uuid.MustParse("a1000000-0000-4000-8000-000000000004")
	raw, err := m.Issue(domain.User{ID: id, Phone: "+79990000000", Roles: []domain.UserRole{domain.RoleStaff}, Restaurants: []uuid.UUID{r4}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := m.Validate(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got != id {
		t.Fatalf("id=%s, want %s", got, id)
	}
	// в токене есть restaurants[] (gateway и сервисы авторизуют без сетевого вызова)
	claims := &AccessClaims{}
	if _, _, err := jwt.NewParser().ParseUnverified(raw, claims); err != nil {
		t.Fatal(err)
	}
	if len(claims.Restaurants) != 1 || claims.Restaurants[0] != r4.String() {
		t.Fatalf("restaurants claim = %v", claims.Restaurants)
	}
	keys := m.JWKS()["keys"].([]map[string]string)
	if len(keys) != 1 || keys[0]["kid"] != "test-key" || keys[0]["n"] == "" || keys[0]["e"] == "" {
		t.Fatalf("unexpected JWKS: %#v", keys)
	}
	if _, ok := keys[0]["d"]; ok {
		t.Fatal("private exponent leaked")
	}
}

func TestEmptyRestaurantsIsArrayNotNull(t *testing.T) {
	priv, pub, _ := keyFiles(t, "a")
	m, _ := New(cfg(priv, pub, "k"))
	raw, _ := m.Issue(domain.User{ID: uuid.New(), Phone: "+79990000000", Roles: []domain.UserRole{domain.RoleCustomer}})
	claims := &AccessClaims{}
	_, _, _ = jwt.NewParser().ParseUnverified(raw, claims)
	if claims.Restaurants == nil {
		t.Fatal("restaurants must be [] for users without binding")
	}
}

func TestRejectsWrongAudienceAndLegacyRoles(t *testing.T) {
	priv, pub, key := keyFiles(t, "a")
	c := cfg(priv, pub, "key")
	c.Issuer, c.Audience = "issuer", "audience-a"
	issuer, _ := New(c)
	c.Audience = "audience-b"
	verifier, _ := New(c)
	raw, _ := issuer.Issue(domain.User{ID: uuid.New(), Phone: "+79990000000", Roles: []domain.UserRole{domain.RoleCustomer}})
	if _, err := verifier.Validate(raw); err == nil {
		t.Fatal("expected wrong audience to be rejected")
	}
	// токен со старой ролью ADMIN (выдан до S5) больше не принимается
	legacy := jwt.NewWithClaims(jwt.SigningMethodRS256, AccessClaims{Phone: "+79990000000", Roles: []domain.UserRole{"ADMIN"},
		RegisteredClaims: jwt.RegisteredClaims{Issuer: "issuer", Subject: uuid.NewString(), Audience: jwt.ClaimStrings{"audience-a"},
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Minute)), IssuedAt: jwt.NewNumericDate(time.Now())}})
	legacy.Header["kid"] = "key"
	s, _ := legacy.SignedString(key)
	if _, err := issuer.Validate(s); err == nil {
		t.Fatal("legacy role must be rejected")
	}
}

func TestRotationKeepsPreviousKeyDuringOverlap(t *testing.T) {
	oldPriv, oldPub, _ := keyFiles(t, "old")
	newPriv, newPub, _ := keyFiles(t, "new")
	oldM, _ := New(cfg(oldPriv, oldPub, "k-2026-09"))
	u := domain.User{ID: uuid.New(), Phone: "+79990000000", Roles: []domain.UserRole{domain.RoleCustomer}}
	oldTok, _ := oldM.Issue(u)

	c := cfg(newPriv, newPub, "k-2026-10")
	c.Previous = map[string]string{"k-2026-09": oldPub}
	m, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Validate(oldTok); err != nil {
		t.Fatalf("token of the previous key must stay valid during overlap: %v", err)
	}
	newTok, _ := m.Issue(u)
	if _, err := m.Validate(newTok); err != nil {
		t.Fatal(err)
	}
	keys := m.JWKS()["keys"].([]map[string]string)
	if len(keys) != 2 || keys[0]["kid"] != "k-2026-10" || keys[1]["kid"] != "k-2026-09" {
		t.Fatalf("JWKS must list current key first, then previous: %#v", keys)
	}
	// после вывода старого ключа его токены отвергаются
	m2, _ := New(cfg(newPriv, newPub, "k-2026-10"))
	if _, err := m2.Validate(oldTok); err == nil {
		t.Fatal("retired key must be rejected")
	}
	// некорректная конфигурация
	c.Previous = map[string]string{"k-2026-10": oldPub}
	if _, err := New(c); err == nil {
		t.Fatal("previous kid equal to current must be rejected")
	}
}
