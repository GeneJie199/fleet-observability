package center

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync"
	"testing"
)

func diagnosticTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	server, err := New(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	handler := server.Handler()
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)
	return httpServer
}

func TestRuntimeDiagnosticsReportsLiveCounters(t *testing.T) {
	server := diagnosticTestServer(t)
	fetch := func() runtimeDiagnostics {
		t.Helper()
		response, err := server.Client().Get(server.URL + "/api/v1/diagnostics/runtime")
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("status=%d", response.StatusCode)
		}
		if response.Header.Get("Cache-Control") != "no-store" {
			t.Fatal("snapshot must not be cached")
		}
		body, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(body, &fields); err != nil || len(fields) != 7 {
			t.Fatalf("unexpected diagnostic fields: %s (%v)", body, err)
		}
		var value runtimeDiagnostics
		if err := json.Unmarshal(body, &value); err != nil {
			t.Fatal(err)
		}
		if value.Goroutines < 1 || value.HeapAllocBytes == 0 || value.HeapAllocBytes > value.HeapInuseBytes ||
			value.HeapInuseBytes > value.HeapSysBytes || value.HeapSysBytes > value.SystemBytes || value.UptimeSeconds < 0 {
			t.Fatalf("invalid live counters: %+v", value)
		}
		return value
	}
	first := fetch()
	release := make(chan struct{})
	defer close(release)
	var ready sync.WaitGroup
	ready.Add(64)
	for i := 0; i < 64; i++ {
		go func() { ready.Done(); <-release }()
	}
	ready.Wait()
	runtime.GC()
	second := fetch()
	if second.Goroutines < first.Goroutines+16 || second.GCCount <= first.GCCount || second.UptimeSeconds <= first.UptimeSeconds {
		t.Fatalf("counters did not reflect actual goroutines/GC/elapsed time: before=%+v after=%+v", first, second)
	}
}

func TestRuntimeDiagnosticsKeepsRealHTTPBrowserBoundary(t *testing.T) {
	server := diagnosticTestServer(t)
	for _, item := range []struct {
		name, host, origin, site string
		want                     int
	}{
		{"local", "", "", "", 200},
		{"same-origin", "", server.URL, "same-origin", 200},
		{"rebound-host", "attacker.invalid", "", "", 403},
		{"host-suffix", "localhost.attacker.invalid", "", "", 403},
		{"foreign-origin", "", "https://attacker.invalid", "same-origin", 403},
		{"cross-site", "", "", "cross-site", 403},
	} {
		t.Run(item.name, func(t *testing.T) {
			request, err := http.NewRequest(http.MethodGet, server.URL+"/api/v1/diagnostics/runtime", nil)
			if err != nil {
				t.Fatal(err)
			}
			if item.host != "" {
				request.Host = item.host
			}
			request.Header.Set("Origin", item.origin)
			request.Header.Set("Sec-Fetch-Site", item.site)
			request.Header.Set("X-Forwarded-Host", "localhost")
			response, err := server.Client().Do(request)
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			if response.StatusCode != item.want {
				t.Fatalf("status=%d want=%d", response.StatusCode, item.want)
			}
		})
	}
	response, err := server.Client().Post(server.URL+"/api/v1/diagnostics/runtime", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusMethodNotAllowed && response.StatusCode != http.StatusNotFound {
		t.Fatalf("non-GET diagnostic request accepted: %d", response.StatusCode)
	}
}

func TestRuntimeDiagnosticsHonorsProtectedReadAuthentication(t *testing.T) {
	const token = "diagnostics-fixture-token"
	server, err := NewWithOptions(t.TempDir(), token, "", Options{ProtectReads: true})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	for _, item := range []struct {
		name, authorization, host string
		want                      int
	}{
		{"missing", "", "", 401}, {"wrong", "Bearer other-fixture", "", 401},
		{"authorized", "Bearer " + token, "", 200}, {"authorized-rebound", "Bearer " + token, "attacker.invalid", 403},
	} {
		t.Run(item.name, func(t *testing.T) {
			request, _ := http.NewRequest(http.MethodGet, httpServer.URL+"/api/v1/diagnostics/runtime", nil)
			request.Header.Set("Authorization", item.authorization)
			if item.host != "" {
				request.Host = item.host
			}
			response, err := httpServer.Client().Do(request)
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			if response.StatusCode != item.want {
				t.Fatalf("status=%d want=%d", response.StatusCode, item.want)
			}
		})
	}
}
