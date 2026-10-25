// Package geo — геодезия без внешних зависимостей: расстояние по формуле гаверсинуса.
package geo

import "math"

// EarthRadiusMeters — радиус Земли; то же значение, что в SQL поиска ресторанов (restaurant-service),
// чтобы «в зоне доставки» у каталога и у orders совпадало до метра.
const EarthRadiusMeters = 6371000.0

// Valid — широта и долгота в допустимых пределах.
func Valid(lat, lon float64) bool {
	return !math.IsNaN(lat) && !math.IsNaN(lon) && lat >= -90 && lat <= 90 && lon >= -180 && lon <= 180
}

// DistanceMeters — расстояние по большому кругу между двумя точками, метры.
func DistanceMeters(lat1, lon1, lat2, lon2 float64) float64 {
	const rad = math.Pi / 180
	p1, p2 := lat1*rad, lat2*rad
	dp, dl := (lat2-lat1)*rad, (lon2-lon1)*rad
	a := math.Sin(dp/2)*math.Sin(dp/2) + math.Cos(p1)*math.Cos(p2)*math.Sin(dl/2)*math.Sin(dl/2)
	return 2 * EarthRadiusMeters * math.Asin(math.Min(1, math.Sqrt(a)))
}

// Move сдвигает точку на долю fraction (0..1) пути к цели по прямой в градусах — достаточно для
// «прогулки» курьера в моке на городских расстояниях.
func Move(lat, lon, toLat, toLon, fraction float64) (float64, float64) {
	if fraction <= 0 {
		return lat, lon
	}
	if fraction >= 1 {
		return toLat, toLon
	}
	return lat + (toLat-lat)*fraction, lon + (toLon-lon)*fraction
}
