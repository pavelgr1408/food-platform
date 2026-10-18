package domain

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

// Discounter считает скидки по строкам заказа. Реальная реализация (реплика
// promotions.discount, банковское округление один раз, ADR-13) — S8; интерфейс
// закладывается сейчас, чтобы расчёт суммы не переделывать.
type Discounter interface {
	Apply(restaurantID string, lines []Line, subtotal int64) Discount
}

// NoopDiscounter — скидок нет.
type NoopDiscounter struct{}

// Apply возвращает нулевую скидку.
func (NoopDiscounter) Apply(string, []Line, int64) Discount { return Discount{} }

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
