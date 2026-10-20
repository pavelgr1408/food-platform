package routes

import (
	"net/http"
	"strings"
	"testing"
)

func TestEmbeddedTableIsValidAndRegistersInServeMux(t *testing.T) {
	rs, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	// ServeMux паникует на конфликтующих паттернах — проверяем, что таблица ему подходит.
	mux := http.NewServeMux()
	for _, r := range rs {
		mux.HandleFunc(r.Pattern, func(http.ResponseWriter, *http.Request) {})
	}
	for _, r := range rs {
		if r.Pattern == "POST /auth/login" && r.Limit != LimitAuth {
			t.Fatal("login must be in the strict limit class")
		}
		if (r.Pattern == "POST /orders" || r.Pattern == "POST /orders/{orderId}/payments") && !r.Sensitive {
			t.Fatalf("%s moves money and must be sensitive", r.Pattern)
		}
	}
}

func TestInternalPathsAreNotPublishable(t *testing.T) {
	for _, p := range []string{"POST /auth/introspect", "POST /auth/authorize", "POST /webhooks/payments/gateway", "GET /metrics"} {
		_, err := Parse([]byte(`[{"pattern":"` + p + `","upstream":"auth","access":"public"}]`))
		if err == nil || !strings.Contains(err.Error(), "must not be published") {
			t.Errorf("%s: want rejection, got %v", p, err)
		}
	}
}

func TestParseRejectsBadRows(t *testing.T) {
	cases := map[string]string{
		"empty":            `[]`,
		"bad pattern":      `[{"pattern":"orders","upstream":"orders","access":"public"}]`,
		"duplicate":        `[{"pattern":"GET /a","upstream":"x","access":"public"},{"pattern":"GET /a","upstream":"x","access":"public"}]`,
		"no upstream":      `[{"pattern":"GET /a","access":"public"}]`,
		"bad access":       `[{"pattern":"GET /a","upstream":"x","access":"anyone"}]`,
		"unknown role":     `[{"pattern":"GET /a","upstream":"x","access":"required","roles":["ADMIN"]}]`,
		"roles w/o auth":   `[{"pattern":"GET /a","upstream":"x","access":"optional","roles":["STAFF"]}]`,
		"public sensitive": `[{"pattern":"POST /a","upstream":"x","access":"public","sensitive":true}]`,
		"bad limit":        `[{"pattern":"GET /a","upstream":"x","access":"public","limit":"turbo"}]`,
		"not json":         `nope`,
	}
	for name, in := range cases {
		if _, err := Parse([]byte(in)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}
