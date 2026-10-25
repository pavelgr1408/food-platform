package domain

import (
	"math/rand/v2"
	"strconv"
	"testing"
	"time"
)

var now0 = time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)

const (
	rest = "a1000000-0000-4000-8000-000000000004"
	itA  = "b2000000-0000-4000-8000-00000000000a"
	itB  = "b2000000-0000-4000-8000-00000000000b"
)

func in(lines ...Line) DiscountInput {
	var sub int64
	for _, l := range lines {
		sub += l.LineTotal
	}
	return DiscountInput{RestaurantID: rest, Currency: "RUB", Fulfillment: FulfillmentPickup, Lines: lines, Subtotal: sub, Now: now0}
}

func line(id string, total int64) Line {
	return Line{MenuItemID: id, Quantity: 1, UnitPrice: total, LineTotal: total}
}

func promo(id string, typ string, val int64, prio int) Promotion {
	return Promotion{ID: id, Version: 1, Scope: "national", Title: id, Currency: "RUB", DiscountType: typ, DiscountVal: val, Priority: prio,
		ValidFrom: now0.Add(-time.Hour), Status: PromotionActive}
}

func TestPercentAndFixed(t *testing.T) {
	d := Calculate([]Promotion{promo("p", DiscountPercent, 1500, 0)}, in(line(itA, 100000)))
	if d.Total != 15000 || len(d.Applied) != 1 || d.Applied[0].PromotionID != "p" || d.Applied[0].Discount != 15000 {
		t.Fatalf("15%% of 1000 RUB: %+v", d)
	}
	d = Calculate([]Promotion{promo("p", DiscountFixed, 10000, 0)}, in(line(itA, 100000)))
	if d.Total != 10000 {
		t.Fatalf("fixed: %+v", d)
	}
	// фиксированная скидка не превышает базу
	d = Calculate([]Promotion{promo("p", DiscountFixed, 999999, 0)}, in(line(itA, 5000)))
	if d.Total != 5000 {
		t.Fatalf("fixed capped by base: %+v", d)
	}
	// 100 % — вся сумма, больше 100 % не бывает
	d = Calculate([]Promotion{promo("p", DiscountPercent, 10000, 0)}, in(line(itA, 5000)))
	if d.Total != 5000 {
		t.Fatalf("100%%: %+v", d)
	}
}

func TestBankersRounding(t *testing.T) {
	// 12,5 % от 0,04 ₽ (4 коп.) = 0,5 коп. → к чётному: 0
	// 12,5 % от 12 коп. = 1,5 → 2; 12,5 % от 20 коп. = 2,5 → 2; 12,5 % от 28 коп. = 3,5 → 4
	for base, want := range map[int64]int64{4: 0, 12: 2, 20: 2, 28: 4, 36: 4, 44: 6} {
		d := Calculate([]Promotion{promo("p", DiscountPercent, 1250, 0)}, in(line(itA, base)))
		if d.Total != want {
			t.Errorf("12.5%% of %d = %d, want %d", base, d.Total, want)
		}
	}
	// не-половинные значения округляются обычно
	if d := Calculate([]Promotion{promo("p", DiscountPercent, 3333, 0)}, in(line(itA, 100))); d.Total != 33 {
		t.Errorf("33.33%% of 100 = %d", d.Total)
	}
	if d := Calculate([]Promotion{promo("p", DiscountPercent, 3367, 0)}, in(line(itA, 100))); d.Total != 34 {
		t.Errorf("33.67%% of 100 = %d", d.Total)
	}
}

