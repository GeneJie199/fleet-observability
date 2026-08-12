package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestEnsureIdentityEnrollsOnceAndPersistsNodeCredential(t *testing.T) {
	var enrollments atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/agents/enroll" || r.Header.Get("Authorization") != "Bearer bootstrap-secret" {
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		enrollments.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"node_id":"node-a","token":"fleet_agent_test-token","created_at":"2026-08-12T00:00:00Z"}`))
	}))
	defer server.Close()
	spoolDir := t.TempDir()
	config := Config{CenterURL: server.URL, NodeID: "node-a", Token: "bootstrap-secret", Interval: time.Minute, SpoolDir: spoolDir, Collectors: []Collector{&countingCollector{}}, Client: server.Client()}
	first, err := newPipeline(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.ensureIdentity(context.Background()); err != nil {
		t.Fatal(err)
	}
	if first.writeToken() != "fleet_agent_test-token" {
		t.Fatalf("write token = %q", first.writeToken())
	}
	credentialPath := filepath.Join(spoolDir, "agent-credential.json")
	if info, err := os.Stat(credentialPath); err != nil || info.IsDir() {
		t.Fatalf("credential file = %+v, %v", info, err)
	}

	second, err := newPipeline(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := second.ensureIdentity(context.Background()); err != nil {
		t.Fatal(err)
	}
	if enrollments.Load() != 1 || second.writeToken() != "fleet_agent_test-token" {
		t.Fatalf("enrollments=%d token=%q", enrollments.Load(), second.writeToken())
	}
}

func TestEnsureIdentityRejectsCredentialForAnotherNode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "credential.json")
	if err := writeAgentCredential(path, agentCredential{NodeID: "node-a", Token: "fleet_agent_token"}); err != nil {
		t.Fatal(err)
	}
	pipeline, err := newPipeline(Config{CenterURL: "http://127.0.0.1", NodeID: "node-b", Interval: time.Minute, SpoolDir: dir, CredentialPath: path, Collectors: []Collector{&countingCollector{}}, Client: http.DefaultClient})
	if err != nil {
		t.Fatal(err)
	}
	if err := pipeline.ensureIdentity(context.Background()); err == nil {
		t.Fatal("expected node credential mismatch")
	}
}
