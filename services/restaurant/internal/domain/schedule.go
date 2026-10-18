// Package domain — модель ресторана и чистая логика расписания/геометрии (без I/O).
package domain

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// MinutesPerDay — конец суток в минутах («24:00»).
const MinutesPerDay = 24 * 60

// Interval — интервал работы в минутах от полуночи дня начала. Закрытие ≤ открытия
// (closes <= opens) означает ночной режим: закрытие наступает на следующие сутки.
type Interval struct {
	Opens  int // 0…1439
	Closes int // 1…1440
}

// overnight — интервал переходит через полночь.
func (i Interval) overnight() bool { return i.Closes <= i.Opens }

// WorkingHours — интервал работы в день недели (ISO: 1 = понедельник … 7 = воскресенье).
type WorkingHours struct {
	DayOfWeek int
	Interval
}

// Exception — исключение на календарную дату в часовом поясе ресторана.
// Closed: в этот день закрыто (Interval не используется). Несколько записей на дату
// образуют несколько интервалов; обычный режим этого дня полностью заменяется.
type Exception struct {
	Date   string // YYYY-MM-DD
	Closed bool
	Interval
	Note string
}

// Schedule — режим работы ресторана.
type Schedule struct {
	Location   *time.Location
	Hours      []WorkingHours
	Exceptions []Exception
}

// intervalsFor возвращает интервалы, начинающиеся в календарный день d (локальная дата).
func (s Schedule) intervalsFor(d time.Time) []Interval {
	date := d.Format("2006-01-02")
	var exc []Interval
	has := false
	for _, e := range s.Exceptions {
		if e.Date != date {
			continue
		}
		has = true
		if !e.Closed {
			exc = append(exc, e.Interval)
		}
	}
	if has {
		return exc
	}
	dow := isoWeekday(d)
	var out []Interval
	for _, h := range s.Hours {
		if h.DayOfWeek == dow {
			out = append(out, h.Interval)
		}
	}
	return out
}

// IsOpen — открыт ли ресторан в момент now. Учитывает часовой пояс, исключения по датам
// и «хвост» ночного интервала предыдущего дня.
func (s Schedule) IsOpen(now time.Time) bool {
	loc := s.Location
	if loc == nil {
		loc = time.UTC
	}
	t := now.In(loc)
	min := t.Hour()*60 + t.Minute()

	for _, iv := range s.intervalsFor(t) {
		if iv.overnight() {
			if min >= iv.Opens {
				return true
			}
		} else if min >= iv.Opens && min < iv.Closes {
			return true
		}
	}
	// Хвост вчерашних ночных интервалов (по режиму вчерашнего дня, включая его исключения).
	y := t.AddDate(0, 0, -1)
	for _, iv := range s.intervalsFor(y) {
		if iv.overnight() && min < iv.Closes {
			return true
		}
	}
	return false
}

func isoWeekday(t time.Time) int {
	d := int(t.Weekday())
	if d == 0 {
		return 7
	}
	return d
}

// FormatMinutes: 540 → "09:00", 1440 → "24:00".
func FormatMinutes(m int) string { return fmt.Sprintf("%02d:%02d", m/60, m%60) }

// ParseClock: "09:00" → 540; "24:00" допустимо только если allow24.
func ParseClock(s string, allow24 bool) (int, error) {
	h, m, ok := strings.Cut(s, ":")
	if !ok || len(h) != 2 || len(m) != 2 {
		return 0, fmt.Errorf("invalid time %q, want HH:MM", s)
	}
	hh, err1 := strconv.Atoi(h)
	mm, err2 := strconv.Atoi(m)
	if err1 != nil || err2 != nil || mm < 0 || mm > 59 || hh < 0 {
		return 0, fmt.Errorf("invalid time %q", s)
	}
	total := hh*60 + mm
	if total > MinutesPerDay-1 && !(allow24 && total == MinutesPerDay) {
		return 0, fmt.Errorf("time %q out of range", s)
	}
	return total, nil
}
