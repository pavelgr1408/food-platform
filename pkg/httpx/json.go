package httpx

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
)

// DefaultMaxBody — лимит тела запроса по умолчанию (1 MiB).
const DefaultMaxBody = 1 << 20

// DecodeJSON читает ровно один JSON-объект: лимит размера, запрет неизвестных
// полей, проверка Content-Type. При ошибке сама пишет problem и возвращает false.
func DecodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	if ct := r.Header.Get("Content-Type"); ct != "" {
		if mt, _, err := mime.ParseMediaType(ct); err != nil || mt != "application/json" {
			WriteProblem(w, r, http.StatusUnsupportedMediaType, "UNSUPPORTED_MEDIA_TYPE", "Content-Type must be application/json")
			return false
		}
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, DefaultMaxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			WriteProblem(w, r, http.StatusRequestEntityTooLarge, "BODY_TOO_LARGE", "request body too large")
			return false
		}
		WriteProblem(w, r, http.StatusBadRequest, "INVALID_JSON", "malformed JSON body")
		return false
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		WriteProblem(w, r, http.StatusBadRequest, "INVALID_JSON", "request body must contain a single JSON value")
		return false
	}
	return true
}

// WriteJSON пишет значение как application/json.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}
