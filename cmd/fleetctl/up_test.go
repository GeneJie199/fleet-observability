package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLoadOrCreateAdminTokenPersistsSecret(t *testing.T) {
	path := filepath.Join(t.TempDir(), "admin-token")
	first, err := loadOrCreateAdminToken(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := loadOrCreateAdminToken(path)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || !strings.HasPrefix(first, "fleet_admin_") || len(first) < 48 {
		t.Fatalf("unexpected token lifecycle: first=%q second=%q", first, second)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("token file permissions are too broad: %s", info.Mode().Perm())
	}
}

func TestValidateLoopbackAddress(t *testing.T) {
	for _, address := range []string{"127.0.0.1:8770", "[::1]:8770", "localhost:0"} {
		if err := validateLoopbackAddress(address); err != nil {
			t.Fatalf("%s: %v", address, err)
		}
	}
	for _, address := range []string{"0.0.0.0:8770", "192.168.1.10:8770", "invalid"} {
		if err := validateLoopbackAddress(address); err == nil {
			t.Fatalf("expected %s to be rejected", address)
		}
	}
}

func TestValidLocalNodeID(t *testing.T) {
	for _, value := range []string{"node-1", "Prod.DB_1", "9-node"} {
		if !validLocalNodeID(value) {
			t.Fatalf("expected %q to be valid", value)
		}
	}
	for _, value := range []string{"", "-node", "node/a", strings.Repeat("a", 129)} {
		if validLocalNodeID(value) {
			t.Fatalf("expected %q to be invalid", value)
		}
	}
}
