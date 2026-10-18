package webhook

import (
	"errors"
	"testing"
	"time"
)

func TestSignVerify(t *testing.T) {
	secret, body, now := []byte("s3cret"), []byte(`{"orderId":"o1","status":"paid"}`), time.Unix(1_800_000_000, 0)
	h := Sign(secret, body, now)
	if err := Verify(secret, body, h, now, 0); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		secret []byte
		body   []byte
		header string
		now    time.Time
		want   error
	}{
		{"tampered body", secret, []byte(`{"orderId":"o1","status":"declined"}`), h, now, ErrMismatch},
		{"wrong secret", []byte("other"), body, h, now, ErrMismatch},
		{"replay too old", secret, body, h, now.Add(10 * time.Minute), ErrExpired},
		{"from the future", secret, body, h, now.Add(-10 * time.Minute), ErrExpired},
		{"garbage", secret, body, "nonsense", now, ErrMalformed},
		{"no sig", secret, body, "t=123", now, ErrMalformed},
	}
	for _, c := range cases {
		if err := Verify(c.secret, c.body, c.header, c.now, 0); !errors.Is(err, c.want) {
			t.Errorf("%s: got %v want %v", c.name, err, c.want)
		}
	}
}
