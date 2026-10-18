package mock

import (
	"context"
	"net/http"
)

// ping — контрольный модуль: проверяет, что бинарь, порты, admin и таймеры работают.
// POST /ping → 202; через Env.Delay() запись «pong» появляется в /_inspect/requests.
type ping struct{}

func init() { Register("ping", func() Module { return &ping{} }) }

func (*ping) Name() string        { return "ping" }
func (*ping) DefaultAddr() string { return ":9090" }

func (p *ping) Register(mux *http.ServeMux, env *Env) {
	mux.HandleFunc("POST /ping", func(w http.ResponseWriter, r *http.Request) {
		env.After(r.Context(), func() {}) // демонстрация: эффект после задержки
		w.WriteHeader(http.StatusAccepted)
	})
}

func (*ping) Start(context.Context, *Env) error { return nil }
