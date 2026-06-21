package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/example/restaurant-auth-service/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Postgres struct{ Pool *pgxpool.Pool }

func Open(ctx context.Context, url string) (*Postgres, error) {
	config, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse database URL: %w", err)
	}
	config.MaxConns, config.MinConns, config.MaxConnLifetime = 20, 2, time.Hour
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return &Postgres{Pool: pool}, nil
}

func (s *Postgres) Close()                                    { s.Pool.Close() }
func (s *Postgres) Ping(ctx context.Context) error            { return s.Pool.Ping(ctx) }
func (s *Postgres) Begin(ctx context.Context) (pgx.Tx, error) { return s.Pool.Begin(ctx) }

func (s *Postgres) PhoneExists(ctx context.Context, q pgx.Tx, phone string) (bool, error) {
	var exists bool
	err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE phone=$1)`, phone).Scan(&exists)
	return exists, err
}

func (s *Postgres) InsertUser(ctx context.Context, q pgx.Tx, user domain.User) error {
	_, err := q.Exec(ctx, `INSERT INTO users(id,phone,password_hash,status,created_at,updated_at,last_login_at) VALUES($1,$2,$3,$4,$5,$6,$7)`, user.ID, user.Phone, user.PasswordHash, user.Status, user.CreatedAt, user.UpdatedAt, user.LastLoginAt)
	if err != nil {
		return err
	}
	for _, role := range user.Roles {
		if _, err = q.Exec(ctx, `INSERT INTO user_roles(user_id,role) VALUES($1,$2)`, user.ID, role); err != nil {
			return err
		}
	}
	return nil
}

func (s *Postgres) UserByPhone(ctx context.Context, q pgx.Tx, phone string) (domain.User, error) {
	return userBy(ctx, q, `SELECT id,phone,password_hash,status,created_at,updated_at,last_login_at FROM users WHERE phone=$1`, phone)
}

func (s *Postgres) UserByID(ctx context.Context, id uuid.UUID) (domain.User, error) {
	return userBy(ctx, s.Pool, `SELECT id,phone,password_hash,status,created_at,updated_at,last_login_at FROM users WHERE id=$1`, id)
}

type querier interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

func userBy(ctx context.Context, q querier, query string, arg any) (domain.User, error) {
	var u domain.User
	err := q.QueryRow(ctx, query, arg).Scan(&u.ID, &u.Phone, &u.PasswordHash, &u.Status, &u.CreatedAt, &u.UpdatedAt, &u.LastLoginAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.User{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.User{}, err
	}
	rows, err := q.Query(ctx, `SELECT role FROM user_roles WHERE user_id=$1 ORDER BY role`, u.ID)
	if err != nil {
		return domain.User{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var role domain.UserRole
		if err := rows.Scan(&role); err != nil {
			return domain.User{}, err
		}
		u.Roles = append(u.Roles, role)
	}
	return u, rows.Err()
}

func (s *Postgres) UpdateLogin(ctx context.Context, q pgx.Tx, id uuid.UUID, now time.Time) error {
	_, err := q.Exec(ctx, `UPDATE users SET last_login_at=$2,updated_at=$2 WHERE id=$1`, id, now)
	return err
}

func (s *Postgres) InsertRefresh(ctx context.Context, q pgx.Tx, t domain.RefreshToken) error {
	_, err := q.Exec(ctx, `INSERT INTO refresh_tokens(id,user_id,token_hash,expires_at,revoked_at,created_at,device_id) VALUES($1,$2,$3,$4,$5,$6,$7)`, t.ID, t.UserID, t.TokenHash, t.ExpiresAt, t.RevokedAt, t.CreatedAt, t.DeviceID)
	return err
}

func (s *Postgres) RefreshForUpdate(ctx context.Context, q pgx.Tx, hash string) (domain.RefreshToken, domain.User, error) {
	var t domain.RefreshToken
	err := q.QueryRow(ctx, `SELECT id,user_id,token_hash,expires_at,revoked_at,created_at,device_id FROM refresh_tokens WHERE token_hash=$1 FOR UPDATE`, hash).Scan(&t.ID, &t.UserID, &t.TokenHash, &t.ExpiresAt, &t.RevokedAt, &t.CreatedAt, &t.DeviceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return t, domain.User{}, domain.ErrNotFound
	}
	if err != nil {
		return t, domain.User{}, err
	}
	u, err := userBy(ctx, q, `SELECT id,phone,password_hash,status,created_at,updated_at,last_login_at FROM users WHERE id=$1`, t.UserID)
	return t, u, err
}

func (s *Postgres) RevokeRefresh(ctx context.Context, q pgx.Tx, id uuid.UUID, now time.Time) error {
	_, err := q.Exec(ctx, `UPDATE refresh_tokens SET revoked_at=$2 WHERE id=$1`, id, now)
	return err
}
