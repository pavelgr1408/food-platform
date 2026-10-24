package service

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
)

// Metrics — бизнес-метрики orders-service (ADR-7: инструментируем с рождения).
type Metrics struct {
	OrdersCreated    *prometheus.CounterVec // payment_method, fulfillment
	Transitions      *prometheus.CounterVec // to
	UpstreamFailures *prometheus.CounterVec // upstream
	CartsPurged      prometheus.Counter
	PaidAfterCancel  prometheus.Counter
	ProductionEvents *prometheus.CounterVec // event (dish_status|lead_time), result (applied|stale|ignored_status)
	RefundEvents     *prometheus.CounterVec // status (pending|refunded|failed): ход возврата от payment (S6)
}

// NewMetrics создаёт метрики; зарегистрируйте их через Collectors().
func NewMetrics() *Metrics {
	return &Metrics{
		OrdersCreated: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "orders_created_total",
			Help: "Созданные заказы."}, []string{"payment_method", "fulfillment"}),
		Transitions: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "orders_status_transitions_total",
			Help: "Переходы статусов заказов."}, []string{"to"}),
		UpstreamFailures: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "orders_upstream_failures_total",
			Help: "Неудачные вызовы menu/restaurant."}, []string{"upstream"}),
		CartsPurged: prometheus.NewCounter(prometheus.CounterOpts{Name: "orders_carts_purged_total",
			Help: "Удалённые заброшенные корзины."}),
		PaidAfterCancel: prometheus.NewCounter(prometheus.CounterOpts{Name: "orders_paid_after_cancel_total",
			Help: "Оплата пришла по уже отменённому заказу — нужен возврат (тревога)."}),
		ProductionEvents: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "orders_production_events_total",
			Help: "События системы производства: применены, устарели или проигнорированы по статусу заказа."}, []string{"event", "result"}),
		RefundEvents: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "orders_refund_events_total",
			Help: "События о ходе возврата денег по отменённым оплаченным заказам."}, []string{"status"}),
	}
}

// Collectors — для metrics.Registry.MustRegister.
func (m *Metrics) Collectors() []prometheus.Collector {
	return []prometheus.Collector{m.OrdersCreated, m.Transitions, m.UpstreamFailures, m.CartsPurged, m.PaidAfterCancel, m.ProductionEvents, m.RefundEvents}
}

// StuckCollectors — метрики «заказ завис в accepted»: производство не берёт принятый заказ
// (авария очереди, E3 UC-ORD-06). Возраст — от accepted_at до now() БД; 0, если принятых заказов нет;
// -1 — запрос не удался.
func StuckCollectors(pool *pgxpool.Pool) []prometheus.Collector {
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
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "orders_stuck_in_accepted_seconds",
			Help: "Возраст самого старого заказа в статусе accepted (производство не приступило)."},
			func() float64 {
				return q(`SELECT COALESCE(EXTRACT(EPOCH FROM now() - min(accepted_at)), 0) FROM orders WHERE status = 'accepted' AND accepted_at IS NOT NULL`)
			}),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "orders_accepted",
			Help: "Заказов в статусе accepted (ожидают производство)."},
			func() float64 { return q(`SELECT count(*) FROM orders WHERE status = 'accepted'`) }),
	}
}
