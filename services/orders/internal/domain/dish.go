package domain

// DishStatus — статус приготовления блюда (orders.dish.status).
type DishStatus string

// Статусы блюд (монотонны: queued → cooking → ready).
const (
	DishQueued  DishStatus = "queued"
	DishCooking DishStatus = "cooking"
	DishReady   DishStatus = "ready"
)

// Valid — известный статус блюда.
func (d DishStatus) Valid() bool { return d == DishQueued || d == DishCooking || d == DishReady }

// Dish — блюдо из снимка системы производства.
type Dish struct {
	ID     string
	Status DishStatus
}

// AggregateDishes выводит статус заказа из снимка блюд (AsyncAPI DishStatus):
// все блюда заказа ready → ready; хотя бы одно cooking или ready → cooking; иначе accepted.
// Блюдо заказа, которого нет в снимке, считается queued. Блюда снимка, которых нет в заказе, игнорируются.
// Пустой список блюд заказа → accepted (нечего готовить — решать не за что).
func AggregateDishes(orderDishIDs []string, snapshot []Dish) Status {
	st := make(map[string]DishStatus, len(snapshot))
	for _, d := range snapshot {
		// при дубликатах в снимке побеждает более «продвинутый» статус
		if rank(d.Status) >= rank(st[d.ID]) {
			st[d.ID] = d.Status
		}
	}
	started, ready, total := false, 0, 0
	seen := map[string]bool{}
	for _, id := range orderDishIDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		total++
		switch st[id] {
		case DishReady:
			ready++
			started = true
		case DishCooking:
			started = true
		}
	}
	switch {
	case total > 0 && ready == total:
		return StatusReady
	case started:
		return StatusCooking
	default:
		return StatusAccepted
	}
}

func rank(d DishStatus) int {
	switch d {
	case DishReady:
		return 3
	case DishCooking:
		return 2
	case DishQueued:
		return 1
	}
	return 0
}
