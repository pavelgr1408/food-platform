package domain

import (
	"context"
	"testing"
	"time"
)

func TestTransitionsTable(t *testing.T) {
	ok := [][2]Status{
		{StatusPendingPayment, StatusAccepted}, {StatusPendingPayment, StatusCancelled},
		{StatusAccepted, StatusCooking}, {StatusAccepted, StatusCancelled},
		{StatusCooking, StatusReady}, {StatusReady, StatusHandedOver}, {StatusReady, StatusOnDelivery},
		{StatusReady, StatusNotReceived}, {StatusOnDelivery, StatusDelivered},
		{StatusDelivered, StatusClosed}, {StatusHandedOver, StatusClosed}, {StatusNotReceived, StatusClosed},
	}
	for _, p := range ok {
		if !CanTransition(p[0], p[1]) {
			t.Errorf("%s → %s must be allowed", p[0], p[1])
		}
	}
	bad := [][2]Status{
		{StatusPendingPayment, StatusCooking}, // MG1: неоплаченный не в производство
		{StatusPendingPayment, StatusReady},
		{StatusCooking, StatusCancelled},
		{StatusCancelled, StatusAccepted}, {StatusClosed, StatusReady},
		{StatusReady, StatusCooking}, {StatusAccepted, StatusAccepted},
	}
	for _, p := range bad {
		if CanTransition(p[0], p[1]) {
			t.Errorf("%s → %s must be forbidden", p[0], p[1])
		}
	}
	for _, s := range []Status{StatusClosed, StatusCancelled} {
		if !s.Terminal() || !s.Valid() {
			t.Errorf("%s: want terminal+valid", s)
		}
	}
	if Status("bogus").Valid() {
		t.Error("bogus status is valid")
	}
	// каждый достижимый статус из таблицы известен
	for from, tos := range transitions {
		for _, to := range tos {
			if !to.Valid() || !from.Valid() {
				t.Errorf("unknown status in table: %s → %s", from, to)
			}
		}
	}
}

func TestPaymentRules(t *testing.T) {
	cases := []struct {
		m       PaymentMethod
		f       FulfillmentType
		allowed bool
	}{
		{PayOnlineCard, FulfillmentPickup, true}, {PayPOS, FulfillmentPickup, true}, {PayCourierCard, FulfillmentPickup, false},
		{PayOnlineCard, FulfillmentDelivery, true}, {PayCourierCard, FulfillmentDelivery, true}, {PayPOS, FulfillmentDelivery, false},
		{"bogus", FulfillmentPickup, false}, {PayOnlineCard, "", false},
	}
	for _, c := range cases {
		if got := c.m.Allowed(c.f); got != c.allowed {
			t.Errorf("%s/%s: got %v want %v", c.m, c.f, got, c.allowed)
		}
	}
	if InitialStatus(PayOnlineCard) != StatusPendingPayment || InitialStatus(PayPOS) != StatusAccepted || InitialStatus(PayCourierCard) != StatusAccepted {
		t.Error("initial status (ADR-10)")
	}
	if PayOnlineCard.EventForm() != "online" || PayCourierCard.EventForm() != "courier" || PayPOS.EventForm() != "restaurant" {
		t.Error("event form mapping")
	}
}

func TestFormatNumber(t *testing.T) {
	d := time.Date(2026, 10, 18, 23, 59, 0, 0, time.UTC)
	if got := FormatNumber(2, d, 7); got != "R2-261018-0007" {
		t.Errorf("got %s", got)
	}
	if got := FormatNumber(12, d, 12345); got != "R12-261018-12345" {
		t.Errorf("got %s", got)
	}
}

func TestValidateSlot(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	h := func(n float64) time.Time { return now.Add(time.Duration(n * float64(time.Hour))) }
	cases := []struct {
		name     string
		from, to time.Time
		ok       bool
	}{
		{"ok", h(1), h(1.5), true},
		{"now within tolerance", now.Add(-30 * time.Second), h(0.5), true},
		{"reversed", h(2), h(1), false},
		{"equal", h(1), h(1), false},
		{"past", h(-1), h(0.5), false},
		{"too long", h(1), h(5.5), false},
		{"exactly 4h", h(1), h(5), true},
		{"too far", h(24 * 8), h(24*8 + 1), false},
	}
	for _, c := range cases {
		if got := ValidateSlot(c.from, c.to, now) == ""; got != c.ok {
			t.Errorf("%s: ok=%v want %v (%s)", c.name, got, c.ok, ValidateSlot(c.from, c.to, now))
		}
	}
}

func TestNormalizePhone(t *testing.T) {
	good := map[string]string{"+7 (999) 123-45-67": "+79991234567", "+79991234567": "+79991234567", " +4915112345678 ": "+4915112345678"}
	for in, want := range good {
		if got, ok := NormalizePhone(in); !ok || got != want {
			t.Errorf("%q: got %q %v", in, got, ok)
		}
	}
	for _, in := range []string{"", "89991234567", "+0123456789", "+7abc1234567", "+123", "+1234567890123456789"} {
		if _, ok := NormalizePhone(in); ok {
			t.Errorf("%q must be rejected", in)
		}
	}
}

func TestDiscountNetNeverNegative(t *testing.T) {
	d, tot := Net(1000, Discount{Total: 300})
	if d != 300 || tot != 700 {
		t.Errorf("got %d %d", d, tot)
	}
	d, tot = Net(1000, Discount{Total: 5000})
	if d != 1000 || tot != 0 {
		t.Errorf("discount must be capped: %d %d", d, tot)
	}
	d, tot = Net(1000, Discount{Total: -5})
	if d != 0 || tot != 1000 {
		t.Errorf("negative discount: %d %d", d, tot)
	}
	if got := (NoopDiscounter{}).Apply(context.Background(), DiscountInput{Subtotal: 100}); got.Total != 0 || len(got.Applied) != 0 {
		t.Error("noop must return zero discount")
	}
}

func TestContactMergeAndAccess(t *testing.T) {
	c := Contact{Name: "A", Phone: "+79990000000"}.Merge(Contact{Phone: "+79991111111", Email: "a@b.c"})
	if c.Name != "A" || c.Phone != "+79991111111" || c.Email != "a@b.c" {
		t.Errorf("merge: %+v", c)
	}
	if !CanBeAccessedBy("", "") || !CanBeAccessedBy("", "u1") || !CanBeAccessedBy("u1", "u1") || CanBeAccessedBy("u1", "u2") || CanBeAccessedBy("u1", "") {
		t.Error("access rule")
	}
}
