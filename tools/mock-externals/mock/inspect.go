package mock

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Recorded — принятый моком запрос.
type Recorded struct {
	At     time.Time           `json:"at"`
	Method string              `json:"method"`
	Path   string              `json:"path"`
	Header map[string][]string `json:"header,omitempty"`
	Body   string              `json:"body,omitempty"`
}

// Recorder хранит последние N запросов (кольцевой буфер) для /_inspect.
type Recorder struct {
	mu   sync.Mutex
	max  int
	data []Recorded
}

// NewRecorder создаёт рекордер на n записей.
func NewRecorder(n int) *Recorder { return &Recorder{max: n} }

// Middleware записывает запрос (кроме /_admin и /_inspect) и пропускает дальше.
func (r *Recorder) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if !strings.HasPrefix(req.URL.Path, "/_") {
			var body []byte
			if req.Body != nil {
				body, _ = io.ReadAll(io.LimitReader(req.Body, 64<<10))
				req.Body = io.NopCloser(strings.NewReader(string(body)))
			}
			r.Add(Recorded{At: time.Now().UTC(), Method: req.Method, Path: req.URL.RequestURI(), Header: req.Header.Clone(), Body: string(body)})
		}
		next.ServeHTTP(w, req)
	})
}

// Add добавляет запись (используют и модули для исходящих вебхуков/пушей).
func (r *Recorder) Add(rec Recorded) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.data = append(r.data, rec)
	if len(r.data) > r.max {
		r.data = r.data[len(r.data)-r.max:]
	}
}

// Snapshot — копия записей.
func (r *Recorder) Snapshot() []Recorded {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Recorded(nil), r.data...)
}

// Reset очищает записи.
func (r *Recorder) Reset() { r.mu.Lock(); r.data = nil; r.mu.Unlock() }

// MountAdmin вешает общие ручки:
//
//	GET  /_inspect/requests      — принятые запросы
//	POST /_admin/reset           — очистить записи
//	GET  /_admin/delay           — текущая задержка; POST /_admin/delay?d=5s — изменить
func MountAdmin(mux *http.ServeMux, env *Env, rec *Recorder) {
	mux.HandleFunc("GET /_inspect/requests", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, rec.Snapshot())
	})
	mux.HandleFunc("POST /_admin/reset", func(w http.ResponseWriter, _ *http.Request) {
		rec.Reset()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /_admin/delay", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"delay": env.Delay().String()})
	})
	mux.HandleFunc("POST /_admin/delay", func(w http.ResponseWriter, r *http.Request) {
		d, err := time.ParseDuration(r.URL.Query().Get("d"))
		if err != nil || d < 0 || d > time.Minute {
			http.Error(w, "query d must be a duration in [0, 1m], e.g. ?d=500ms", http.StatusBadRequest)
			return
		}
		env.SetDelay(d)
		writeJSON(w, http.StatusOK, map[string]string{"delay": d.String()})
	})
	mux.HandleFunc("GET /_admin/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "OK"})
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
