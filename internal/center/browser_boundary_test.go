package center

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRealLoopbackBrowserBoundary(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !localBrowserRequest(r) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	for _, tc := range []struct {
		name, host, origin, site string
		want                     int
	}{
		{"normal", "", "", "", 204},
		{"rebound", "attacker.example", "", "", 403},
		{"host-suffix", "localhost.attacker.example", "", "", 403},
		{"foreign-origin", "", "https://attacker.example", "", 403},
		{"cross-site", "", "", "cross-site", 403},
		{"same-origin", "", srv.URL, "same-origin", 204},
		{"loopback-alias", "127.0.0.136:8765", "", "", 204},
		{"ipv6-loopback", "[::1]:8765", "", "", 204},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, _ := http.NewRequest("GET", srv.URL, nil)
			if tc.host != "" {
				req.Host = tc.host
			}
			req.Header.Set("Origin", tc.origin)
			req.Header.Set("Sec-Fetch-Site", tc.site)
			res, err := srv.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			res.Body.Close()
			if res.StatusCode != tc.want {
				t.Fatalf("got %d want %d", res.StatusCode, tc.want)
			}
		})
	}
}
