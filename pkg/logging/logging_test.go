package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
)

func TestContextFieldsAndRedaction(t *testing.T) {
	var buf bytes.Buffer
	l := New(Options{Service: "orders", Env: "dev", Level: "info", Writer: &buf})
	ctx := WithTraceID(WithRequestID(context.Background(), "req-1"), "abc")
	l.InfoContext(ctx, "hello", "phone", "+79990000000", "password", "hunter2", "orderId", "o1")
	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"service": "orders", "env": "dev", "request_id": "req-1", "trace_id": "abc", "password": "***", "orderId": "o1", "phone": "+799******00"}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("%s=%v want %v", k, m[k], v)
		}
	}
}

func TestLevelFilter(t *testing.T) {
	var buf bytes.Buffer
	l := New(Options{Level: "warn", Writer: &buf})
	l.Info("no")
	if buf.Len() != 0 {
		t.Fatal("info must be filtered")
	}
	l.Warn("yes")
	if buf.Len() == 0 {
		t.Fatal("warn must pass")
	}
}
