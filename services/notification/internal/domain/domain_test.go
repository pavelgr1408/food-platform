package domain

import (
	"strings"
	"testing"
)

func TestTypeFor(t *testing.T) {
	want := map[string]Type{"accepted": OrderCreated, "ready": OrderReady, "on_delivery": OnDelivery, "delivered": Delivered,
		"handed_over": HandedOver, "cancelled": OrderCancelled, "not_received": OrderNotReceived}
	for st, typ := range want {
		if got, ok := TypeFor(st); !ok || got != typ {
			t.Errorf("%s: %q %v", st, got, ok)
		}
	}
	for _, st := range []string{"pending_payment", "cooking", "closed", "", "bogus"} {
		if _, ok := TypeFor(st); ok {
			t.Errorf("%q must not produce a notification", st)
		}
	}
}

func TestParseAcceptLanguage(t *testing.T) {
	cases := map[string]Locale{"": RU, "ru": RU, "en": EN, "en-US,en;q=0.9": EN, "de,en;q=0.5": EN, "fr-FR, de": RU,
		"EN_gb": EN, "ru-RU,ru;q=0.9,en;q=0.8": RU, "*": RU, ";q=1": RU}
	for in, want := range cases {
		if got := ParseAcceptLanguage(in); got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
}

func TestValidateToken(t *testing.T) {
	for _, ok := range []string{"a", "abc123:DEF_ghi-jkl", strings.Repeat("x", MaxTokenLen)} {
		if err := ValidateToken(ok); err != nil {
			t.Errorf("%.20q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", " ", "a b", "a\tb", "a\nb", "a\x00b", strings.Repeat("x", MaxTokenLen+1)} {
		if err := ValidateToken(bad); err == nil {
			t.Errorf("%.20q must be rejected", bad)
		}
	}
}

func TestMessagePlatformAndValid(t *testing.T) {
	if APNS.MessagePlatform() != "ios" || FCM.MessagePlatform() != "android" || Huawei.MessagePlatform() != "huawei" {
		t.Fatal("platform mapping")
	}
	if !APNS.Valid() || Platform("ios").Valid() || Platform("").Valid() {
		t.Fatal("valid platforms are apns, fcm, huawei only")
	}
}

func TestRender(t *testing.T) {
	got := Render(OrderReady, "", RU, "R4-261025-0001")
	if got.Title != "Заказ готов" || !strings.Contains(got.Body, "R4-261025-0001") {
		t.Fatalf("%+v", got)
	}
	if got := Render(OrderReady, "", EN, "X1"); got.Title != "Order ready" || got.Body != "Order #X1 is ready." {
		t.Fatalf("%+v", got)
	}
	// точный ключ с причиной точнее общего
	if a, b := Render(OrderCancelled, "payment_timeout", RU, "1"), Render(OrderCancelled, "", RU, "1"); a.Body == b.Body {
		t.Fatalf("reason-specific text expected: %q", a.Body)
	}
	// неизвестная причина — общий текст; неизвестный язык — язык по умолчанию
	if a, b := Render(OrderCancelled, "customer_request", RU, "1"), Render(OrderCancelled, "", RU, "1"); a != b {
		t.Fatalf("unknown reason must fall back: %+v vs %+v", a, b)
	}
	if a, b := Render(OnDelivery, "", Locale("de"), "1"), Render(OnDelivery, "", RU, "1"); a != b {
		t.Fatalf("unknown locale must fall back: %+v", a)
	}
}

func TestEveryTypeHasTextInEveryLocale(t *testing.T) {
	for _, st := range []string{"accepted", "ready", "on_delivery", "delivered", "handed_over", "cancelled", "not_received"} {
		typ, _ := TypeFor(st)
		for _, l := range []Locale{RU, EN} {
			x := Render(typ, "", l, "N")
			if x.Title == "" || !strings.Contains(x.Body, "N") || strings.Contains(x.Body, "%!") {
				t.Errorf("%s/%s: %+v", typ, l, x)
			}
		}
	}
	for _, p := range []string{"payment_timeout", "out_of_stock", "restaurant_closed"} {
		for _, l := range []Locale{RU, EN} {
			if x := Render(OrderCancelled, p, l, "N"); strings.Contains(x.Body, "%!") {
				t.Errorf("%s/%s: %+v", p, l, x)
			}
		}
	}
	for _, p := range []string{"pickup_timeout", "undelivered"} {
		for _, l := range []Locale{RU, EN} {
			if x := Render(OrderNotReceived, p, l, "N"); strings.Contains(x.Body, "%!") {
				t.Errorf("%s/%s: %+v", p, l, x)
			}
		}
	}
}

func TestPriority(t *testing.T) {
	if PriorityOf(OrderReady) != PriorityHigh || PriorityOf(OrderCreated) != PriorityNormal {
		t.Fatal("priority")
	}
}
