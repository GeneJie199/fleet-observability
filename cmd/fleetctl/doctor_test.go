package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestDoctorChecksProtectedCenter(t *testing.T) {
	const token = "fleet_admin_abcdefghijklmnopqrstuvwxyz0123456789"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/health" {
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"total_nodes":1}`))
	}))
	defer server.Close()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "admin-token"), []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := doctor([]string{"--url", server.URL, "--data", dir, "--require-node"}); err != nil {
		t.Fatal(err)
	}
}

func TestDoctorOfflineAllowsFreshInstall(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-created")
	if err := doctor([]string{"--data", path, "--offline"}); err != nil {
		t.Fatal(err)
	}
}

func TestDoctorAcceptsEnvironmentTokenFile(t *testing.T) {
	const token = "fleet_admin_abcdefghijklmnopqrstuvwxyz0123456789"
	path := filepath.Join(t.TempDir(), "center.env")
	if err := os.WriteFile(path, []byte("# managed by installer\nFLEET_TOKEN="+token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	results := &doctorResults{}
	if got := checkDoctorToken(results, path); got != token || results.failed != 0 || results.passed != 1 {
		t.Fatalf("token=%q results=%+v", got, results)
	}
}
