//go:build linux

package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNativeProcessCollectorFindsCurrentTestProcess(t *testing.T) {
	command, err := os.ReadFile(filepath.Join("/proc", "self", "cmdline"))
	if err != nil {
		t.Skip("procfs unavailable")
	}
	parts := strings.Split(string(command), "\x00")
	match := filepath.Base(parts[0])
	result := collectProcessTarget(context.Background(), ProcessTarget{Name: "test-process", Match: match, Required: true}, time.Now().UTC(), nil)
	if result.err != nil || len(result.points) == 0 {
		t.Fatalf("result=%+v", result)
	}
}
