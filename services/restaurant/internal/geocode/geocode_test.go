package geocode

import "testing"

func TestResolve(t *testing.T) {
	cases := []struct {
		in   string
		city string
		ok   bool
	}{
		{"Москва", "Москва", true},
		{"г. Москва, Тверская 12", "Москва", true},
		{"  санкт-петербург ", "Санкт-Петербург", true},
		{"СПб, Невский 28", "Санкт-Петербург", true},
		{"Санкт Петербург", "Санкт-Петербург", true},
		{"казань", "Казань", true},
		{"Новосибирск", "", false},
		{"", "", false},
		{"Москвабад", "", false}, // не подстрока слова
	}
	for _, c := range cases {
		city, _, ok := Resolve(c.in)
		if ok != c.ok || city != c.city {
			t.Errorf("Resolve(%q) = %q,%v; want %q,%v", c.in, city, ok, c.city, c.ok)
		}
	}
}
