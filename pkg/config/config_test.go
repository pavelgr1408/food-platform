package config

import (
	"strings"
	"testing"
	"time"
)

func TestReaderAccumulatesErrors(t *testing.T) {
	r := FromMap(map[string]string{"A": "x", "TTL": "15m", "N": "7", "B": "true", "L": " a, b ,,c"})
	if r.String("A", "d") != "x" || r.String("MISSING", "d") != "d" {
		t.Fatal("string")
	}
	if r.Duration("TTL", 0) != 15*time.Minute || r.Int("N", 0) != 7 || !r.Bool("B", false) {
		t.Fatal("typed")
	}
	if got := r.List("L", nil); len(got) != 3 || got[1] != "b" {
		t.Fatalf("list %v", got)
	}
	if r.Err() != nil {
		t.Fatalf("unexpected %v", r.Err())
	}
	r.Int("A", 0)
	r.Required("NOPE")
	err := r.Err()
	if err == nil || !strings.Contains(err.Error(), "NOPE is required") || !strings.Contains(err.Error(), `A="x"`) {
		t.Fatalf("want both errors, got %v", err)
	}
}

func TestLoadBase(t *testing.T) {
	r := FromMap(map[string]string{"DATABASE_URL": "postgres://x"})
	b := LoadBase(r, "orders", "8084", true)
	if b.HTTPPort != "8084" || b.DatabaseURL == "" || b.Env != "dev" || r.Err() != nil {
		t.Fatalf("%+v %v", b, r.Err())
	}
	if LoadBase(FromMap(nil), "x", "1", false).DatabaseURL != "" {
		t.Fatal("db should be empty")
	}
	r2 := FromMap(nil)
	LoadBase(r2, "x", "1", true)
	if r2.Err() == nil {
		t.Fatal("DATABASE_URL must be required")
	}
}
