package service

import "github.com/prometheus/client_golang/prometheus"

// Metrics — бизнес-метрики loyalty-service.
type Metrics struct {
	Actions *prometheus.CounterVec // action (create|update|activate|complete), result (ok|noop|forbidden|conflict|invalid|error)
	Authz   *prometheus.CounterVec // result (allowed|denied|unavailable)
}

// NewMetrics создаёт метрики.
func NewMetrics() *Metrics {
	return &Metrics{
		Actions: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "loyalty_promotion_actions_total",
			Help: "Операции над акциями по результату."}, []string{"action", "result"}),
		Authz: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "loyalty_authz_total",
			Help: "Проверки права управлять акциями."}, []string{"result"}),
	}
}

// Collectors — для metrics.Registry.MustRegister.
func (m *Metrics) Collectors() []prometheus.Collector {
	return []prometheus.Collector{m.Actions, m.Authz}
}
