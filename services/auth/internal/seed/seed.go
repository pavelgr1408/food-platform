// Package seed — dev-пользователи всех ролей платформы (SEED_ON_START=true, только вне prod).
//
// Пароли здесь — публичные dev-константы, как в README. Идемпотентно: существующий пользователь не
// меняется (ни пароль, ни статус), роли и привязки досыпаются. Хэш считается только для нового.
package seed

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/pavelgr1408/food-platform/services/auth/internal/domain"
	"github.com/pavelgr1408/food-platform/services/auth/internal/service"
	"github.com/pavelgr1408/food-platform/services/auth/internal/store"
)

// Рестораны seed-справочника restaurant-service (a1000000-…-00000000000N).
var (
	restaurant1 = uuid.MustParse("a1000000-0000-4000-8000-000000000001") // Москва, Тверская
	restaurant4 = uuid.MustParse("a1000000-0000-4000-8000-000000000004") // Санкт-Петербург, Невский (ресторан Postman-сценариев)
)

// User — описание dev-пользователя.
type User struct {
	ID          uuid.UUID
	Phone       string
	Password    string
	Status      domain.UserStatus
	Roles       []domain.UserRole
	Restaurants []uuid.UUID
}

func id(n int) uuid.UUID { return uuid.MustParse(fmt.Sprintf("00000000-0000-0000-0000-%012d", n)) }

// LegacyDevIDs — учётки из миграции 002 (наследие Java-версии), которые создаются при любом окружении;
// в prod их удаляют при старте.
func LegacyDevIDs() []uuid.UUID { return []uuid.UUID{id(1), id(2), id(3)} }

// Users — dev-набор. Первые три совпадают с учётками из миграции 002 (те же id и телефоны):
// ADMIN стал HQ_STAFF. Остальные — по одной на роль и по ресторану.
func Users() []User {
	return []User{
		{id(1), "+79990000000", "123456", domain.StatusActive, []domain.UserRole{domain.RoleCustomer}, nil},
		{id(2), "+79991111111", "admin123", domain.StatusActive, []domain.UserRole{domain.RoleHQStaff}, nil},
		{id(3), "+79992222222", "blocked123", domain.StatusBlocked, []domain.UserRole{domain.RoleCustomer}, nil},
		{id(4), "+79993333333", "staff123", domain.StatusActive, []domain.UserRole{domain.RoleStaff}, []uuid.UUID{restaurant4}},
		{id(5), "+79994444444", "staff2123", domain.StatusActive, []domain.UserRole{domain.RoleStaff}, []uuid.UUID{restaurant1}},
		{id(6), "+79995555555", "owner123", domain.StatusActive, []domain.UserRole{domain.RoleFranchiseOwner}, []uuid.UUID{restaurant4}},
	}
}

// Apply досыпает dev-пользователей.
func Apply(ctx context.Context, st *store.Postgres, svc *service.Service, log *slog.Logger) error {
	created := 0
	for _, u := range Users() {
		hash := ""
		exists, err := st.UserExists(ctx, u.ID)
		if err != nil {
			return err
		}
		if !exists {
			if hash, err = svc.HashPassword(u.Password); err != nil {
				return err
			}
		}
		now := time.Now().UTC()
		ok, err := st.EnsureUser(ctx, domain.User{ID: u.ID, Phone: u.Phone, PasswordHash: hash, Status: u.Status,
			Roles: u.Roles, Restaurants: u.Restaurants, CreatedAt: now, UpdatedAt: now})
		if err != nil {
			return fmt.Errorf("seed %s: %w", u.Phone, err)
		}
		if ok {
			created++
		}
	}
	log.Info("seed applied", "users", len(Users()), "created", created)
	return nil
}
