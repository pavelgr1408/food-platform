package store

import (
	"context"
	"errors"
	"testing"

	"github.com/pavelgr1408/food-platform/pkg/pgxutil"
	"github.com/pavelgr1408/food-platform/pkg/pgxutil/pgtest"
	"github.com/pavelgr1408/food-platform/services/menu/internal/domain"
	"github.com/pavelgr1408/food-platform/services/menu/internal/seed"
	"github.com/pavelgr1408/food-platform/services/menu/migrations"
)

var ctx = context.Background()

const (
	r1      = "a1000000-0000-4000-8000-000000000001"
	r2      = "a1000000-0000-4000-8000-000000000002"
	noMenu  = "a1000000-0000-4000-8000-000000000006"
	unknown = "a1000000-0000-4000-8000-0000000000ff"
)

func newStore(t *testing.T) (*Store, []domain.Menu) {
	t.Helper()
	pool := pgtest.New(t)
	if err := pgxutil.Migrate(ctx, pool, nil, pgxutil.Source{Module: "menu", FS: migrations.FS}); err != nil {
		t.Fatal(err)
	}
	ms, err := seed.Menus()
	if err != nil {
		t.Fatal(err)
	}
	s := New(pool)
	if err := s.Upsert(ctx, ms); err != nil {
		t.Fatal(err)
	}
	return s, ms
}

func TestGetMenuMatchesSeedAndKeepsOrder(t *testing.T) {
	s, ms := newStore(t)
	got, err := s.GetMenu(ctx, r1)
	if err != nil {
		t.Fatal(err)
	}
	want := ms[0]
	if got.Version != 1 || got.Currency != "RUB" || len(got.Categories) != len(want.Categories) {
		t.Fatalf("got %+v", got)
	}
	for ci, c := range want.Categories {
		g := got.Categories[ci]
		if g.ID != c.ID || g.Name != c.Name || len(g.Items) != len(c.Items) {
			t.Fatalf("category %d: %+v vs %+v", ci, g, c)
		}
		for ii, it := range c.Items {
			gi := g.Items[ii]
			if gi.ID != it.ID || gi.Price != it.Price || gi.Available != it.Available || len(gi.Modifiers) != len(it.Modifiers) {
				t.Fatalf("item %q: %+v vs %+v", it.Name, gi, it)
			}
			for mi, md := range it.Modifiers {
				if gi.Modifiers[mi] != md {
					t.Fatalf("modifier: %+v vs %+v", gi.Modifiers[mi], md)
				}
			}
		}
	}
	if _, err := s.GetMenu(ctx, noMenu); !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
	if _, err := s.GetMenu(ctx, unknown); !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}

func find(m domain.Menu, name string) domain.Item {
	for _, c := range m.Categories {
		for _, it := range c.Items {
			if it.Name == name {
				return it
			}
		}
	}
	panic("no item " + name)
}

func TestLoadItemsAndValidateEndToEnd(t *testing.T) {
	s, _ := newStore(t)
	menu, _ := s.GetMenu(ctx, r1)
	burger := find(menu, "BLT Классик")
	brownie := find(menu, "Брауни") // в стоп-листе
	ghost := "00000000-0000-4000-8000-000000000000"

	ver, cur, items, err := s.LoadItems(ctx, r1, []string{burger.ID, brownie.ID, ghost})
	if err != nil {
		t.Fatal(err)
	}
	if ver != 1 || cur != "RUB" || len(items) != 2 || len(items[burger.ID].Modifiers) != 3 {
		t.Fatalf("ver=%d cur=%s items=%+v", ver, cur, items)
	}
	res := domain.Validate(items, []domain.Line{
		{MenuItemID: burger.ID, Quantity: 2, ModifierIDs: []string{burger.Modifiers[0].ID}},
		{MenuItemID: brownie.ID, Quantity: 1},
		{MenuItemID: ghost, Quantity: 1},
	})
	if res.Valid || res.Lines[0].ReasonCode != "" || res.Lines[1].ReasonCode != domain.ReasonItemUnavailable ||
		res.Lines[2].ReasonCode != domain.ReasonItemNotFound {
		t.Fatalf("%+v", res)
	}
	if res.Total != 2*(34900+8900) {
		t.Fatalf("total %d", res.Total)
	}
}

func TestLoadItemsIsolatedByRestaurant(t *testing.T) {
	s, _ := newStore(t)
	m1, _ := s.GetMenu(ctx, r1)
	burger1 := find(m1, "BLT Классик")
	// позиция r1 запрошена в контексте r2 → не найдена (ресторан — часть ключа)
	_, _, items, err := s.LoadItems(ctx, r2, []string{burger1.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("cross-restaurant leak: %+v", items)
	}
	if _, _, _, err := s.LoadItems(ctx, noMenu, []string{burger1.ID}); !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
	// пустой список → меню есть, позиций нет
	if _, _, items, err := s.LoadItems(ctx, r1, nil); err != nil || len(items) != 0 {
		t.Errorf("empty ids: %v %v", items, err)
	}
}

func TestUpsertBumpsVersionOnlyOnChange(t *testing.T) {
	s, ms := newStore(t)
	// повторный seed без изменений: версия прежняя
	if err := s.Upsert(ctx, ms); err != nil {
		t.Fatal(err)
	}
	m, _ := s.GetMenu(ctx, r1)
	if m.Version != 1 {
		t.Fatalf("idempotent seed must keep version, got %d", m.Version)
	}
	// изменение цены: версия +1, содержимое заменено
	changed := ms[0]
	changed.Categories = append([]domain.Category(nil), changed.Categories...)
	changed.Categories[0].Items = append([]domain.Item(nil), changed.Categories[0].Items...)
	changed.Categories[0].Items[0].Price = 35900
	if err := s.Upsert(ctx, []domain.Menu{changed}); err != nil {
		t.Fatal(err)
	}
	m, _ = s.GetMenu(ctx, r1)
	if m.Version != 2 || m.Categories[0].Items[0].Price != 35900 {
		t.Fatalf("after change: v=%d price=%d", m.Version, m.Categories[0].Items[0].Price)
	}
	// стоп-лист тоже меняет версию (ETag клиентов инвалидируется)
	changed.Categories[0].Items[0].Available = false
	if err := s.Upsert(ctx, []domain.Menu{changed}); err != nil {
		t.Fatal(err)
	}
	if m, _ = s.GetMenu(ctx, r1); m.Version != 3 || m.Categories[0].Items[0].Available {
		t.Fatalf("availability change: %+v", m.Version)
	}
}

func TestMoneyConstraints(t *testing.T) {
	pool := pgtest.New(t)
	_ = pgxutil.Migrate(ctx, pool, nil, pgxutil.Source{Module: "menu", FS: migrations.FS})
	s := New(pool)
	bad := domain.Menu{RestaurantID: r1, Currency: "RUB", Categories: []domain.Category{{ID: "c1000000-0000-4000-8000-000000000001", Name: "c",
		Items: []domain.Item{{ID: "d1000000-0000-4000-8000-000000000001", Name: "i", Price: -1, Available: true}}}}}
	if err := s.Upsert(ctx, []domain.Menu{bad}); err == nil {
		t.Fatal("negative price must be rejected by CHECK")
	}
}
