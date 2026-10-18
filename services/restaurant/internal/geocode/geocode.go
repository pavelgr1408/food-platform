// Package geocode — мок-справочник «город → центроид» (A1 UC-FND-01).
// Бэкенд к картам не обращается: геолокация и настоящий геокодинг — на клиенте (Д2).
// Справочник фиксированный; точки берутся по нормализованной подстроке адреса.
package geocode

import (
	"strings"

	"github.com/pavelgr1408/food-platform/services/restaurant/internal/domain"
)

type entry struct {
	Name    string
	Aliases []string
	Center  domain.Point
}

var directory = []entry{
	{"Москва", []string{"moscow", "мск"}, domain.Point{Lat: 55.7558, Lon: 37.6173}},
	{"Санкт-Петербург", []string{"санкт петербург", "петербург", "спб", "питер", "saint petersburg", "st petersburg"}, domain.Point{Lat: 59.9343, Lon: 30.3351}},
	{"Казань", []string{"kazan"}, domain.Point{Lat: 55.7961, Lon: 49.1064}},
	{"Екатеринбург", []string{"yekaterinburg", "екб"}, domain.Point{Lat: 56.8389, Lon: 60.6057}},
}

// Resolve находит город в произвольной строке адреса («г. Москва, Тверская 12»).
// Возвращает каноническое название города и центроид.
func Resolve(address string) (city string, center domain.Point, ok bool) {
	q := normalize(address)
	if q == "" {
		return "", domain.Point{}, false
	}
	for _, e := range directory {
		for _, n := range append([]string{e.Name}, e.Aliases...) {
			if containsWord(q, normalize(n)) {
				return e.Name, e.Center, true
			}
		}
	}
	return "", domain.Point{}, false
}

func normalize(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "ё", "е")
	s = strings.NewReplacer("-", " ", ",", " ", ".", " ").Replace(s)
	return strings.Join(strings.Fields(s), " ")
}

// containsWord: needle входит в haystack по границам слов.
func containsWord(haystack, needle string) bool {
	if needle == "" {
		return false
	}
	h, n := " "+haystack+" ", " "+needle+" "
	return strings.Contains(h, n)
}
