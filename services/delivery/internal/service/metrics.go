package service

import "github.com/prometheus/client_golang/prometheus"

// Metrics — бизнес-метрики delivery-service (ADR-7: инструментируем с рождения).
type Metrics struct {
	Transitions     *prometheus.CounterVec   // status, source
	CourierCalls    *prometheus.CounterVec   // op, result (ok|rejected|error)
	CourierDuration *prometheus.HistogramVec // op
	Webhooks        *prometheus.CounterVec   // result (applied|duplicate|ignored|not_found)
	LocationWrites  *prometheus.CounterVec   // result (ok|error)
	StateEvents     *prometheus.CounterVec   // result (created|updated|ignored|skipped|no_location|cancelled)
	Reconciled      *prometheus.CounterVec   // result (dispatched|polled|timeout|cancelled|retry|rejected)
}

// NewMetrics создаёт метрики; зарегистрируйте их через Collectors().
func NewMetrics() *Metrics {
	return &Metrics{
		Transitions: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "delivery_status_transitions_total",
			Help: "Переходы статусов доставки."}, []string{"status", "source"}),
		CourierCalls: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "delivery_courier_calls_total",
			Help: "Вызовы курьерской системы."}, []string{"op", "result"}),
		CourierDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "delivery_courier_call_duration_seconds",
			Help: "Длительность вызовов курьерской системы.", Buckets: []float64{.05, .1, .25, .5, 1, 2.5, 5, 10}}, []string{"op"}),
		Webhooks: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "delivery_webhooks_total",
			Help: "Вебхуки курьерской системы по результату."}, []string{"result"}),
		LocationWrites: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "delivery_location_writes_total",
			Help: "Записи координат курьера в Redis."}, []string{"result"}),
		StateEvents: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "delivery_state_events_total",
			Help: "Обработка orders.state."}, []string{"result"}),
		Reconciled: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "delivery_reconcile_total",
			Help: "Результаты диспетчеризации и опроса курьерской системы."}, []string{"result"}),
	}
}

// Collectors — для metrics.Registry.MustRegister.
func (m *Metrics) Collectors() []prometheus.Collector {
	return []prometheus.Collector{m.Transitions, m.CourierCalls, m.CourierDuration, m.Webhooks, m.LocationWrites, m.StateEvents, m.Reconciled}
}
