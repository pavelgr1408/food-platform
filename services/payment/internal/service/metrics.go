package service

import "github.com/prometheus/client_golang/prometheus"

// Metrics — бизнес-метрики payment-service (ADR-7: инструментируем с рождения).
type Metrics struct {
	Created         *prometheus.CounterVec   // method
	Transitions     *prometheus.CounterVec   // status
	GatewayCalls    *prometheus.CounterVec   // op, result (ok|rejected|error)
	GatewayDuration *prometheus.HistogramVec // op
	Webhooks        *prometheus.CounterVec   // source, result (applied|duplicate|not_found|amount_mismatch)
	AmountMismatch  prometheus.Counter
	PaidAfterCancel prometheus.Counter
	Conflicts       *prometheus.CounterVec // from, to — противоречащие уведомления (не применены)
	Reconciled      *prometheus.CounterVec // result (paid|declined|pending|unknown|error|expired)
}

// NewMetrics создаёт метрики; зарегистрируйте их через Collectors().
func NewMetrics() *Metrics {
	return &Metrics{
		Created: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "payment_created_total",
			Help: "Созданные попытки оплаты."}, []string{"method"}),
		Transitions: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "payment_status_transitions_total",
			Help: "Переходы статусов платежей."}, []string{"status"}),
		GatewayCalls: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "payment_gateway_calls_total",
			Help: "Вызовы платёжного шлюза."}, []string{"op", "result"}),
		GatewayDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "payment_gateway_call_duration_seconds",
			Help: "Длительность вызовов шлюза.", Buckets: []float64{.05, .1, .25, .5, 1, 2.5, 5, 10}}, []string{"op"}),
		Webhooks: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "payment_webhooks_total",
			Help: "Принятые вебхуки по результату."}, []string{"source", "result"}),
		AmountMismatch: prometheus.NewCounter(prometheus.CounterOpts{Name: "payment_amount_mismatch_total",
			Help: "Сумма в уведомлении шлюза не совпала с суммой платежа (тревога)."}),
		PaidAfterCancel: prometheus.NewCounter(prometheus.CounterOpts{Name: "payment_paid_after_cancel_total",
			Help: "Деньги получены по уже отменённому заказу — нужен возврат (тревога)."}),
		Conflicts: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "payment_conflicting_updates_total",
			Help: "Не применённые противоречащие обновления статуса."}, []string{"from", "to"}),
		Reconciled: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "payment_reconcile_total",
			Help: "Результаты сверки со шлюзом."}, []string{"result"}),
	}
}

// Collectors — для metrics.Registry.MustRegister.
func (m *Metrics) Collectors() []prometheus.Collector {
	return []prometheus.Collector{m.Created, m.Transitions, m.GatewayCalls, m.GatewayDuration, m.Webhooks,
		m.AmountMismatch, m.PaidAfterCancel, m.Conflicts, m.Reconciled}
}
