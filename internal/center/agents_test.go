package center

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAgentEnrollmentBindsWritesToOneNodeAndSupportsRevocation(t *testing.T) {
	s, err := New(t.TempDir(), "bootstrap-secret")
	if err != nil {
		t.Fatal(err)
	}
	enroll := func(body string) (int, enrollmentResponse) {
		request := httptest.NewRequest(http.MethodPost, "/api/v1/agents/enroll", strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer bootstrap-secret")
		recorder := httptest.NewRecorder()
		s.Handler().ServeHTTP(recorder, request)
		var response enrollmentResponse
		_ = json.Unmarshal(recorder.Body.Bytes(), &response)
		return recorder.Code, response
	}
	status, credential := enroll(`{"node_id":"node-a"}`)
	if status != http.StatusCreated || credential.NodeID != "node-a" || !strings.HasPrefix(credential.Token, "fleet_agent_") {
		t.Fatalf("enroll = %d %+v", status, credential)
	}
	if status, _ := enroll(`{"node_id":"node-a"}`); status != http.StatusConflict {
		t.Fatalf("duplicate enrollment = %d", status)
	}

	write := func(method, path, body, token string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, path, strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer "+token)
		recorder := httptest.NewRecorder()
		s.Handler().ServeHTTP(recorder, request)
		return recorder
	}
	report := `{"metrics":{"cpu_percent":12}}`
	if recorder := write(http.MethodPost, "/api/v1/nodes/node-a/report", report, credential.Token); recorder.Code != http.StatusAccepted {
		t.Fatalf("bound report = %d %s", recorder.Code, recorder.Body.String())
	}
	if recorder := write(http.MethodPost, "/api/v1/nodes/node-b/report", report, credential.Token); recorder.Code != http.StatusForbidden {
		t.Fatalf("cross-node report = %d %s", recorder.Code, recorder.Body.String())
	}
	now := time.Now().UTC()
	batch := fmt.Sprintf(`{"schema":"telemetry.batch/v1","node_id":"node-b","source":"native-agent","sequence":1,"sent_at":%q,"points":[{"metric":"cpu_percent","timestamp_ms":%d,"value":20}]}`, now.Format(time.RFC3339Nano), now.UnixMilli())
	if recorder := write(http.MethodPost, "/api/v1/telemetry/batches", batch, credential.Token); recorder.Code != http.StatusForbidden {
		t.Fatalf("cross-node telemetry = %d %s", recorder.Code, recorder.Body.String())
	}

	request := httptest.NewRequest(http.MethodGet, "/api/v1/agents", nil)
	recorder := httptest.NewRecorder()
	s.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"node_id":"node-a"`) || strings.Contains(recorder.Body.String(), credential.Token) || strings.Contains(recorder.Body.String(), "token_hash") {
		t.Fatalf("agent list leaked credential = %d %s", recorder.Code, recorder.Body.String())
	}

	if recorder := write(http.MethodDelete, "/api/v1/agents/node-a", "", "bootstrap-secret"); recorder.Code != http.StatusNoContent {
		t.Fatalf("revoke = %d %s", recorder.Code, recorder.Body.String())
	}
	if recorder := write(http.MethodPost, "/api/v1/nodes/node-a/report", report, credential.Token); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("revoked report = %d %s", recorder.Code, recorder.Body.String())
	}
	status, rotated := enroll(`{"node_id":"node-a","rotate":true}`)
	if status != http.StatusCreated || rotated.Token == credential.Token {
		t.Fatalf("rotate = %d %+v", status, rotated)
	}
}
