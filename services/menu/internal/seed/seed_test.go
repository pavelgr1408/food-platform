package seed

import (
	"strings"
	"testing"
)

func TestEmbeddedSeed(t *testing.T) {
	ms, err := Menus()
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 5 {
		t.Fatalf("menus: %d", len(ms))
	}
	ids := map[string]bool{}
	sawUnavailable := false
	for _, m := range ms {
		for _, c := range m.Categories {
			for _, it := range c.Items {
				if ids[it.ID] {
					t.Fatalf("duplicate id %s", it.ID)
				}
				ids[it.ID] = true
				sawUnavailable = sawUnavailable || !it.Available
				for _, md := range it.Modifiers {
					if ids[md.ID] {
						t.Fatalf("duplicate modifier id %s", md.ID)
					}
					ids[md.ID] = true
				}
			}
		}
	}
	if !sawUnavailable {
		t.Error("seed must contain a stop-list item (E1 scenario)")
	}
}

func TestIDsAreDeterministic(t *testing.T) {
	a := ID("r", "item", "x")
	if a != ID("r", "item", "x") || a == ID("r2", "item", "x") || a == ID("r", "category", "x") {
		t.Fatal("ID must be deterministic and scoped")
	}
}

func TestParseRejectsBad(t *testing.T) {
	for i, b := range []string{
		`{"templates":{},"restaurants":[{"restaurantId":"a1000000-0000-4000-8000-000000000001","template":"nope"}]}`,
		`{"templates":{"t":[]},"restaurants":[{"restaurantId":"bad","template":"t"}]}`,
		`{"templates":{"t":[{"category":"c","items":[{"key":"k","name":"n","price":-1}]}]},"restaurants":[{"restaurantId":"a1000000-0000-4000-8000-000000000001","template":"t"}]}`,
		`{"templates":{"t":[{"category":"c","items":[{"key":"k","name":"n","price":1}]}]},"restaurants":[{"restaurantId":"a1000000-0000-4000-8000-000000000001","template":"t","unavailable":["typo"]}]}`,
	} {
		if _, err := Parse([]byte(b)); err == nil {
			t.Errorf("case %d: expected error", i)
		}
	}
}

func TestSeedImageURLs(t *testing.T) {
	ms, err := Menus()
	if err != nil {
		t.Fatal(err)
	}
	withImage, without := 0, 0
	for _, m := range ms {
		for _, c := range m.Categories {
			for _, it := range c.Items {
				if it.ImageURL == "" {
					without++
					continue
				}
				withImage++
				if !strings.HasPrefix(it.ImageURL, "https://") {
					t.Errorf("%s: imageUrl %q is not https", it.Name, it.ImageURL)
				}
			}
		}
	}
	if withImage == 0 {
		t.Error("seed must contain imageUrl")
	}
	_ = without
}

func TestParseRejectsBadImageURL(t *testing.T) {
	for _, u := range []string{"http://x.example/a.jpg", "javascript:alert(1)", "https://x.example/a b.jpg"} {
		b := `{"templates":{"t":[{"category":"c","items":[{"key":"k","name":"n","imageUrl":"` + u + `","price":1}]}]},"restaurants":[{"restaurantId":"a1000000-0000-4000-8000-000000000001","template":"t"}]}`
		if _, err := Parse([]byte(b)); err == nil {
			t.Errorf("imageUrl %q must be rejected", u)
		}
	}
}
