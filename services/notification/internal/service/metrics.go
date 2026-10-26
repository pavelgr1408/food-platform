package service

import "github.com/prometheus/client_golang/prometheus"

// Metrics — бизнес-метрики notification-service (ADR-7: инструментируем с рождения).
type Metrics struct {
	Events   *prometheus.CounterVec   // result (queued|ignored|no_recipient|no_devices|stale|duplicate)
	Queued   prometheus.Counter       // уведомлений поставлено в очередь
	Push     *prometheus.CounterVec   // platform, result (sent|invalid_token|retry|failed|skipped)
	Duration *prometheus.HistogramVec // platform
	Expired  prometheus.Counter       // закрыто как устаревшее (старше MAX_EVENT_AGE)
	Revoked  prometheus.Counter       // токены, удалённые по ответу провайдера
	Tokens   *prometheus.CounterVec   // op (created|updated|moved|deleted|evicted)
}

// NewMetrics создаёт метрики; зарегистрируйте их через Collectors().
func NewMetrics() *Metrics {
	return &Metrics{
		Events: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "notification_events_total",
			Help: "Обработка orders.status по результату."}, []string{"result"}),
		Queued: prometheus.NewCounter(prometheus.CounterOpts{Name: "notification_queued_total",
			Help: "Уведомления, поставленные в очередь отправки."}),
		Push: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "notification_push_total",
			Help: "Попытки отправки push по платформе и исходу."}, []string{"platform", "result"}),
		Duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "notification_push_duration_seconds",
			Help: "Длительность вызова провайдера.", Buckets: []float64{.05, .1, .25, .5, 1, 2.5, 5, 10}}, []string{"platform"}),
		Expired: prometheus.NewCounter(prometheus.CounterOpts{Name: "notification_expired_total",
			Help: "Уведомления, закрытые как устаревшие."}),
		Revoked: prometheus.NewCounter(prometheus.CounterOpts{Name: "notification_tokens_revoked_total",
			Help: "Токены, удалённые по ответу провайдера."}),
		Tokens: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "notification_token_ops_total",
			Help: "Операции с токенами устройств."}, []string{"op"}),
	}
}

// Collectors — для metrics.Registry.MustRegister.
func (m *Metrics) Collectors() []prometheus.Collector {
	return []prometheus.Collector{m.Events, m.Queued, m.Push, m.Duration, m.Expired, m.Revoked, m.Tokens}
}
