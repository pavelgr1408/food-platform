package mock

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestOutcomeForAmount(t *testing.T) {
	for minor, want := range map[int64]Outcome{
		54900: OutcomePaid, 54913: OutcomeDeclined, 54999: OutcomeSilent, 13: OutcomeDeclined, 99: OutcomeSilent, 100: OutcomePaid, -13: OutcomeDeclined,
	} {
		if got := OutcomeForAmount(minor); got != want {
			t.Errorf("%d: got %s want %s", minor, got, want)
		}
	}
}

func newEnv() *Env {
	return NewEnv(slog.New(slog.NewJSONHandler(io.Discard, nil)), nil, 20*time.Millisecond)
}

func TestAfterRunsAfterDelayAndStopsOnCancel(t *testing.T) {
	env := newEnv()
	var ran atomic.Int32
	env.After(context.Background(), func() { ran.Add(1) })
	ctx, cancel := context.WithCancel(context.Background())
	env.AfterD(ctx, time.Hour, func() { ran.Add(10) })
	cancel()
	env.Wait()
	if ran.Load() != 1 {
		t.Fatalf("ran=%d (delayed effect must run, cancelled must not)", ran.Load())
	}
}

func TestBuildUnknown(t *testing.T) {
	if _, err := Build([]string{"ping", "nope"}); err == nil || !strings.Contains(err.Error(), "available") {
		t.Fatalf("want helpful error, got %v", err)
	}
	if mods, err := Build([]string{"ping"}); err != nil || len(mods) != 1 {
		t.Fatal(err)
	}
}

func TestAdminAndInspect(t *testing.T) {
	env, rec := newEnv(), NewRecorder(2)
	m, _ := Build([]string{"ping"})
	mux := http.NewServeMux()
	m[0].Register(mux, env)
	MountAdmin(mux, env, rec)
	h := rec.Middleware(mux)

	do := func(method, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
		return w
	}
	if w := do("POST", "/ping", `{"a":1}`); w.Code != 202 {
		t.Fatal(w.Code)
	}
	do("POST", "/ping", `{"a":2}`)
	do("POST", "/ping", `{"a":3}`) // кольцевой буфер на 2
	w := do("GET", "/_inspect/requests", "")
	if strings.Count(w.Body.String(), `"method":"POST"`) != 2 || !strings.Contains(w.Body.String(), `{\"a\":3}`) || strings.Contains(w.Body.String(), `{\"a\":1}`) {
		t.Fatalf("ring buffer: %s", w.Body)
	}
	if w := do("POST", "/_admin/delay?d=5s", ""); w.Code != 200 || env.Delay() != 5*time.Second {
		t.Fatalf("delay: %d %v", w.Code, env.Delay())
	}
	if w := do("POST", "/_admin/delay?d=nonsense", ""); w.Code != 400 {
		t.Fatal("bad delay must be rejected")
	}
	do("POST", "/_admin/reset", "")
	if w := do("GET", "/_inspect/requests", ""); strings.TrimSpace(w.Body.String()) != "[]" && strings.TrimSpace(w.Body.String()) != "null" {
		t.Fatalf("reset: %s", w.Body)
	}
}