func TestApplicability(t *testing.T) {
	base := promo("p", DiscountPercent, 1000, 0)
	l := line(itA, 100000)
	cases := map[string]func(*Promotion, *DiscountInput){
		"not started": func(p *Promotion, _ *DiscountInput) { p.ValidFrom = now0.Add(time.Minute) },
		"ended":       func(p *Promotion, _ *DiscountInput) { e := now0; p.ValidTo = &e },
		"completed":   func(p *Promotion, _ *DiscountInput) { p.Status = PromotionCompleted },
		"other restaurant": func(p *Promotion, _ *DiscountInput) {
			p.Scope, p.RestaurantID = "local", "a1000000-0000-4000-8000-000000000001"
		},
		"other currency":      func(p *Promotion, _ *DiscountInput) { p.Currency = "USD" },
		"gift":                func(p *Promotion, _ *DiscountInput) { p.DiscountType = "gift" },
		"below min subtotal":  func(p *Promotion, _ *DiscountInput) { p.MinSubtotal = 100001 },
		"delivery only":       func(p *Promotion, _ *DiscountInput) { p.Fulfillment = "delivery" },
		"fulfillment not set": func(p *Promotion, i *DiscountInput) { p.Fulfillment = "pickup"; i.Fulfillment = "" },
		"item not in cart":    func(p *Promotion, _ *DiscountInput) { p.MenuItemIDs = []string{itB} },
		"unknown scope":       func(p *Promotion, _ *DiscountInput) { p.Scope = "regional" },
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			p, i := base, in(l)
			mut(&p, &i)
			if d := Calculate([]Promotion{p}, i); d.Total != 0 || len(d.Applied) != 0 {
				t.Fatalf("must not apply: %+v", d)
			}
		})
	}
	// граничные: ровно minSubtotal, начало окна включительно, local своего ресторана (регистр id не важен)
	p := base
	p.MinSubtotal, p.ValidFrom = 100000, now0
	p.Scope, p.RestaurantID = "local", "A1000000-0000-4000-8000-000000000004"
	if d := Calculate([]Promotion{p}, in(l)); d.Total != 10000 {
		t.Fatalf("boundary: %+v", d)
	}
	// бессрочная
	p = base
	p.ValidTo = nil
	if d := Calculate([]Promotion{p}, in(l)); d.Total == 0 {
		t.Fatal("open-ended promotion must apply")
	}
}

func TestItemTargetedDiscount(t *testing.T) {
	p := promo("p", DiscountPercent, 2000, 0)
	p.MenuItemIDs = []string{itA}
	d := Calculate([]Promotion{p}, in(line(itA, 30000), line(itB, 70000)))
	if d.Total != 6000 { // 20 % только от позиции A
		t.Fatalf("%+v", d)
	}
}

func TestPriorityAndExclusivity(t *testing.T) {
	hi, lo := promo("hi", DiscountPercent, 1000, 5), promo("lo", DiscountPercent, 3000, 1)
	d := Calculate([]Promotion{lo, hi}, in(line(itA, 100000)))
	if len(d.Applied) != 1 || d.Applied[0].PromotionID != "hi" || d.Total != 10000 {
		t.Fatalf("higher priority wins, lower non-stackable is skipped: %+v", d)
	}
	// непересекающиеся позиции — применяются обе
	hi.MenuItemIDs, lo.MenuItemIDs = []string{itA}, []string{itB}
	d = Calculate([]Promotion{lo, hi}, in(line(itA, 100000), line(itB, 100000)))
	if len(d.Applied) != 2 || d.Total != 10000+30000 {
		t.Fatalf("disjoint items: %+v", d)
	}
	// порядок применения детерминирован: приоритет, затем id
	a, b := promo("a", DiscountFixed, 100, 3), promo("b", DiscountFixed, 100, 3)
	a.Stackable, b.Stackable = true, true
	d1 := Calculate([]Promotion{a, b}, in(line(itA, 1000)))
	d2 := Calculate([]Promotion{b, a}, in(line(itA, 1000)))
	if d1.Applied[0].PromotionID != "a" || d2.Applied[0].PromotionID != "a" || d1.Total != 200 {
		t.Fatalf("%+v %+v", d1, d2)
	}
}

func TestStackableAppliesToRemainder(t *testing.T) {
	first := promo("first", DiscountPercent, 1000, 9) // 10 % от 1000 ₽ = 100 ₽
	first.Stackable = true
	second := promo("second", DiscountPercent, 1000, 8) // 10 % от остатка 900 ₽ = 90 ₽ (не 100)
	second.Stackable = true
	d := Calculate([]Promotion{second, first}, in(line(itA, 100000)))
	if d.Total != 10000+9000 || d.Applied[1].Discount != 9000 {
		t.Fatalf("stack on remainder: %+v", d)
	}
	// нестекируемая высокого приоритета + stackable ниже: stackable добавляется к остатку
	ns := promo("ns", DiscountPercent, 2000, 9)
	st := promo("st", DiscountFixed, 5000, 1)
	st.Stackable = true
	d = Calculate([]Promotion{st, ns}, in(line(itA, 100000)))
	if d.Total != 20000+5000 || len(d.Applied) != 2 {
		t.Fatalf("%+v", d)
	}
	// stackable после нестекируемой не «забивает» вторую нестекируемую на тех же строках
	ns2 := promo("ns2", DiscountPercent, 500, 2)
	d = Calculate([]Promotion{ns, st, ns2}, in(line(itA, 100000)))
	if len(d.Applied) != 2 {
		t.Fatalf("second non-stackable on the same lines must be skipped: %+v", d)
	}
}

