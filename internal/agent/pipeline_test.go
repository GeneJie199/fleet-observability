package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GeneJie199/fleet-observability/internal/center"
	"github.com/GeneJie199/fleet-observability/internal/events"
	"github.com/GeneJie199/fleet-observability/internal/telemetry"
)

type countingCollector struct {
	mu    sync.Mutex
	calls int
}

type gatedCollector struct {
	id      string
	started chan<- string
	release <-chan struct{}
}

func (c gatedCollector) ID() string              { return c.id }
func (c gatedCollector) Interval() time.Duration { return time.Minute }
func (c gatedCollector) Collect(ctx context.Context, observedAt time.Time) (Collection, error) {
	c.started <- c.id
	select {
	case <-c.release:
		return Collection{ReportMetrics: map[string]any{c.id: observedAt.UnixMilli()}}, nil
	case <-ctx.Done():
		return Collection{}, ctx.Err()
	}
}

type timeoutCollector struct{}

type recoveryCollector struct{}

func (recoveryCollector) ID() string              { return "recovery" }
func (recoveryCollector) Interval() time.Duration { return time.Minute }
func (recoveryCollector) Collect(_ context.Context, observedAt time.Time) (Collection, error) {
	return Collection{
		Points:        []telemetry.Point{{Metric: "recovery_value", TimestampMS: observedAt.UnixMilli(), Value: 1, Kind: telemetry.Gauge}},
		Events:        []events.Entry{{TimestampMS: observedAt.UnixMilli(), Kind: "collector", Severity: "info", Message: "recovered"}},
		ReportMetrics: map[string]any{"recovery_value": 1},
	}, nil
}

func (timeoutCollector) ID() string              { return "timeout" }
func (timeoutCollector) Interval() time.Duration { return time.Minute }
func (timeoutCollector) Collect(ctx context.Context, _ time.Time) (Collection, error) {
	<-ctx.Done()
	return Collection{}, ctx.Err()
}

func (c *countingCollector) ID() string              { return "counting" }
func (c *countingCollector) Interval() time.Duration { return time.Minute }
func (c *countingCollector) Collect(_ context.Context, observedAt time.Time) (Collection, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	value := float64(c.calls)
	return Collection{
		Points:        []telemetry.Point{{Metric: "test_value", TimestampMS: observedAt.UnixMilli(), Value: value, Kind: telemetry.Gauge}},
		ReportMetrics: map[string]any{"test_value": value},
	}, nil
}

