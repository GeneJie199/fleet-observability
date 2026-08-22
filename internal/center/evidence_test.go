package center

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/GeneJie199/fleet-observability/internal/events"
)

func TestEvidencePackageRedactsSecretsAndExcludesRawLogsByDefault(t *testing.T) {
	s, err := New(t.TempDir(), "secret")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	report := fmt.Sprintf(`{"observed_at":%q,"agent":{"hostname":"node-a"},"metrics":{"cpu_percent":96,"api_key_value":123},"inventory":{"hostname":"node-a","resources":[{"type":"host","id":"host:a"}]}}`, now.Format(time.RFC3339Nano))
	req := httptest.NewRequest(http.MethodPost, "/api/v1/nodes/node-a/report", strings.NewReader(report))
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("report = %d %s", rec.Code, rec.Body.String())
	}
	s.mu.Lock()
	alerts, _ := s.readAlerts()
	alerts = append(alerts, Alert{ID: "alert-secret", NodeID: "node-a", Severity: "critical", Kind: "test", Title: "API token=visible-secret failed", Detail: "Authorization: Bearer abc123", Status: "open", ObservedAt: now.Format(time.RFC3339Nano), UpdatedAt: now.Format(time.RFC3339Nano), Evidence: map[string]any{"password": "database-secret", "safe": "url=https://user:pass@example.com/path"}})
	if err := s.writeAlerts(alerts); err != nil {
		t.Fatal(err)
	}
	s.mu.Unlock()
	jwt := "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJvcHMtdXNlciJ9.signatureABC123"
	batch := events.Batch{Schema: events.BatchSchema, NodeID: "node-a", Source: "file-log", Sequence: 1, SentAt: now.Format(time.RFC3339Nano), Events: []events.Entry{{TimestampMS: now.UnixMilli(), Kind: "log", Severity: "error", Message: "password=raw-log-secret --password cli-secret jwt=" + jwt}}}
	if _, err := s.eventStore.Append(batch); err != nil {
		t.Fatal(err)
	}

	create := httptest.NewRequest(http.MethodPost, "/api/v1/evidence/packages", strings.NewReader(`{"created_by":"on-call","node_ids":["node-a"],"lookback_minutes":60}`))
	create.Header.Set("Authorization", "Bearer secret")
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, create)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, secret := range []string{"visible-secret", "abc123", "database-secret", "raw-log-secret", "user:pass"} {
		if strings.Contains(body, secret) {
			t.Fatalf("evidence leaked %q: %s", secret, body)
		}
	}
	if !strings.Contains(body, "[REDACTED]") || !strings.Contains(body, `"total":1`) || !strings.Contains(body, "raw log and event message text") || strings.Contains(body, "api_key_value") {
		t.Fatalf("privacy evidence is incomplete: %s", body)
	}
	withExcerpts := httptest.NewRequest(http.MethodPost, "/api/v1/evidence/packages", strings.NewReader(`{"created_by":"on-call","node_ids":["node-a"],"lookback_minutes":60,"include_event_excerpts":true}`))
	withExcerpts.Header.Set("Authorization", "Bearer secret")
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, withExcerpts)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create excerpts = %d %s", rec.Code, rec.Body.String())
	}
	for _, secret := range []string{"raw-log-secret", "cli-secret", jwt} {
		if strings.Contains(rec.Body.String(), secret) {
			t.Fatalf("redacted event excerpt leaked %q: %s", secret, rec.Body.String())
		}
	}
	if !strings.Contains(rec.Body.String(), "[REDACTED JWT]") {
		t.Fatalf("JWT redaction marker is missing: %s", rec.Body.String())
	}
}

