package telemetry

import (
	"testing"
	"time"
)

func TestValidateAndNumericPoints(t *testing.T) {
	now := time.Now().UTC()
	batch := Batch{NodeID: "node-01", Source: "native", Sequence: 1, Points: NumericPoints(map[string]any{"cpu_percent": 42.5, "collected_at": "ignored", "bad metric": 1}, now.UnixMilli(), map[string]string{"region": "cn-east"})}
	batch.Normalize(now)
	if err := ValidateBatch(batch, now, 100); err != nil {
		t.Fatal(err)
	}
	if len(batch.Points) != 1 || batch.Points[0].Metric != "cpu_percent" || batch.Points[0].Labels["region"] != "cn-east" {
		t.Fatalf("points = %+v", batch.Points)
	}
}

func TestValidateRejectsCardinalityAndFutureTimestamp(t *testing.T) {
	now := time.Now().UTC()
	labels := map[string]string{}
	for i := 0; i < 33; i++ {
		labels[string(rune('a'+i%20))+"_label"] = "value"
	}
	batch := Batch{Schema: BatchSchema, NodeID: "node-01", Source: "native", SentAt: now.Format(time.RFC3339Nano), Points: []Point{{Metric: "cpu_percent", Labels: labels, TimestampMS: now.Add(time.Hour).UnixMilli(), Value: 1, Kind: Gauge}}}
	if err := ValidateBatch(batch, now, 100); err == nil {
		t.Fatal("expected invalid batch")
	}
}
