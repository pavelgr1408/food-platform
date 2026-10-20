package clientip

import (
	"net/http/httptest"
	"net/netip"
	"testing"
)

func TestResolver(t *testing.T) {
	trusted := Resolver{Trusted: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}}
	cases := []struct {
		name, remote, xff, want string
		r                       Resolver
	}{
		{"direct client, header ignored", "203.0.113.7:5555", "1.2.3.4", "203.0.113.7", trusted},
		{"no trusted proxies configured", "10.1.1.1:80", "1.2.3.4", "10.1.1.1", Resolver{}},
		{"via ingress", "10.1.1.1:80", "198.51.100.9", "198.51.100.9", trusted},
		{"client forged leftmost entries", "10.1.1.1:80", "6.6.6.6, 198.51.100.9", "198.51.100.9", trusted},
		{"two proxies", "10.1.1.1:80", "198.51.100.9, 10.2.2.2", "198.51.100.9", trusted},
		{"all hops trusted", "10.1.1.1:80", "10.3.3.3", "10.1.1.1", trusted},
		{"garbage header", "10.1.1.1:80", "not-an-ip", "10.1.1.1", trusted},
		{"no header", "10.1.1.1:80", "", "10.1.1.1", trusted},
		{"ipv4-mapped peer", "[::ffff:10.1.1.1]:80", "198.51.100.9", "198.51.100.9", trusted},
		{"ipv6 client", "10.1.1.1:80", "2001:db8::1", "2001:db8::1", trusted},
	}
	for _, c := range cases {
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = c.remote
		if c.xff != "" {
			req.Header.Set("X-Forwarded-For", c.xff)
		}
		if got := c.r.From(req); got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
}
