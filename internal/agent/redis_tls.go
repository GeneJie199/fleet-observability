package agent

import (
	"context"
	"crypto/tls"
	"net"
)

type tlsDialer struct {
	dialer     *net.Dialer
	serverName string
}

func (dialer *tlsDialer) DialContext(ctx context.Context, address string) (net.Conn, error) {
	return (&tls.Dialer{NetDialer: dialer.dialer, Config: &tls.Config{MinVersion: tls.VersionTLS12, ServerName: dialer.serverName}}).DialContext(ctx, "tcp", address)
}
