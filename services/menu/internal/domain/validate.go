package domain

// Коды причин недоступности (contracts/openapi/menu.yaml: ValidationReasonCode).
const (
	ReasonItemNotFound        = "ITEM_NOT_FOUND"
	ReasonItemUnavailable     = "ITEM_UNAVAILABLE"
	ReasonModifierNotFound    = "MODIFIER_NOT_FOUND"
	ReasonModifierUnavailable = "MODIFIER_UNAVAILABLE"
)

var reasonText = map[string]string{
	ReasonItemNotFound:        "Позиции нет в меню ресторана",
	ReasonItemUnavailable:     "Позиция недоступна",
	ReasonModifierNotFound:    "Модификатор не относится к позиции",
	ReasonModifierUnavailable: "Модификатор недоступен",
}

// Line — запрошенная строка заказа.
type Line struct {
	MenuItemID  string
	Quantity    int
	ModifierIDs []string
}

// LineResult — результат проверки строки.
type LineResult struct {
	MenuItemID string
	Quantity   int
	Available  bool
	// UnitPrice — цена за единицу с найденными модификаторами; nil, если позиции нет в меню.
	UnitPrice  *int64
	LineTotal  *int64
	ReasonCode string
	Reason     string
}

// Result — итог проверки.
type Result struct {
	Valid bool
	Lines []LineResult
	Total int64 // сумма LineTotal доступных строк
}

// Validate сверяет строки с позициями меню (items — позиции, загруженные по запрошенным id).
// Порядок строк результата совпадает с порядком запроса. Приоритет причин:
// ITEM_NOT_FOUND > ITEM_UNAVAILABLE > MODIFIER_NOT_FOUND > MODIFIER_UNAVAILABLE.
func Validate(items map[string]Item, lines []Line) Result {
	res := Result{Valid: true, Lines: make([]LineResult, 0, len(lines))}
	for _, l := range lines {
		lr := LineResult{MenuItemID: l.MenuItemID, Quantity: l.Quantity, Available: true}
		it, ok := items[l.MenuItemID]
		if !ok {
			lr.Available, lr.ReasonCode = false, ReasonItemNotFound
		} else {
			unit := it.Price
			if !it.Available {
				lr.Available, lr.ReasonCode = false, ReasonItemUnavailable
			}
			for _, mid := range l.ModifierIDs {
				m, found := findModifier(it, mid)
				switch {
				case !found:
					if lr.ReasonCode == "" || lr.ReasonCode == ReasonModifierUnavailable {
						lr.Available, lr.ReasonCode = false, ReasonModifierNotFound
					}
				default:
					unit += m.Price
					if !m.Available && lr.ReasonCode == "" {
						lr.Available, lr.ReasonCode = false, ReasonModifierUnavailable
					}
				}
			}
			total := unit * int64(l.Quantity)
			lr.UnitPrice, lr.LineTotal = &unit, &total
		}
		if lr.ReasonCode != "" {
			lr.Reason = reasonText[lr.ReasonCode]
			res.Valid = false
		} else {
			res.Total += *lr.LineTotal
		}
		res.Lines = append(res.Lines, lr)
	}
	return res
}

func findModifier(it Item, id string) (Modifier, bool) {
	for _, m := range it.Modifiers {
		if m.ID == id {
			return m, true
		}
	}
	return Modifier{}, false
}
