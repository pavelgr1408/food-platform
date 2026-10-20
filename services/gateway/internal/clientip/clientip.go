// Package clientip определяет адрес клиента с учётом доверенных прокси (ingress, балансировщик).
package clientip

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// Resolver определяет клиента. X-Forwarded-For принимается только от доверенных прокси: иначе любой
// клиент подставил бы чужой адрес и обошёл лимит запросов.
type Resolver struct{ Trusted []netip.Prefix }

func (r Resolver) trusted(a netip.Addr) bool {
	for _, p := range r.Trusted {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// From возвращает адрес клиента. Если непосредственный сосед не доверенный — это он сам (заголовок
// игнорируется). Иначе идём по X-Forwarded-For справа налево и берём первый адрес, не принадлежащий
// доверенным прокси: цепочку слева клиент мог подделать, а справа её дописывали наши прокси.
func (r Resolver) From(req *http.Request) string {
	host, _, err := net.SplitHostPort(req.RemoteAddr)
	if err != nil {
		host = req.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	peer = peer.Unmap()
	if !r.trusted(peer) {
		return peer.String()
	}
	hops := strings.Split(strings.Join(req.Header.Values("X-Forwarded-For"), ","), ",")
	for i := len(hops) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			return peer.String() // мусор в заголовке — не доверяем остальному
		}
		a = a.Unmap()
		if !r.trusted(a) {
			return a.String()
		}
	}
	return peer.String()
}

// PeerTrusted — непосредственный сосед запроса является доверенным прокси.
func (r Resolver) PeerTrusted(req *http.Request) bool {
	host, _, err := net.SplitHostPort(req.RemoteAddr)
	if err != nil {
		host = req.RemoteAddr
	}
	a, err := netip.ParseAddr(host)
	return err == nil && r.trusted(a.Unmap())
}
