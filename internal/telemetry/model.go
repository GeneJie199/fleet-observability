package telemetry

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const BatchSchema = "telemetry.batch/v1"
const MaxBatchPoints = 10000

type MetricKind string

const (
	Gauge   MetricKind = "gauge"
	Counter MetricKind = "counter"
)

var (
	metricNamePattern = regexp.MustCompile(`^[A-Za-z_:][A-Za-z0-9_:.-]{0,199}$`)
	labelNamePattern  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]{0,63}$`)
	identityPattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
)

type Point struct {
	Metric      string            `json:"metric"`
	Labels      map[string]string `json:"labels,omitempty"`
	TimestampMS int64             `json:"timestamp_ms"`
	Value       float64           `json:"value"`
	Kind        MetricKind        `json:"kind,omitempty"`
	Unit        string            `json:"unit,omitempty"`
}

type Batch struct {
	Schema   string  `json:"schema"`
	NodeID   string  `json:"node_id"`
	Source   string  `json:"source"`
	Sequence uint64  `json:"sequence"`
	SentAt   string  `json:"sent_at"`
	Points   []Point `json:"points"`
}

func (b *Batch) Normalize(now time.Time) {
	if b.Schema == "" {
		b.Schema = BatchSchema
	}
	if b.Source == "" {
		b.Source = "native"
	}
	if b.SentAt == "" {
		b.SentAt = now.UTC().Format(time.RFC3339Nano)
	}
	for i := range b.Points {
		if b.Points[i].Kind == "" {
			b.Points[i].Kind = Gauge
		}
		if b.Points[i].TimestampMS == 0 {
			b.Points[i].TimestampMS = now.UnixMilli()
		}
	}
}

func ValidateBatch(b Batch, now time.Time, maxPoints int) error {
	if b.Schema != BatchSchema {
		return fmt.Errorf("unsupported telemetry schema %q", b.Schema)
	}
	if !identityPattern.MatchString(b.NodeID) {
		return errors.New("invalid node_id")
	}
	if !identityPattern.MatchString(b.Source) {
		return errors.New("invalid source")
	}
	if _, err := time.Parse(time.RFC3339Nano, b.SentAt); err != nil {
		return errors.New("sent_at must be RFC3339")
	}
	if maxPoints <= 0 || maxPoints > MaxBatchPoints {
		maxPoints = MaxBatchPoints
	}
	if len(b.Points) == 0 || len(b.Points) > maxPoints {
		return fmt.Errorf("points must contain between 1 and %d items", maxPoints)
	}
	oldest := now.Add(-400 * 24 * time.Hour).UnixMilli()
	newest := now.Add(10 * time.Minute).UnixMilli()
	for i, point := range b.Points {
		if !metricNamePattern.MatchString(point.Metric) {
			return fmt.Errorf("point %d has invalid metric name", i)
		}
		if math.IsNaN(point.Value) || math.IsInf(point.Value, 0) {
			return fmt.Errorf("point %d has a non-finite value", i)
		}
		if point.TimestampMS < oldest || point.TimestampMS > newest {
			return fmt.Errorf("point %d timestamp is outside the accepted window", i)
		}
		if point.Kind != Gauge && point.Kind != Counter {
			return fmt.Errorf("point %d has invalid metric kind", i)
		}
		if len(point.Unit) > 40 || len(point.Labels) > 32 {
			return fmt.Errorf("point %d has too many labels or an invalid unit", i)
		}
		for key, value := range point.Labels {
			if !labelNamePattern.MatchString(key) || len(value) > 256 {
				return fmt.Errorf("point %d has an invalid label", i)
			}
		}
	}
	return nil
}

func SeriesKey(nodeID, source string, point Point) string {
	keys := make([]string, 0, len(point.Labels))
	for key := range point.Labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var out strings.Builder
	out.WriteString(point.Metric)
	out.WriteByte(0)
	out.WriteString(nodeID)
	out.WriteByte(0)
	out.WriteString(source)
	for _, key := range keys {
		out.WriteByte(0)
		out.WriteString(key)
		out.WriteByte('=')
		out.WriteString(point.Labels[key])
	}
	return out.String()
}

func NumericPoints(metrics map[string]any, timestampMS int64, labels map[string]string) []Point {
	out := make([]Point, 0, len(metrics))
	for name, raw := range metrics {
		value, ok := number(raw)
		if !ok || !metricNamePattern.MatchString(name) {
			continue
		}
		kind := Gauge
		if strings.HasSuffix(name, "_total") {
			kind = Counter
		}
		out = append(out, Point{Metric: name, Labels: cloneLabels(labels), TimestampMS: timestampMS, Value: value, Kind: kind})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Metric < out[j].Metric })
	return out
}

func cloneLabels(labels map[string]string) map[string]string {
	if len(labels) == 0 {
		return nil
	}
	out := make(map[string]string, len(labels))
	for key, value := range labels {
		if labelNamePattern.MatchString(key) && len(value) <= 256 {
			out[key] = value
		}
	}
	return out
}

func number(value any) (float64, bool) {
	switch value := value.(type) {
	case float64:
		return value, true
	case float32:
		return float64(value), true
	case int:
		return float64(value), true
	case int64:
		return float64(value), true
	case uint64:
		return float64(value), true
	case interface{ Float64() (float64, error) }:
		number, err := value.Float64()
		return number, err == nil
	case string:
		number, err := strconv.ParseFloat(value, 64)
		return number, err == nil
	default:
		return 0, false
	}
}
