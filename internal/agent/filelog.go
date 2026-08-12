package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/GeneJie199/fleet-observability/internal/events"
)

type FileLogConfig struct {
	Files []FileLogSource `json:"files"`
}

type FileLogSource struct {
	Name         string `json:"name"`
	Path         string `json:"path"`
	Service      string `json:"service,omitempty"`
	Kind         string `json:"kind,omitempty"`
	Format       string `json:"format,omitempty"`
	FromStart    bool   `json:"from_start,omitempty"`
	MaxReadBytes int64  `json:"max_read_bytes,omitempty"`
}

type logOffset struct {
	Offset      int64 `json:"offset"`
	Initialized bool  `json:"initialized"`
}

type FileLogCollector struct {
	Every      time.Duration
	ConfigPath string
	StatePath  string
}

var validLogIdentity = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

func (collector FileLogCollector) ID() string { return "file-logs" }
func (collector FileLogCollector) Interval() time.Duration {
	if collector.Every < time.Second {
		return 5 * time.Second
	}
	return collector.Every
}

func (collector FileLogCollector) Collect(ctx context.Context, observedAt time.Time) (Collection, error) {
	config, err := readFileLogConfig(collector.ConfigPath)
	if err != nil {
		return Collection{}, err
	}
	offsets := map[string]logOffset{}
	if data, readErr := os.ReadFile(collector.StatePath); readErr == nil {
		if err := json.Unmarshal(data, &offsets); err != nil {
			return Collection{}, fmt.Errorf("decode log offsets: %w", err)
		}
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return Collection{}, readErr
	}
	entries := []events.Entry{}
	for _, source := range config.Files {
		select {
		case <-ctx.Done():
			return Collection{}, ctx.Err()
		default:
		}
		parsed, offset, collectErr := collectLogFile(source, offsets[source.Path], observedAt)
		if collectErr != nil {
			entries = append(entries, events.Entry{TimestampMS: observedAt.UnixMilli(), Kind: "collector", Severity: "error", Service: source.Service, Message: collectErr.Error(), Attributes: map[string]string{"collector": "file-logs", "source": source.Name}})
			continue
		}
		offsets[source.Path] = offset
		entries = append(entries, parsed...)
	}
	data, err := json.MarshalIndent(offsets, "", "  ")
	if err != nil {
		return Collection{}, err
	}
	if err := os.MkdirAll(filepath.Dir(collector.StatePath), 0o700); err != nil {
		return Collection{}, err
	}
	if err := atomicSpoolWrite(collector.StatePath, append(data, '\n')); err != nil {
		return Collection{}, err
	}
	return Collection{Events: entries, ReportMetrics: map[string]any{"log_sources": len(config.Files)}}, nil
}

func readFileLogConfig(path string) (FileLogConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return FileLogConfig{}, err
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	var config FileLogConfig
	if err := decoder.Decode(&config); err != nil {
		return FileLogConfig{}, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return FileLogConfig{}, errors.New("log config must contain one JSON object")
	}
	if len(config.Files) == 0 || len(config.Files) > 100 {
		return FileLogConfig{}, errors.New("log config must contain between 1 and 100 files")
	}
	seen := map[string]bool{}
	seenPaths := map[string]bool{}
	for _, source := range config.Files {
		kind := source.Kind
		if kind == "" {
			kind = "log"
		}
		if !validLogIdentity.MatchString(source.Name) || strings.TrimSpace(source.Path) == "" || seen[source.Name] || seenPaths[source.Path] || !validLogIdentity.MatchString(kind) || len(source.Service) > 200 || (source.Format != "" && source.Format != "auto" && source.Format != "json" && source.Format != "text") {
			return FileLogConfig{}, errors.New("log sources require a unique name, path, and auto/json/text format")
		}
		seen[source.Name] = true
		seenPaths[source.Path] = true
	}
	return config, nil
}

