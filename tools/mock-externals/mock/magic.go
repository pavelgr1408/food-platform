package mock

// Outcome — детерминированный исход по «магической» сумме (в минорных единицах):
// последние две цифры 13 → отказ, 99 → «тишина» (вебхук не приходит, статус unknown),
// иначе успех. Правило едино для платёжного шлюза и POS (ADR/план §5).
type Outcome string

const (
	OutcomePaid     Outcome = "paid"
	OutcomeDeclined Outcome = "declined"
	OutcomeSilent   Outcome = "silent"
)

// OutcomeForAmount вычисляет исход по сумме в минорных единицах (копейках).
func OutcomeForAmount(minor int64) Outcome {
	if minor < 0 {
		minor = -minor
	}
	switch minor % 100 {
	case 13:
		return OutcomeDeclined
	case 99:
		return OutcomeSilent
	}
	return OutcomePaid
}
