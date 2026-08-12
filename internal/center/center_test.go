package center

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReportLifecycleAndAuth(t *testing.T) {
	s, err := New(t.TempDir(), "secret")
	if err != nil {
		t.Fatal(err)
	}
	body := `{"inventory":{"hostname":"node-1","summary":{"processes":4}},"drift":{"highest_risk":"WARNING"}}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/nodes/node-1/report", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d", rec.Code)
	}
	req = httptest.NewRequest(http.MethodPost, "/api/v1/nodes/node-1/report", bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer secret")
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/nodes", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"node_id":"node-1"`) || !strings.Contains(rec.Body.String(), `"highest_risk":"WARNING"`) {
		t.Fatalf("list=%s", rec.Body.String())
	}
}

func TestOperationalAPIs(t *testing.T) {
	s, err := New(t.TempDir(), "secret")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	body := `{"observed_at":"` + now + `","metrics":{"cpu_percent":97,"memory_percent":40,"disk_percent":50},"inventory":{"hostname":"node-a","summary":{"processes":4,"services":2},"resources":[{"type":"service","id":"svc:web","service":{"name":"web"}}],"relationships":[]},"drift":{"highest_risk":"CRITICAL","added":[{"ID":"endpoint:new","Type":"endpoint","Summary":"new public port","Severity":"CRITICAL"}],"removed":[],"changed":[]}}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/nodes/node-a/report", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("post=%d %s", rec.Code, rec.Body.String())
	}
	get := func(path string) *httptest.ResponseRecorder {
		x := httptest.NewRecorder()
		s.Handler().ServeHTTP(x, httptest.NewRequest(http.MethodGet, path, nil))
		if x.Code != http.StatusOK {
			t.Fatalf("GET %s=%d %s", path, x.Code, x.Body.String())
		}
		return x
	}
	if !strings.Contains(get("/api/v1/overview").Body.String(), `"critical_nodes":1`) {
		t.Fatal("overview did not mark critical node")
	}
	var alerts []Alert
	if err := json.Unmarshal(get("/api/v1/alerts").Body.Bytes(), &alerts); err != nil || len(alerts) < 2 {
		t.Fatalf("alerts=%+v err=%v", alerts, err)
	}
	patch := httptest.NewRequest(http.MethodPatch, "/api/v1/alerts/"+alerts[0].ID, strings.NewReader(`{"status":"acknowledged","assignee":"on-call","note":"investigating"}`))
	patch.Header.Set("Authorization", "Bearer secret")
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, patch)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"assignee":"on-call"`) || !strings.Contains(rec.Body.String(), `"note":"investigating"`) {
		t.Fatalf("patch alert=%d %s", rec.Code, rec.Body.String())
	}
	var changes []Change
	if err := json.Unmarshal(get("/api/v1/changes").Body.Bytes(), &changes); err != nil || len(changes) != 1 {
		t.Fatalf("changes=%+v err=%v", changes, err)
	}
	patch = httptest.NewRequest(http.MethodPatch, "/api/v1/changes/"+changes[0].ID, strings.NewReader(`{"classification":"expected","release_id":"rel-1","reviewed_by":"release-owner","decision_note":"declared in release plan"}`))
	patch.Header.Set("Authorization", "Bearer secret")
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, patch)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"release_id":"rel-1"`) || !strings.Contains(rec.Body.String(), `"reviewed_by":"release-owner"`) {
		t.Fatalf("patch change=%d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(get("/api/v1/topology").Body.String(), `"svc:web"`) {
		t.Fatal("topology missing service")
	}
	if !strings.Contains(get("/api/v1/nodes/node-a/history").Body.String(), `"cpu_percent":97`) {
		t.Fatal("history missing metrics")
	}
}

func TestNodeHistoryIsCompactedAndNormalizedOnRestart(t *testing.T) {
	dir := t.TempDir()
	s, err := NewWithOptions(dir, "", "", Options{HistoryMaxEntries: 2, HistoryMaxBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	for value := 1; value <= 3; value++ {
		body := fmt.Sprintf(`{"observed_at":%q,"metrics":{"sequence":%d}}`, time.Now().UTC().Add(time.Duration(value)*time.Second).Format(time.RFC3339Nano), value)
		recorder := httptest.NewRecorder()
		s.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/v1/nodes/node-history/report", strings.NewReader(body)))
		if recorder.Code != http.StatusAccepted {
			t.Fatalf("report %d = %d %s", value, recorder.Code, recorder.Body.String())
		}
	}
	historyPath := filepath.Join(dir, "history", "node-history.ndjson")
	data, err := os.ReadFile(historyPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(strings.TrimSpace(string(data)), "\n") != 1 || strings.Contains(string(data), `"sequence":1`) {
		t.Fatalf("compacted history = %s", data)
	}
	if err = os.WriteFile(historyPath, append(data, []byte("not-json\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewWithOptions(dir, "", "", Options{HistoryMaxEntries: 2, HistoryMaxBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	if restarted.historyCounts["node-history"] != 2 {
		t.Fatalf("restarted history count = %d", restarted.historyCounts["node-history"])
	}
	data, _ = os.ReadFile(historyPath)
	if strings.Contains(string(data), "not-json") {
		t.Fatalf("invalid history line survived restart: %s", data)
	}
}

func TestInfraScoutDispositionAndFingerprintFlowIntoChanges(t *testing.T) {
	report := Report{NodeID: "node-a", ObservedAt: "2026-08-12T10:00:00Z", Drift: json.RawMessage(`{"highest_risk":"WARNING","added":[{"id":"service:web","type":"service","summary":"added web","severity":"WARNING","fingerprint":"drift_first","classification":"approved","decision":{"actor":"platform-owner","note":"release rel-7"}}],"removed":[],"changed":[]}`)}
	changes := deriveChanges(report)
	if len(changes) != 1 || changes[0].Classification != "approved" || changes[0].ReviewedBy != "platform-owner" || changes[0].DecisionNote != "release rel-7" {
		t.Fatalf("derived changes = %+v", changes)
	}
	firstID := changes[0].ID
	report.Drift = json.RawMessage(`{"added":[{"id":"service:web","type":"service","summary":"added web","severity":"WARNING","fingerprint":"drift_second","classification":"unexpected"}],"removed":[],"changed":[]}`)
	changes = deriveChanges(report)
	if len(changes) != 1 || changes[0].ID == firstID {
		t.Fatalf("materially different fingerprint reused change identity: %+v", changes)
	}
}

func TestFreshInfraScoutDecisionOverridesOldFleetDecision(t *testing.T) {
	old := []Change{{ID: "change-1", Classification: "approved", ReviewedBy: "fleet-owner", DecisionNote: "old"}}
	fresh := []Change{{ID: "change-1", Classification: "denied", ReviewedBy: "infra-owner", DecisionNote: "new"}}
	merged := mergeChanges(old, fresh)
	if merged[0].Classification != "denied" || merged[0].ReviewedBy != "infra-owner" {
		t.Fatalf("fresh source decision was not applied: %+v", merged[0])
	}
	fresh = []Change{{ID: "change-1", Classification: "unexpected"}}
	merged = mergeChanges(merged, fresh)
	if merged[0].Classification != "denied" || merged[0].ReviewedBy != "infra-owner" {
		t.Fatalf("unreviewed refresh erased operator decision: %+v", merged[0])
	}
}

func TestRejectsTraversalAndInvalidJSON(t *testing.T) {
	s, _ := New(t.TempDir(), "")
	for _, path := range []string{"/api/v1/nodes/../report", "/api/v1/nodes/bad%2Fid/report"} {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`)))
		if rec.Code >= 200 && rec.Code < 300 {
			t.Fatalf("%s status=%d", path, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/groups", strings.NewReader(`{"name":"x","node_ids":[]} {}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("trailing JSON status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestNodeDetailAppliesFreshness(t *testing.T) {
	s, err := New(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-10 * time.Minute).UTC().Format(time.RFC3339)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/nodes/stale-node/report", strings.NewReader(`{"observed_at":"`+old+`","metrics":{"cpu_percent":10}}`))
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("post = %d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/nodes/stale-node", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"health":"stale"`) {
		t.Fatalf("detail = %d %s", rec.Code, rec.Body.String())
	}
}

func TestGroupsCoverageAndRelationshipReview(t *testing.T) {
	s, err := New(t.TempDir(), "secret")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	reports := map[string]string{
		"node-a": `{"observed_at":"` + now + `","metrics":{"cpu_percent":20,"memory_percent":30,"disk_percent":40,"checks":[{"name":"orders-http","kind":"http","status":"ok","latency_ms":8}],"applications":[{"name":"orders-db","kind":"postgres","up":true,"required":true,"connections":6}]},"inventory":{"hostname":"node-a","summary":{"services":1},"resources":[{"type":"host","id":"host:a","host":{"hostname":"node-a"}},{"type":"service","id":"svc:a","service":{"name":"orders-api"}}],"relationships":[{"source":"host:a","target":"svc:a","type":"hosts","confidence":0.9,"evidence":["systemd"]}]},"drift":{"highest_risk":"INFO"}}`,
		"node-b": `{"observed_at":"` + now + `","metrics":{"cpu_percent":10},"inventory":{"hostname":"node-b","resources":[{"type":"host","id":"host:b"}]},"drift":{"highest_risk":"INFO"}}`,
	}
	for id, body := range reports {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/nodes/"+id+"/report", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer secret")
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("report %s = %d %s", id, rec.Code, rec.Body.String())
		}
	}

	create := httptest.NewRequest(http.MethodPost, "/api/v1/groups", strings.NewReader(`{"id":"prod","name":"Production","node_ids":["node-a"]}`))
	create.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, create)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create group = %d %s", rec.Code, rec.Body.String())
	}

	get := func(path string) *httptest.ResponseRecorder {
		x := httptest.NewRecorder()
		s.Handler().ServeHTTP(x, httptest.NewRequest(http.MethodGet, path, nil))
		if x.Code != http.StatusOK {
			t.Fatalf("GET %s = %d %s", path, x.Code, x.Body.String())
		}
		return x
	}
	if body := get("/api/v1/nodes?group=prod").Body.String(); !strings.Contains(body, `"node-a"`) || strings.Contains(body, `"node-b"`) {
		t.Fatalf("group nodes = %s", body)
	}
	if body := get("/api/v1/overview?group=prod").Body.String(); !strings.Contains(body, `"total_nodes":1`) {
		t.Fatalf("group overview = %s", body)
	}
	var coverage Coverage
	if err := json.Unmarshal(get("/api/v1/coverage?group=prod").Body.Bytes(), &coverage); err != nil || coverage.Total != 5 || coverage.Covered != 5 {
		t.Fatalf("coverage = %+v err=%v", coverage, err)
	}
	var databases []DatabaseStatus
	if err := json.Unmarshal(get("/api/v1/databases?group=prod").Body.Bytes(), &databases); err != nil || len(databases) != 1 || databases[0].Name != "orders-db" || databases[0].Connections != 6 {
		t.Fatalf("databases = %+v err=%v", databases, err)
	}
	var topology Topology
	if err := json.Unmarshal(get("/api/v1/topology?group=prod&view=deployment").Body.Bytes(), &topology); err != nil || len(topology.Edges) != 1 {
		t.Fatalf("topology = %+v err=%v", topology, err)
	}
	edgeID := topology.Edges[0].ID
	patch := httptest.NewRequest(http.MethodPatch, "/api/v1/topology/relationships/"+edgeID, strings.NewReader(`{"confirmation":"confirmed","reviewed_by":"platform-owner","note":"verified against systemd"}`))
	patch.Header.Set("Authorization", "Bearer secret")
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, patch)
	if rec.Code != http.StatusOK {
		t.Fatalf("review relationship = %d %s", rec.Code, rec.Body.String())
	}
	if body := get("/api/v1/topology?group=prod").Body.String(); !strings.Contains(body, `"confirmation":"confirmed"`) || !strings.Contains(body, `"reviewed_by":"platform-owner"`) {
		t.Fatalf("reviewed topology = %s", body)
	}
}

func TestTopologyRelationshipUsesHistoryForFirstAndLastSeen(t *testing.T) {
	s, err := New(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	times := []string{
		time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339),
		time.Now().Add(-time.Minute).UTC().Format(time.RFC3339),
	}
	for _, observedAt := range times {
		body := `{"observed_at":"` + observedAt + `","inventory":{"resources":[{"type":"host","id":"host:a"},{"type":"service","id":"svc:a"}],"relationships":[{"source":"host:a","target":"svc:a","type":"hosts","confidence":0.9}]}}`
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/nodes/node-a/report", strings.NewReader(body)))
		if rec.Code != http.StatusAccepted {
			t.Fatalf("post = %d %s", rec.Code, rec.Body.String())
		}
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/topology", nil))
	var topology Topology
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &topology) != nil || len(topology.Edges) != 1 {
		t.Fatalf("topology = %d %s", rec.Code, rec.Body.String())
	}
	if topology.Edges[0].FirstSeenAt != times[0] || topology.Edges[0].LastSeenAt != times[1] {
		t.Fatalf("relationship times = %+v, want first=%s last=%s", topology.Edges[0], times[0], times[1])
	}
}

func TestNativeTelemetryIngestQueryCatalogAndDedup(t *testing.T) {
	s, err := New(t.TempDir(), "secret")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	body := fmt.Sprintf(`{"schema":"telemetry.batch/v1","node_id":"node-a","source":"native","sequence":11,"sent_at":%q,"points":[{"metric":"cpu_percent","timestamp_ms":%d,"value":30,"kind":"gauge","unit":"percent"},{"metric":"cpu_percent","timestamp_ms":%d,"value":50,"kind":"gauge","unit":"percent"}]}`, now.Format(time.RFC3339Nano), now.Add(-20*time.Second).UnixMilli(), now.Add(-10*time.Second).UnixMilli())
	post := func(payload string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/telemetry/batches", strings.NewReader(payload))
		req.Header.Set("Authorization", "Bearer secret")
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		return rec
	}
	if rec := post(body); rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), `"duplicate":false`) {
		t.Fatalf("ingest = %d %s", rec.Code, rec.Body.String())
	}
	if rec := post(body); rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), `"duplicate":true`) {
		t.Fatalf("dedup = %d %s", rec.Code, rec.Body.String())
	}
	query := fmt.Sprintf("/api/v1/telemetry/query?metric=cpu_percent&node=node-a&start=%d&end=%d&step=1m&aggregate=avg", now.Add(-time.Minute).UnixMilli(), now.UnixMilli())
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, query, nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"value":40`) {
		t.Fatalf("query = %d %s", rec.Code, rec.Body.String())
	}
	for _, path := range []string{"/api/v1/telemetry/catalog", "/api/v1/telemetry/sources"} {
		rec = httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "cpu_percent") && path != "/api/v1/telemetry/sources" {
			t.Fatalf("GET %s = %d %s", path, rec.Code, rec.Body.String())
		}
	}
}

func TestCompatibilityTelemetryEndpoints(t *testing.T) {
	s, err := New(t.TempDir(), "secret")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	tests := []struct {
		name        string
		path        string
		contentType string
		body        string
		metric      string
	}{
		{name: "prometheus", path: "/api/v1/ingest/prometheus?node=node-prom", contentType: "text/plain", body: fmt.Sprintf("# TYPE http_requests_total counter\nhttp_requests_total{method=\"GET\"} 9 %d\n", now.UnixMilli()), metric: "http_requests_total"},
		{name: "influx", path: "/api/v1/ingest/influx?node=node-influx&precision=ms", contentType: "text/plain", body: fmt.Sprintf("cpu,region=cn-east usage=18.5 %d\n", now.UnixMilli()), metric: "cpu_usage"},
		{name: "otlp", path: "/v1/metrics?node=node-otlp", contentType: "application/json", body: fmt.Sprintf(`{"resourceMetrics":[{"scopeMetrics":[{"metrics":[{"name":"runtime.memory","gauge":{"dataPoints":[{"timeUnixNano":"%d","asDouble":64}]}}]}]}]}`, now.UnixNano()), metric: "runtime.memory"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(test.body))
			req.Header.Set("Authorization", "Bearer secret")
			req.Header.Set("Content-Type", test.contentType)
			req.Header.Set("X-Telemetry-Sequence", "1")
			rec := httptest.NewRecorder()
			s.Handler().ServeHTTP(rec, req)
			if rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), `"points":1`) {
				t.Fatalf("ingest = %d %s", rec.Code, rec.Body.String())
			}
			query := fmt.Sprintf("/api/v1/telemetry/query?metric=%s&start=%d&end=%d", test.metric, now.Add(-time.Minute).UnixMilli(), now.Add(time.Minute).UnixMilli())
			rec = httptest.NewRecorder()
			s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, query, nil))
			if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), test.metric) {
				t.Fatalf("query = %d %s", rec.Code, rec.Body.String())
			}
		})
	}

	unauthorized := httptest.NewRecorder()
	s.Handler().ServeHTTP(unauthorized, httptest.NewRequest(http.MethodPost, "/api/v1/ingest/prometheus?node=n", strings.NewReader("x 1")))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", unauthorized.Code)
	}
	missingNode := httptest.NewRequest(http.MethodPost, "/api/v1/ingest/prometheus", strings.NewReader("x 1"))
	missingNode.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, missingNode)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing node status = %d", rec.Code)
	}
}