func collectLogFile(source FileLogSource, state logOffset, observedAt time.Time) ([]events.Entry, logOffset, error) {
	file, err := os.Open(source.Path)
	if err != nil {
		return nil, state, fmt.Errorf("read log source %s: %w", source.Name, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, state, err
	}
	if !state.Initialized {
		state.Initialized = true
		if !source.FromStart {
			state.Offset = info.Size()
		}
	}
	if info.Size() < state.Offset {
		state.Offset = 0
	}
	if _, err := file.Seek(state.Offset, io.SeekStart); err != nil {
		return nil, state, err
	}
	maxBytes := source.MaxReadBytes
	if maxBytes <= 0 {
		maxBytes = 1 << 20
	}
	if maxBytes > 16<<20 {
		maxBytes = 16 << 20
	}
	reader := bufio.NewReader(io.LimitReader(file, maxBytes))
	entries := []events.Entry{}
	for {
		lineStart := state.Offset
		line, readErr := reader.ReadString('\n')
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return entries, state, readErr
		}
		if len(line) > 0 && strings.HasSuffix(line, "\n") {
			state.Offset += int64(len(line))
			line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
			if strings.TrimSpace(line) != "" {
				entries = append(entries, parseLogLine(source, line, observedAt))
			}
		} else {
			state.Offset = lineStart
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
	}
	return entries, state, nil
}

func parseLogLine(source FileLogSource, line string, observedAt time.Time) events.Entry {
	entry := events.Entry{TimestampMS: observedAt.UnixMilli(), Kind: source.Kind, Severity: "info", Service: source.Service, Message: line, Attributes: map[string]string{"log_source": source.Name, "file": filepath.Base(source.Path)}}
	if entry.Kind == "" {
		entry.Kind = "log"
	}
	format := source.Format
	if format == "" || format == "auto" {
		if strings.HasPrefix(strings.TrimSpace(line), "{") {
			format = "json"
		} else {
			format = "text"
		}
	}
	if format == "json" {
		var value map[string]any
		decoder := json.NewDecoder(strings.NewReader(line))
		decoder.UseNumber()
		if decoder.Decode(&value) == nil {
			for _, key := range []string{"message", "msg", "log"} {
				if text, ok := value[key].(string); ok && text != "" {
					entry.Message = text
					break
				}
			}
			for _, key := range []string{"level", "severity"} {
				if level, ok := value[key].(string); ok {
					entry.Severity = normalizeLogSeverity(level)
				}
			}
			for _, key := range []string{"timestamp", "time", "@timestamp", "ts"} {
				if parsed, ok := logTimestamp(value[key]); ok {
					entry.TimestampMS = parsed
					break
				}
			}
			for key, raw := range value {
				if len(entry.Attributes) >= 32 || key == "message" || key == "msg" || key == "log" || key == "timestamp" || key == "time" || key == "@timestamp" || key == "level" || key == "severity" {
					continue
				}
				switch raw := raw.(type) {
				case string:
					entry.Attributes[safeAttributeKey(key)] = truncate(raw, 1000)
				case json.Number:
					entry.Attributes[safeAttributeKey(key)] = string(raw)
				case bool:
					entry.Attributes[safeAttributeKey(key)] = strconv.FormatBool(raw)
				}
			}
			entry.Message = truncate(entry.Message, 16384)
			return entry
		}
	}
	entry.Severity = severityFromText(line)
	if fields := strings.Fields(line); len(fields) > 1 {
		if parsed, err := time.Parse(time.RFC3339Nano, fields[0]); err == nil {
			entry.TimestampMS = parsed.UnixMilli()
			entry.Message = strings.TrimSpace(strings.TrimPrefix(line, fields[0]))
		}
	}
	entry.Message = truncate(entry.Message, 16384)
	return entry
}

func normalizeLogSeverity(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "trace", "debug":
		return "debug"
	case "warn", "warning":
		return "warning"
	case "err", "error":
		return "error"
	case "fatal", "panic", "critical", "crit":
		return "critical"
	default:
		return "info"
	}
}

func severityFromText(line string) string {
	upper := strings.ToUpper(line)
	for _, candidate := range []struct{ token, level string }{{"FATAL", "critical"}, {"PANIC", "critical"}, {"CRITICAL", "critical"}, {"ERROR", "error"}, {" ERR ", "error"}, {"WARN", "warning"}, {"DEBUG", "debug"}, {"TRACE", "debug"}} {
		if strings.Contains(upper, candidate.token) {
			return candidate.level
		}
	}
	return "info"
}

func logTimestamp(raw any) (int64, bool) {
	switch value := raw.(type) {
	case string:
		if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
			return parsed.UnixMilli(), true
		}
		if number, err := strconv.ParseInt(value, 10, 64); err == nil {
			if number > 1e15 {
				number /= 1e6
			} else if number < 1e11 {
				number *= 1000
			}
			return number, true
		}
	case json.Number:
		return logTimestamp(string(value))
	}
	return 0, false
}

func safeAttributeKey(raw string) string {
	var out strings.Builder
	for _, char := range raw {
		if char >= 'A' && char <= 'Z' || char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || char == '_' || char == '.' || char == '-' {
			out.WriteRune(char)
		} else {
			out.WriteByte('_')
		}
	}
	key := strings.Trim(out.String(), "_")
	if key == "" || key[0] >= '0' && key[0] <= '9' {
		return "field_" + key
	}
	return truncate(key, 128)
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}
