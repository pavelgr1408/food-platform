package returnurl

import "testing"

func TestCheck(t *testing.T) {
	a, err := NewAllowed("https://Shop.Example", "http://localhost:3000/", "https://app.example:8443")
	if err != nil {
		t.Fatal(err)
	}
	ok := []string{
		"https://shop.example/orders/1", "https://SHOP.example:443/x?y=1#z", "http://localhost:3000/orders",
		"https://app.example:8443/", "https://shop.example",
	}
	bad := []string{
		"https://evil.example/orders", "http://shop.example/orders", "https://shop.example.evil.com/", "https://evil.com/https://shop.example",
		"https://shop.example@evil.com/", "https://shop.example:pass@shop.example/", "https://app.example/", "http://localhost:3001/",
		"javascript:alert(1)", "ftp://shop.example/", "//evil.example/", "/orders", "", "https:shop.example", "https://shop.example\\@evil.com/",
		"https://evil.com#@shop.example", "https://xn--shp-example.test/",
	}
	for _, u := range ok {
		if err := a.Check(u); err != nil {
			t.Errorf("%q must pass: %v", u, err)
		}
	}
	for _, u := range bad {
		if err := a.Check(u); err == nil {
			t.Errorf("%q must be rejected", u)
		}
	}
}

func TestOriginValidation(t *testing.T) {
	for _, o := range []string{"*", "https://*.shop.example", "https://shop.example/path", "https://shop.example?x=1", "https://u@shop.example", "ftp://x", "shop.example", ""} {
		if _, err := Origin(o); err == nil {
			t.Errorf("%q must be rejected", o)
		}
	}
	for in, want := range map[string]string{"HTTPS://Shop.Example:443/": "https://shop.example", "http://[::1]:3000": "http://[::1]:3000", "http://a:80": "http://a"} {
		if got, err := Origin(in); err != nil || got != want {
			t.Errorf("%q → %q, %v; want %q", in, got, err, want)
		}
	}
}