func TestMetricRuleDurationAcknowledgementRecoveryAndRestart(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir, "secret")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	rule := `{"id":"latency-high","name":"Checkout latency high","metric":"request_latency_ms","operator":"gt","threshold":100,"for_seconds":60,"severity":"critical","enabled":true}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/rules", strings.NewReader(rule))
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create rule = %d %s", rec.Code, rec.Body.String())
	}

	postBatch := func(server *Server, sequence uint64, points string) *httptest.ResponseRecorder {
		body := fmt.Sprintf(`{"schema":"telemetry.batch/v1","node_id":"checkout-1","source":"native-agent","sequence":%d,"sent_at":%q,"points":[%s]}`, sequence, now.Format(time.RFC3339Nano), points)
		request := httptest.NewRequest(http.MethodPost, "/api/v1/telemetry/batches", strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer secret")
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, request)
		return response
	}
	point := func(at time.Time, value float64) string {
		return fmt.Sprintf(`{"metric":"request_latency_ms","labels":{"service":"checkout"},"timestamp_ms":%d,"value":%g,"kind":"gauge","unit":"milliseconds"}`, at.UnixMilli(), value)
	}
	if response := postBatch(s, 1, point(now.Add(-70*time.Second), 120)); response.Code != http.StatusAccepted {
		t.Fatalf("pending batch = %d %s", response.Code, response.Body.String())
	}
	list := httptest.NewRecorder()
	s.Handler().ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/api/v1/alerts", nil))
	if strings.Contains(list.Body.String(), "latency-high") {
		t.Fatalf("rule fired before duration: %s", list.Body.String())
	}

	s, err = New(dir, "secret")
	if err != nil {
		t.Fatal(err)
	}
	if response := postBatch(s, 2, point(now.Add(-5*time.Second), 130)); response.Code != http.StatusAccepted {
		t.Fatalf("firing batch = %d %s", response.Code, response.Body.String())
	}
	list = httptest.NewRecorder()
	s.Handler().ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/api/v1/alerts", nil))
	var alerts []Alert
	if err = json.Unmarshal(list.Body.Bytes(), &alerts); err != nil || len(alerts) != 1 || alerts[0].Kind != "rule.latency-high" || alerts[0].Status != "open" {
		t.Fatalf("firing alerts = %+v error=%v", alerts, err)
	}

	ack := httptest.NewRequest(http.MethodPatch, "/api/v1/alerts/"+alerts[0].ID, strings.NewReader(`{"status":"acknowledged","assignee":"on-call","note":"capacity work scheduled"}`))
	ack.Header.Set("Authorization", "Bearer secret")
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, ack)
	if rec.Code != http.StatusOK {
		t.Fatalf("ack = %d %s", rec.Code, rec.Body.String())
	}
	if response := postBatch(s, 3, point(now, 140)); response.Code != http.StatusAccepted {
		t.Fatalf("repeat firing batch = %d %s", response.Code, response.Body.String())
	}
	list = httptest.NewRecorder()
	s.Handler().ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/api/v1/alerts", nil))
	alerts = nil
	_ = json.Unmarshal(list.Body.Bytes(), &alerts)
	if alerts[0].Status != "acknowledged" || alerts[0].Assignee != "on-call" {
		t.Fatalf("acknowledgement not preserved: %+v", alerts[0])
	}
	if response := postBatch(s, 4, point(now, 80)); response.Code != http.StatusAccepted {
		t.Fatalf("recovery batch = %d %s", response.Code, response.Body.String())
	}
	list = httptest.NewRecorder()
	s.Handler().ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/api/v1/alerts", nil))
	alerts = nil
	_ = json.Unmarshal(list.Body.Bytes(), &alerts)
	if alerts[0].Status != "resolved" {
		t.Fatalf("recovered alert = %+v", alerts[0])
	}
}

func TestNativeAgentReportDoesNotDuplicateTelemetry(t *testing.T) {
	s, err := New(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	batch := fmt.Sprintf(`{"schema":"telemetry.batch/v1","node_id":"node-a","source":"native-agent","sequence":1,"sent_at":%q,"points":[{"metric":"cpu_percent","timestamp_ms":%d,"value":20}]}`, now.Format(time.RFC3339Nano), now.UnixMilli())
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/telemetry/batches", strings.NewReader(batch)))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("batch = %d %s", rec.Code, rec.Body.String())
	}
	report := fmt.Sprintf(`{"node_id":"node-a","observed_at":%q,"metrics":{"cpu_percent":20}}`, now.Format(time.RFC3339Nano))
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/nodes/node-a/report", strings.NewReader(report)))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("report = %d %s", rec.Code, rec.Body.String())
	}
	catalog := s.telemetry.Catalog()
	if len(catalog) != 1 || catalog[0].Series != 1 || catalog[0].Samples != 1 {
		t.Fatalf("catalog duplicated native telemetry: %+v", catalog)
	}
}

func TestNativeEventIngestQueryCatalogAndAuth(t *testing.T) {
	s, err := New(t.TempDir(), "secret")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	body := fmt.Sprintf(`{"schema":"event.batch/v1","node_id":"node-a","source":"file-log","sequence":7,"sent_at":%q,"events":[{"timestamp_ms":%d,"kind":"log","severity":"error","service":"checkout","message":"database timeout","attributes":{"request_id":"req-7"}}]}`, now.Format(time.RFC3339Nano), now.UnixMilli())
	unauthorized := httptest.NewRecorder()
	s.Handler().ServeHTTP(unauthorized, httptest.NewRequest(http.MethodPost, "/api/v1/events/batches", strings.NewReader(body)))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized = %d", unauthorized.Code)
	}
	post := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/events/batches", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer secret")
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		return rec
	}
	if rec := post(); rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), `"duplicate":false`) {
		t.Fatalf("ingest = %d %s", rec.Code, rec.Body.String())
	}
	if rec := post(); rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), `"duplicate":true`) {
		t.Fatalf("dedup = %d %s", rec.Code, rec.Body.String())
	}
	query := fmt.Sprintf("/api/v1/events?node=node-a&severity=error&search=database+req-7&start=%d&end=%d", now.Add(-time.Minute).UnixMilli(), now.Add(time.Minute).UnixMilli())
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, query, nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "database timeout") || !strings.Contains(rec.Body.String(), `"total_matched":1`) {
		t.Fatalf("query = %d %s", rec.Code, rec.Body.String())
	}
	for _, path := range []string{"/api/v1/events/catalog", "/api/v1/events/sources"} {
		rec = httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "file-log") {
			t.Fatalf("GET %s = %d %s", path, rec.Code, rec.Body.String())
		}
	}
}

func TestProtectedReadAPIsRequireTokenButHealthAndUIRemainAvailable(t *testing.T) {
	s, err := NewWithOptions(t.TempDir(), "secret", "", Options{ProtectReads: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/", "/api/v1/health"} {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("public GET %s = %d", path, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/overview", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous overview = %d", rec.Code)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/overview", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("authenticated overview = %d %s", rec.Code, rec.Body.String())
	}
}
