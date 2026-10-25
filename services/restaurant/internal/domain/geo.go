package domain

import (
	"math"

	"github.com/pavelgr1408/food-platform/pkg/geo"
)

// EarthRadiusM — средний радиус Земли, м.
const EarthRadiusM = 6371000.0

// Point — географическая точка.
type Point struct{ Lat, Lon float64 }

// DistanceMeters — расстояние по большому кругу (формула гаверсинуса), округлённое до метра.
// Эталон для тестов SQL-реализации в store.
func DistanceMeters(a, b Point) int {
	return int(math.Round(geo.DistanceMeters(a.Lat, a.Lon, b.Lat, b.Lon)))
}

// BBox — прямоугольник широта/долгота. Если LonAll — фильтр по долготе не нужен (полюс).
// Если WrapLon — прямоугольник пересекает антимеридиан: lon >= LonMin OR lon <= LonMax.
type BBox struct {
	LatMin, LatMax float64
	LonMin, LonMax float64
	LonAll         bool
	WrapLon        bool
}

// BoundingBox строит прямоугольник, гарантированно содержащий круг радиуса radiusM
// вокруг c (сфера, с запасом на сжатие Земли).
func BoundingBox(c Point, radiusM float64) BBox {
	const rad = math.Pi / 180
	dLat := radiusM / EarthRadiusM / rad * 1.001
	b := BBox{LatMin: math.Max(-90, c.Lat-dLat), LatMax: math.Min(90, c.Lat+dLat)}
	if c.Lat+dLat >= 90 || c.Lat-dLat <= -90 {
		b.LonAll = true // круг накрывает полюс
		return b
	}
	// максимум |dLon| на круге — asin(sin(d)/cos(lat)); для малых радиусов ≈ dLat/cos(lat)
	ratio := math.Sin(radiusM/EarthRadiusM) / math.Cos(c.Lat*rad)
	if ratio >= 1 {
		b.LonAll = true
		return b
	}
	dLon := math.Asin(ratio) / rad * 1.001
	b.LonMin, b.LonMax = c.Lon-dLon, c.Lon+dLon
	switch {
	case b.LonMin < -180:
		b.LonMin += 360
		b.WrapLon = true
	case b.LonMax > 180:
		b.LonMax -= 360
		b.WrapLon = true
	}
	return b
}
