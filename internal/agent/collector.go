package agent

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/GeneJie199/fleet-observability/internal/events"
	"github.com/GeneJie199/fleet-observability/internal/telemetry"
)

type Collection struct {
	Points        []telemetry.Point
	Events        []events.Entry
	ReportMetrics map[string]any
}

type Collector interface {
	ID() string
	Interval() time.Duration
	// Collect must stop promptly when the context is canceled.
	Collect(context.Context, time.Time) (Collection, error)
}

type SystemCollector struct {
	Every  time.Duration
	Labels map[string]string
}

func (c SystemCollector) ID() string { return "system" }
func (c SystemCollector) Interval() time.Duration {
	if c.Every < time.Second {
		return 10 * time.Second
	}
	return c.Every
}
func (c SystemCollector) Collect(ctx context.Context, observedAt time.Time) (Collection, error) {
	metrics, err := CollectMetrics(ctx)
	if err != nil {
		return Collection{}, err
	}
	return Collection{Points: telemetry.NumericPoints(metrics, observedAt.UnixMilli(), c.Labels), ReportMetrics: metrics}, nil
}

type ProbeCollector struct {
	Every      time.Duration
	ConfigPath string
	Labels     map[string]string
}

func (c ProbeCollector) ID() string { return "service-probes" }
func (c ProbeCollector) Interval() time.Duration {
	if c.Every < time.Second {
		return 30 * time.Second
	}
	return c.Every
}
func (c ProbeCollector) Collect(ctx context.Context, observedAt time.Time) (Collection, error) {
	results, err := RunProbes(ctx, c.ConfigPath)
	if err != nil {
		return Collection{}, err
	}
	points := make([]telemetry.Point, 0, len(results)*2)
	for _, result := range results {
		labels := copyMetricLabels(c.Labels)
		labels["probe"] = result.Name
		labels["probe_kind"] = result.Kind
		labels["required"] = strconv.FormatBool(result.Required)
		up := 0.0
		if result.Status == "ok" {
			up = 1
		}
		points = append(points,
			telemetry.Point{Metric: "probe_up", Labels: labels, TimestampMS: observedAt.UnixMilli(), Value: up, Kind: telemetry.Gauge},
			telemetry.Point{Metric: "probe_latency_milliseconds", Labels: labels, TimestampMS: observedAt.UnixMilli(), Value: float64(result.LatencyMS), Kind: telemetry.Gauge, Unit: "milliseconds"},
		)
		if connections, ok := numberValue(result.Values["connections"]); ok {
			points = append(points, telemetry.Point{Metric: "database_connections", Labels: labels, TimestampMS: observedAt.UnixMilli(), Value: connections, Kind: telemetry.Gauge})
		}
	}
	return Collection{Points: points, ReportMetrics: map[string]any{"checks": results}}, nil
}

func copyMetricLabels(labels map[string]string) map[string]string {
	out := make(map[string]string, len(labels)+3)
	for key, value := range labels {
		out[key] = value
	}
	return out
}

func numberValue(value any) (float64, bool) {
	switch value := value.(type) {
	case float64:
		return value, true
	case int:
		return float64(value), true
	case int64:
		return float64(value), true
	case jsonNumber:
		number, err := strconv.ParseFloat(string(value), 64)
		return number, err == nil
	default:
		return 0, false
	}
}

type jsonNumber string

func collectorHealthPoint(id string, observedAt time.Time, up bool, duration time.Duration) []telemetry.Point {
	value := 0.0
	if up {
		value = 1
	}
	labels := map[string]string{"collector": id}
	return []telemetry.Point{
		{Metric: "agent_collector_up", Labels: labels, TimestampMS: observedAt.UnixMilli(), Value: value, Kind: telemetry.Gauge},
		{Metric: "agent_collector_duration_seconds", Labels: labels, TimestampMS: observedAt.UnixMilli(), Value: duration.Seconds(), Kind: telemetry.Gauge, Unit: "seconds"},
	}
}

func mergeMetrics(target map[string]any, source map[string]any) error {
	for key, value := range source {
		if _, exists := target[key]; exists {
			return fmt.Errorf("collector returned duplicate report metric %q", key)
		}
		target[key] = value
	}
	return nil
}
