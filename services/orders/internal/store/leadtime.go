package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// LeadTime — последняя известная загрузка производства ресторана (production.lead.time).
type LeadTime struct {
	RestaurantID string
	Minutes      int
	Load         float64
	MeasuredAt   time.Time
}

// UpsertLeadTime сохраняет значение, если оно не старше уже сохранённого (по measuredAt).
// applied=false — событие устарело и проигнорировано.
func (s *Store) UpsertLeadTime(ctx context.Context, db DBTX, lt LeadTime, receivedAt time.Time) (applied bool, err error) {
	tag, err := db.Exec(ctx, `INSERT INTO production_lead_time_replica (restaurant_id, lead_time_minutes, current_load, measured_at, received_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (restaurant_id) DO UPDATE SET lead_time_minutes = EXCLUDED.lead_time_minutes,
			current_load = EXCLUDED.current_load, measured_at = EXCLUDED.measured_at, received_at = EXCLUDED.received_at
		WHERE EXCLUDED.measured_at >= production_lead_time_replica.measured_at`,
		lt.RestaurantID, lt.Minutes, lt.Load, lt.MeasuredAt, receivedAt)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// GetLeadTime читает реплику ресторана; found=false — данных нет.
func (s *Store) GetLeadTime(ctx context.Context, db DBTX, restaurantID string) (lt LeadTime, found bool, err error) {
	err = db.QueryRow(ctx, `SELECT restaurant_id::text, lead_time_minutes, current_load, measured_at
		FROM production_lead_time_replica WHERE restaurant_id = $1`, restaurantID).
		Scan(&lt.RestaurantID, &lt.Minutes, &lt.Load, &lt.MeasuredAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return LeadTime{}, false, nil
	}
	lt.MeasuredAt = lt.MeasuredAt.UTC()
	return lt, err == nil, err
}
