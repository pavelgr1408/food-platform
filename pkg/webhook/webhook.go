// Package webhook — подпись и проверка входящих/исходящих вебхуков (HMAC-SHA256).
//
// Схема (как у Stripe): заголовок X-Signature: t=<unix>,v1=<hex(HMAC(secret, t+"."+body))>.
// Verify проверяет подпись в константное время и отвергает запросы старше окна
// (защита от replay); дедупликацию по event-id делает потребитель (webhook_events).
package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Header — имя заголовка с подписью.
const Header = "X-Signature"

// DefaultTolerance — допустимое расхождение часов / окно replay.
const DefaultTolerance = 5 * time.Minute

var (
	ErrMalformed = errors.New("webhook: malformed signature header")
	ErrMismatch  = errors.New("webhook: signature mismatch")
	ErrExpired   = errors.New("webhook: timestamp outside tolerance")
)

// Sign возвращает значение заголовка X-Signature для тела body.
func Sign(secret []byte, body []byte, now time.Time) string {
	ts := strconv.FormatInt(now.Unix(), 10)
	return fmt.Sprintf("t=%s,v1=%s", ts, mac(secret, ts, body))
}

// Verify проверяет заголовок. tolerance<=0 → DefaultTolerance.
func Verify(secret []byte, body []byte, header string, now time.Time, tolerance time.Duration) error {
	if tolerance <= 0 {
		tolerance = DefaultTolerance
	}
	var ts, sig string
	for _, part := range strings.Split(header, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			return ErrMalformed
		}
		switch k {
		case "t":
			ts = v
		case "v1":
			sig = v
		}
	}
	sec, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || sig == "" {
		return ErrMalformed
	}
	want := mac(secret, ts, body)
	got, err := hex.DecodeString(sig)
	wantB, _ := hex.DecodeString(want)
	if err != nil || !hmac.Equal(got, wantB) {
		return ErrMismatch
	}
	if d := now.Sub(time.Unix(sec, 0)); d > tolerance || d < -tolerance {
		return ErrExpired
	}
	return nil
}

func mac(secret []byte, ts string, body []byte) string {
	h := hmac.New(sha256.New, secret)
	h.Write([]byte(ts))
	h.Write([]byte{'.'})
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}
