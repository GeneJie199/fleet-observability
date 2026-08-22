//go:build windows

package agent

import (
	"net"
	"testing"
)

func TestWindowsDiscoveryFindsLocalListenerWithoutCommandLine(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	listeners, err := discoverListeners(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range listeners {
		if item.Port == port {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("listener on port %d was not discovered: %+v", port, listeners)
	}
	processes, err := discoverProcesses(t.Context(), 32)
	if err != nil {
		t.Fatal(err)
	}
	if len(processes) == 0 {
		t.Fatal("tasklist returned no process names")
	}
}

func TestParseWindowsEndpoint(t *testing.T) {
	for input, expected := range map[string]int{"127.0.0.1:8770": 8770, "0.0.0.0:5432": 5432, "[::]:443": 443} {
		_, port, ok := parseWindowsEndpoint(input)
		if !ok || port != expected {
			t.Fatalf("parse %q = %d, %v", input, port, ok)
		}
	}
}
