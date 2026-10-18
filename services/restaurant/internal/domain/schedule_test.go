package domain

import (
	"testing"
	"time"

	_ "time/tzdata"
)

func loc(t *testing.T, name string) *time.Location {
	t.Helper()
	l, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func at(t *testing.T, l *time.Location, s string) time.Time {
	t.Helper()
	v, err := time.ParseInLocation("2006-01-02 15:04", s, l)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func allWeek(open, close int) []WorkingHours {
	var out []WorkingHours
	for d := 1; d <= 7; d++ {
		out = append(out, WorkingHours{DayOfWeek: d, Interval: Interval{open, close}})
	}
	return out
}

func TestIsOpenDaytime(t *testing.T) {
	msk := loc(t, "Europe/Moscow")
	s := Schedule{Location: msk, Hours: allWeek(9*60, 22*60)}
	cases := []struct {
		when string
		want bool
	}{
		{"2026-10-19 08:59", false},
		{"2026-10-19 09:00", true}, // открытие включительно
		{"2026-10-19 21:59", true},
		{"2026-10-19 22:00", false}, // закрытие исключительно
		{"2026-10-19 03:00", false},
	}
	for _, c := range cases {
		if got := s.IsOpen(at(t, msk, c.when)); got != c.want {
			t.Errorf("%s: got %v want %v", c.when, got, c.want)
		}
	}
}

func TestIsOpenUsesRestaurantTimezone(t *testing.T) {
	msk := loc(t, "Europe/Moscow")
	s := Schedule{Location: msk, Hours: allWeek(9*60, 22*60)}
	// 07:00 UTC = 10:00 Москва → открыто; 19:30 UTC = 22:30 Москва → закрыто
	if !s.IsOpen(time.Date(2026, 10, 19, 7, 0, 0, 0, time.UTC)) {
		t.Error("10:00 MSK must be open")
	}
	if s.IsOpen(time.Date(2026, 10, 19, 19, 30, 0, 0, time.UTC)) {
		t.Error("22:30 MSK must be closed")
	}
}

func TestIsOpenOvernight(t *testing.T) {
	msk := loc(t, "Europe/Moscow")
	// пятница (5) и суббота (6): 10:00–02:00 следующих суток; остальные дни 10:00–23:00
	var h []WorkingHours
	for d := 1; d <= 7; d++ {
		if d == 5 || d == 6 {
			h = append(h, WorkingHours{d, Interval{10 * 60, 2 * 60}})
		} else {
			h = append(h, WorkingHours{d, Interval{10 * 60, 23 * 60}})
		}
	}
	s := Schedule{Location: msk, Hours: h}
	// 2026-10-23 — пятница
	cases := []struct {
		when string
		want bool
	}{
		{"2026-10-23 23:30", true},  // пятница, после обычных 23:00
		{"2026-10-24 01:59", true},  // хвост пятницы в субботу
		{"2026-10-24 02:00", false}, // закрылись
		{"2026-10-24 09:59", false},
		{"2026-10-25 01:00", true},  // хвост субботы в воскресенье
		{"2026-10-26 01:00", false}, // хвоста у воскресенья нет
		{"2026-10-22 23:30", false}, // четверг закрылся в 23:00
	}
	for _, c := range cases {
		if got := s.IsOpen(at(t, msk, c.when)); got != c.want {
			t.Errorf("%s: got %v want %v", c.when, got, c.want)
		}
	}
}

func TestIsOpen24h(t *testing.T) {
	spb := loc(t, "Europe/Moscow")
	s := Schedule{Location: spb, Hours: allWeek(0, MinutesPerDay)}
	for _, w := range []string{"2026-10-19 00:00", "2026-10-19 12:00", "2026-10-19 23:59"} {
		if !s.IsOpen(at(t, spb, w)) {
			t.Errorf("%s must be open (24h)", w)
		}
	}
}

func TestIsOpenBreak(t *testing.T) {
	msk := loc(t, "Europe/Moscow")
	var h []WorkingHours
	for d := 1; d <= 7; d++ {
		h = append(h, WorkingHours{d, Interval{9 * 60, 13 * 60}}, WorkingHours{d, Interval{14 * 60, 22 * 60}})
	}
	s := Schedule{Location: msk, Hours: h}
	if s.IsOpen(at(t, msk, "2026-10-19 13:30")) {
		t.Error("lunch break must be closed")
	}
	if !s.IsOpen(at(t, msk, "2026-10-19 14:00")) {
		t.Error("after break must be open")
	}
}

func TestExceptions(t *testing.T) {
	msk := loc(t, "Europe/Moscow")
	s := Schedule{
		Location: msk,
		Hours:    allWeek(9*60, 22*60),
		Exceptions: []Exception{
			{Date: "2026-12-31", Interval: Interval{10 * 60, 18 * 60}},
			{Date: "2027-01-01", Closed: true},
		},
	}
	cases := []struct {
		when string
		want bool
	}{
		{"2026-12-31 09:30", false}, // короткий день: открытие в 10
		{"2026-12-31 17:59", true},
		{"2026-12-31 20:00", false}, // обычно открыто, но исключение заменяет режим
		{"2027-01-01 12:00", false}, // закрыто весь день
		{"2027-01-02 12:00", true},
	}
	for _, c := range cases {
		if got := s.IsOpen(at(t, msk, c.when)); got != c.want {
			t.Errorf("%s: got %v want %v", c.when, got, c.want)
		}
	}
}

func TestExceptionOnYesterdayCancelsOvernightTail(t *testing.T) {
	msk := loc(t, "Europe/Moscow")
	// 2026-10-23 пятница 10:00–02:00, но на эту дату закрыто → хвоста в субботу 01:00 нет
	s := Schedule{
		Location:   msk,
		Hours:      allWeek(10*60, 2*60),
		Exceptions: []Exception{{Date: "2026-10-23", Closed: true}},
	}
	if s.IsOpen(at(t, msk, "2026-10-24 01:00")) {
		t.Error("closed Friday must not produce a Saturday tail")
	}
	if !s.IsOpen(at(t, msk, "2026-10-25 01:00")) {
		t.Error("Saturday's tail still applies")
	}
}

func TestDSTLocalClock(t *testing.T) {
	// Europe/Berlin: 2026-10-25 03:00 CEST → 02:00 CET. Режим считается по локальным часам стены.
	ber := loc(t, "Europe/Berlin")
	s := Schedule{Location: ber, Hours: allWeek(9*60, 22*60)}
	if !s.IsOpen(time.Date(2026, 10, 25, 8, 30, 0, 0, time.UTC)) { // 09:30 CET
		t.Error("09:30 local must be open after DST change")
	}
	if s.IsOpen(time.Date(2026, 10, 25, 7, 30, 0, 0, time.UTC)) { // 08:30 CET
		t.Error("08:30 local must be closed")
	}
}

func TestParseClock(t *testing.T) {
	for _, c := range []struct {
		in    string
		allow bool
		want  int
		err   bool
	}{
		{"09:00", false, 540, false}, {"00:00", false, 0, false}, {"23:59", false, 1439, false},
		{"24:00", true, 1440, false}, {"24:00", false, 0, true}, {"25:00", true, 0, true},
		{"9:00", false, 0, true}, {"09:60", false, 0, true}, {"abc", false, 0, true},
	} {
		got, err := ParseClock(c.in, c.allow)
		if (err != nil) != c.err || (!c.err && got != c.want) {
			t.Errorf("ParseClock(%q,%v) = %d,%v", c.in, c.allow, got, err)
		}
	}
	if FormatMinutes(540) != "09:00" || FormatMinutes(1440) != "24:00" {
		t.Error("FormatMinutes")
	}
}
