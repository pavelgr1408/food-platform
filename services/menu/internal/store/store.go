// Package store — доступ к menu-db (pgx). Оба чтения — одним запросом (один снимок данных).
package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pavelgr1408/food-platform/pkg/pgxutil"
	"github.com/pavelgr1408/food-platform/services/menu/internal/domain"
)

// ErrNotFound — у ресторана нет меню.
var ErrNotFound = errors.New("menu not found")

// Store — хранилище меню.
type Store struct{ pool *pgxpool.Pool }

// New создаёт Store.
func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// GetMenu возвращает меню целиком: категории и позиции в порядке `position`.
func (s *Store) GetMenu(ctx context.Context, restaurantID string) (domain.Menu, error) {
	rows, err := s.pool.Query(ctx, `
SELECT mn.version, mn.currency,
       c.id::text, c.name,
       i.id::text, i.name, i.description, i.price, i.available,
       md.id::text, md.name, md.price, md.available
FROM menus mn
LEFT JOIN categories c ON c.restaurant_id = mn.restaurant_id
LEFT JOIN items i      ON i.category_id = c.id
LEFT JOIN modifiers md ON md.item_id = i.id
WHERE mn.restaurant_id = $1
ORDER BY c.position, c.id, i.position, i.id, md.position, md.id`, restaurantID)
	if err != nil {
		return domain.Menu{}, err
	}
	defer rows.Close()

	m := domain.Menu{RestaurantID: restaurantID}
	found := false
	for rows.Next() {
		var (
			cID, cName, iID, iName, iDesc, mID, mName *string
			iPrice, mPrice                            *int64
			iAvail, mAvail                            *bool
		)
		if err := rows.Scan(&m.Version, &m.Currency, &cID, &cName, &iID, &iName, &iDesc, &iPrice, &iAvail,
			&mID, &mName, &mPrice, &mAvail); err != nil {
			return domain.Menu{}, err
		}
		found = true
		if cID == nil {
			continue // меню без категорий
		}
		if n := len(m.Categories); n == 0 || m.Categories[n-1].ID != *cID {
			m.Categories = append(m.Categories, domain.Category{ID: *cID, Name: *cName})
		}
		cat := &m.Categories[len(m.Categories)-1]
		if iID == nil {
			continue
		}
		if n := len(cat.Items); n == 0 || cat.Items[n-1].ID != *iID {
			cat.Items = append(cat.Items, domain.Item{ID: *iID, Name: *iName, Description: *iDesc, Price: *iPrice, Available: *iAvail})
		}
		it := &cat.Items[len(cat.Items)-1]
		if mID != nil {
			it.Modifiers = append(it.Modifiers, domain.Modifier{ID: *mID, Name: *mName, Price: *mPrice, Available: *mAvail})
		}
	}
	if err := rows.Err(); err != nil {
		return domain.Menu{}, err
	}
	if !found {
		return domain.Menu{}, ErrNotFound
	}
	return m, nil
}

// LoadItems — горячий путь validateMenuItems: одна выборка позиций ресторана по списку id
// вместе со всеми их модификаторами, версией и валютой меню. Ненайденные id в map отсутствуют.
func (s *Store) LoadItems(ctx context.Context, restaurantID string, itemIDs []string) (version int64, currency string, items map[string]domain.Item, err error) {
	rows, err := s.pool.Query(ctx, `
SELECT mn.version, mn.currency,
       i.id::text, i.name, i.price, i.available,
       md.id::text, md.name, md.price, md.available
FROM menus mn
LEFT JOIN items i      ON i.restaurant_id = mn.restaurant_id AND i.id = ANY($2::uuid[])
LEFT JOIN modifiers md ON md.item_id = i.id
WHERE mn.restaurant_id = $1
ORDER BY i.id, md.position, md.id`, restaurantID, itemIDs)
	if err != nil {
		return 0, "", nil, err
	}
	defer rows.Close()

	items = make(map[string]domain.Item, len(itemIDs))
	found := false
	for rows.Next() {
		var (
			iID, iName, mID, mName *string
			iPrice, mPrice         *int64
			iAvail, mAvail         *bool
		)
		if err := rows.Scan(&version, &currency, &iID, &iName, &iPrice, &iAvail, &mID, &mName, &mPrice, &mAvail); err != nil {
			return 0, "", nil, err
		}
		found = true
		if iID == nil {
			continue
		}
		it, ok := items[*iID]
		if !ok {
			it = domain.Item{ID: *iID, Name: *iName, Price: *iPrice, Available: *iAvail}
		}
		if mID != nil {
			it.Modifiers = append(it.Modifiers, domain.Modifier{ID: *mID, Name: *mName, Price: *mPrice, Available: *mAvail})
		}
		items[*iID] = it
	}
	if err := rows.Err(); err != nil {
		return 0, "", nil, err
	}
	if !found {
		return 0, "", nil, ErrNotFound
	}
	return version, currency, items, nil
}

