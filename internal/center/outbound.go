package center

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var blockedOutboundRanges = mustParseCIDRs(
	"100.64.0.0/10",
	"192.0.0.0/24",
	"192.0.2.0/24",
	"198.18.0.0/15",
	"198.51.100.0/24",
	"203.0.113.0/24",
	"2001:db8::/32",
)

func mustParseCIDRs(values ...string) []*net.IPNet {
	ranges := make([]*net.IPNet, 0, len(values))
	for _, value := range values {
		_, network, err := net.ParseCIDR(value)
		if err != nil {
			panic(err)
		}
		ranges = append(ranges, network)
	}
	return ranges
}

func validateOutboundURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, errors.New("endpoint must be an absolute HTTP(S) URL")
	}
	if parsed.User != nil || strings.TrimSpace(parsed.Hostname()) == "" {
		return nil, errors.New("endpoint must not contain credentials")
	}
	return parsed, nil
}

func validateOutboundHeaderName(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return errors.New("header name is empty")
	}
	for _, character := range []byte(value) {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || strings.ContainsRune("!#$%&'*+-.^_`|~", rune(character)) {
			continue
		}
		return errors.New("header name contains an invalid character")
	}
	switch http.CanonicalHeaderKey(value) {
	case "Host", "Connection", "Content-Length", "Proxy-Authorization", "Transfer-Encoding", "Upgrade":
		return errors.New("transport-controlled header is not allowed")
	}
	return nil
}

func newOutboundHTTPClient(timeout time.Duration, allowPrivate bool) *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		return dialOutboundContext(ctx, dialer, network, address, allowPrivate)
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func dialOutboundContext(ctx context.Context, dialer *net.Dialer, network, address string, allowPrivate bool) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("invalid outbound address: %w", err)
	}
	addresses, err := resolveOutboundIPs(ctx, host)
	if err != nil {
		return nil, err
	}
	for _, address := range addresses {
		if err := validateOutboundIP(address.IP, allowPrivate); err != nil {
			return nil, err
		}
	}
	selected := addresses[0].IP.String()
	return dialer.DialContext(ctx, network, net.JoinHostPort(selected, port))
}

func resolveOutboundIPs(ctx context.Context, host string) ([]net.IPAddr, error) {
	if parsed := net.ParseIP(strings.TrimSpace(host)); parsed != nil {
		return []net.IPAddr{{IP: parsed}}, nil
	}
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, errors.New("endpoint host could not be resolved")
	}
	if len(addresses) == 0 {
		return nil, errors.New("endpoint host resolved to no addresses")
	}
	return addresses, nil
}

func validateOutboundIP(ip net.IP, allowPrivate bool) error {
	if ip == nil || ip.IsUnspecified() || ip.IsMulticast() || ip.IsInterfaceLocalMulticast() || ip.IsLinkLocalMulticast() || ip.IsLinkLocalUnicast() {
		return errors.New("endpoint resolves to a blocked network range")
	}
	for _, network := range blockedOutboundRanges {
		if network.Contains(ip) {
			return errors.New("endpoint resolves to a blocked network range")
		}
	}
	if ip.IsLoopback() || ip.IsPrivate() {
		if allowPrivate {
			return nil
		}
		return errors.New("endpoint resolves to a private network; set allow_private_network only for a trusted local service")
	}
	if !ip.IsGlobalUnicast() {
		return errors.New("endpoint resolves to a blocked network range")
	}
	return nil
}
