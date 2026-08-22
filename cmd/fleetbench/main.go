package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"time"

	"github.com/GeneJie199/fleet-observability/internal/events"
	"github.com/GeneJie199/fleet-observability/internal/telemetry"
)

const benchmarkSchema = "fleetscope.scale-benchmark/v1"

type report struct {
	Schema        string        `json:"schema"`
	CreatedAt     string        `json:"created_at"`
	Revision      string        `json:"revision,omitempty"`
	Platform      platform      `json:"platform"`
	Workload      workload      `json:"workload"`
	Results       results       `json:"results"`
	RuntimeMemory runtimeMemory `json:"runtime_memory"`
}

type platform struct {
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	GoVersion string `json:"go_version"`
	CPUs      int    `json:"logical_cpus"`
}

type workload struct {
	Nodes           int `json:"nodes"`
	MetricsPerNode  int `json:"metrics_per_node"`
	Intervals       int `json:"intervals"`
	IntervalSeconds int `json:"interval_seconds"`
	EventsPerNode   int `json:"events_per_node"`
	SimulatedHours  int `json:"simulated_hours"`
}

type results struct {
	TelemetrySamples        int     `json:"telemetry_samples"`
	Events                  int     `json:"events"`
	TelemetryIngestSeconds  float64 `json:"telemetry_ingest_seconds"`
	EventIngestSeconds      float64 `json:"event_ingest_seconds"`
	TelemetrySamplesPerSec  float64 `json:"telemetry_samples_per_second"`
	EventsPerSec            float64 `json:"events_per_second"`
	TelemetryDiskBytes      int64   `json:"telemetry_disk_bytes"`
	EventDiskBytes          int64   `json:"event_disk_bytes"`
	EstimatedDailyDiskBytes int64   `json:"estimated_daily_disk_bytes"`
	RestartLoadSeconds      float64 `json:"restart_load_seconds"`
	QuerySeconds            float64 `json:"query_seconds"`
	QuerySeries             int     `json:"query_series"`
	RetainedEvents          int     `json:"retained_events"`
}

type runtimeMemory struct {
	HeapAllocBytes uint64 `json:"heap_alloc_bytes"`
	HeapInUseBytes uint64 `json:"heap_in_use_bytes"`
	SysBytes       uint64 `json:"sys_bytes"`
	GCCount        uint32 `json:"gc_count"`
}

