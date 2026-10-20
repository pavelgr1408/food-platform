package domain

import (
	"errors"
	"slices"
	"time"

	"github.com/google/uuid"
)

type UserStatus string

const (
	StatusActive              UserStatus = "ACTIVE"
	StatusBlocked             UserStatus = "BLOCKED"
	StatusDeleted             UserStatus = "DELETED"
	StatusPendingVerification UserStatus = "PENDING_VERIFICATION"
)

// UserRole — роль платформы (auth.yaml: Role). Курьер и кухня — внешние системы, ролей нет.
type UserRole string

const (
	RoleCustomer       UserRole = "CUSTOMER"
	RoleStaff          UserRole = "STAFF"
	RoleFranchiseOwner UserRole = "FRANCHISE_OWNER"
	RoleHQStaff        UserRole = "HQ_STAFF"
)

// Valid — роль входит в целевой набор.
func (r UserRole) Valid() bool {
	switch r {
	case RoleCustomer, RoleStaff, RoleFranchiseOwner, RoleHQStaff:
		return true
	}
	return false
}

type User struct {
	ID           uuid.UUID
	Phone        string
	PasswordHash string
	Status       UserStatus
	Roles        []UserRole
	Restaurants  []uuid.UUID // привязка STAFF/FRANCHISE_OWNER к ресторанам (user_restaurants)
	CreatedAt    time.Time
	UpdatedAt    time.Time
	LastLoginAt  *time.Time
}

func (u User) Active() bool { return u.Status == StatusActive }

// HasRole — у пользователя есть хотя бы одна из ролей.
func (u User) HasRole(roles ...UserRole) bool {
	for _, r := range roles {
		if slices.Contains(u.Roles, r) {
			return true
		}
	}
	return false
}

// BoundTo — пользователь привязан к ресторану.
func (u User) BoundTo(id uuid.UUID) bool { return slices.Contains(u.Restaurants, id) }

type RefreshToken struct {
	ID        uuid.UUID
	UserID    uuid.UUID
	TokenHash string
	ExpiresAt time.Time
	RevokedAt *time.Time
	CreatedAt time.Time
	DeviceID  *uuid.UUID
}

// Ошибки домена; httpapi сопоставляет их с кодами problem (auth.yaml).
var (
	ErrDuplicatePhone      = errors.New("Phone is already registered")
	ErrInvalidCredentials  = errors.New("Invalid phone or password")
	ErrInvalidAccessToken  = errors.New("Invalid access token")
	ErrInvalidRefreshToken = errors.New("Invalid refresh token")
	ErrExpiredRefreshToken = errors.New("Refresh token expired")
	ErrUserNotActive       = errors.New("User is not active")
	ErrNotFound            = errors.New("not found")
)