func TestAIExplanationRequiresExactPreviewConfirmation(t *testing.T) {
	var receivedAuthorization, receivedPrompt string
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuthorization = r.Header.Get("Authorization")
		var payload struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		if len(payload.Messages) > 0 {
			receivedPrompt = payload.Messages[0].Content
		}
		writeJSON(w, map[string]any{"choices": []map[string]any{{"message": map[string]string{"content": "Observed CPU alert. Root cause is not proven."}}}})
	}))
	defer provider.Close()
	t.Setenv("FLEET_AI_ENDPOINT", provider.URL)
	t.Setenv("FLEET_AI_KEY", "provider-secret")
	dir := t.TempDir()
	config := &AIProviderConfig{Type: "openai-compatible", EndpointEnv: "FLEET_AI_ENDPOINT", APIKeyEnv: "FLEET_AI_KEY", Model: "local-test"}
	s, err := NewWithOptions(dir, "secret", "", Options{AIConfig: config})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	report := fmt.Sprintf(`{"observed_at":%q,"metrics":{"cpu_percent":96},"inventory":{"hostname":"node-a"}}`, now)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/nodes/node-a/report", strings.NewReader(report))
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	create := httptest.NewRequest(http.MethodPost, "/api/v1/evidence/packages", strings.NewReader(`{"created_by":"on-call","node_ids":["node-a"]}`))
	create.Header.Set("Authorization", "Bearer secret")
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, create)
	var pack EvidencePackage
	if err := json.Unmarshal(rec.Body.Bytes(), &pack); err != nil {
		t.Fatal(err)
	}
	previewRec := httptest.NewRecorder()
	s.Handler().ServeHTTP(previewRec, httptest.NewRequest(http.MethodGet, "/api/v1/evidence/packages/"+pack.ID+"/ai-preview", nil))
	var preview AIRequestPreview
	if err := json.Unmarshal(previewRec.Body.Bytes(), &preview); err != nil || preview.PreviewHash == "" || !preview.ProviderConfigured {
		t.Fatalf("preview = %s error=%v", previewRec.Body.String(), err)
	}
	bad := httptest.NewRequest(http.MethodPost, "/api/v1/evidence/packages/"+pack.ID+"/explain", strings.NewReader(`{"confirmed":true,"preview_hash":"wrong"}`))
	bad.Header.Set("Authorization", "Bearer secret")
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, bad)
	if rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("unconfirmed = %d %s", rec.Code, rec.Body.String())
	}
	approvedBody := fmt.Sprintf(`{"confirmed":true,"preview_hash":%q}`, preview.PreviewHash)
	approved := httptest.NewRequest(http.MethodPost, "/api/v1/evidence/packages/"+pack.ID+"/explain", strings.NewReader(approvedBody))
	approved.Header.Set("Authorization", "Bearer secret")
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, approved)
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), "Root cause is not proven") {
		t.Fatalf("explain = %d %s", rec.Code, rec.Body.String())
	}
	if receivedAuthorization != "Bearer provider-secret" || !strings.Contains(receivedPrompt, evidencePackageSchema) || strings.Contains(receivedPrompt, "provider-secret") {
		t.Fatalf("provider request auth=%q prompt=%q", receivedAuthorization, receivedPrompt)
	}
	list := httptest.NewRecorder()
	s.Handler().ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/api/v1/evidence/packages/"+pack.ID+"/explanations", nil))
	if !strings.Contains(list.Body.String(), "Root cause is not proven") || !strings.Contains(list.Body.String(), "unverified advisory") {
		t.Fatalf("stored explanations = %s", list.Body.String())
	}
}

func TestEvidencePackageRetentionPrunesOldestPackage(t *testing.T) {
	s, err := NewWithOptions(t.TempDir(), "secret", "", Options{EvidenceMaxPackages: 2})
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for index := 0; index < 3; index++ {
		body := fmt.Sprintf(`{"created_by":"retention-test","incident_id":"incident-%d"}`, index)
		request := httptest.NewRequest(http.MethodPost, "/api/v1/evidence/packages", strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer secret")
		recorder := httptest.NewRecorder()
		s.Handler().ServeHTTP(recorder, request)
		if recorder.Code != http.StatusCreated {
			t.Fatalf("create %d = %d %s", index, recorder.Code, recorder.Body.String())
		}
		var pack EvidencePackage
		if err = json.Unmarshal(recorder.Body.Bytes(), &pack); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, pack.ID)
		time.Sleep(2 * time.Millisecond)
	}
	list := httptest.NewRecorder()
	s.Handler().ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/api/v1/evidence/packages", nil))
	var summaries []EvidencePackageSummary
	if err = json.Unmarshal(list.Body.Bytes(), &summaries); err != nil || len(summaries) != 2 {
		t.Fatalf("retained packages = %s error=%v", list.Body.String(), err)
	}
	missing := httptest.NewRecorder()
	s.Handler().ServeHTTP(missing, httptest.NewRequest(http.MethodGet, "/api/v1/evidence/packages/"+ids[0], nil))
	if missing.Code != http.StatusNotFound {
		t.Fatalf("oldest package still exists: %d %s", missing.Code, missing.Body.String())
	}
}
