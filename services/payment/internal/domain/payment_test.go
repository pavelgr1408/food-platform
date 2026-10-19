package domain

import "testing"

func TestTransitions(t *testing.T) {
	ok := [][2]Status{{StatusPending, StatusPaid}, {StatusPending, StatusDeclined}, {StatusPending, StatusUnknown},
		{StatusUnknown, StatusPaid}, {StatusUnknown, StatusDeclined}, {StatusUnknown, StatusPending}}
	for _, p := range ok {
		if !CanTransition(p[0], p[1]) {
			t.Errorf("%s → %s must be allowed", p[0], p[1])
		}
	}
	bad := [][2]Status{{StatusPaid, StatusDeclined}, {StatusPaid, StatusPending}, {StatusPaid, StatusUnknown},
		{StatusDeclined, StatusPaid}, {StatusDeclined, StatusPending}, {StatusPending, StatusPending}}
	for _, p := range bad {
		if CanTransition(p[0], p[1]) {
			t.Errorf("%s → %s must be forbidden", p[0], p[1])
		}
	}
	if !StatusPaid.Final() || !StatusDeclined.Final() || StatusUnknown.Final() {
		t.Error("final statuses")
	}
	if !StatusPending.Live() || !StatusUnknown.Live() || StatusPaid.Live() || StatusDeclined.Live() {
		t.Error("live statuses")
	}
}

func TestMethodMapping(t *testing.T) {
	for _, m := range []Method{MethodOnlineCard, MethodCourierCard, MethodPOS} {
		back, ok := MethodFromEvent(m.EventForm())
		if !ok || back != m {
			t.Errorf("%s: roundtrip via %q gave %q", m, m.EventForm(), back)
		}
	}
	if _, ok := MethodFromEvent("bitcoin"); ok {
		t.Error("unknown event method must not map")
	}
}

func TestMethodsFor(t *testing.T) {
	got := MethodsFor("pickup", MethodOnlineCard, true)
	if len(got) != 2 || got[0].Type != MethodOnlineCard || !got[0].Enabled || got[1].Type != MethodPOS || got[1].Enabled {
		t.Fatalf("pickup: %+v", got)
	}
	got = MethodsFor("delivery", MethodCourierCard, true)
	if got[0].Enabled || got[1].Type != MethodCourierCard || !got[1].Enabled {
		t.Fatalf("delivery: %+v", got)
	}
	for _, m := range MethodsFor("pickup", MethodOnlineCard, false) {
		if m.Enabled {
			t.Fatal("nothing is enabled for a non-payable order")
		}
	}
}

func TestAccess(t *testing.T) {
	if !CanBeAccessedBy("", "") || !CanBeAccessedBy("", "u1") || !CanBeAccessedBy("u1", "u1") || CanBeAccessedBy("u1", "u2") || CanBeAccessedBy("u1", "") {
		t.Fatal("access rules")
	}
}
