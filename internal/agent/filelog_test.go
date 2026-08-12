package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFileLogCollectorParsesJSONTextPersistsOffsetsAndHandlesTruncation(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "service.log")
	now := time.Now().UTC().Truncate(time.Second)
	initial := `{"timestamp":"` + now.Add(-time.Minute).Format(time.RFC3339Nano) + `","level":"error","message":"database timeout","request_id":"req-1"}` + "\n" + now.Format(time.RFC3339Nano) + " WARN queue is full\n"
	if err := os.WriteFile(logPath, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "logs.json")
	config := FileLogConfig{Files: []FileLogSource{{Name: "checkout-log", Path: logPath, Service: "checkout", Format: "auto", FromStart: true}}}
	data, _ := json.Marshal(config)
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	collector := FileLogCollector{ConfigPath: configPath, StatePath: filepath.Join(dir, "offsets.json")}
	first, err := collector.Collect(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Events) != 2 || first.Events[0].Severity != "error" || first.Events[0].Attributes["request_id"] != "req-1" || first.Events[1].Severity != "warning" {
		t.Fatalf("first events = %+v", first.Events)
	}
	second, err := collector.Collect(context.Background(), now)
	if err != nil || len(second.Events) != 0 {
		t.Fatalf("second events = %+v error=%v", second.Events, err)
	}
	file, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = file.WriteString("INFO payment accepted\n")
	_ = file.Close()
	third, err := collector.Collect(context.Background(), now)
	if err != nil || len(third.Events) != 1 || third.Events[0].Message != "INFO payment accepted" {
		t.Fatalf("third events = %+v error=%v", third.Events, err)
	}
	if err := os.WriteFile(logPath, []byte("ERROR log rotated\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rotated, err := collector.Collect(context.Background(), now)
	if err != nil || len(rotated.Events) != 1 || rotated.Events[0].Severity != "error" {
		t.Fatalf("rotated events = %+v error=%v", rotated.Events, err)
	}
}

func TestFileLogCollectorStartsAtEndByDefault(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "service.log")
	_ = os.WriteFile(logPath, []byte("old line\n"), 0o600)
	configPath := filepath.Join(dir, "logs.json")
	data, _ := json.Marshal(FileLogConfig{Files: []FileLogSource{{Name: "service-log", Path: logPath}}})
	_ = os.WriteFile(configPath, data, 0o600)
	collector := FileLogCollector{ConfigPath: configPath, StatePath: filepath.Join(dir, "offsets.json")}
	collection, err := collector.Collect(context.Background(), time.Now().UTC())
	if err != nil || len(collection.Events) != 0 {
		t.Fatalf("collection = %+v error=%v", collection, err)
	}
}
