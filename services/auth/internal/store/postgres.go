package store

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pavelgr1408/food-platform/pkg/pgxutil"
	"github.com/pavelgr1408/food-platform/services/auth/internal/domain"
)

// Postgres — хранилище auth-service. Пул создаёт main (pkg/pgxutil).
type Postgres struct{ Pool *pgxpool.Pool }

// New оборачивает пул.
func New(pool *pgxpool.Pool) *Postgres { return &Postgres{Pool: pool} }

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
	return s.setAccess(ctx, q, user.ID, user.Roles, user.Restaurants)
}

// setAccess добавляет роли и привязки (идемпотентно: существующие не дублируются).
func (s *Postgres) setAccess(ctx context.Context, q pgx.Tx, id uuid.UUID, roles []domain.UserRole, restaurants []uuid.UUID) error {
	for _, role := range roles {
		if _, err := q.Exec(ctx, `INSERT INTO user_roles(user_id,role) VALUES($1,$2) ON CONFLICT DO NOTHING`, id, role); err != nil {
			return err
		}
	}
	for _, r := range restaurants {
		if _, err := q.Exec(ctx, `INSERT INTO user_restaurants(user_id,restaurant_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, id, r); err != nil {
			return err
		}
	}
	return nil
}

// EnsureUser создаёт пользователя, если его нет (seed). Существующего не меняет — ни пароль, ни статус;
// роли и привязки досыпает; если телефон занят другим пользователем — пропускает. Возвращает true, если пользователь создан.
func (s *Postgres) EnsureUser(ctx context.Context, user domain.User) (created bool, err error) {
	err = pgxutil.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `INSERT INTO users(id,phone,password_hash,status,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$5) ON CONFLICT DO NOTHING`,
			user.ID, user.Phone, user.PasswordHash, user.Status, user.CreatedAt)
		if err != nil {
			return err
		}
		created = tag.RowsAffected() == 1
		if !created {
			// Конфликт мог быть по телефону (его занял другой пользователь): тогда id в базе нет и
			// досыпать роли некому — seed пропускает такую запись, а не падает на FK.
			var exists bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id=$1)`, user.ID).Scan(&exists); err != nil || !exists {
				return err
			}
		}
		return s.setAccess(ctx, tx, user.ID, user.Roles, user.Restaurants)
	})
	return created, err
}

// UserExists — есть ли пользователь с таким id (seed не хэширует пароль зря).
func (s *Postgres) UserExists(ctx context.Context, id uuid.UUID) (bool, error) {
	var ok bool
	err := s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id=$1)`, id).Scan(&ok)
	return ok, err
}

// DeleteUsers удаляет пользователей (каскадом — роли, привязки, токены). Нужен, чтобы в prod не
// остались учётки из миграции 002 (dev-seed Java-версии).
func (s *Postgres) DeleteUsers(ctx context.Context, ids []uuid.UUID) (int64, error) {
	tag, err := s.Pool.Exec(ctx, `DELETE FROM users WHERE id = ANY($1)`, ids)
	return tag.RowsAffected(), err
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
	for rows.Next() {
		var role domain.UserRole
		if err := rows.Scan(&role); err != nil {
			rows.Close()
			return domain.User{}, err
		}
		u.Roles = append(u.Roles, role)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return domain.User{}, err
	}
	rows, err = q.Query(ctx, `SELECT restaurant_id FROM user_restaurants WHERE user_id=$1 ORDER BY restaurant_id`, u.ID)
	if err != nil {
		return domain.User{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return domain.User{}, err
		}
		u.Restaurants = append(u.Restaurants, id)
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
