package domain

import "testing"

func TestAggregateDishes(t *testing.T) {
	d := func(id string, s DishStatus) Dish { return Dish{id, s} }
	order := []string{"a", "b", "a"} // одна позиция меню может занимать две строки заказа
	cases := []struct {
		name string
		snap []Dish
		want Status
	}{
		{"all queued", []Dish{d("a", DishQueued), d("b", DishQueued)}, StatusAccepted},
		{"empty snapshot", nil, StatusAccepted},
		{"one cooking", []Dish{d("a", DishCooking), d("b", DishQueued)}, StatusCooking},
		{"one ready others queued", []Dish{d("a", DishReady), d("b", DishQueued)}, StatusCooking},
		{"all cooking", []Dish{d("a", DishCooking), d("b", DishCooking)}, StatusCooking},
		{"one ready one cooking", []Dish{d("a", DishReady), d("b", DishCooking)}, StatusCooking},
		{"all ready", []Dish{d("a", DishReady), d("b", DishReady)}, StatusReady},
		{"dish missing from snapshot is queued", []Dish{d("a", DishReady)}, StatusCooking},
		{"foreign dishes ignored", []Dish{d("x", DishReady), d("a", DishQueued)}, StatusAccepted},
		{"duplicate: most advanced wins", []Dish{d("a", DishQueued), d("a", DishReady), d("b", DishReady)}, StatusReady},
	}
	for _, c := range cases {
		if got := AggregateDishes(order, c.snap); got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
	if got := AggregateDishes(nil, []Dish{d("a", DishReady)}); got != StatusAccepted {
		t.Errorf("order without dishes must not become ready, got %s", got)
	}
}