// hash — стабильный отпечаток содержимого меню (без версии).
func hash(m domain.Menu) (string, error) {
	type view struct {
		Currency   string
		Categories []domain.Category
	}
	b, err := json.Marshal(view{m.Currency, m.Categories})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// Upsert идемпотентно записывает меню (seed). Если содержимое не изменилось (по content_hash) —
// ничего не трогает и версию не увеличивает; иначе заменяет категории/позиции/модификаторы и
// увеличивает version. Одна транзакция на меню.
func (s *Store) Upsert(ctx context.Context, menus []domain.Menu) error {
	for _, m := range menus {
		h, err := hash(m)
		if err != nil {
			return err
		}
		err = pgxutil.InTx(ctx, s.pool, func(tx pgx.Tx) error {
			var old string
			err := tx.QueryRow(ctx, `SELECT content_hash FROM menus WHERE restaurant_id = $1 FOR UPDATE`, m.RestaurantID).Scan(&old)
			switch {
			case errors.Is(err, pgx.ErrNoRows):
				if _, err := tx.Exec(ctx, `INSERT INTO menus (restaurant_id, currency, content_hash) VALUES ($1, $2, $3)`,
					m.RestaurantID, m.Currency, h); err != nil {
					return err
				}
			case err != nil:
				return err
			case old == h:
				return nil
			default:
				if _, err := tx.Exec(ctx, `DELETE FROM categories WHERE restaurant_id = $1`, m.RestaurantID); err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `UPDATE menus SET version = version + 1, currency = $2, content_hash = $3, updated_at = now()
					WHERE restaurant_id = $1`, m.RestaurantID, m.Currency, h); err != nil {
					return err
				}
			}
			for ci, c := range m.Categories {
				if _, err := tx.Exec(ctx, `INSERT INTO categories (id, restaurant_id, name, position) VALUES ($1, $2, $3, $4)`,
					c.ID, m.RestaurantID, c.Name, ci); err != nil {
					return fmt.Errorf("category %q: %w", c.Name, err)
				}
				for ii, it := range c.Items {
					if _, err := tx.Exec(ctx, `INSERT INTO items (id, restaurant_id, category_id, name, description, price, available, position)
						VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
						it.ID, m.RestaurantID, c.ID, it.Name, it.Description, it.Price, it.Available, ii); err != nil {
						return fmt.Errorf("item %q: %w", it.Name, err)
					}
					for mi, md := range it.Modifiers {
						if _, err := tx.Exec(ctx, `INSERT INTO modifiers (id, item_id, name, price, available, position)
							VALUES ($1, $2, $3, $4, $5, $6)`, md.ID, it.ID, md.Name, md.Price, md.Available, mi); err != nil {
							return fmt.Errorf("modifier %q: %w", md.Name, err)
						}
					}
				}
			}
			return nil
		})
		if err != nil {
			return fmt.Errorf("upsert menu %s: %w", m.RestaurantID, err)
		}
	}
	return nil
}
