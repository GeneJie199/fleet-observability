package agent

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GeneJie199/fleet-observability/internal/center"
)

func TestPushSendsAuthenticatedReport(t *testing.T) {
	var received center.Report
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/nodes/node-01/report" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer secret" {
			t.Errorf("authorization = %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Errorf("decode report: %v", err)
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	err := Push(context.Background(), Config{
		CenterURL: srv.URL,
		NodeID:    "node-01",
		Token:     "secret",
		Labels:    map[string]string{"version": "1.2.3"},
		Client:    srv.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if received.NodeID != "node-01" || received.Agent.Version == "" || received.ObservedAt == "" {
		t.Fatalf("report = %+v", received)
	}
	if received.Labels["version"] != "1.2.3" || len(received.Metrics) == 0 {
		t.Fatalf("report labels/metrics = %+v / %+v", received.Labels, received.Metrics)
	}
}

func TestPushRejectsUnexpectedCenterStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "no", http.StatusUnauthorized)
	}))
	defer srv.Close()
	err := Push(context.Background(), Config{CenterURL: srv.URL, NodeID: "node-01", Client: srv.Client()})
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("error = %v", err)
	}
}

func TestRunProbes(t *testing.T) {
	httpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("ready"))
	}))
	defer httpSrv.Close()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr == nil {
			_ = conn.Close()
		}
	}()

	path := filepath.Join(t.TempDir(), "probes.json")
	config := ProbeConfig{
		HTTP: []HTTPProbe{
			{Name: "health", URL: httpSrv.URL, WantStatus: http.StatusCreated, Contains: "ready", Required: true},
			{Name: "content-mismatch", URL: httpSrv.URL, WantStatus: http.StatusCreated, Contains: "missing"},
		},
		TCP:       []TCPProbe{{Name: "tcp", Address: listener.Addr().String(), Required: true}},
		Databases: []DatabaseProbe{{Name: "bad-db", Engine: "sqlite", DSNEnv: "IGNORED"}, {Name: "missing-dsn", Engine: "postgres", DSNEnv: "FLEET_TEST_MISSING_DSN"}},
	}
	b, _ := json.Marshal(config)
	if err = os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	results, err := RunProbes(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 5 {
		t.Fatalf("results = %+v", results)
	}
	if results[0].Status != "ok" || results[1].Status != "error" || results[2].Status != "ok" {
		t.Fatalf("probe statuses = %+v", results)
	}
	if !strings.Contains(results[3].Detail, "postgres or mysql") || !strings.Contains(results[4].Detail, "is empty") {
		t.Fatalf("database errors = %+v", results[3:])
	}
}

func TestRunProbesRejectsUnknownAndTrailingFields(t *testing.T) {
	for name, data := range map[string]string{
		"unknown":  `{"unexpected": true}`,
		"trailing": `{} {}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "probes.json")
			if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := RunProbes(context.Background(), path); err == nil {
				t.Fatal("expected invalid config error")
			}
		})
	}
}

func TestParseLabels(t *testing.T) {
	labels, err := ParseLabels([]string{"environment=prod", " region = cn-east "})
	if err != nil || labels["environment"] != "prod" || labels["region"] != "cn-east" {
		t.Fatalf("labels = %v, error = %v", labels, err)
	}
	if _, err = ParseLabels([]string{"invalid"}); err == nil {
		t.Fatal("expected invalid label error")
	}
}
