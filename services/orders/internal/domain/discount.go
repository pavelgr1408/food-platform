package domain

import (
	"context"
	"math/bits"
	"slices"
	"strings"
	"time"
)

// AppliedPromotion — применённая акция.
type AppliedPromotion struct {
	PromotionID string
	Title       string
	Discount    int64 // копейки
}

// Discount — результат расчёта скидок.
type Discount struct {
	Total   int64
	Applied []AppliedPromotion
}

// Типы скидки (promotions.discount: discount.type).
const (
	DiscountPercent = "percent" // value — сотые доли процента: 1500 = 15 %
	DiscountFixed   = "fixed"   // value — копейки
)

// PromotionStatus — статус акции в реплике.
const (
	PromotionActive    = "active"
	PromotionCompleted = "completed"
)

// Promotion — акция из реплики promotions.discount (снимок loyalty-service).
type Promotion struct {
	ID           string
	Version      int
	Scope        string // national | local
	RestaurantID string // для local
	Title        string
	Currency     string
	DiscountType string
	DiscountVal  int64
	Priority     int
	ValidFrom    time.Time
	ValidTo      *time.Time // nil — бессрочно
	Status       string
	MenuItemIDs  []string // пусто — весь заказ
	MinSubtotal  int64
	Fulfillment  string // "", pickup, delivery
	Stackable    bool
}

// DiscountInput — что нужно для расчёта скидки.
type DiscountInput struct {
	RestaurantID string
	Currency     string
	Fulfillment  FulfillmentType // пусто — способ получения ещё не выбран
	Lines        []Line
	Subtotal     int64
	Now          time.Time
}

// Discounter считает скидки по состоянию корзины/заказа. Реализация — реплика акций (service.ReplicaDiscounter).
type Discounter interface {
	Apply(ctx context.Context, in DiscountInput) Discount
}

// NoopDiscounter — скидок нет.
type NoopDiscounter struct{}

// Apply возвращает нулевую скидку.
func (NoopDiscounter) Apply(context.Context, DiscountInput) Discount { return Discount{} }

// Net — сумма к оплате: скидка не может превысить подытог.
func Net(subtotal int64, d Discount) (discount, total int64) {
	discount = d.Total
	if discount < 0 {
		discount = 0
	}
	if discount > subtotal {
		discount = subtotal
	}
	return discount, subtotal - discount
}

// Applicable — подходит ли акция по статусу, сроку, охвату, валюте, способу получения и подытогу.
func (p Promotion) Applicable(in DiscountInput) bool {
	if p.Status != PromotionActive || (p.DiscountType != DiscountPercent && p.DiscountType != DiscountFixed) || p.DiscountVal <= 0 {
		return false
	}
	if in.Now.Before(p.ValidFrom) || (p.ValidTo != nil && !in.Now.Before(*p.ValidTo)) {
		return false
	}
	if p.Scope == "local" && !strings.EqualFold(p.RestaurantID, in.RestaurantID) {
		return false
	}
	if p.Scope != "local" && p.Scope != "national" {
		return false
	}
	cur := p.Currency
	if cur == "" {
		cur = "RUB"
	}
	if !strings.EqualFold(cur, in.Currency) {
		return false
	}
	if p.Fulfillment != "" && string(in.Fulfillment) != p.Fulfillment {
		return false
	}
	return in.Subtotal >= p.MinSubtotal
}

// Calculate считает скидку по правилам loyalty.yaml («Расчёт скидки»), ADR-13: только целые числа.
//
// Акции применяются по убыванию приоритета (при равенстве — по id). База акции — сумма остатков подходящих
// строк (menuItemIds; пусто — весь заказ). Нестекируемая акция не применяется, если одна из её строк уже
// затронута другой применённой нестекируемой (выигрывает больший приоритет); stackable применяются всегда,
// каждая к остатку после предыдущих. percent округляется банковским способом один раз на акцию; fixed не
// превышает базу. Скидка акции распределяется по строкам пропорционально остатку (метод наибольших остатков).
func Calculate(promos []Promotion, in DiscountInput) Discount {
	var cand []Promotion
	for _, p := range promos {
		if p.Applicable(in) {
			cand = append(cand, p)
		}
	}
	slices.SortFunc(cand, func(a, b Promotion) int {
		if a.Priority != b.Priority {
			return b.Priority - a.Priority
		}
		return strings.Compare(a.ID, b.ID)
	})
	remaining := make([]int64, len(in.Lines))
	for i, l := range in.Lines {
		remaining[i] = l.LineTotal
	}
	exclusive := make([]bool, len(in.Lines)) // строки, затронутые нестекируемой акцией
	var out Discount
	for _, p := range cand {
		var idx []int
		for i, l := range in.Lines {
			if len(p.MenuItemIDs) == 0 || containsFold(p.MenuItemIDs, l.MenuItemID) {
				idx = append(idx, i)
			}
		}
		if len(idx) == 0 {
			continue
		}
		if !p.Stackable && anyTrue(exclusive, idx) {
			continue
		}
		var base int64
		for _, i := range idx {
			base += remaining[i]
		}
		if base <= 0 {
			continue
		}
		var d int64
		switch p.DiscountType {
		case DiscountPercent:
			d = roundHalfEven(base*min(p.DiscountVal, 10000), 10000)
		case DiscountFixed:
			d = min(p.DiscountVal, base)
		}
		d = min(d, base)
		if d <= 0 {
			continue
		}
		parts := allocate(d, idx, remaining, base)
		for k, i := range idx {
			remaining[i] -= parts[k]
			if !p.Stackable {
				exclusive[i] = true
			}
		}
		out.Total += d
		out.Applied = append(out.Applied, AppliedPromotion{PromotionID: p.ID, Title: p.Title, Discount: d})
	}
	return out
}

func containsFold(ids []string, id string) bool {
	return slices.ContainsFunc(ids, func(s string) bool { return strings.EqualFold(s, id) })
}

func anyTrue(flags []bool, idx []int) bool {
	for _, i := range idx {
		if flags[i] {
			return true
		}
	}
	return false
}

// roundHalfEven делит n на den (> 0) с банковским округлением: ровно половина уходит к чётному.
func roundHalfEven(n, den int64) int64 {
	q, r := n/den, n%den
	switch {
	case 2*r > den:
		q++
	case 2*r == den && q%2 == 1:
		q++
	}
	return q
}

// allocate раскладывает d по строкам idx пропорционально остаткам (метод наибольших остатков):
// сумма долей равна d, доля строки не превышает её остаток. Умножение — 128-битное, без переполнения.
func allocate(d int64, idx []int, remaining []int64, base int64) []int64 {
	parts := make([]int64, len(idx))
	fracs := make([]uint64, len(idx))
	var given int64
	for k, i := range idx {
		hi, lo := bits.Mul64(uint64(d), uint64(remaining[i]))
		q, r := bits.Div64(hi, lo, uint64(base)) // d ≤ base ⇒ hi < base, паники нет
		parts[k], fracs[k] = int64(q), r
		given += int64(q)
	}
	order := make([]int, len(idx))
	for k := range order {
		order[k] = k
	}
	slices.SortStableFunc(order, func(a, b int) int {
		switch {
		case fracs[a] > fracs[b]:
			return -1
		case fracs[a] < fracs[b]:
			return 1
		}
		return 0
	})
	for _, k := range order {
		if given >= d {
			break
		}
		if parts[k] < remaining[idx[k]] {
			parts[k]++
			given++
		}
	}
	return parts
}
