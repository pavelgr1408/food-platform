package httpx

import (
	"context"
	"net/http"
	"sync/atomic"
)

type routeKey struct{}

type routeHolder struct{ pattern atomic.Pointer[string] }

func withRouteHolder(ctx context.Context) context.Context {
	return context.WithValue(ctx, routeKey{}, &routeHolder{})
}

// TrackRoute оборачивает ServeMux ИЗНУТРИ цепочки: после обработки запоминает шаблон
// маршрута в контексте, чтобы внешние middleware (AccessLog, метрики) видели его.
//
// Зачем: ServeMux записывает r.Pattern в тот *http.Request, который получил. Любой
// middleware между ними, делающий r.WithContext(...) (например, authclient), создаёт
// копию — и внешние слои остаются с пустым Pattern. Метки route в метриках и логах
// превратились бы в "unmatched".
//
//	httpx.Chain(httpx.TrackRoute(mux), httpx.RequestContext(), …)
func TrackRoute(mux http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r)
		if h, ok := r.Context().Value(routeKey{}).(*routeHolder); ok && r.Pattern != "" {
			p := r.Pattern
			h.pattern.Store(&p)
		}
	})
}

// RouteOf возвращает шаблон маршрута обработанного запроса ("" если не найден).
// Вызывать ПОСЛЕ next.ServeHTTP во внешнем middleware.
func RouteOf(r *http.Request) string {
	if r.Pattern != "" {
		return r.Pattern
	}
	if h, ok := r.Context().Value(routeKey{}).(*routeHolder); ok {
		if p := h.pattern.Load(); p != nil {
			return *p
		}
	}
	return ""
}
