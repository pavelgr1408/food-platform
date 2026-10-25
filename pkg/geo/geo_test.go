package geo

import (
	"math"
	"testing"
)

func TestDistance(t *testing.T) {
	// Москва (Красная площадь) — Санкт-Петербург (Дворцовая): ~634 км.
	d := DistanceMeters(55.7539, 37.6208, 59.9387, 30.3158)
	if d < 630_000 || d > 640_000 {
		t.Fatalf("Moscow-SPb = %.0f m", d)
	}
	if DistanceMeters(10, 20, 10, 20) != 0 {
		t.Fatal("same point must be 0")
	}
	// симметрия и антимеридиан
	if a, b := DistanceMeters(0, 179.9, 0, -179.9), DistanceMeters(0, -179.9, 0, 179.9); math.Abs(a-b) > 1e-6 || a > 30_000 {
		t.Fatalf("antimeridian: %v %v", a, b)
	}
}

func TestValidAndMove(t *testing.T) {
	if !Valid(0, 0) || Valid(91, 0) || Valid(0, 181) || Valid(math.NaN(), 0) {
		t.Fatal("Valid")
	}
	if la, lo := Move(0, 0, 10, 20, 0.5); la != 5 || lo != 10 {
		t.Fatalf("half: %v %v", la, lo)
	}
	if la, lo := Move(0, 0, 10, 20, 2); la != 10 || lo != 20 {
		t.Fatal("clamp to target")
	}
	if la, lo := Move(1, 2, 10, 20, -1); la != 1 || lo != 2 {
		t.Fatal("no backwards")
	}
}
