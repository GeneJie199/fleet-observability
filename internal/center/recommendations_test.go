package center

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRecommendationsExposeSafeSuggestedConfiguration(t *testing.T) {
	s, err := New(t.TempDir(), "secret")
	if err != nil {
		t.Fatal(err)
	}
	report := `{"observed_at":"2026-08-23T00:00:00Z","inventory":{"recommendations":[{"id":"rec-postgres","kind":"postgres","name":"postgres-5432","status":"needs-credentials","reason":"listener found","required_fields":["dsn_env"],"suggested":{"dsn_env":"FLEET_POSTGRES_5432_DSN"},"observed_at":"2026-08-23T00:00:00Z"}]}}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/nodes/node-a/report", strings.NewReader(report))
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("report=%d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/recommendations", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("recommendations=%d %s", rec.Code, rec.Body.String())
	}
	var recommendations []DiscoveryRecommendation
	if err = json.Unmarshal(rec.Body.Bytes(), &recommendations); err != nil {
		t.Fatal(err)
	}
	if len(recommendations) != 1 || recommendations[0].NodeID != "node-a" || recommendations[0].Suggested["dsn_env"] != "FLEET_POSTGRES_5432_DSN" {
		t.Fatalf("unexpected recommendations: %+v", recommendations)
	}
	if strings.Contains(strings.ToLower(rec.Body.String()), "password\":") {
		t.Fatalf("recommendation API leaked an inline password field: %s", rec.Body.String())
	}
}
