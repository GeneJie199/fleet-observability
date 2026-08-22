package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRecognizedProcessResourceOmitsCommandAndUsesStableIdentity(t *testing.T) {
	command := `/usr/sbin/nginx --password=super-secret --token bearer-secret`
	first, recommendation, kind, ok := recognizedProcessResource("node-a", command, "2026-08-23T00:00:00Z")
	if !ok || kind != "nginx" || first.ID != "native.process:node-a:nginx" {
		t.Fatalf("unexpected process discovery: resource=%+v recommendation=%+v kind=%q ok=%v", first, recommendation, kind, ok)
	}
	second, _, _, _ := recognizedProcessResource("node-a", `/usr/sbin/nginx -g daemon`, "2026-08-23T00:01:00Z")
	if second.ID != first.ID {
		t.Fatalf("process identity changed with command arguments: %q != %q", second.ID, first.ID)
	}
	b, err := json.Marshal(struct {
		Resource       discoveryResource       `json:"resource"`
		Recommendation discoveryRecommendation `json:"recommendation"`
	}{first, recommendation})
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"super-secret", "bearer-secret", "--password", "--token"} {
		if strings.Contains(string(b), secret) {
			t.Fatalf("serialized discovery leaked command data %q: %s", secret, b)
		}
	}
}

func TestListenerRecommendationUsesEnvironmentReferences(t *testing.T) {
	recommendation := listenerRecommendation("node-a", discoveredListener{Address: "0.0.0.0", Port: 5432}, "2026-08-23T00:00:00Z")
	if recommendation == nil || recommendation.Status != "needs-credentials" {
		t.Fatalf("unexpected recommendation: %+v", recommendation)
	}
	b, err := json.Marshal(recommendation)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"dsn_env":"FLEET_POSTGRES_5432_DSN"`) {
		t.Fatalf("recommendation must use an environment reference: %s", b)
	}
	for _, forbidden := range []string{"password\":", "secret\":", "token\":"} {
		if strings.Contains(strings.ToLower(string(b)), forbidden) {
			t.Fatalf("recommendation contains an inline credential field %q: %s", forbidden, b)
		}
	}
}

func TestNativeInventoryIsValidAndContainsNoCommandLine(t *testing.T) {
	b, err := DiscoverInventory(t.Context(), "node-test")
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Schema    string              `json:"schema"`
		Resources []discoveryResource `json:"resources"`
	}
	if err = json.Unmarshal(b, &document); err != nil {
		t.Fatal(err)
	}
	if document.Schema != "fleetscope.native-inventory/v1" || len(document.Resources) == 0 {
		t.Fatalf("unexpected inventory: %s", b)
	}
	if strings.Contains(string(b), `"command"`) {
		t.Fatalf("inventory must not serialize process command lines: %s", b)
	}
}
