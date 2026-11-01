package center

import (
	"net"
	"net/http"
	"strings"
)

// localBrowserRequest binds local listeners to loopback Host and same-origin browser requests.
// Remote listeners retain their explicit remote authentication policy.
func localBrowserRequest(r *http.Request) bool {
	addr, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
	if !ok {
		return true
	}
	bound, _, err := net.SplitHostPort(addr.String())
	if err != nil || !net.ParseIP(bound).IsLoopback() {
		return true
	}
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	ip := net.ParseIP(host)
	if !strings.EqualFold(host, "localhost") && (ip == nil || !ip.IsLoopback()) {
		return false
	}
	if strings.EqualFold(r.Header.Get("Sec-Fetch-Site"), "cross-site") {
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		if origin != scheme+"://"+r.Host {
			return false
		}
	}
	return true
}