func TestNeverExceedsSubtotalAndLines(t *testing.T) {
	ps := []Promotion{promo("a", DiscountPercent, 10000, 3), promo("b", DiscountPercent, 10000, 2), promo("c", DiscountFixed, 1000, 1)}
	for i := range ps {
		ps[i].Stackable = true
	}
	d := Calculate(ps, in(line(itA, 700), line(itB, 300)))
	if d.Total != 1000 {
		t.Fatalf("100%% then nothing left: %+v", d)
	}
	if len(d.Applied) != 1 {
		t.Fatalf("exhausted base must stop applying: %+v", d)
	}
}

func TestAllocateConservesSum(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	for n := 0; n < 5000; n++ {
		k := 1 + r.IntN(6)
		lines := make([]Line, k)
		var sub int64
		for i := range lines {
			lines[i] = line("b2000000-0000-4000-8000-0000000000"+strconv.Itoa(10+i), int64(1+r.IntN(500000)))
			sub += lines[i].LineTotal
		}
		var ps []Promotion
		for j := 0; j < 1+r.IntN(4); j++ {
			p := promo("p"+strconv.Itoa(j), DiscountPercent, int64(1+r.IntN(10000)), r.IntN(5))
			p.Stackable = true
			if r.IntN(2) == 0 {
				p.DiscountType, p.DiscountVal = DiscountFixed, int64(1+r.IntN(300000))
			}
			if r.IntN(2) == 0 {
				p.MenuItemIDs = []string{lines[r.IntN(k)].MenuItemID}
			}
			ps = append(ps, p)
		}
		d := Calculate(ps, DiscountInput{RestaurantID: rest, Currency: "RUB", Lines: lines, Subtotal: sub, Now: now0})
		var sum int64
		for _, a := range d.Applied {
			if a.Discount <= 0 {
				t.Fatalf("non-positive discount: %+v", d)
			}
			sum += a.Discount
		}
		if sum != d.Total || d.Total < 0 || d.Total > sub {
			t.Fatalf("iteration %d: total=%d sum=%d subtotal=%d (%+v)", n, d.Total, sum, sub, d)
		}
	}
}

func TestAllocateLargeNumbersNoOverflow(t *testing.T) {
	big := int64(9_000_000_000_000) // 9·10^12 коп.
	lines := []Line{line(itA, big), line(itB, big+1), line("b2000000-0000-4000-8000-00000000000c", big+2)}
	p := promo("p", DiscountPercent, 3333, 0)
	d := Calculate([]Promotion{p}, in(lines...))
	sub := 3*big + 3
	if d.Total != roundHalfEven(sub*3333, 10000) {
		t.Fatalf("large percent: %d", d.Total)
	}
	f := promo("f", DiscountFixed, 10_000_000_000, 0)
	d = Calculate([]Promotion{f}, in(lines...))
	if d.Total != 10_000_000_000 {
		t.Fatalf("large fixed: %+v", d)
	}
}

func TestNoLinesOrNoPromos(t *testing.T) {
	if d := Calculate(nil, in(line(itA, 100))); d.Total != 0 || d.Applied != nil {
		t.Fatalf("%+v", d)
	}
	if d := Calculate([]Promotion{promo("p", DiscountPercent, 1000, 0)}, DiscountInput{RestaurantID: rest, Currency: "RUB", Now: now0}); d.Total != 0 {
		t.Fatalf("%+v", d)
	}
}

func TestNet(t *testing.T) {
	if disc, total := Net(1000, Discount{Total: 300}); disc != 300 || total != 700 {
		t.Fatal(disc, total)
	}
	if disc, total := Net(1000, Discount{Total: 5000}); disc != 1000 || total != 0 {
		t.Fatal(disc, total)
	}
	if disc, total := Net(1000, Discount{Total: -5}); disc != 0 || total != 1000 {
		t.Fatal(disc, total)
	}
}
