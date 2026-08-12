package agent

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestApplicationCollectorCollectsNginxAndRedisNatively(t *testing.T) {
	nginx := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("X-Monitor-Key") != "test-secret" {
			http.Error(response, "unauthorized", http.StatusUnauthorized)
			return
		}
		fmt.Fprint(response, "Active connections: 7\nserver accepts handled requests\n 100 99 321\nReading: 1 Writing: 2 Waiting: 4\n")
	}))
	defer nginx.Close()
	t.Setenv("FLEET_NGINX_HEADER", "test-secret")

	redisAddress, stopRedis := startFakeRedis(t, "# Server\r\nuptime_in_seconds:42\r\n# Clients\r\nconnected_clients:3\r\nblocked_clients:1\r\n# Memory\r\nused_memory:2048\r\nused_memory_rss:4096\r\n# Stats\r\ntotal_commands_processed:100\r\nkeyspace_hits:80\r\nkeyspace_misses:20\r\n# Keyspace\r\ndb0:keys=9,expires=2,avg_ttl=1000\r\n")
	defer stopRedis()

	dir := t.TempDir()
	configPath := filepath.Join(dir, "applications.json")
	config := fmt.Sprintf(`{"max_concurrency":2,"nginx":[{"name":"edge","url":%q,"header_env":{"X-Monitor-Key":"FLEET_NGINX_HEADER"},"required":true}],"redis":[{"name":"cache","address":%q,"required":true}]}`, nginx.URL, redisAddress)
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	collector := ApplicationCollector{ConfigPath: configPath, Labels: map[string]string{"environment": "test"}}
	collection, err := collector.Collect(context.Background(), time.Unix(1_700_000_000, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	if len(collection.Events) != 0 {
		t.Fatalf("events=%+v", collection.Events)
	}
	assertApplicationPoint(t, collection, "nginx_connections_active", "edge", 7)
	assertApplicationPoint(t, collection, "nginx_http_requests_total", "edge", 321)
	assertApplicationPoint(t, collection, "redis_connected_clients", "cache", 3)
	assertApplicationPoint(t, collection, "redis_used_memory_bytes", "cache", 2048)
	assertApplicationPoint(t, collection, "redis_keys", "cache", 9)
	applications, ok := collection.ReportMetrics["applications"].([]map[string]any)
	if !ok || len(applications) != 2 {
		t.Fatalf("application report=%#v", collection.ReportMetrics["applications"])
	}
}

func TestApplicationCollectorKeepsHealthyTargetsWhenRequiredTargetFails(t *testing.T) {
	nginx := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		fmt.Fprint(response, "Active connections: 1\nserver accepts handled requests\n 2 2 3\nReading: 0 Writing: 1 Waiting: 0\n")
	}))
	defer nginx.Close()
	dir := t.TempDir()
	path := filepath.Join(dir, "applications.json")
	config := fmt.Sprintf(`{"nginx":[{"name":"healthy","url":%q}],"redis":[{"name":"required-cache","address":"127.0.0.1:1","timeout_seconds":1,"required":true}]}`, nginx.URL)
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	collection, err := (ApplicationCollector{ConfigPath: path}).Collect(context.Background(), time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	assertApplicationPoint(t, collection, "nginx_connections_active", "healthy", 1)
	assertApplicationPoint(t, collection, "application_target_up", "required-cache", 0)
	if len(collection.Events) != 1 || collection.Events[0].Severity != "error" || strings.Contains(collection.Events[0].Message, "password") {
		t.Fatalf("failure event=%+v", collection.Events)
	}
}

func TestApplicationConfigRejectsUnknownFieldsAndDuplicateNames(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "applications.json")
	if err := os.WriteFile(path, []byte(`{"nginx":[{"name":"same","url":"http://127.0.0.1/status","password":"secret"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readApplicationConfig(path); err == nil {
		t.Fatal("inline secret field must be rejected")
	}
	if err := os.WriteFile(path, []byte(`{"nginx":[{"name":"same","url":"http://127.0.0.1/status"}],"redis":[{"name":"same","address":"127.0.0.1:6379"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readApplicationConfig(path); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate error=%v", err)
	}
}

func assertApplicationPoint(t *testing.T, collection Collection, metric, application string, value float64) {
	t.Helper()
	for _, point := range collection.Points {
		if point.Metric == metric && point.Labels["application"] == application {
			if point.Value != value {
				t.Fatalf("%s value=%v want=%v", metric, point.Value, value)
			}
			return
		}
	}
	t.Fatalf("missing %s for %s in %+v", metric, application, collection.Points)
}

func startFakeRedis(t *testing.T, info string) (string, func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		defer connection.Close()
		reader := bufio.NewReader(connection)
		for lines := 0; lines < 5; lines++ {
			line, readErr := reader.ReadString('\n')
			if readErr != nil || strings.EqualFold(strings.TrimSpace(line), "$3") {
				break
			}
		}
		fmt.Fprintf(connection, "$%d\r\n%s\r\n", len(info), info)
	}()
	return listener.Addr().String(), func() {
		listener.Close()
		<-done
	}
}
