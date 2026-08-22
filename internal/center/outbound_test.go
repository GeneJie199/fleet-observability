package center

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestOutboundNetworkPolicy(t *testing.T) {
	for _, value := range []string{"127.0.0.1", "10.0.0.1", "::1"} {
		if err := validateOutboundIP(net.ParseIP(value), false); err == nil {
			t.Fatalf("private address %s was allowed by default", value)
		}
		if err := validateOutboundIP(net.ParseIP(value), true); err != nil {
			t.Fatalf("trusted private address %s was rejected: %v", value, err)
		}
	}
	for _, value := range []string{"169.254.169.254", "100.64.0.1", "192.0.2.1", "2001:db8::1"} {
		if err := validateOutboundIP(net.ParseIP(value), true); err == nil {
			t.Fatalf("special address %s was allowed", value)
		}
	}
	if _, err := validateOutboundURL("http://user:secret@example.com/hook"); err == nil {
		t.Fatal("URL credentials were accepted")
	}
	for _, name := range []string{"Host", "Proxy-Authorization", "Bad Header", "X-Test\r\nInjected"} {
		if err := validateOutboundHeaderName(name); err == nil {
			t.Fatalf("unsafe header %q was accepted", name)
		}
	}
	if err := validateOutboundHeaderName("X-Fleet-Signature"); err != nil {
		t.Fatalf("safe header was rejected: %v", err)
	}
}

func TestOutboundClientDoesNotFollowRedirects(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", "http://169.254.169.254/latest/meta-data")
		w.WriteHeader(http.StatusFound)
	}))
	defer server.Close()
	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := newOutboundHTTPClient(time.Second, true).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusFound {
		t.Fatalf("redirect status = %d", response.StatusCode)
	}
}
