// Package service — сценарии каталога ресторанов: поиск (UC-FND-01) и карточка ресторана.
package service

import (
	"context"
	"errors"
	"time"

	"github.com/pavelgr1408/food-platform/pkg/httpx"
	"github.com/pavelgr1408/food-platform/services/restaurant/internal/domain"
	"github.com/pavelgr1408/food-platform/services/restaurant/internal/geocode"
	"github.com/pavelgr1408/food-platform/services/restaurant/internal/store"
)

// Границы параметров поиска (контракт restaurant.yaml).
const (
	DefaultRadiusM = 5000
	MinRadiusM     = 100
	MaxRadiusM     = 50000
)

// Подсказки при пустом результате (A2/E2).
const (
	HintPickup       = "pickup"
	HintExpandRadius = "expand_radius"
	HintChangeAddr   = "change_address"
)

// Repository — то, что сервису нужно от хранилища.
type Repository interface {
	Get(ctx context.Context, id string, excFrom time.Time) (domain.Restaurant, error)
	Candidates(ctx context.Context, center *domain.Point, radiusM int, excFrom, excTo time.Time) ([]domain.Restaurant, error)
}

// Service — сценарии каталога.
type Service struct {
	Repo Repository
	Now  func() time.Time
}

// New создаёт Service с системными часами.
func New(repo Repository) *Service { return &Service{Repo: repo, Now: time.Now} }

// Params — параметры поиска (уже провалидированные httpapi).
type Params struct {
	Center       *domain.Point // lat/lon, если заданы
	Address      string        // используется, если Center == nil
	DeliveryOnly bool
	OpenNow      bool
	RadiusM      int
	Limit        int
	Offset       int
}

// Item — ресторан в выдаче с вычисленными признаками.
type Item struct {
	domain.Restaurant
	DeliveryAvailable bool
	OpenNow           bool
}

// Result — страница выдачи.
type Result struct {
	Items  []Item
	Total  int
	Center *domain.Point
	Hint   string
}

// Search подбирает рестораны. Фильтры delivery/openNow применяются в Go поверх кандидатов
// радиуса: число ресторанов в радиусе мало (см. README), зато `total` точный и можно
// отдать подсказку `pickup`.
func (s *Service) Search(ctx context.Context, p Params) (Result, error) {
	now := s.Now()
	center := p.Center
	if center == nil && p.Address != "" {
		_, c, ok := geocode.Resolve(p.Address)
		if !ok {
			return Result{Items: []Item{}, Hint: HintChangeAddr}, nil
		}
		center = &c
	}

	// Исключения расписания: окно ±2 суток покрывает любой часовой пояс и «вчера» для ночных интервалов.
	cands, err := s.Repo.Candidates(ctx, center, p.RadiusM, now.AddDate(0, 0, -2), now.AddDate(0, 0, 2))
	if err != nil {
		return Result{}, err
	}

	var matched, nonDelivery []Item
	for _, r := range cands {
		it := Item{Restaurant: r, OpenNow: r.Schedule.IsOpen(now)}
		it.DeliveryAvailable = r.DeliveryEnabled
		if center != nil && r.DistanceM != nil {
			it.DeliveryAvailable = r.DeliveryEnabled && *r.DistanceM <= r.DeliveryRadiusM
		}
		if p.OpenNow && !it.OpenNow {
			continue
		}
		if p.DeliveryOnly && !it.DeliveryAvailable {
			nonDelivery = append(nonDelivery, it)
			continue
		}
		matched = append(matched, it)
	}

	res := Result{Total: len(matched), Center: center, Items: []Item{}}
	if p.Offset < len(matched) {
		end := min(p.Offset+p.Limit, len(matched))
		res.Items = matched[p.Offset:end]
	}
	if len(matched) == 0 {
		switch {
		case p.DeliveryOnly && len(nonDelivery) > 0:
			res.Hint = HintPickup
		case center != nil && p.RadiusM < MaxRadiusM:
			res.Hint = HintExpandRadius
		case center != nil:
			res.Hint = HintChangeAddr
		}
	}
	return res, nil
}

// Get возвращает карточку активного ресторана.
func (s *Service) Get(ctx context.Context, id string) (Item, error) {
	now := s.Now()
	r, err := s.Repo.Get(ctx, id, now.AddDate(0, 0, -2))
	if errors.Is(err, store.ErrNotFound) {
		return Item{}, httpx.NewError(404, "RESTAURANT_NOT_FOUND", "restaurant not found")
	}
	if err != nil {
		return Item{}, err
	}
	return Item{Restaurant: r, OpenNow: r.Schedule.IsOpen(now), DeliveryAvailable: r.DeliveryEnabled}, nil
}