func TestPipelineSendsTelemetryAndReplacesCollectorSnapshot(t *testing.T) {
	var mu sync.Mutex
	var batches []telemetry.Batch
	var reports []center.Report
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/api/v1/telemetry/batches":
			var batch telemetry.Batch
			if err := json.NewDecoder(r.Body).Decode(&batch); err != nil {
				t.Errorf("decode telemetry: %v", err)
			}
			batches = append(batches, batch)
		case "/api/v1/nodes/node-1/report":
			var report center.Report
			if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
				t.Errorf("decode report: %v", err)
			}
			reports = append(reports, report)
		default:
			t.Errorf("unexpected endpoint %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	collector := &countingCollector{}
	pipeline, err := newPipeline(Config{
		CenterURL:      server.URL,
		NodeID:         "node-1",
		Token:          "secret",
		Interval:       time.Minute,
		ReportInterval: time.Minute,
		SpoolDir:       t.TempDir(),
		Collectors:     []Collector{collector},
		Client:         server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = pipeline.cycle(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if err = pipeline.cycle(context.Background(), true); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(batches) != 2 || batches[0].Sequence != 1 || batches[1].Sequence != 2 {
		t.Fatalf("batches = %+v", batches)
	}
	if len(reports) != 2 || reports[1].Metrics["test_value"] != float64(2) {
		t.Fatalf("reports = %+v", reports)
	}
	if count, _, pendingErr := pipeline.spool.pending(); pendingErr != nil || count != 0 {
		t.Fatalf("pending = (%d, %v)", count, pendingErr)
	}
}

func TestPipelineRunsDueCollectorsConcurrently(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()
	started := make(chan string, 2)
	release := make(chan struct{})
	pipeline, err := newPipeline(Config{
		CenterURL:               server.URL,
		NodeID:                  "node-concurrent",
		Interval:                time.Minute,
		ReportInterval:          time.Minute,
		CollectorTimeout:        time.Second,
		MaxConcurrentCollectors: 2,
		SpoolDir:                t.TempDir(),
		Collectors: []Collector{
			gatedCollector{id: "first", started: started, release: release},
			gatedCollector{id: "second", started: started, release: release},
		},
		Client: server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- pipeline.cycle(context.Background(), true) }()
	for range 2 {
		select {
		case <-started:
		case <-time.After(500 * time.Millisecond):
			close(release)
			t.Fatal("collectors did not start concurrently")
		}
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestPipelineDrainsFullSpoolsAfterCenterRecovers(t *testing.T) {
	now := time.Now().UTC()
	padding := strings.Repeat("x", 1200)
	telemetrySeed := telemetry.Batch{Schema: telemetry.BatchSchema, NodeID: "node-recovery", Source: "native-agent", Sequence: 1, SentAt: now.Format(time.RFC3339Nano), Points: []telemetry.Point{{Metric: "seed", Labels: map[string]string{"padding": padding}, TimestampMS: now.UnixMilli(), Value: 1, Kind: telemetry.Gauge}}}
	eventSeed := events.Batch{Schema: events.BatchSchema, NodeID: "node-recovery", Source: "native-agent", Sequence: 1, SentAt: now.Format(time.RFC3339Nano), Events: []events.Entry{{TimestampMS: now.UnixMilli(), Kind: "collector", Severity: "info", Message: "seed " + padding}}}
	telemetryJSON, _ := json.Marshal(telemetrySeed)
	eventJSON, _ := json.Marshal(eventSeed)
	maxBytes := int64(len(telemetryJSON))
	if int64(len(eventJSON)) > maxBytes {
		maxBytes = int64(len(eventJSON))
	}
	maxBytes += 16

	var mu sync.Mutex
	received := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		received[r.URL.Path]++
		mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()
	pipeline, err := newPipeline(Config{CenterURL: server.URL, NodeID: "node-recovery", Token: "secret", Interval: time.Minute, ReportInterval: time.Minute, SpoolDir: t.TempDir(), MaxSpoolBytes: maxBytes, Collectors: []Collector{recoveryCollector{}}, Client: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	if err = pipeline.spool.enqueue(telemetrySeed); err != nil {
		t.Fatal(err)
	}
	if err = pipeline.eventSpool.enqueue(eventSeed); err != nil {
		t.Fatal(err)
	}
	if err = pipeline.cycle(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if count, _, pendingErr := pipeline.spool.pending(); pendingErr != nil || count != 0 {
		t.Fatalf("telemetry pending = (%d, %v)", count, pendingErr)
	}
	if count, _, pendingErr := pipeline.eventSpool.pending(); pendingErr != nil || count != 0 {
		t.Fatalf("event pending = (%d, %v)", count, pendingErr)
	}
	mu.Lock()
	defer mu.Unlock()
	if received["/api/v1/telemetry/batches"] != 2 || received["/api/v1/events/batches"] != 2 || received["/api/v1/nodes/node-recovery/report"] != 1 {
		t.Fatalf("received = %+v", received)
	}
}

func TestPipelineCollectorTimeoutDoesNotBlockReporting(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()
	pipeline, err := newPipeline(Config{
		CenterURL:        server.URL,
		NodeID:           "node-timeout",
		Interval:         time.Minute,
		ReportInterval:   time.Minute,
		CollectorTimeout: 30 * time.Millisecond,
		SpoolDir:         t.TempDir(),
		Collectors:       []Collector{timeoutCollector{}},
		Client:           server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	err = pipeline.cycle(context.Background(), true)
	if err == nil || !strings.Contains(err.Error(), context.DeadlineExceeded.Error()) {
		t.Fatalf("error = %v", err)
	}
	if errors.Is(err, context.Canceled) || time.Since(started) > time.Second {
		t.Fatalf("collector timeout took %s: %v", time.Since(started), err)
	}
}

func TestCollectorJitterIsDeterministicAndBounded(t *testing.T) {
	now := time.Unix(100, 0)
	maximum := 5 * time.Second
	first := collectorJitter("node-a", "system", now, maximum)
	if second := collectorJitter("node-a", "system", now, maximum); second != first {
		t.Fatalf("jitter changed: %s != %s", first, second)
	}
	if first < 0 || first >= maximum {
		t.Fatalf("jitter out of range: %s", first)
	}
	if zero := collectorJitter("node-a", "system", now, 0); zero != 0 {
		t.Fatalf("zero jitter = %s", zero)
	}
}

func TestConfiguredCollectorIntervalUsesSpecificOverride(t *testing.T) {
	if got := configuredCollectorInterval(5*time.Second, time.Minute); got != 5*time.Second {
		t.Fatalf("specific interval = %s", got)
	}
	if got := configuredCollectorInterval(0, time.Minute); got != time.Minute {
		t.Fatalf("fallback interval = %s", got)
	}
}
