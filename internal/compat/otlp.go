package compat

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/GeneJie199/fleet-observability/internal/telemetry"
)

type otlpRequest struct {
	ResourceMetrics []struct {
		Resource struct {
			Attributes []otlpAttribute `json:"attributes"`
		} `json:"resource"`
		ScopeMetrics []struct {
			Scope struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			} `json:"scope"`
			Metrics []struct {
				Name  string          `json:"name"`
				Unit  string          `json:"unit"`
				Gauge *otlpDataPoints `json:"gauge"`
				Sum   *otlpDataPoints `json:"sum"`
			} `json:"metrics"`
		} `json:"scopeMetrics"`
	} `json:"resourceMetrics"`
}

type otlpDataPoints struct {
	DataPoints []struct {
		Attributes   []otlpAttribute `json:"attributes"`
		TimeUnixNano string          `json:"timeUnixNano"`
		AsDouble     *float64        `json:"asDouble"`
		AsInt        json.Number     `json:"asInt"`
	} `json:"dataPoints"`
}

type otlpAttribute struct {
	Key   string         `json:"key"`
	Value map[string]any `json:"value"`
}

func ParseOTLPJSON(reader io.Reader, now time.Time) ([]telemetry.Point, error) {
	decoder := json.NewDecoder(reader)
	decoder.UseNumber()
	var request otlpRequest
	if err := decoder.Decode(&request); err != nil {
		return nil, fmt.Errorf("invalid OTLP JSON: %w", err)
	}
	points := []telemetry.Point{}
	for _, resource := range request.ResourceMetrics {
		resourceLabels := otlpLabels(resource.Resource.Attributes)
		for _, scope := range resource.ScopeMetrics {
			if scope.Scope.Name != "" {
				resourceLabels["otel_scope_name"] = scope.Scope.Name
			}
			if scope.Scope.Version != "" {
				resourceLabels["otel_scope_version"] = scope.Scope.Version
			}
			for _, metric := range scope.Metrics {
				if metric.Gauge != nil {
					parsed, err := otlpPoints(metric.Name, metric.Unit, telemetry.Gauge, metric.Gauge, resourceLabels, now)
					if err != nil {
						return nil, err
					}
					points = append(points, parsed...)
				}
				if metric.Sum != nil {
					parsed, err := otlpPoints(metric.Name, metric.Unit, telemetry.Counter, metric.Sum, resourceLabels, now)
					if err != nil {
						return nil, err
					}
					points = append(points, parsed...)
				}
			}
		}
	}
	if len(points) == 0 {
		return nil, fmt.Errorf("no OTLP gauge or sum data points found")
	}
	return points, nil
}

func otlpPoints(name, unit string, kind telemetry.MetricKind, data *otlpDataPoints, resourceLabels map[string]string, now time.Time) ([]telemetry.Point, error) {
	points := make([]telemetry.Point, 0, len(data.DataPoints))
	for _, point := range data.DataPoints {
		value := 0.0
		var err error
		if point.AsDouble != nil {
			value = *point.AsDouble
		} else if point.AsInt != "" {
			value, err = strconv.ParseFloat(string(point.AsInt), 64)
			if err != nil {
				return nil, fmt.Errorf("metric %s has invalid asInt", name)
			}
		} else {
			continue
		}
		timestamp := now.UnixMilli()
		if point.TimeUnixNano != "" {
			nanoseconds, parseErr := strconv.ParseInt(point.TimeUnixNano, 10, 64)
			if parseErr != nil {
				return nil, fmt.Errorf("metric %s has invalid timeUnixNano", name)
			}
			timestamp = nanoseconds / int64(time.Millisecond)
		}
		labels := make(map[string]string, len(resourceLabels)+len(point.Attributes))
		for key, value := range resourceLabels {
			labels[key] = value
		}
		for key, value := range otlpLabels(point.Attributes) {
			labels[key] = value
		}
		points = append(points, telemetry.Point{Metric: metricName(name), Labels: labels, TimestampMS: timestamp, Value: value, Kind: kind, Unit: unit})
	}
	return points, nil
}

func otlpLabels(attributes []otlpAttribute) map[string]string {
	labels := map[string]string{}
	for _, attribute := range attributes {
		for _, field := range []string{"stringValue", "intValue", "doubleValue", "boolValue"} {
			if value, ok := attribute.Value[field]; ok {
				labels[labelName(attribute.Key)] = fmt.Sprint(value)
				break
			}
		}
	}
	return labels
}
