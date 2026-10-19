package service

import "github.com/prometheus/client_golang/prometheus"

// Metrics — бизнес-метрики orders-service (ADR-7: инструментируем с рождения).
type Metrics struct {
	OrdersCreated    *prometheus.CounterVec // payment_method, fulfillment
	Transitions      *prometheus.CounterVec // to
	UpstreamFailures *prometheus.CounterVec // upstream
	CartsPurged      prometheus.Counter
	PaidAfterCancel  prometheus.Counter
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
	}
}

// Collectors — для metrics.Registry.MustRegister.
func (m *Metrics) Collectors() []prometheus.Collector {
	return []prometheus.Collector{m.OrdersCreated, m.Transitions, m.UpstreamFailures, m.CartsPurged, m.PaidAfterCancel}
}
