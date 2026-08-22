package center

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestTopologySnapshotsDiffAndChangeCenter(t *testing.T) {
	s, err := New(t.TempDir(), "secret")
	if err != nil {
		t.Fatal(err)
	}
	postReport := func(observedAt, resources, relationships string) {
		body := `{"observed_at":"` + observedAt + `","inventory":{"hostname":"node-a","resources":` + resources + `,"relationships":` + relationships + `}}`
		req := httptest.NewRequest(http.MethodPost, "/api/v1/nodes/node-a/report", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer secret")
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("report = %d %s", rec.Code, rec.Body.String())
		}
	}
	now := time.Now().UTC().Truncate(time.Second)
	postReport(now.Format(time.RFC3339), `[{"type":"host","id":"host:a","host":{"hostname":"node-a"}},{"type":"service","id":"service:api","service":{"name":"api"}}]`, `[{"source":"host:a","target":"service:api","type":"runs","confidence":0.9,"evidence":["systemd"]}]`)
	postReport(now.Add(time.Minute).Format(time.RFC3339), `[{"type":"host","id":"host:a","host":{"hostname":"node-a"}},{"type":"service","id":"service:api","service":{"name":"api"}},{"type":"database","id":"database:orders","metadata":{"name":"orders"}}]`, `[{"source":"host:a","target":"service:api","type":"runs","confidence":0.9,"evidence":["systemd"]},{"source":"service:api","target":"database:orders","type":"connects_to","confidence":0.8,"evidence":["socket"]}]`)

	list := httptest.NewRecorder()
	s.Handler().ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/api/v1/topology/snapshots", nil))
	var summaries []TopologySnapshotSummary
	if err := json.Unmarshal(list.Body.Bytes(), &summaries); err != nil || len(summaries) != 2 {
		t.Fatalf("snapshots = %s error=%v", list.Body.String(), err)
	}
	diff := httptest.NewRecorder()
	s.Handler().ServeHTTP(diff, httptest.NewRequest(http.MethodGet, "/api/v1/topology/diff?from="+summaries[1].ID+"&to="+summaries[0].ID, nil))
	var topologyDiff TopologyDiff
	if err := json.Unmarshal(diff.Body.Bytes(), &topologyDiff); err != nil || len(topologyDiff.AddedNodes) != 1 || len(topologyDiff.AddedEdges) != 1 {
		t.Fatalf("diff = %s error=%v", diff.Body.String(), err)
	}
	changes := httptest.NewRecorder()
	s.Handler().ServeHTTP(changes, httptest.NewRequest(http.MethodGet, "/api/v1/changes", nil))
	if !strings.Contains(changes.Body.String(), `"resource_type":"topology.node"`) || !strings.Contains(changes.Body.String(), `"resource_type":"topology.edge"`) {
		t.Fatalf("topology changes missing: %s", changes.Body.String())
	}

	manual := httptest.NewRequest(http.MethodPost, "/api/v1/topology/snapshots", strings.NewReader(`{"release_id":"release-42","incident_id":"incident-7","note":"post-deploy"}`))
	manual.Header.Set("Authorization", "Bearer secret")
	created := httptest.NewRecorder()
	s.Handler().ServeHTTP(created, manual)
	if created.Code != http.StatusCreated || !strings.Contains(created.Body.String(), `"release_id":"release-42"`) {
		t.Fatalf("manual snapshot = %d %s", created.Code, created.Body.String())
	}
}

func TestRelationshipReviewAppendsAudit(t *testing.T) {
	s, err := New(t.TempDir(), "secret")
	if err != nil {
		t.Fatal(err)
	}
	report := `{"inventory":{"hostname":"node-a","resources":[{"type":"host","id":"host:a"},{"type":"service","id":"service:api"}],"relationships":[{"source":"host:a","target":"service:api","type":"runs","confidence":0.9,"evidence":["systemd"]}]}}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/nodes/node-a/report", strings.NewReader(report))
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("report = %d %s", rec.Code, rec.Body.String())
	}
	relationshipID := stableID("relationship", "host:a", "service:api", "runs")
	patch := httptest.NewRequest(http.MethodPatch, "/api/v1/topology/relationships/"+relationshipID, strings.NewReader(`{"confirmation":"confirmed","reviewed_by":"operator","note":"verified from unit file"}`))
	patch.Header.Set("Authorization", "Bearer secret")
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, patch)
	if rec.Code != http.StatusOK {
		t.Fatalf("review = %d %s", rec.Code, rec.Body.String())
	}
	audit := httptest.NewRecorder()
	s.Handler().ServeHTTP(audit, httptest.NewRequest(http.MethodGet, "/api/v1/topology/relationships/audit?relationship="+relationshipID, nil))
	if !strings.Contains(audit.Body.String(), `"previous":"unreviewed"`) || !strings.Contains(audit.Body.String(), `"current":"confirmed"`) {
		t.Fatalf("audit = %s", audit.Body.String())
	}
}
