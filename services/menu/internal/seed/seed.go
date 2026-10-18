// Package seed — стартовые меню (dev/demo). CRUD-API у сервиса нет, поэтому меню загружаются
// при старте идемпотентным upsert'ом (SEED_ON_START=true). Идентификаторы категорий, позиций и
// модификаторов детерминированы (UUIDv5 от restaurantId и ключа) — стабильны между перезапусками.
package seed

import (
	_ "embed"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"

	"github.com/pavelgr1408/food-platform/services/menu/internal/domain"
)

//go:embed menus.json
var menusJSON []byte

// namespace — фиксированное пространство имён для UUIDv5 (не секрет).
var namespace = uuid.MustParse("6b1f4a52-7c3e-4e1b-9d2a-0b7e5c9a1f10")

type file struct {
	Templates map[string][]struct {
		Category string `json:"category"`
		Items    []struct {
			Key         string `json:"key"`
			Name        string `json:"name"`
			Description string `json:"description"`
			Price       int64  `json:"price"`
			Modifiers   []struct {
				Key   string `json:"key"`
				Name  string `json:"name"`
				Price int64  `json:"price"`
			} `json:"modifiers"`
		} `json:"items"`
	} `json:"templates"`
	Restaurants []struct {
		RestaurantID string   `json:"restaurantId"`
		Template     string   `json:"template"`
		Unavailable  []string `json:"unavailable"`
	} `json:"restaurants"`
}

// ID возвращает детерминированный идентификатор сущности меню ресторана.
func ID(restaurantID, kind, key string) string {
	return uuid.NewSHA1(namespace, []byte(restaurantID+"/"+kind+"/"+key)).String()
}

// Menus разбирает и валидирует встроенный справочник.
func Menus() ([]domain.Menu, error) { return Parse(menusJSON) }

// Parse разбирает JSON справочника (экспортирована для тестов).
func Parse(data []byte) ([]domain.Menu, error) {
	var f file
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("seed: %w", err)
	}
	var out []domain.Menu
	for _, r := range f.Restaurants {
		if _, err := uuid.Parse(r.RestaurantID); err != nil {
			return nil, fmt.Errorf("seed: bad restaurantId %q: %w", r.RestaurantID, err)
		}
		tpl, ok := f.Templates[r.Template]
		if !ok {
			return nil, fmt.Errorf("seed %s: unknown template %q", r.RestaurantID, r.Template)
		}
		off := map[string]bool{}
		for _, k := range r.Unavailable {
			off[k] = true
		}
		used := map[string]bool{}
		m := domain.Menu{RestaurantID: r.RestaurantID, Currency: "RUB"}
		for _, c := range tpl {
			cat := domain.Category{ID: ID(r.RestaurantID, "category", c.Category), Name: c.Category}
			for _, it := range c.Items {
				if it.Price < 0 {
					return nil, fmt.Errorf("seed %s: item %q has negative price", r.RestaurantID, it.Key)
				}
				used[it.Key] = true
				item := domain.Item{ID: ID(r.RestaurantID, "item", it.Key), Name: it.Name, Description: it.Description,
					Price: it.Price, Available: !off[it.Key]}
				for _, md := range it.Modifiers {
					if md.Price < 0 {
						return nil, fmt.Errorf("seed %s: modifier %q has negative price", r.RestaurantID, md.Key)
					}
					used[md.Key] = true
					item.Modifiers = append(item.Modifiers, domain.Modifier{
						ID: ID(r.RestaurantID, "modifier", it.Key+"/"+md.Key), Name: md.Name, Price: md.Price, Available: !off[md.Key]})
				}
				cat.Items = append(cat.Items, item)
			}
			m.Categories = append(m.Categories, cat)
		}
		for k := range off {
			if !used[k] {
				return nil, fmt.Errorf("seed %s: unavailable key %q is not in template %q", r.RestaurantID, k, r.Template)
			}
		}
		out = append(out, m)
	}
	return out, nil
}
