package httpx

import (
	"context"
	"net/http"
	"time"
)

// Check — проверка зависимости для readiness.
type Check struct {
	Name string
	Fn   func(ctx context.Context) error
}

// Liveness: GET /health/ → {"status":"OK"} (контракт ДЗ-3, ADR-2). Без обращений к зависимостям.
func Liveness() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		WriteJSON(w, http.StatusOK, map[string]string{"status": "OK"})
	})
}

// Readiness: GET /actuator/health → {"status":"UP|DOWN","components":{...}}; 503 если что-то DOWN.
func Readiness(checks ...Check) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		status, code := "UP", http.StatusOK
		comps := map[string]map[string]string{}
		for _, c := range checks {
			if err := c.Fn(ctx); err != nil {
				comps[c.Name] = map[string]string{"status": "DOWN"}
				status, code = "DOWN", http.StatusServiceUnavailable
				continue
			}
			comps[c.Name] = map[string]string{"status": "UP"}
		}
		WriteJSON(w, code, map[string]any{"status": status, "components": comps})
	})
}

// RegisterHealth вешает стандартные пробы на mux.
func RegisterHealth(mux *http.ServeMux, checks ...Check) {
	live := Liveness()
	mux.Handle("GET /health", live)
	mux.Handle("GET /health/{$}", live)
	mux.Handle("GET /actuator/health", Readiness(checks...))
}
