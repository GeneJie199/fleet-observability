package compat

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/GeneJie199/fleet-observability/internal/telemetry"
)

func ParsePrometheus(reader io.Reader, now time.Time) ([]telemetry.Point, error) {
	types := map[string]telemetry.MetricKind{}
	points := []telemetry.Point{}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), 2<<20)
	for lineNumber := 1; scanner.Scan(); lineNumber++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "# HELP ") || strings.HasPrefix(line, "# UNIT ") {
			continue
		}
		if strings.HasPrefix(line, "# TYPE ") {
			fields := strings.Fields(line)
			if len(fields) != 4 {
				return nil, fmt.Errorf("line %d: invalid TYPE declaration", lineNumber)
			}
			kind := telemetry.Gauge
			if fields[3] == "counter" {
				kind = telemetry.Counter
			}
			types[fields[2]] = kind
			continue
		}
		if strings.HasPrefix(line, "#") {
			continue
		}
		point, err := parsePrometheusSample(line, now, types)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNumber, err)
		}
		points = append(points, point)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(points) == 0 {
		return nil, fmt.Errorf("no numeric Prometheus samples found")
	}
	return points, nil
}

func parsePrometheusSample(line string, now time.Time, types map[string]telemetry.MetricKind) (telemetry.Point, error) {
	metricPart, valuePart, ok := cutMetricAndValue(line)
	if !ok {
		return telemetry.Point{}, fmt.Errorf("expected metric and value")
	}
	name, labels, err := parsePrometheusMetric(metricPart)
	if err != nil {
		return telemetry.Point{}, err
	}
	fields := strings.Fields(valuePart)
	if len(fields) < 1 || len(fields) > 2 {
		return telemetry.Point{}, fmt.Errorf("expected value and optional timestamp")
	}
	value, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return telemetry.Point{}, fmt.Errorf("invalid value")
	}
	timestamp := now.UnixMilli()
	if len(fields) == 2 {
		timestamp, err = strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			return telemetry.Point{}, fmt.Errorf("invalid millisecond timestamp")
		}
	}
	kind := types[name]
	if kind == "" {
		kind = telemetry.Gauge
		if strings.HasSuffix(name, "_total") {
			kind = telemetry.Counter
		}
	}
	return telemetry.Point{Metric: name, Labels: labels, TimestampMS: timestamp, Value: value, Kind: kind}, nil
}

func cutMetricAndValue(line string) (string, string, bool) {
	quoted := false
	escaped := false
	for i := 0; i < len(line); i++ {
		if escaped {
			escaped = false
			continue
		}
		if line[i] == '\\' {
			escaped = true
			continue
		}
		if line[i] == '"' {
			quoted = !quoted
			continue
		}
		if !quoted && (line[i] == ' ' || line[i] == '\t') {
			return strings.TrimSpace(line[:i]), strings.TrimSpace(line[i+1:]), true
		}
	}
	return "", "", false
}

func parsePrometheusMetric(raw string) (string, map[string]string, error) {
	open := strings.IndexByte(raw, '{')
	if open < 0 {
		return raw, nil, nil
	}
	if !strings.HasSuffix(raw, "}") {
		return "", nil, fmt.Errorf("unterminated label set")
	}
	name := raw[:open]
	labels := map[string]string{}
	for _, pair := range splitUnescaped(raw[open+1:len(raw)-1], ',') {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		key, value, ok := strings.Cut(pair, "=")
		if !ok {
			return "", nil, fmt.Errorf("invalid label")
		}
		decoded, err := strconv.Unquote(strings.TrimSpace(value))
		if err != nil {
			return "", nil, fmt.Errorf("invalid quoted label value")
		}
		labels[key] = decoded
	}
	return name, labels, nil
}
