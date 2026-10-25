package domain

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

var (
	t0   = time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	r1   = "a1000000-0000-4000-8000-000000000001"
	r2   = "a1000000-0000-4000-8000-000000000002"
	item = "b2000000-0000-4000-8000-000000000001"
)

func valid() Promotion {
	return Promotion{Title: " Скидка ", Scope: ScopeLocal, RestaurantID: strings.ToUpper(r1), Priority: 5,
		StartsAt: t0, EndsAt: t0.Add(48 * time.Hour), Discount: Discount{DiscountPercent, 1500},
		Conditions: Conditions{MenuItemIDs: []string{item}, MinSubtotal: 50000}}
}

func TestNormalizeOK(t *testing.T) {
	p, err := Normalize(valid())
	if err != nil {
		t.Fatal(err)
	}
	if p.Title != "Скидка" || p.Currency != "RUB" || p.RestaurantID != r1 {
		t.Fatalf("not normalized: %+v", p)
	}
}

func TestNormalizeRejects(t *testing.T) {
	cases := map[string]func(*Promotion){
		"empty title":        func(p *Promotion) { p.Title = "  " },
		"local without rest": func(p *Promotion) { p.RestaurantID = "" },
		"local bad rest":     func(p *Promotion) { p.RestaurantID = "x" },
		"national with rest": func(p *Promotion) { p.Scope = ScopeNational },
		"bad scope":          func(p *Promotion) { p.Scope = "regional" },
		"priority high":      func(p *Promotion) { p.Priority = MaxPriority + 1 },
		"priority negative":  func(p *Promotion) { p.Priority = -1 },
		"ends before starts": func(p *Promotion) { p.EndsAt = p.StartsAt },
		"bad currency":       func(p *Promotion) { p.Currency = "ru" },
		"percent zero":       func(p *Promotion) { p.Discount.Value = 0 },
		"percent over 100":   func(p *Promotion) { p.Discount.Value = MaxPercent + 1 },
		"fixed zero":         func(p *Promotion) { p.Discount = Discount{DiscountFixed, 0} },
		"gift":               func(p *Promotion) { p.Discount = Discount{"gift", 0} },
		"negative min":       func(p *Promotion) { p.Conditions.MinSubtotal = -1 },
		"bad fulfillment":    func(p *Promotion) { p.Conditions.Fulfillment = "drone" },
		"bad item id":        func(p *Promotion) { p.Conditions.MenuItemIDs = []string{"nope"} },
		"duplicate item":     func(p *Promotion) { p.Conditions.MenuItemIDs = []string{item, strings.ToUpper(item)} },
		"too many items":     func(p *Promotion) { p.Conditions.MenuItemIDs = manyIDs(MaxItemIDs + 1) },
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			p := valid()
			mut(&p)
			if _, err := Normalize(p); err == nil {
				t.Fatal("want validation error")
			} else if _, ok := err.(*ValidationError); !ok {
				t.Fatalf("want *ValidationError, got %T", err)
			}
		})
	}
}

func manyIDs(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = uuid.NewString()
	}
	return out
}

func promo(id string, scope Scope, rest string, prio int, from, to time.Time, items ...string) Promotion {
	return Promotion{ID: id, Scope: scope, RestaurantID: rest, Priority: prio, Status: StatusActive,
		StartsAt: from, EndsAt: to, Conditions: Conditions{MenuItemIDs: items}}
}

func TestConflicts(t *testing.T) {
	day := 24 * time.Hour
	base := promo("a", ScopeLocal, r1, 1, t0, t0.Add(10*day))
	tests := []struct {
		name string
		o    Promotion
		want bool
	}{
		{"same everything", promo("b", ScopeLocal, r1, 1, t0, t0.Add(10*day)), true},
		{"national vs local", promo("b", ScopeNational, "", 1, t0, t0.Add(10*day)), true},
		{"other restaurant", promo("b", ScopeLocal, r2, 1, t0, t0.Add(10*day)), false},
		{"different priority", promo("b", ScopeLocal, r1, 2, t0, t0.Add(10*day)), false},
		{"adjacent periods", promo("b", ScopeLocal, r1, 1, t0.Add(10*day), t0.Add(20*day)), false},
		{"overlapping periods", promo("b", ScopeLocal, r1, 1, t0.Add(9*day), t0.Add(20*day)), true},
		{"disjoint items", func() Promotion { p := promo("b", ScopeLocal, r1, 1, t0, t0.Add(10*day), item); return p }(), true}, // base — весь заказ
		{"same id", promo("a", ScopeLocal, r1, 1, t0, t0.Add(10*day)), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Conflicts(base, tc.o); got != tc.want {
				t.Fatalf("Conflicts = %v, want %v", got, tc.want)
			}
			if got := Conflicts(tc.o, base); got != tc.want {
				t.Fatalf("Conflicts (reversed) = %v, want %v", got, tc.want)
			}
		})
	}
	// разные позиции не конфликтуют
	a := promo("a", ScopeLocal, r1, 1, t0, t0.Add(day), item)
	b := promo("b", ScopeLocal, r1, 1, t0, t0.Add(day), "b2000000-0000-4000-8000-000000000002")
	if Conflicts(a, b) {
		t.Fatal("disjoint item sets must not conflict")
	}
	// stackable не конфликтует
	b.Conditions.MenuItemIDs = []string{item}
	b.Conditions.Stackable = true
	if Conflicts(a, b) {
		t.Fatal("stackable must not conflict")
	}
}

func TestFindConflictsSkipsNonActive(t *testing.T) {
	cand := promo("c", ScopeNational, "", 1, t0, t0.Add(time.Hour))
	draft := promo("d", ScopeNational, "", 1, t0, t0.Add(time.Hour))
	draft.Status = StatusDraft
	act := promo("a", ScopeNational, "", 1, t0, t0.Add(time.Hour))
	got := FindConflicts(cand, []Promotion{draft, act})
	if len(got) != 1 || got[0] != "a" {
		t.Fatalf("got %v", got)
	}
}

func TestWindow(t *testing.T) {
	p := promo("a", ScopeNational, "", 0, t0, t0.Add(time.Hour))
	if !p.InWindow(t0) || p.InWindow(t0.Add(time.Hour)) || p.InWindow(t0.Add(-time.Nanosecond)) {
		t.Fatal("window is [startsAt, endsAt)")
	}
	if !p.Expired(t0.Add(time.Hour)) || p.Expired(t0) {
		t.Fatal("Expired")
	}
}
