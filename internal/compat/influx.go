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

func ParseInflux(reader io.Reader, now time.Time, precision string) ([]telemetry.Point, error) {
	points := []telemetry.Point{}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), 2<<20)
	for lineNumber := 1; scanner.Scan(); lineNumber++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parsed, err := parseInfluxLine(line, now, precision)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNumber, err)
		}
		points = append(points, parsed...)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(points) == 0 {
		return nil, fmt.Errorf("no numeric Influx fields found")
	}
	return points, nil
}

func parseInfluxLine(line string, now time.Time, precision string) ([]telemetry.Point, error) {
	parts := splitUnescaped(line, ' ')
	compact := parts[:0]
	for _, part := range parts {
		if part != "" {
			compact = append(compact, part)
		}
	}
	if len(compact) < 2 || len(compact) > 3 {
		return nil, fmt.Errorf("expected measurement fields and optional timestamp")
	}
	identity := splitUnescaped(compact[0], ',')
	measurement := metricName(unescape(identity[0]))
	labels := map[string]string{}
	for _, rawTag := range identity[1:] {
		key, value, ok := strings.Cut(rawTag, "=")
		if !ok {
			return nil, fmt.Errorf("invalid tag")
		}
		labels[labelName(unescape(key))] = unescape(value)
	}
	timestamp := now.UnixMilli()
	if len(compact) == 3 {
		raw, err := strconv.ParseInt(compact[2], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid timestamp")
		}
		timestamp, err = influxMilliseconds(raw, precision)
		if err != nil {
			return nil, err
		}
	}
	points := []telemetry.Point{}
	for _, rawField := range splitUnescaped(compact[1], ',') {
		key, rawValue, ok := strings.Cut(rawField, "=")
		if !ok {
			return nil, fmt.Errorf("invalid field")
		}
		value, ok := influxNumber(rawValue)
		if !ok {
			continue
		}
		points = append(points, telemetry.Point{Metric: metricName(measurement + "_" + unescape(key)), Labels: labels, TimestampMS: timestamp, Value: value, Kind: telemetry.Gauge})
	}
	if len(points) == 0 {
		return nil, fmt.Errorf("line contains no numeric fields")
	}
	return points, nil
}

func influxNumber(raw string) (float64, bool) {
	if raw == "true" || raw == "t" || raw == "TRUE" {
		return 1, true
	}
	if raw == "false" || raw == "f" || raw == "FALSE" {
		return 0, true
	}
	raw = strings.TrimSuffix(strings.TrimSuffix(raw, "i"), "u")
	value, err := strconv.ParseFloat(raw, 64)
	return value, err == nil
}

func influxMilliseconds(value int64, precision string) (int64, error) {
	switch precision {
	case "", "ns":
		return value / int64(time.Millisecond), nil
	case "us", "u":
		return value / int64(time.Millisecond/time.Microsecond), nil
	case "ms":
		return value, nil
	case "s":
		return value * 1000, nil
	default:
		return 0, fmt.Errorf("unsupported precision %q", precision)
	}
}
