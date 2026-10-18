// Package store — доступ к restaurant-db (pgx). Поиск по радиусу: bounding-box по индексу
// (lat, lon) + гаверсинус в SQL; без PostGIS (см. README сервиса).
package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pavelgr1408/food-platform/pkg/pgxutil"
	"github.com/pavelgr1408/food-platform/services/restaurant/internal/domain"
)

// ErrNotFound — ресторана нет или он неактивен.
var ErrNotFound = errors.New("restaurant not found")

// MaxCandidates — потолок выборки кандидатов поиска (защита от выкачивания всего справочника).
const MaxCandidates = 2000

// Store — хранилище ресторанов.
type Store struct{ pool *pgxpool.Pool }

// New создаёт Store.
func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

const restCols = `r.id::text, r.name, r.city, r.address, r.lat, r.lon, r.timezone,
       r.delivery_enabled, r.delivery_radius_m, r.active`

func scanRestaurant(row pgx.Row, extra ...any) (domain.Restaurant, error) {
	var r domain.Restaurant
	dst := append([]any{&r.ID, &r.Name, &r.City, &r.Address, &r.Location.Lat, &r.Location.Lon,
		&r.Timezone, &r.DeliveryEnabled, &r.DeliveryRadiusM, &r.Active}, extra...)
	if err := row.Scan(dst...); err != nil {
		return r, err
	}
	loc, err := time.LoadLocation(r.Timezone)
	if err != nil {
		return r, fmt.Errorf("restaurant %s: bad timezone %q: %w", r.ID, r.Timezone, err)
	}
	r.Schedule.Location = loc
	return r, nil
}

