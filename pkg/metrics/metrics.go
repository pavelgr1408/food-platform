// Package metrics — Prometheus-метрики сервиса: RED по маршрутам + Go/process.
//
//	http_requests_total{service,method,route,code}
//	http_request_duration_seconds{service,method,route}   (histogram → квантили в Grafana)
//	http_requests_in_flight{service}
//
// route — шаблон ServeMux (httpx.TrackRoute) ("GET /orders/{orderId}"), а не фактический путь:
// кардинальность ограничена числом маршрутов.
package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/pavelgr1408/food-platform/pkg/httpx"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Registry — реестр метрик одного сервиса.
type Registry struct {
	Reg      *prometheus.Registry
	reqs     *prometheus.CounterVec
	dur      *prometheus.HistogramVec
	inflight prometheus.Gauge
}

// DefBuckets — корзины под межсервисные API (5 мс … 10 с).
var DefBuckets = []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10}

// New создаёт реестр с const-label service и стандартными коллекторами.
func New(service string) *Registry {
	reg := prometheus.NewRegistry()
	wrapped := prometheus.WrapRegistererWith(prometheus.Labels{"service": service}, reg)
	m := &Registry{
		Reg: reg,
		reqs: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "http_requests_total", Help: "Количество HTTP-запросов."}, []string{"method", "route", "code"}),
		dur: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "http_request_duration_seconds", Help: "Длительность обработки HTTP-запросов.", Buckets: DefBuckets},
			[]string{"method", "route"}),
		inflight: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "http_requests_in_flight", Help: "Запросов в обработке."}),
	}
	wrapped.MustRegister(m.reqs, m.dur, m.inflight,
		collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	return m
}

// MustRegister регистрирует дополнительные коллекторы (бизнес-метрики, outbox и т.п.)
// с тем же const-label service.
func (m *Registry) MustRegister(service string, cs ...prometheus.Collector) {
	prometheus.WrapRegistererWith(prometheus.Labels{"service": service}, m.Reg).MustRegister(cs...)
}

// Handler — обработчик GET /metrics.
func (m *Registry) Handler() http.Handler {
	return promhttp.HandlerFor(m.Reg, promhttp.HandlerOpts{})
}

// Middleware собирает RED-метрики. Ставьте снаружи mux, который обёрнут в httpx.TrackRoute.
func (m *Registry) Middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			m.inflight.Inc()
			defer m.inflight.Dec()
			rec := &statusWriter{ResponseWriter: w, status: http.StatusOK}
			start := time.Now()
			next.ServeHTTP(rec, r)
			route := httpx.RouteOf(r)
			if route == "" {
				route = "unmatched"
			}
			m.reqs.WithLabelValues(r.Method, route, strconv.Itoa(rec.status)).Inc()
			m.dur.WithLabelValues(r.Method, route).Observe(time.Since(start).Seconds())
		})
	}
}

type statusWriter struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (s *statusWriter) WriteHeader(c int) {
	if !s.wrote {
		s.status, s.wrote = c, true
	}
	s.ResponseWriter.WriteHeader(c)
}
func (s *statusWriter) Write(b []byte) (int, error) {
	s.wrote = true
	return s.ResponseWriter.Write(b)
}
func (s *statusWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }
