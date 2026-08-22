package center

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNotificationDeliveryIsPersistentGroupedAndDeduplicated(t *testing.T) {
	var received int
	var body map[string]any
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received++
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode webhook: %v", err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer endpoint.Close()
	t.Setenv("FLEET_TEST_WEBHOOK", endpoint.URL)
	dir := t.TempDir()
	config := NotificationConfig{Channels: []NotificationChannel{{ID: "operations", Type: "webhook", Enabled: true, EndpointEnv: "FLEET_TEST_WEBHOOK"}}, Policy: NotificationPolicy{DedupeWindowSeconds: 600, MaxQueue: 10, MaxAttempts: 3, InitialRetrySeconds: 1}}
	engine, err := NewNotificationEngine(dir, &config, "")
	if err != nil {
		t.Fatal(err)
	}
	alerts := []Alert{{ID: "alert-a", NodeID: "node-a", Severity: "critical", Title: "API down"}, {ID: "alert-b", NodeID: "node-b", Severity: "warning", Title: "Disk high"}}
	if err := engine.Enqueue(alerts); err != nil {
		t.Fatal(err)
	}
	if err := engine.Enqueue(alerts); err != nil {
		t.Fatal(err)
	}
	if err := engine.DrainDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if received != 1 || body["event"] != "fleet.alert.opened" {
		t.Fatalf("received=%d body=%+v", received, body)
	}
	deliveries, err := engine.readDeliveries()
	if err != nil || len(deliveries) != 1 || deliveries[0].Status != "delivered" || len(deliveries[0].AlertIDs) != 2 {
		t.Fatalf("deliveries=%+v error=%v", deliveries, err)
	}
	restarted, err := NewNotificationEngine(dir, &config, "")
	if err != nil {
		t.Fatal(err)
	}
	if deliveries, err = restarted.readDeliveries(); err != nil || len(deliveries) != 1 || deliveries[0].DeliveredAt == "" {
		t.Fatalf("restarted deliveries=%+v error=%v", deliveries, err)
	}
}

func TestNotificationSuppressionAndRetry(t *testing.T) {
	now := time.Now().UTC()
	config := NotificationConfig{
		Channels: []NotificationChannel{{ID: "operations", Type: "slack", Enabled: true, EndpointEnv: "FLEET_TEST_SLACK"}},
		Policy:   NotificationPolicy{DedupeWindowSeconds: 60, MaxQueue: 10, MaxAttempts: 3, InitialRetrySeconds: 1, Suppressions: []NotificationSuppression{{NodeID: "node-maintenance", Until: now.Add(time.Hour).Format(time.RFC3339), Reason: "planned work"}}},
	}
	dir := t.TempDir()
	engine, err := NewNotificationEngine(dir, &config, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("FLEET_TEST_SLACK", "http://127.0.0.1:1/unavailable")
	if err := engine.Enqueue([]Alert{{ID: "suppressed", NodeID: "node-maintenance", Severity: "warning"}, {ID: "active", NodeID: "node-a", Severity: "warning"}}); err != nil {
		t.Fatal(err)
	}
	if err := engine.DrainDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	deliveries, _ := engine.readDeliveries()
	if len(deliveries) != 2 || deliveries[0].Status != "pending" || deliveries[0].Attempts != 1 || deliveries[1].Status != "suppressed" || !strings.Contains(deliveries[1].SuppressedBy, "planned work") {
		t.Fatalf("deliveries=%+v", deliveries)
	}

	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer endpoint.Close()
	t.Setenv("FLEET_TEST_SLACK", endpoint.URL)
	deliveries[0].NextAttemptAt = now.Add(-time.Second).Format(time.RFC3339Nano)
	if err := engine.writeDeliveries(deliveries); err != nil {
		t.Fatal(err)
	}
	if err := engine.DrainDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	deliveries, _ = engine.readDeliveries()
	if deliveries[0].Status != "delivered" || deliveries[0].Attempts != 2 {
		t.Fatalf("retried delivery=%+v", deliveries[0])
	}
}

func TestNotificationConfigRejectsInlineEndpointAndSecrets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notifications.json")
	if err := os.WriteFile(path, []byte(`{"channels":[{"id":"ops","type":"webhook","enabled":true}],"policy":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadNotificationConfig(path); err == nil || !strings.Contains(err.Error(), "endpoint_env") {
		t.Fatalf("expected endpoint_env validation, got %v", err)
	}
}
