package domain

import "testing"

func catalog() map[string]Item {
	return map[string]Item{
		"burger": {ID: "burger", Price: 34900, Available: true, Modifiers: []Modifier{
			{ID: "bacon", Price: 8900, Available: true},
			{ID: "cheese", Price: 5000, Available: false},
		}},
		"cake": {ID: "cake", Price: 17900, Available: false},
		"cola": {ID: "cola", Price: 12900, Available: true},
	}
}

func p(v int64) *int64 { return &v }

func TestValidateHappyPathSumsInKopecks(t *testing.T) {
	r := Validate(catalog(), []Line{
		{MenuItemID: "burger", Quantity: 2, ModifierIDs: []string{"bacon"}},
		{MenuItemID: "cola", Quantity: 3},
	})
	if !r.Valid || r.Total != 2*(34900+8900)+3*12900 {
		t.Fatalf("%+v", r)
	}
	if *r.Lines[0].UnitPrice != 43800 || *r.Lines[0].LineTotal != 87600 || *r.Lines[1].LineTotal != 38700 {
		t.Fatalf("lines: %+v", r.Lines)
	}
}

func TestValidateReasons(t *testing.T) {
	cases := []struct {
		name   string
		line   Line
		code   string
		hasPrc bool
	}{
		{"unknown item", Line{MenuItemID: "ghost", Quantity: 1}, ReasonItemNotFound, false},
		{"stop-list item", Line{MenuItemID: "cake", Quantity: 1}, ReasonItemUnavailable, true},
		{"foreign modifier", Line{MenuItemID: "cola", Quantity: 1, ModifierIDs: []string{"bacon"}}, ReasonModifierNotFound, true},
		{"stop-list modifier", Line{MenuItemID: "burger", Quantity: 1, ModifierIDs: []string{"cheese"}}, ReasonModifierUnavailable, true},
		{"unavailable item beats modifier problems", Line{MenuItemID: "cake", Quantity: 1, ModifierIDs: []string{"x"}}, ReasonItemUnavailable, true},
		{"not-found beats unavailable modifier", Line{MenuItemID: "burger", Quantity: 1, ModifierIDs: []string{"cheese", "zzz"}}, ReasonModifierNotFound, true},
		{"not-found modifier first then unavailable", Line{MenuItemID: "burger", Quantity: 1, ModifierIDs: []string{"zzz", "cheese"}}, ReasonModifierNotFound, true},
	}
	for _, c := range cases {
		r := Validate(catalog(), []Line{c.line})
		l := r.Lines[0]
		if r.Valid || l.Available || l.ReasonCode != c.code || l.Reason == "" || (l.UnitPrice != nil) != c.hasPrc {
			t.Errorf("%s: %+v", c.name, r)
		}
		if r.Total != 0 {
			t.Errorf("%s: unavailable line must not add to total", c.name)
		}
	}
}

func TestValidateMixedKeepsOrderAndOnlyCountsAvailable(t *testing.T) {
	r := Validate(catalog(), []Line{
		{MenuItemID: "cola", Quantity: 1},
		{MenuItemID: "cake", Quantity: 5},
		{MenuItemID: "cola", Quantity: 2},
	})
	if r.Valid || len(r.Lines) != 3 || r.Lines[1].MenuItemID != "cake" || r.Lines[1].Available {
		t.Fatalf("%+v", r)
	}
	if r.Total != 3*12900 {
		t.Fatalf("total %d", r.Total)
	}
	_ = p
}

func TestValidateReturnsItemNameSnapshot(t *testing.T) {
	c := catalog()
	burger := c["burger"]
	burger.Name = "Бургер"
	c["burger"] = burger
	r := Validate(c, []Line{{MenuItemID: "burger", Quantity: 1}, {MenuItemID: "ghost", Quantity: 1}})
	if r.Lines[0].Name != "Бургер" {
		t.Errorf("name snapshot: %q", r.Lines[0].Name)
	}
	if r.Lines[1].Name != "" {
		t.Errorf("unknown item must have no name: %q", r.Lines[1].Name)
	}
}
