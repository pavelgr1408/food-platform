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

	"github.com/example/restaurant-auth-service/internal/domain"
	"github.com/google/uuid"
)

func TestIssueValidateAndJWKS(t *testing.T) {
	dir := t.TempDir()
	privatePath := filepath.Join(dir, "private.pem")
	publicPath := filepath.Join(dir, "public.pem")
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	privatePEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	publicDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	publicPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicDER})
	if err := os.WriteFile(privatePath, privatePEM, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(publicPath, publicPEM, 0600); err != nil {
		t.Fatal(err)
	}
	m, err := New(privatePath, publicPath, "restaurant-auth-service", "restaurant-api", "test-key", 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	id := uuid.New()
	raw, err := m.Issue(domain.User{ID: id, Phone: "+79990000000", Roles: []domain.UserRole{domain.RoleCustomer}})
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
	keys := m.JWKS()["keys"].([]map[string]string)
	if len(keys) != 1 || keys[0]["kid"] != "test-key" || keys[0]["n"] == "" || keys[0]["e"] == "" {
		t.Fatalf("unexpected JWKS: %#v", keys)
	}
	if _, ok := keys[0]["d"]; ok {
		t.Fatal("private exponent leaked")
	}
}

func TestRejectsWrongAudience(t *testing.T) {
	dir := t.TempDir()
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	privatePath := filepath.Join(dir, "private.pem")
	publicPath := filepath.Join(dir, "public.pem")
	_ = os.WriteFile(privatePath, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0600)
	publicDER, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
	_ = os.WriteFile(publicPath, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicDER}), 0600)
	issuer, _ := New(privatePath, publicPath, "issuer", "audience-a", "key", time.Minute)
	verifier, _ := New(privatePath, publicPath, "issuer", "audience-b", "key", time.Minute)
	raw, _ := issuer.Issue(domain.User{ID: uuid.New(), Phone: "+79990000000", Roles: []domain.UserRole{domain.RoleCustomer}})
	if _, err := verifier.Validate(raw); err == nil {
		t.Fatal("expected wrong audience to be rejected")
	}
}
