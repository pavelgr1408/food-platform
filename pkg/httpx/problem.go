// Package httpx — общий HTTP-слой сервисов: middleware, JSON/problem-хелперы,
// health, сервер с graceful shutdown и исходящий клиент с retry/circuit breaker.
package httpx

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
)

// ContentTypeProblem — media type RFC 9457.
const ContentTypeProblem = "application/problem+json"

// Problem — тело ошибки; совпадает со схемой Problem в contracts/openapi/_common.yaml.
type Problem struct {
	Type     string `json:"type,omitempty"`
	Title    string `json:"title"`
	Status   int    `json:"status"`
	Detail   string `json:"detail,omitempty"`
	Instance string `json:"instance,omitempty"`
	Code     string `json:"code,omitempty"`
}

// Error — доменная ошибка с HTTP-семантикой. Сервисный слой возвращает её,
// httpapi превращает в problem через HandleError.
type Error struct {
	Status int
	Code   string
	Detail string
}

func (e *Error) Error() string { return e.Code + ": " + e.Detail }

// NewError создаёт *Error.
func NewError(status int, code, detail string) *Error {
	return &Error{Status: status, Code: code, Detail: detail}
}

// WriteProblem пишет application/problem+json.
func WriteProblem(w http.ResponseWriter, r *http.Request, status int, code, detail string) {
	p := Problem{Type: "about:blank", Title: http.StatusText(status), Status: status, Detail: detail, Code: code}
	if r != nil {
		p.Instance = r.URL.Path
	}
	w.Header().Set("Content-Type", ContentTypeProblem)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(p)
}

// HandleError: *Error → его статус; всё остальное → 500 без утечки деталей, с логом.
func HandleError(w http.ResponseWriter, r *http.Request, log *slog.Logger, err error) {
	var he *Error
	if errors.As(err, &he) {
		WriteProblem(w, r, he.Status, he.Code, he.Detail)
		return
	}
	if log != nil {
		log.ErrorContext(r.Context(), "unhandled error", "error", err, "path", r.URL.Path)
	}
	WriteProblem(w, r, http.StatusInternalServerError, "INTERNAL", "internal server error")
}
