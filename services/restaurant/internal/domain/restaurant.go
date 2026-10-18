package domain

// Restaurant — ресторан сети.
type Restaurant struct {
	ID               string
	Name             string
	City             string
	Address          string
	Location         Point
	Timezone         string
	DeliveryEnabled  bool
	DeliveryRadiusM  int
	Active           bool
	Schedule         Schedule
	DistanceM        *int // заполняется при поиске с центром
	DeliveryInRadius bool // центр попадает в зону доставки (при поиске с центром)
}
