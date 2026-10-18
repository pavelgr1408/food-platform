// Package service — сценарии menu-service: получение меню и проверка позиций.
package service

import (
	"context"
	"errors"

	"github.com/pavelgr1408/food-platform/pkg/httpx"
	"github.com/pavelgr1408/food-platform/services/menu/internal/domain"
	"github.com/pavelgr1408/food-platform/services/menu/internal/store"
)

// Repository — то, что сервису нужно от хранилища.
type Repository interface {
	GetMenu(ctx context.Context, restaurantID string) (domain.Menu, error)
	LoadItems(ctx context.Context, restaurantID string, itemIDs []string) (int64, string, map[string]domain.Item, error)
}

// Service — сценарии меню.
type Service struct{ Repo Repository }

// New создаёт Service.
func New(repo Repository) *Service { return &Service{Repo: repo} }

var errNoMenu = httpx.NewError(404, "MENU_NOT_FOUND", "menu not found for restaurant")

// Menu возвращает меню ресторана.
func (s *Service) Menu(ctx context.Context, restaurantID string) (domain.Menu, error) {
	m, err := s.Repo.GetMenu(ctx, restaurantID)
	if errors.Is(err, store.ErrNotFound) {
		return domain.Menu{}, errNoMenu
	}
	return m, err
}

// Validation — результат проверки с версией и валютой меню.
type Validation struct {
	domain.Result
	MenuVersion int64
	Currency    string
}

// Validate проверяет строки против актуального меню одной выборкой из БД.
func (s *Service) Validate(ctx context.Context, restaurantID string, lines []domain.Line) (Validation, error) {
	seen := make(map[string]struct{}, len(lines))
	ids := make([]string, 0, len(lines))
	for _, l := range lines {
		if _, ok := seen[l.MenuItemID]; !ok {
			seen[l.MenuItemID] = struct{}{}
			ids = append(ids, l.MenuItemID)
		}
	}
	ver, cur, items, err := s.Repo.LoadItems(ctx, restaurantID, ids)
	if errors.Is(err, store.ErrNotFound) {
		return Validation{}, errNoMenu
	}
	if err != nil {
		return Validation{}, err
	}
	return Validation{Result: domain.Validate(items, lines), MenuVersion: ver, Currency: cur}, nil
}
