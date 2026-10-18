package httpx

import (
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"runtime/debug"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/pavelgr1408/food-platform/pkg/logging"
)

// Middleware — обёртка над http.Handler.
type Middleware func(http.Handler) http.Handler

// Chain применяет middleware так, что первый в списке — самый внешний.
func Chain(h http.Handler, mws ...Middleware) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

// HeaderRequestID — заголовок идентификатора запроса.
const HeaderRequestID = "X-Request-Id"

var (
	safeID      = regexp.MustCompile(`^[A-Za-z0-9._\-]{1,64}$`)
	traceparent = regexp.MustCompile(`^00-([0-9a-f]{32})-([0-9a-f]{16})-[0-9a-f]{2}$`)
)

// RequestContext: request id (из X-Request-Id либо новый) и trace id (из
// traceparent либо новый) кладутся в контекст и в ответ. Должен стоять снаружи
// Recover и AccessLog.
func RequestContext() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := r.Header.Get(HeaderRequestID)
			if !safeID.MatchString(id) {
				id = uuid.NewString()
			}
			trace := ""
			if m := traceparent.FindStringSubmatch(r.Header.Get("traceparent")); m != nil {
				trace = m[1]
			} else {
				trace = randHex(16)
			}
			ctx := logging.WithTraceID(logging.WithRequestID(r.Context(), id), trace)
			w.Header().Set(HeaderRequestID, id)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Recover превращает панику в 500 problem и пишет стек в лог.
func Recover(log *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					if rec == http.ErrAbortHandler {
						panic(rec)
					}
					log.ErrorContext(r.Context(), "panic recovered", "panic", rec, "stack", string(debug.Stack()))
					WriteProblem(w, r, http.StatusInternalServerError, "INTERNAL", "internal server error")
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// StatusRecorder запоминает статус и размер ответа; поддерживает Unwrap (Flusher и т.п.).
type StatusRecorder struct {
	http.ResponseWriter
	Status int
	Bytes  int
	wrote  bool
}

func (s *StatusRecorder) WriteHeader(code int) {
	if !s.wrote {
		s.Status, s.wrote = code, true
	}
	s.ResponseWriter.WriteHeader(code)
}
func (s *StatusRecorder) Write(b []byte) (int, error) {
	if !s.wrote {
		s.Status, s.wrote = http.StatusOK, true
	}
	n, err := s.ResponseWriter.Write(b)
	s.Bytes += n
	return n, err
}
func (s *StatusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// NewStatusRecorder оборачивает w.
func NewStatusRecorder(w http.ResponseWriter) *StatusRecorder {
	return &StatusRecorder{ResponseWriter: w, Status: http.StatusOK}
}

// AccessLog пишет одну запись на запрос. Маршрут — шаблон из ServeMux (r.Pattern),
// чтобы не плодить кардинальность. Пробы и /metrics логируются на уровне debug.
func AccessLog(log *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := NewStatusRecorder(w)
			next.ServeHTTP(rec, r)
			level := slog.LevelInfo
			if isProbe(r.URL.Path) {
				level = slog.LevelDebug
			} else if rec.Status >= 500 {
				level = slog.LevelError
			}
			log.Log(r.Context(), level, "http request",
				"method", r.Method, "route", r.Pattern, "path", r.URL.Path, "status", rec.Status,
				"bytes", rec.Bytes, "duration_ms", float64(time.Since(start).Microseconds())/1000,
				"remote_ip", clientIP(r), "user_agent", r.UserAgent())
		})
	}
}

func isProbe(p string) bool {
	return p == "/metrics" || strings.HasPrefix(p, "/health") || strings.HasPrefix(p, "/actuator/")
}

func clientIP(r *http.Request) string {
	if h, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return h
	}
	return r.RemoteAddr
}

// MaxBody ограничивает размер тела (для маршрутов, не использующих DecodeJSON).
func MaxBody(n int64) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, n)
			next.ServeHTTP(w, r)
		})
	}
}

// Standard — стандартный набор: RequestContext → Recover → AccessLog (+ extra внутри).
func Standard(log *slog.Logger, extra ...Middleware) Middleware {
	all := append([]Middleware{RequestContext(), Recover(log), AccessLog(log)}, extra...)
	return func(h http.Handler) http.Handler { return Chain(h, all...) }
}
