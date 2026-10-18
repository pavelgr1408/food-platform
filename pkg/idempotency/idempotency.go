// Package idempotency — идемпотентность создающих запросов по заголовку Idempotency-Key.
//
// Контракт: повтор запроса с тем же ключом и тем же телом возвращает ТОТ ЖЕ ответ
// (заголовок Idempotent-Replay: true) без повторных побочных эффектов; тот же ключ
// с другим телом → 422; параллельный дубликат, пока первый ещё выполняется → 409
// + Retry-After. Ответы 5xx не запоминаются — клиент может безопасно повторить.
//
// Гонки закрывает одна атомарная INSERT … ON CONFLICT: исполнять запрос будет ровно один.
// Для строгой атомарности «бизнес-данные + запись ключа» (создание заказа) обработчик
// может завершить ключ в своей транзакции через Claim.CompleteTx.
package idempotency

import (
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pavelgr1408/food-platform/pkg/httpx"
	"github.com/pavelgr1408/food-platform/pkg/pgxutil"
)

//go:embed sql/*.sql
var sqlFS embed.FS

// Migrations — источник миграций для pgxutil.Migrate.
func Migrations() pgxutil.Source {
	sub, err := fs.Sub(sqlFS, "sql")
	if err != nil {
		panic(err)
	}
	return pgxutil.Source{Module: "idempotency", FS: sub}
}

// Header — имя заголовка.
const Header = "Idempotency-Key"

// MaxKeyLen — максимальная длина ключа (как в OpenAPI IdempotencyKey).
const MaxKeyLen = 128

// Outcome — результат попытки захватить ключ.
type Outcome int

const (
	Acquired   Outcome = iota // ключ новый (или протух) — исполняйте запрос
	Replay                    // уже выполнен — верните сохранённый ответ
	InProgress                // другой исполнитель ещё работает
	Mismatch                  // ключ уже использован с другим запросом
)

// Record — сохранённый ответ.
type Record struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    []byte            `json:"-"`
}

// Store — хранилище ключей в PostgreSQL сервиса.
type Store struct {
	pool        *pgxpool.Pool
	TTL         time.Duration // сколько помнить ответ, по умолчанию 24h
	LockTimeout time.Duration // через сколько «зависший» in_progress можно перехватить, по умолчанию 30s
}

// NewStore создаёт хранилище.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool, TTL: 24 * time.Hour, LockTimeout: 30 * time.Second}
}

// Begin пытается захватить (scope,key). Атомарно: INSERT … ON CONFLICT DO UPDATE WHERE
// (протух | зависшая блокировка того же запроса).
func (s *Store) Begin(ctx context.Context, scope, key string, hash []byte) (Outcome, *Record, error) {
	var one int
	err := s.pool.QueryRow(ctx, `
		INSERT INTO idempotency_keys (scope, key, request_hash, state, expires_at)
		VALUES ($1, $2, $3, 'in_progress', now() + $4::interval)
		ON CONFLICT (scope, key) DO UPDATE
		   SET request_hash = EXCLUDED.request_hash, state = 'in_progress',
		       response_status = NULL, response_headers = NULL, response_body = NULL,
		       locked_at = now(), created_at = now(), expires_at = EXCLUDED.expires_at
		 WHERE idempotency_keys.expires_at < now()
		    OR (idempotency_keys.state = 'in_progress'
		        AND idempotency_keys.locked_at < now() - $5::interval
		        AND idempotency_keys.request_hash = EXCLUDED.request_hash)
		RETURNING 1`, scope, key, hash, s.TTL.String(), s.LockTimeout.String()).Scan(&one)
	if err == nil {
		return Acquired, nil, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return 0, nil, err
	}
	var (
		storedHash []byte
		state      string
		status     *int
		hdr        []byte
		body       []byte
	)
	err = s.pool.QueryRow(ctx, `SELECT request_hash, state, response_status, response_headers, response_body
		FROM idempotency_keys WHERE scope=$1 AND key=$2`, scope, key).Scan(&storedHash, &state, &status, &hdr, &body)
	if errors.Is(err, pgx.ErrNoRows) { // удалён между запросами (Abandon/Purge) — пробуем заново
		return s.Begin(ctx, scope, key, hash)
	}
	if err != nil {
		return 0, nil, err
	}
	if !bytes.Equal(storedHash, hash) {
		return Mismatch, nil, nil
	}
	if state != "completed" || status == nil {
		return InProgress, nil, nil
	}
	rec := &Record{Status: *status, Body: body}
	if len(hdr) > 0 {
		_ = json.Unmarshal(hdr, &rec.Headers)
	}
	return Replay, rec, nil
}

// Execer — pgx.Tx, *pgxpool.Pool и т.п.
type Execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// Complete сохраняет ответ. db — пул или транзакция бизнес-операции.
func (s *Store) Complete(ctx context.Context, db Execer, scope, key string, rec Record) error {
	h, _ := json.Marshal(rec.Headers)
	tag, err := db.Exec(ctx, `UPDATE idempotency_keys
		SET state='completed', response_status=$3, response_headers=$4, response_body=$5
		WHERE scope=$1 AND key=$2`, scope, key, rec.Status, h, rec.Body)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("idempotency: key %q not held", key)
	}
	return nil
}

// Abandon освобождает ключ (ответ 5xx / паника), чтобы клиент мог повторить запрос.
func (s *Store) Abandon(ctx context.Context, scope, key string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM idempotency_keys WHERE scope=$1 AND key=$2 AND state='in_progress'`, scope, key)
	return err
}

// Purge удаляет протухшие ключи; вызывайте периодически (см. Run).
func (s *Store) Purge(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM idempotency_keys WHERE expires_at < now()`)
	return tag.RowsAffected(), err
}

// Run периодически чистит протухшие ключи до отмены ctx.
func (s *Store) Run(ctx context.Context, log *slog.Logger) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if n, err := s.Purge(ctx); err != nil {
				log.WarnContext(ctx, "idempotency purge failed", "error", err)
			} else if n > 0 {
				log.InfoContext(ctx, "idempotency keys purged", "count", n)
			}
		}
	}
}

// Options — настройки middleware.
type Options struct {
	Required bool                         // 400, если заголовка нет (createOrder: true)
	Scope    func(r *http.Request) string // дополнительная область, например владелец заказа; по умолчанию пусто
	Log      *slog.Logger
}

// Claim — захваченный ключ; доступен обработчику через ClaimFrom.
type Claim struct {
	store      *Store
	Scope, Key string
	done       atomic.Bool
}

// CompleteTx завершает ключ в транзакции обработчика: ответ и бизнес-данные
// фиксируются атомарно. После успешного вызова middleware сам ключ не трогает.
func (c *Claim) CompleteTx(ctx context.Context, tx Execer, rec Record) error {
	if err := c.store.Complete(ctx, tx, c.Scope, c.Key, rec); err != nil {
		return err
	}
	c.done.Store(true)
	return nil
}

type claimKey struct{}

// ClaimFrom возвращает захваченный ключ запроса (nil, если Idempotency-Key не передан).
func ClaimFrom(ctx context.Context) *Claim { c, _ := ctx.Value(claimKey{}).(*Claim); return c }

