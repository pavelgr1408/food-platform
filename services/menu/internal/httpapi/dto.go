package httpapi

import (
	"fmt"

	"github.com/google/uuid"

	"github.com/pavelgr1408/food-platform/services/menu/internal/domain"
	"github.com/pavelgr1408/food-platform/services/menu/internal/service"
)

// money — Money из _common.yaml: целые копейки (ADR-13).
type money struct {
	Amount   int64  `json:"amount"`
	Currency string `json:"currency"`
}

type modifierDTO struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Price     money  `json:"price"`
	Available bool   `json:"available"`
}

type itemDTO struct {
	ID          string        `json:"id"`
	Name        string        `json:"name"`
	Description string        `json:"description,omitempty"`
	Price       money         `json:"price"`
	Available   bool          `json:"available"`
	Modifiers   []modifierDTO `json:"modifiers"`
}

type categoryDTO struct {
	ID    string    `json:"id"`
	Name  string    `json:"name"`
	Items []itemDTO `json:"items"`
}

type menuDTO struct {
	RestaurantID string        `json:"restaurantId"`
	MenuVersion  int64         `json:"menuVersion"`
	Categories   []categoryDTO `json:"categories"`
}

func toMenu(m domain.Menu) menuDTO {
	out := menuDTO{RestaurantID: m.RestaurantID, MenuVersion: m.Version, Categories: make([]categoryDTO, 0, len(m.Categories))}
	for _, c := range m.Categories {
		cd := categoryDTO{ID: c.ID, Name: c.Name, Items: make([]itemDTO, 0, len(c.Items))}
		for _, it := range c.Items {
			id := itemDTO{ID: it.ID, Name: it.Name, Description: it.Description, Price: money{it.Price, m.Currency},
				Available: it.Available, Modifiers: make([]modifierDTO, 0, len(it.Modifiers))}
			for _, md := range it.Modifiers {
				id.Modifiers = append(id.Modifiers, modifierDTO{md.ID, md.Name, money{md.Price, m.Currency}, md.Available})
			}
			cd.Items = append(cd.Items, id)
		}
		out.Categories = append(out.Categories, cd)
	}
	return out
}

type validationRequest struct {
	Items []struct {
		MenuItemID  string   `json:"menuItemId"`
		Quantity    int      `json:"quantity"`
		ModifierIDs []string `json:"modifierIds"`
	} `json:"items"`
}

// lines проверяет запрос по лимитам контракта и переводит в доменные строки.
// Второе значение — описание первой найденной ошибки (пусто, если всё корректно).
func (v validationRequest) lines() ([]domain.Line, string) {
	if n := len(v.Items); n < 1 || n > maxLines {
		return nil, fmt.Sprintf("items must contain 1..%d lines", maxLines)
	}
	out := make([]domain.Line, 0, len(v.Items))
	for i, it := range v.Items {
		if _, err := uuid.Parse(it.MenuItemID); err != nil {
			return nil, fmt.Sprintf("items[%d].menuItemId must be a UUID", i)
		}
		if it.Quantity < 1 || it.Quantity > maxQuantity {
			return nil, fmt.Sprintf("items[%d].quantity must be in 1..%d", i, maxQuantity)
		}
		if len(it.ModifierIDs) > maxModifiers {
			return nil, fmt.Sprintf("items[%d].modifierIds must contain at most %d ids", i, maxModifiers)
		}
		seen := map[string]bool{}
		for _, m := range it.ModifierIDs {
			if _, err := uuid.Parse(m); err != nil {
				return nil, fmt.Sprintf("items[%d].modifierIds must contain UUIDs", i)
			}
			if seen[m] {
				return nil, fmt.Sprintf("items[%d].modifierIds must not contain duplicates", i)
			}
			seen[m] = true
		}
		out = append(out, domain.Line{MenuItemID: it.MenuItemID, Quantity: it.Quantity, ModifierIDs: it.ModifierIDs})
	}
	return out, ""
}

type lineDTO struct {
	MenuItemID   string `json:"menuItemId"`
	Quantity     int    `json:"quantity"`
	Available    bool   `json:"available"`
	Name         string `json:"name,omitempty"`
	CurrentPrice *money `json:"currentPrice,omitempty"`
	LineTotal    *money `json:"lineTotal,omitempty"`
	ReasonCode   string `json:"reasonCode,omitempty"`
	Reason       string `json:"reason,omitempty"`
}

type validationDTO struct {
	Valid       bool      `json:"valid"`
	MenuVersion int64     `json:"menuVersion"`
	Lines       []lineDTO `json:"lines"`
	Total       money     `json:"total"`
}

func toValidation(v service.Validation) validationDTO {
	out := validationDTO{Valid: v.Valid, MenuVersion: v.MenuVersion, Total: money{v.Total, v.Currency},
		Lines: make([]lineDTO, 0, len(v.Lines))}
	for _, l := range v.Lines {
		d := lineDTO{MenuItemID: l.MenuItemID, Quantity: l.Quantity, Available: l.Available, Name: l.Name, ReasonCode: l.ReasonCode, Reason: l.Reason}
		if l.UnitPrice != nil {
			d.CurrentPrice = &money{*l.UnitPrice, v.Currency}
			d.LineTotal = &money{*l.LineTotal, v.Currency}
		}
		out.Lines = append(out.Lines, d)
	}
	return out
}
