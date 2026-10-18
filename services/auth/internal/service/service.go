package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"time"

	"github.com/example/restaurant-auth-service/internal/domain"
	"github.com/example/restaurant-auth-service/internal/store"
	"github.com/example/restaurant-auth-service/internal/token"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
)

type Service struct {
	store      *store.Postgres
	tokens     *token.Manager
	refreshTTL time.Duration
	now        func() time.Time
}
type TokenPair struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	TokenType    string `json:"tokenType"`
	ExpiresIn    int64  `json:"expiresIn"`
}

func New(st *store.Postgres, tokens *token.Manager, refreshTTL time.Duration) *Service {
	return &Service{store: st, tokens: tokens, refreshTTL: refreshTTL, now: time.Now}
}

func (s *Service) Register(ctx context.Context, phone, password string) (domain.User, error) {
	tx, err := s.store.Begin(ctx)
	if err != nil {
		return domain.User{}, err
	}
	defer tx.Rollback(ctx)
	exists, err := s.store.PhoneExists(ctx, tx, phone)
	if err != nil {
		return domain.User{}, err
	}
	if exists {
		return domain.User{}, domain.ErrDuplicatePhone
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	if err != nil {
		return domain.User{}, err
	}
	now := s.now().UTC()
	u := domain.User{ID: uuid.New(), Phone: phone, PasswordHash: string(hash), Status: domain.StatusActive, Roles: []domain.UserRole{domain.RoleCustomer}, CreatedAt: now, UpdatedAt: now}
	if err := s.store.InsertUser(ctx, tx, u); err != nil {
		return domain.User{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.User{}, err
	}
	return u, nil
}

func (s *Service) Login(ctx context.Context, phone, password string) (TokenPair, error) {
	tx, err := s.store.Begin(ctx)
	if err != nil {
		return TokenPair{}, err
	}
	defer tx.Rollback(ctx)
	u, err := s.store.UserByPhone(ctx, tx, phone)
	if errors.Is(err, domain.ErrNotFound) {
		return TokenPair{}, domain.ErrInvalidCredentials
	}
	if err != nil {
		return TokenPair{}, err
	}
	if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)) != nil {
		return TokenPair{}, domain.ErrInvalidCredentials
	}
	if !u.Active() {
		return TokenPair{}, domain.ErrUserNotActive
	}
	now := s.now().UTC()
	if err := s.store.UpdateLogin(ctx, tx, u.ID, now); err != nil {
		return TokenPair{}, err
	}
	pair, err := s.issue(ctx, tx, u, now)
	if err != nil {
		return TokenPair{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return TokenPair{}, err
	}
	return pair, nil
}

func (s *Service) Refresh(ctx context.Context, plain string) (TokenPair, error) {
	tx, err := s.store.Begin(ctx)
	if err != nil {
		return TokenPair{}, err
	}
	defer tx.Rollback(ctx)
	now := s.now().UTC()
	current, u, err := s.validRefresh(ctx, tx, plain, now)
	if err != nil {
		return TokenPair{}, err
	}
	if err := s.store.RevokeRefresh(ctx, tx, current.ID, now); err != nil {
		return TokenPair{}, err
	}
	pair, err := s.issue(ctx, tx, u, now)
	if err != nil {
		return TokenPair{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return TokenPair{}, err
	}
	return pair, nil
}

func (s *Service) Logout(ctx context.Context, plain string) error {
	tx, err := s.store.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	now := s.now().UTC()
	current, _, err := s.validRefresh(ctx, tx, plain, now)
	if err != nil {
		return err
	}
	if err := s.store.RevokeRefresh(ctx, tx, current.ID, now); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) Me(ctx context.Context, id uuid.UUID) (domain.User, error) {
	u, err := s.store.UserByID(ctx, id)
	if errors.Is(err, domain.ErrNotFound) {
		return domain.User{}, domain.ErrInvalidAccessToken
	}
	if err != nil {
		return domain.User{}, err
	}
	if !u.Active() {
		return domain.User{}, domain.ErrUserNotActive
	}
	return u, nil
}

func (s *Service) Introspect(ctx context.Context, raw string) (domain.User, bool) {
	id, err := s.tokens.Validate(raw)
	if err != nil {
		return domain.User{}, false
	}
	u, err := s.store.UserByID(ctx, id)
	if err != nil || !u.Active() {
		return domain.User{}, false
	}
	return u, true
}
func (s *Service) ValidateAccess(raw string) (uuid.UUID, error) { return s.tokens.Validate(raw) }
func (s *Service) JWKS() map[string]any                         { return s.tokens.JWKS() }

func (s *Service) validRefresh(ctx context.Context, tx pgx.Tx, plain string, now time.Time) (domain.RefreshToken, domain.User, error) {
	t, u, err := s.store.RefreshForUpdate(ctx, tx, refreshHash(plain))
	if errors.Is(err, domain.ErrNotFound) {
		return t, u, domain.ErrInvalidRefreshToken
	}
	if err != nil {
		return t, u, err
	}
	if t.RevokedAt != nil {
		return t, u, domain.ErrInvalidRefreshToken
	}
	if !t.ExpiresAt.After(now) {
		return t, u, domain.ErrExpiredRefreshToken
	}
	if !u.Active() {
		return t, u, domain.ErrUserNotActive
	}
	return t, u, nil
}

func (s *Service) issue(ctx context.Context, tx pgx.Tx, u domain.User, now time.Time) (TokenPair, error) {
	access, err := s.tokens.Issue(u)
	if err != nil {
		return TokenPair{}, err
	}
	bytes := make([]byte, 64)
	if _, err := rand.Read(bytes); err != nil {
		return TokenPair{}, err
	}
	plain := base64.RawURLEncoding.EncodeToString(bytes)
	t := domain.RefreshToken{ID: uuid.New(), UserID: u.ID, TokenHash: refreshHash(plain), ExpiresAt: now.Add(s.refreshTTL), CreatedAt: now}
	if err := s.store.InsertRefresh(ctx, tx, t); err != nil {
		return TokenPair{}, err
	}
	return TokenPair{AccessToken: access, RefreshToken: plain, TokenType: "Bearer", ExpiresIn: s.tokens.ExpiresIn()}, nil
}

func refreshHash(plain string) string {
	sum := sha256.Sum256([]byte(plain))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
