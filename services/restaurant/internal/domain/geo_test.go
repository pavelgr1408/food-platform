package domain

import (
	"math"
	"math/rand/v2"
	"testing"
)

func TestDistanceKnown(t *testing.T) {
	// Красная площадь → Эрмитаж ≈ 634 км
	d := DistanceMeters(Point{55.7539, 37.6208}, Point{59.9398, 30.3146})
	if d < 630_000 || d > 640_000 {
		t.Fatalf("distance %d", d)
	}
	if DistanceMeters(Point{10, 10}, Point{10, 10}) != 0 {
		t.Fatal("same point must be 0")
	}
}

func inBox(b BBox, p Point) bool {
	if p.Lat < b.LatMin || p.Lat > b.LatMax {
		return false
	}
	if b.LonAll {
		return true
	}
	if b.WrapLon {
		return p.Lon >= b.LonMin || p.Lon <= b.LonMax
	}
	return p.Lon >= b.LonMin && p.Lon <= b.LonMax
}

// Любая точка внутри круга обязана попасть в bounding box (иначе поиск теряет рестораны).
func TestBoundingBoxContainsCircle(t *testing.T) {
	rnd := rand.New(rand.NewPCG(1, 2))
	centers := []Point{{55.75, 37.6}, {59.9, 30.3}, {0, 0}, {-33.9, 151.2}, {64.5, 40.5}, {69, 33}, {0, 179.9}, {0, -179.9}, {89.9, 10}}
	for _, c := range centers {
		for _, r := range []float64{100, 5000, 50000} {
			b := BoundingBox(c, r)
			for i := 0; i < 3000; i++ {
				// случайная точка внутри круга: азимут + расстояние по сфере
				az := rnd.Float64() * 2 * math.Pi
				dist := rnd.Float64() * r / EarthRadiusM
				lat1, lon1 := c.Lat*math.Pi/180, c.Lon*math.Pi/180
				lat2 := math.Asin(math.Sin(lat1)*math.Cos(dist) + math.Cos(lat1)*math.Sin(dist)*math.Cos(az))
				lon2 := lon1 + math.Atan2(math.Sin(az)*math.Sin(dist)*math.Cos(lat1), math.Cos(dist)-math.Sin(lat1)*math.Sin(lat2))
				p := Point{lat2 * 180 / math.Pi, math.Mod(lon2*180/math.Pi+540, 360) - 180}
				if DistanceMeters(c, p) > int(r) {
					continue
				}
				if !inBox(b, p) {
					t.Fatalf("center %v r=%v: point %v (d=%d) outside box %+v", c, r, p, DistanceMeters(c, p), b)
				}
			}
		}
	}
}