func main() {
	nodes := flag.Int("nodes", 100, "number of simulated Fleet nodes")
	intervals := flag.Int("intervals", 360, "samples per metric and node")
	intervalSeconds := flag.Int("interval-seconds", 10, "seconds represented by each interval")
	eventsPerNode := flag.Int("events-per-node", 36, "events retained per node")
	dataDir := flag.String("data-dir", "", "benchmark data directory; a disposable temporary directory is used when omitted")
	flag.Parse()
	if *nodes < 1 || *nodes > 1000 || *intervals < 1 || *intervals > 50000 || *intervalSeconds < 1 || *intervalSeconds > 3600 || *eventsPerNode < 1 || *eventsPerNode > events.MaxBatchEvents {
		fatal(errors.New("benchmark workload is outside the supported safety bounds"))
	}
	dir := *dataDir
	cleanup := func() {}
	if dir == "" {
		var err error
		dir, err = os.MkdirTemp("", "fleetscope-scale-")
		if err != nil {
			fatal(err)
		}
		cleanup = func() { _ = os.RemoveAll(dir) }
	}
	defer cleanup()
	value, err := run(dir, *nodes, *intervals, *intervalSeconds, *eventsPerNode)
	if err != nil {
		fatal(err)
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err = encoder.Encode(value); err != nil {
		fatal(err)
	}
}

func run(dir string, nodes, intervals, intervalSeconds, eventsPerNode int) (report, error) {
	metricNames := []string{"cpu_percent", "memory_percent", "disk_percent", "load_1m", "network_receive_bytes_total", "network_transmit_bytes_total", "process_count", "uptime_seconds"}
	telemetryDir := filepath.Join(dir, "telemetry")
	eventDir := filepath.Join(dir, "events")
	telemetrySamples := nodes * intervals * len(metricNames)
	eventCount := nodes * eventsPerNode
	telemetryStore, err := telemetry.OpenStore(telemetryDir, telemetry.StoreOptions{MaxSeries: nodes * len(metricNames) * 2, MaxSamples: telemetrySamples + 1})
	if err != nil {
		return report{}, err
	}
	eventStore, err := events.OpenStore(eventDir, events.Options{MaxEvents: eventCount + 1})
	if err != nil {
		return report{}, err
	}
	now := time.Now().UTC().Truncate(time.Second)
	start := now.Add(-time.Duration(intervals*intervalSeconds) * time.Second)
	telemetryStarted := time.Now()
	for node := 0; node < nodes; node++ {
		nodeID := fmt.Sprintf("scale-node-%04d", node)
		points := make([]telemetry.Point, 0, telemetry.MaxBatchPoints)
		sequence := uint64(1)
		flush := func() error {
			if len(points) == 0 {
				return nil
			}
			batch := telemetry.Batch{Schema: telemetry.BatchSchema, NodeID: nodeID, Source: "fleetbench", Sequence: sequence, SentAt: now.Format(time.RFC3339Nano), Points: points}
			if _, appendErr := telemetryStore.Append(batch); appendErr != nil {
				return appendErr
			}
			sequence++
			points = make([]telemetry.Point, 0, telemetry.MaxBatchPoints)
			return nil
		}
		for interval := 0; interval < intervals; interval++ {
			timestamp := start.Add(time.Duration(interval*intervalSeconds) * time.Second).UnixMilli()
			for metricIndex, metric := range metricNames {
				points = append(points, telemetry.Point{Metric: metric, TimestampMS: timestamp, Value: float64((node+interval+metricIndex)%100) + 0.25, Kind: telemetry.Gauge, Labels: map[string]string{"environment": "scale"}})
				if len(points) == telemetry.MaxBatchPoints {
					if err = flush(); err != nil {
						return report{}, fmt.Errorf("append telemetry for node %d: %w", node, err)
					}
				}
			}
		}
		if err = flush(); err != nil {
			return report{}, fmt.Errorf("append telemetry for node %d: %w", node, err)
		}
	}
	telemetryDuration := time.Since(telemetryStarted)
	eventsStarted := time.Now()
	for node := 0; node < nodes; node++ {
		entries := make([]events.Entry, 0, eventsPerNode)
		for index := 0; index < eventsPerNode; index++ {
			timestamp := start.Add(time.Duration(index*intervals*intervalSeconds/eventsPerNode) * time.Second).UnixMilli()
			entries = append(entries, events.Entry{TimestampMS: timestamp, Kind: "log", Severity: "info", Service: "scale-service", Message: fmt.Sprintf("synthetic scale event %d for node %d", index, node), Attributes: map[string]string{"environment": "scale"}})
		}
		batch := events.Batch{Schema: events.BatchSchema, NodeID: fmt.Sprintf("scale-node-%04d", node), Source: "fleetbench", Sequence: 1, SentAt: now.Format(time.RFC3339Nano), Events: entries}
		if _, err = eventStore.Append(batch); err != nil {
			return report{}, fmt.Errorf("append events for node %d: %w", node, err)
		}
	}
	eventDuration := time.Since(eventsStarted)
	telemetryBytes, err := directoryBytes(telemetryDir)
	if err != nil {
		return report{}, err
	}
	eventBytes, err := directoryBytes(eventDir)
	if err != nil {
		return report{}, err
	}
	telemetryStore, eventStore = nil, nil
	runtime.GC()
	restartStarted := time.Now()
	telemetryStore, err = telemetry.OpenStore(telemetryDir, telemetry.StoreOptions{MaxSeries: nodes * len(metricNames) * 2, MaxSamples: telemetrySamples + 1})
	if err != nil {
		return report{}, fmt.Errorf("reload telemetry: %w", err)
	}
	eventStore, err = events.OpenStore(eventDir, events.Options{MaxEvents: eventCount + 1})
	if err != nil {
		return report{}, fmt.Errorf("reload events: %w", err)
	}
	restartDuration := time.Since(restartStarted)
	queryStarted := time.Now()
	query := telemetryStore.Query(telemetry.Query{Metric: "cpu_percent", StartMS: start.UnixMilli(), EndMS: now.UnixMilli(), Step: time.Minute, Aggregate: "avg"})
	eventResult := eventStore.Query(events.Query{StartMS: start.UnixMilli(), EndMS: now.UnixMilli(), Limit: 1})
	queryDuration := time.Since(queryStarted)
	runtime.GC()
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	runtime.KeepAlive(telemetryStore)
	runtime.KeepAlive(eventStore)
	simulatedSeconds := int64(intervals * intervalSeconds)
	dailyBytes := (telemetryBytes + eventBytes) * int64(24*time.Hour/time.Second) / simulatedSeconds
	return report{
		Schema:    benchmarkSchema,
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Revision:  revision(),
		Platform:  platform{OS: runtime.GOOS, Arch: runtime.GOARCH, GoVersion: runtime.Version(), CPUs: runtime.NumCPU()},
		Workload:  workload{Nodes: nodes, MetricsPerNode: len(metricNames), Intervals: intervals, IntervalSeconds: intervalSeconds, EventsPerNode: eventsPerNode, SimulatedHours: int(simulatedSeconds / 3600)},
		Results: results{
			TelemetrySamples: telemetrySamples, Events: eventCount,
			TelemetryIngestSeconds: telemetryDuration.Seconds(), EventIngestSeconds: eventDuration.Seconds(),
			TelemetrySamplesPerSec: float64(telemetrySamples) / telemetryDuration.Seconds(), EventsPerSec: float64(eventCount) / eventDuration.Seconds(),
			TelemetryDiskBytes: telemetryBytes, EventDiskBytes: eventBytes, EstimatedDailyDiskBytes: dailyBytes,
			RestartLoadSeconds: restartDuration.Seconds(), QuerySeconds: queryDuration.Seconds(), QuerySeries: len(query.Series), RetainedEvents: eventResult.TotalMatched,
		},
		RuntimeMemory: runtimeMemory{HeapAllocBytes: memory.HeapAlloc, HeapInUseBytes: memory.HeapInuse, SysBytes: memory.Sys, GCCount: memory.NumGC},
	}, nil
}

func directoryBytes(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type().IsRegular() {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			total += info.Size()
		}
		return nil
	})
	return total, err
}

func revision() string {
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			if setting.Key == "vcs.revision" {
				return setting.Value
			}
		}
	}
	return ""
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "fleetbench:", err)
	os.Exit(1)
}
