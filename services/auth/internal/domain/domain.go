package domain

import (
	"errors"
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

type UserRole string

const (
	RoleCustomer UserRole = "CUSTOMER"
	RoleAdmin    UserRole = "ADMIN"
	RoleCourier  UserRole = "COURIER"
	RoleKitchen  UserRole = "KITCHEN"
)

type User struct {
	ID           uuid.UUID
	Phone        string
	PasswordHash string
	Status       UserStatus
	Roles        []UserRole
	CreatedAt    time.Time
	UpdatedAt    time.Time
	LastLoginAt  *time.Time
}

func (u User) Active() bool { return u.Status == StatusActive }

type RefreshToken struct {
	ID        uuid.UUID
	UserID    uuid.UUID
	TokenHash string
	ExpiresAt time.Time
	RevokedAt *time.Time
	CreatedAt time.Time
	DeviceID  *uuid.UUID
}

var (
	ErrDuplicatePhone      = errors.New("Phone is already registered")
	ErrInvalidCredentials  = errors.New("Invalid phone or password")
	ErrInvalidAccessToken  = errors.New("Invalid access token")
	ErrInvalidRefreshToken = errors.New("Invalid refresh token")
	ErrExpiredRefreshToken = errors.New("Refresh token expired")
	ErrUserNotActive       = errors.New("User is not active")
	ErrNotFound            = errors.New("not found")
)
