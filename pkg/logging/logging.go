// Package logging — структурные JSON-логи (slog) для EFK.
//
// Поля: time, level, msg, service, env + request_id/trace_id из контекста.
// Секреты и PII маскируются по имени атрибута (см. sensitiveKeys).
package logging

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
)

type ctxKey int

const (
	requestIDKey ctxKey = iota
	traceIDKey
)

// Options настраивает логгер.
type Options struct {
	Service string
	Env     string
	Level   string // debug|info|warn|error
	Writer  io.Writer
}

// New создаёт JSON-логгер с контекстными полями и маскированием.
func New(o Options) *slog.Logger {
	w := o.Writer
	if w == nil {
		w = os.Stdout
	}
	h := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: parseLevel(o.Level), ReplaceAttr: redact})
	var base slog.Handler = ctxHandler{h}
	l := slog.New(base)
	if o.Service != "" {
		l = l.With("service", o.Service)
	}
	if o.Env != "" {
		l = l.With("env", o.Env)
	}
	return l
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	}
	return slog.LevelInfo
}

// ctxHandler добавляет request_id/trace_id из контекста в каждую запись.
type ctxHandler struct{ slog.Handler }

func (h ctxHandler) Handle(ctx context.Context, r slog.Record) error {
	if id := RequestID(ctx); id != "" {
		r.AddAttrs(slog.String("request_id", id))
	}
	if id := TraceID(ctx); id != "" {
		r.AddAttrs(slog.String("trace_id", id))
	}
	return h.Handler.Handle(ctx, r)
}
func (h ctxHandler) WithAttrs(a []slog.Attr) slog.Handler { return ctxHandler{h.Handler.WithAttrs(a)} }
func (h ctxHandler) WithGroup(n string) slog.Handler      { return ctxHandler{h.Handler.WithGroup(n)} }

// WithRequestID кладёт идентификатор запроса в контекст.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey, id)
}

// RequestID возвращает идентификатор запроса из контекста.
func RequestID(ctx context.Context) string { s, _ := ctx.Value(requestIDKey).(string); return s }

// WithTraceID кладёт trace id (W3C, 32 hex) в контекст.
func WithTraceID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, traceIDKey, id)
}

// TraceID возвращает trace id из контекста.
func TraceID(ctx context.Context) string { s, _ := ctx.Value(traceIDKey).(string); return s }

var sensitiveKeys = map[string]struct{}{
	"password": {}, "password_hash": {}, "token": {}, "access_token": {}, "refresh_token": {},
	"authorization": {}, "secret": {}, "private_key": {}, "signature": {}, "card_number": {}, "cvv": {},
}

var phoneKeys = map[string]struct{}{"phone": {}, "customer_phone": {}}

func redact(groups []string, a slog.Attr) slog.Attr {
	key := strings.ToLower(a.Key)
	if _, ok := sensitiveKeys[key]; ok {
		return slog.String(a.Key, "***")
	}
	if _, ok := phoneKeys[key]; ok && a.Value.Kind() == slog.KindString {
		return slog.String(a.Key, MaskPhone(a.Value.String()))
	}
	return a
}

// MaskPhone оставляет первые 4 и последние 2 символа: +799*****00.
func MaskPhone(p string) string {
	r := []rune(p)
	if len(r) <= 6 {
		return strings.Repeat("*", len(r))
	}
	return string(r[:4]) + strings.Repeat("*", len(r)-6) + string(r[len(r)-2:])
}
