package domain

import "testing"

func TestCanAdvance(t *testing.T) {
	all := []Status{StatusSearching, StatusAssigned, StatusPickedUp, StatusEnRoute, StatusDelivered, StatusFailed}
	ok := map[[2]Status]bool{
		{StatusSearching, StatusAssigned}: true, {StatusSearching, StatusPickedUp}: true, {StatusSearching, StatusEnRoute}: true,
		{StatusSearching, StatusDelivered}: true, {StatusSearching, StatusFailed}: true,
		{StatusAssigned, StatusPickedUp}: true, {StatusAssigned, StatusEnRoute}: true, {StatusAssigned, StatusDelivered}: true,
		{StatusAssigned, StatusFailed}: true, {StatusPickedUp, StatusEnRoute}: true, {StatusPickedUp, StatusDelivered}: true,
		{StatusPickedUp, StatusFailed}: true, {StatusEnRoute, StatusDelivered}: true, {StatusEnRoute, StatusFailed}: true,
	}
	for _, from := range all {
		for _, to := range all {
			if got, want := CanAdvance(from, to), ok[[2]Status{from, to}]; got != want {
				t.Errorf("%s -> %s: got %v want %v", from, to, got, want)
			}
		}
	}
	if CanAdvance("bogus", StatusAssigned) || CanAdvance(StatusSearching, "bogus") {
		t.Error("unknown statuses must not advance")
	}
}

func TestShouldDeliver(t *testing.T) {
	cases := []struct {
		f, m, p string
		want    bool
	}{
		{"pickup", "online", "paid", false},
		{"delivery", "online", "pending", false}, // предоплата ещё не подтверждена (ADR-10)
		{"delivery", "online", "paid", true},
		{"delivery", "courier", "pending", true}, // оплата курьеру — сразу
		{"delivery", "restaurant", "pending", true},
	}
	for _, c := range cases {
		if got := ShouldDeliver(c.f, c.m, c.p); got != c.want {
			t.Errorf("%+v: got %v", c, got)
		}
	}
}

func TestHintAndVisibility(t *testing.T) {
	if (Delivery{Status: StatusFailed, FailReason: ReasonNoCourier}).Hint() != "pickup" {
		t.Error("no_courier must hint pickup")
	}
	if (Delivery{Status: StatusFailed, FailReason: ReasonUndelivered}).Hint() != "" || (Delivery{Status: StatusSearching}).Hint() != "" {
		t.Error("only no_courier hints pickup")
	}
	if !StatusAssigned.CourierVisible() || StatusDelivered.CourierVisible() || StatusSearching.CourierVisible() || StatusFailed.CourierVisible() {
		t.Error("courier PII visibility")
	}
	if !StatusPickedUp.Active() || !StatusEnRoute.Active() || StatusAssigned.Active() {
		t.Error("Active")
	}
	if MaskPhone("+79990000000") != "+7********00" || MaskPhone("12") != "***" {
		t.Errorf("MaskPhone: %q", MaskPhone("+79990000000"))
	}
}
