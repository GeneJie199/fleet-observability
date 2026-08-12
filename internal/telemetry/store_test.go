package telemetry

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStoreAppendQueryRestartAndSequenceDedup(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC().Truncate(time.Second)
	store, err := OpenStore(dir, StoreOptions{Retention: 24 * time.Hour, MaxSeries: 100})
	if err != nil {
		t.Fatal(err)
	}
	batch := Batch{Schema: BatchSchema, NodeID: "node-a", Source: "native", Sequence: 7, SentAt: now.Format(time.RFC3339Nano), Points: []Point{
		{Metric: "cpu_percent", TimestampMS: now.Add(-20 * time.Second).UnixMilli(), Value: 20, Kind: Gauge},
		{Metric: "cpu_percent", TimestampMS: now.Add(-10 * time.Second).UnixMilli(), Value: 40, Kind: Gauge},
	}}
	duplicate, err := store.Append(batch)
	if err != nil || duplicate {
		t.Fatalf("append duplicate=%v err=%v", duplicate, err)
	}
	if duplicate, err = store.Append(batch); err != nil || !duplicate {
		t.Fatalf("dedup duplicate=%v err=%v", duplicate, err)
	}
	result := store.Query(Query{Metric: "cpu_percent", NodeID: "node-a", StartMS: now.Add(-time.Minute).UnixMilli(), EndMS: now.UnixMilli(), Step: time.Minute, Aggregate: "avg"})
	if len(result.Series) != 1 || len(result.Series[0].Points) != 1 || result.Series[0].Points[0].Value != 30 {
		t.Fatalf("query = %+v", result)
	}
	if err := os.Remove(filepath.Join(dir, "sequences.json")); err != nil {
		t.Fatal(err)
	}
	restarted, err := OpenStore(dir, StoreOptions{Retention: 24 * time.Hour, MaxSeries: 100})
	if err != nil {
		t.Fatal(err)
	}
	result = restarted.Query(Query{Metric: "cpu_percent", StartMS: now.Add(-time.Minute).UnixMilli(), EndMS: now.UnixMilli(), Step: time.Second})
	if len(result.Series) != 1 || len(result.Series[0].Points) != 2 {
		t.Fatalf("restart query = %+v", result)
	}
	if len(restarted.Catalog()) != 1 || len(restarted.Sources()) != 1 {
		t.Fatalf("catalog=%+v sources=%+v", restarted.Catalog(), restarted.Sources())
	}
	if duplicate, appendErr := restarted.Append(batch); appendErr != nil || !duplicate {
		t.Fatalf("recovered sequence dedup = (%v, %v)", duplicate, appendErr)
	}
	other := Batch{Schema: BatchSchema, NodeID: "node-b", Source: "native", Sequence: 1, SentAt: now.Format(time.RFC3339Nano), Points: []Point{{Metric: "cpu_percent", TimestampMS: now.Add(-5 * time.Second).UnixMilli(), Value: 99, Kind: Gauge}}}
	if _, err := restarted.Append(other); err != nil {
		t.Fatal(err)
	}
	nodes := map[string]bool{"node-a": true}
	filtered := restarted.Query(Query{Metric: "cpu_percent", NodeIDs: nodes, StartMS: now.Add(-time.Minute).UnixMilli(), EndMS: now.UnixMilli(), Step: time.Second})
	if len(filtered.Series) != 1 || filtered.Series[0].NodeID != "node-a" || len(restarted.CatalogForNodes(nodes)) != 1 || restarted.CatalogForNodes(nodes)[0].Samples != 2 || len(restarted.SourcesForNodes(nodes)) != 1 {
		t.Fatalf("filtered query=%+v catalog=%+v sources=%+v", filtered, restarted.CatalogForNodes(nodes), restarted.SourcesForNodes(nodes))
	}
}

func TestStoreCardinalityLimit(t *testing.T) {
	now := time.Now().UTC()
	store, err := OpenStore(t.TempDir(), StoreOptions{MaxSeries: 1})
	if err != nil {
		t.Fatal(err)
	}
	batch := Batch{Schema: BatchSchema, NodeID: "node-a", Source: "native", SentAt: now.Format(time.RFC3339Nano), Points: []Point{
		{Metric: "cpu_percent", TimestampMS: now.UnixMilli(), Value: 1, Kind: Gauge},
		{Metric: "memory_percent", TimestampMS: now.UnixMilli(), Value: 2, Kind: Gauge},
	}}
	if _, err := store.Append(batch); err == nil {
		t.Fatal("expected cardinality error")
	}
}

func TestStoreRetainedSampleLimitAllowsReplacement(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	store, err := OpenStore(t.TempDir(), StoreOptions{MaxSeries: 10, MaxSamples: 2})
	if err != nil {
		t.Fatal(err)
	}
	first := Batch{NodeID: "node-a", Source: "native", Sequence: 1, SentAt: now.Format(time.RFC3339Nano), Points: []Point{
		{Metric: "cpu_percent", TimestampMS: now.Add(-time.Second).UnixMilli(), Value: 1, Kind: Gauge},
		{Metric: "cpu_percent", TimestampMS: now.UnixMilli(), Value: 2, Kind: Gauge},
	}}
	if _, err = store.Append(first); err != nil {
		t.Fatal(err)
	}
	replacement := Batch{NodeID: "node-a", Source: "native", Sequence: 2, SentAt: now.Format(time.RFC3339Nano), Points: []Point{{Metric: "cpu_percent", TimestampMS: now.UnixMilli(), Value: 3, Kind: Gauge}}}
	if _, err = store.Append(replacement); err != nil {
		t.Fatalf("replace existing timestamp: %v", err)
	}
	overflow := Batch{NodeID: "node-a", Source: "native", Sequence: 3, SentAt: now.Format(time.RFC3339Nano), Points: []Point{{Metric: "cpu_percent", TimestampMS: now.Add(time.Second).UnixMilli(), Value: 4, Kind: Gauge}}}
	if _, err = store.Append(overflow); err == nil || !strings.Contains(err.Error(), "retained sample limit") {
		t.Fatalf("overflow error = %v", err)
	}
}

func TestConfiguredBatchLimitCannotExceedProtocol(t *testing.T) {
	now := time.Now().UTC()
	points := make([]Point, MaxBatchPoints+1)
	for index := range points {
		points[index] = Point{Metric: "load", TimestampMS: now.UnixMilli() + int64(index), Value: float64(index), Kind: Gauge}
	}
	batch := Batch{Schema: BatchSchema, NodeID: "node-a", Source: "native", SentAt: now.Format(time.RFC3339Nano), Points: points}
	if err := ValidateBatch(batch, now, MaxBatchPoints*2); err == nil {
		t.Fatal("oversized protocol batch passed validation")
	}
}
