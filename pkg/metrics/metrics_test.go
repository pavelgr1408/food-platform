package metrics

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pavelgr1408/food-platform/pkg/httpx"
)

func TestREDMetrics(t *testing.T) {
	m := New("orders")
	mux := http.NewServeMux()
	mux.HandleFunc("GET /orders/{id}", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) })
	mux.Handle("GET /metrics", m.Handler())
	h := httpx.Chain(httpx.TrackRoute(mux), httpx.RequestContext(), m.Middleware())

	for _, p := range []string{"/orders/1", "/orders/2", "/nope"} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", p, nil))
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	body, _ := io.ReadAll(rec.Body)
	out := string(body)
	for _, want := range []string{
		`http_requests_total{code="404",method="GET",route="GET /orders/{id}",service="orders"} 2`,
		`route="unmatched"`,
		`http_request_duration_seconds_bucket{method="GET",route="GET /orders/{id}",service="orders",le="0.005"}`,
		`go_goroutines{service="orders"}`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(out, "/orders/1") {
		t.Error("raw path leaked into labels (cardinality)")
	}
}