// Get возвращает активный ресторан с режимом работы и ближайшими исключениями
// (on_date >= excFrom).
func (s *Store) Get(ctx context.Context, id string, excFrom time.Time) (domain.Restaurant, error) {
	r, err := scanRestaurant(s.pool.QueryRow(ctx,
		`SELECT `+restCols+` FROM restaurants r WHERE r.id = $1 AND r.active`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return r, ErrNotFound
	}
	if err != nil {
		return r, err
	}
	m := map[string]*domain.Restaurant{r.ID: &r}
	if err := s.loadSchedules(ctx, m, excFrom, time.Time{}); err != nil {
		return r, err
	}
	return r, nil
}

// Candidates возвращает активные рестораны. center == nil — весь список по названию;
// иначе рестораны в радиусе, отсортированные по расстоянию (DistanceM заполнен).
// Исключения расписания загружаются в окне [excFrom, excTo].
func (s *Store) Candidates(ctx context.Context, center *domain.Point, radiusM int, excFrom, excTo time.Time) ([]domain.Restaurant, error) {
	var (
		rows pgx.Rows
		err  error
	)
	if center == nil {
		rows, err = s.pool.Query(ctx, `SELECT `+restCols+` FROM restaurants r WHERE r.active
			ORDER BY r.name, r.id LIMIT $1`, MaxCandidates)
	} else {
		bb := domain.BoundingBox(*center, float64(radiusM))
		args := []any{center.Lat, center.Lon, bb.LatMin, bb.LatMax, radiusM, MaxCandidates}
		lonCond := "TRUE"
		switch {
		case bb.LonAll:
		case bb.WrapLon:
			args = append(args, bb.LonMin, bb.LonMax)
			lonCond = "(r.lon >= $7 OR r.lon <= $8)"
		default:
			args = append(args, bb.LonMin, bb.LonMax)
			lonCond = "r.lon BETWEEN $7 AND $8"
		}
		rows, err = s.pool.Query(ctx, `
SELECT `+restCols+`, q.dist FROM (
  SELECT r.id, 2 * 6371000 * asin(least(1, sqrt(
           power(sin(radians(r.lat - $1) / 2), 2) +
           cos(radians($1)) * cos(radians(r.lat)) * power(sin(radians(r.lon - $2) / 2), 2)))) AS dist
  FROM restaurants r
  WHERE r.active AND r.lat BETWEEN $3 AND $4 AND `+lonCond+`
) q JOIN restaurants r ON r.id = q.id
WHERE q.dist <= $5
ORDER BY q.dist, r.name, r.id
LIMIT $6`, args...)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []domain.Restaurant
	for rows.Next() {
		var dist *float64
		var r domain.Restaurant
		if center == nil {
			r, err = scanRestaurant(rows)
		} else {
			dist = new(float64)
			r, err = scanRestaurant(rows, dist)
		}
		if err != nil {
			return nil, err
		}
		if dist != nil {
			d := int(*dist + 0.5)
			r.DistanceM = &d
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return out, nil
	}
	m := make(map[string]*domain.Restaurant, len(out))
	for i := range out {
		m[out[i].ID] = &out[i]
	}
	if err := s.loadSchedules(ctx, m, excFrom, excTo); err != nil {
		return nil, err
	}
	return out, nil
}

// loadSchedules догружает working_hours и schedule_exceptions пачкой (по 2 запроса на выборку).
// excTo нулевой — без верхней границы.
func (s *Store) loadSchedules(ctx context.Context, m map[string]*domain.Restaurant, excFrom, excTo time.Time) error {
	ids := make([]string, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	hr, err := s.pool.Query(ctx,
		`SELECT restaurant_id::text, day_of_week, opens_min, closes_min
		   FROM working_hours WHERE restaurant_id = ANY($1::uuid[])
		  ORDER BY restaurant_id, day_of_week, opens_min`, ids)
	if err != nil {
		return err
	}
	for hr.Next() {
		var id string
		var dow, o, c int16
		if err := hr.Scan(&id, &dow, &o, &c); err != nil {
			hr.Close()
			return err
		}
		r := m[id]
		r.Schedule.Hours = append(r.Schedule.Hours, domain.WorkingHours{
			DayOfWeek: int(dow), Interval: domain.Interval{Opens: int(o), Closes: int(c)}})
	}
	hr.Close()
	if err := hr.Err(); err != nil {
		return err
	}

	q := `SELECT restaurant_id::text, to_char(on_date, 'YYYY-MM-DD'), opens_min, closes_min, note
	        FROM schedule_exceptions WHERE restaurant_id = ANY($1::uuid[]) AND on_date >= $2::date`
	args := []any{ids, excFrom.Format("2006-01-02")}
	if !excTo.IsZero() {
		q += ` AND on_date <= $3::date`
		args = append(args, excTo.Format("2006-01-02"))
	}
	q += ` ORDER BY restaurant_id, on_date, opens_min NULLS FIRST`
	er, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return err
	}
	defer er.Close()
	for er.Next() {
		var id, date, note string
		var o, c *int16
		if err := er.Scan(&id, &date, &o, &c, &note); err != nil {
			return err
		}
		e := domain.Exception{Date: date, Note: note}
		if o == nil {
			e.Closed = true
		} else {
			e.Interval = domain.Interval{Opens: int(*o), Closes: int(*c)}
		}
		r := m[id]
		r.Schedule.Exceptions = append(r.Schedule.Exceptions, e)
	}
	return er.Err()
}

// Upsert идемпотентно записывает рестораны (seed): строка ресторана — upsert по id,
// режим работы и исключения заменяются целиком. Одна транзакция.
func (s *Store) Upsert(ctx context.Context, rs []domain.Restaurant) error {
	return pgxutil.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		for _, r := range rs {
			if _, err := tx.Exec(ctx, `
INSERT INTO restaurants (id, name, city, address, lat, lon, timezone, delivery_enabled, delivery_radius_m, active)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
ON CONFLICT (id) DO UPDATE SET name = EXCLUDED.name, city = EXCLUDED.city, address = EXCLUDED.address,
  lat = EXCLUDED.lat, lon = EXCLUDED.lon, timezone = EXCLUDED.timezone,
  delivery_enabled = EXCLUDED.delivery_enabled, delivery_radius_m = EXCLUDED.delivery_radius_m,
  active = EXCLUDED.active, updated_at = now()`,
				r.ID, r.Name, r.City, r.Address, r.Location.Lat, r.Location.Lon, r.Timezone,
				r.DeliveryEnabled, r.DeliveryRadiusM, r.Active); err != nil {
				return fmt.Errorf("upsert restaurant %s: %w", r.ID, err)
			}
			if _, err := tx.Exec(ctx, `DELETE FROM working_hours WHERE restaurant_id = $1`, r.ID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `DELETE FROM schedule_exceptions WHERE restaurant_id = $1`, r.ID); err != nil {
				return err
			}
			for _, h := range r.Schedule.Hours {
				if _, err := tx.Exec(ctx, `INSERT INTO working_hours (restaurant_id, day_of_week, opens_min, closes_min)
					VALUES ($1, $2, $3, $4)`, r.ID, h.DayOfWeek, h.Opens, h.Closes); err != nil {
					return fmt.Errorf("insert hours %s: %w", r.ID, err)
				}
			}
			for _, e := range r.Schedule.Exceptions {
				var o, c *int
				if !e.Closed {
					o, c = &e.Opens, &e.Closes
				}
				if _, err := tx.Exec(ctx, `INSERT INTO schedule_exceptions (restaurant_id, on_date, opens_min, closes_min, note)
					VALUES ($1, $2::date, $3, $4, $5)`, r.ID, e.Date, o, c, e.Note); err != nil {
					return fmt.Errorf("insert exception %s: %w", r.ID, err)
				}
			}
		}
		return nil
	})
}