// Middleware оборачивает обработчик конкретного маршрута: mux.Handle("POST /orders", store.Middleware(opt)(h)).
func (s *Store) Middleware(opt Options) func(http.Handler) http.Handler {
	log := opt.Log
	if log == nil {
		log = slog.Default()
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := r.Header.Get(Header)
			if key == "" {
				if opt.Required {
					httpx.WriteProblem(w, r, http.StatusBadRequest, "IDEMPOTENCY_KEY_REQUIRED", "Idempotency-Key header is required")
					return
				}
				next.ServeHTTP(w, r)
				return
			}
			if len(key) > MaxKeyLen || !printable(key) {
				httpx.WriteProblem(w, r, http.StatusBadRequest, "IDEMPOTENCY_KEY_INVALID", "Idempotency-Key must be 1-128 printable ASCII characters")
				return
			}
			body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, httpx.DefaultMaxBody))
			if err != nil {
				httpx.WriteProblem(w, r, http.StatusRequestEntityTooLarge, "BODY_TOO_LARGE", "request body too large")
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))

			scope := r.Method + " " + r.URL.Path
			if opt.Scope != nil {
				scope += "|" + opt.Scope(r)
			}
			h := sha256.New()
			for _, part := range [][]byte{[]byte(r.Method), []byte(r.URL.Path), []byte(r.URL.RawQuery), body} {
				h.Write(part)
				h.Write([]byte{0})
			}
			outcome, rec, err := s.Begin(r.Context(), scope, key, h.Sum(nil))
			if err != nil {
				log.ErrorContext(r.Context(), "idempotency store failed", "error", err)
				httpx.WriteProblem(w, r, http.StatusServiceUnavailable, "IDEMPOTENCY_UNAVAILABLE", "idempotency store unavailable")
				return
			}
			switch outcome {
			case Replay:
				for k, v := range rec.Headers {
					w.Header().Set(k, v)
				}
				w.Header().Set("Idempotent-Replay", "true")
				w.WriteHeader(rec.Status)
				_, _ = w.Write(rec.Body)
				return
			case InProgress:
				w.Header().Set("Retry-After", "1")
				httpx.WriteProblem(w, r, http.StatusConflict, "IDEMPOTENCY_IN_PROGRESS", "a request with this Idempotency-Key is still being processed")
				return
			case Mismatch:
				httpx.WriteProblem(w, r, http.StatusUnprocessableEntity, "IDEMPOTENCY_KEY_REUSED", "Idempotency-Key was already used with a different request")
				return
			}

			claim := &Claim{store: s, Scope: scope, Key: key}
			cap := &capture{header: http.Header{}, status: http.StatusOK}
			ok := false
			defer func() {
				if ok {
					return
				}
				// паника в обработчике — освобождаем ключ
				_ = s.Abandon(context.WithoutCancel(r.Context()), scope, key)
			}()
			next.ServeHTTP(cap, r.WithContext(context.WithValue(r.Context(), claimKey{}, claim)))
			ok = true
			ctx := context.WithoutCancel(r.Context())
			switch {
			case cap.status >= 500:
				_ = s.Abandon(ctx, scope, key)
			case !claim.done.Load():
				record := Record{Status: cap.status, Body: cap.body.Bytes(), Headers: map[string]string{}}
				for _, hn := range []string{"Content-Type", "Location"} {
					if v := cap.header.Get(hn); v != "" {
						record.Headers[hn] = v
					}
				}
				if err := s.Complete(ctx, s.pool, scope, key, record); err != nil {
					log.ErrorContext(ctx, "idempotency complete failed", "error", err, "key", key)
				}
			}
			for k, v := range cap.header {
				w.Header()[k] = v
			}
			w.WriteHeader(cap.status)
			_, _ = w.Write(cap.body.Bytes())
		})
	}
}

func printable(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < 0x21 || s[i] > 0x7e {
			return false
		}
	}
	return true
}

// capture буферизует ответ обработчика, чтобы сохранить его до отправки клиенту.
type capture struct {
	header http.Header
	status int
	body   bytes.Buffer
	wrote  bool
}

func (c *capture) Header() http.Header { return c.header }
func (c *capture) WriteHeader(code int) {
	if !c.wrote {
		c.status, c.wrote = code, true
	}
}
func (c *capture) Write(b []byte) (int, error) { c.wrote = true; return c.body.Write(b) }
