package kafka

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
)

// OutboxCollectors — метрики очереди outbox для алертов (заказ «застрял»,
// Kafka недоступна): outbox_pending и outbox_oldest_pending_age_seconds.
// Регистрировать через metrics.Registry.MustRegister.
func OutboxCollectors(pool *pgxpool.Pool) []prometheus.Collector {
	q := func(sql string) float64 {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		var v float64
		if err := pool.QueryRow(ctx, sql).Scan(&v); err != nil {
			return -1
		}
		return v
	}
	return []prometheus.Collector{
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "outbox_pending",
			Help: "Неопубликованных событий в outbox."},
			func() float64 { return q(`SELECT count(*) FROM outbox WHERE published_at IS NULL`) }),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "outbox_oldest_pending_age_seconds",
			Help: "Возраст самого старого неопубликованного события."},
			func() float64 {
				return q(`SELECT COALESCE(EXTRACT(EPOCH FROM now() - min(created_at)), 0) FROM outbox WHERE published_at IS NULL`)
			}),
	}
}
