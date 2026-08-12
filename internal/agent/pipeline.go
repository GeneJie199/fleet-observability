package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/GeneJie199/fleet-observability/internal/center"
	"github.com/GeneJie199/fleet-observability/internal/events"
	"github.com/GeneJie199/fleet-observability/internal/telemetry"
)

type pipeline struct {
	config            Config
	collectors        []Collector
	next              map[string]time.Time
	latest            map[string]any
	latestByCollector map[string]map[string]any
	spool             *spool
	eventSpool        *eventSpool
	agentToken        string
	lastReport        time.Time
}

func newPipeline(config Config) (*pipeline, error) {
	if config.CenterURL == "" || config.NodeID == "" {
		return nil, errors.New("center URL and node ID are required")
	}
	if config.Interval < time.Second {
		config.Interval = 10 * time.Second
	}
	if config.ReportInterval < config.Interval {
		config.ReportInterval = 5 * time.Minute
	}
	if config.CollectorTimeout <= 0 {
		config.CollectorTimeout = 15 * time.Second
	}
	if config.Jitter < 0 {
		return nil, errors.New("collector jitter cannot be negative")
	}
	if config.MaxConcurrentCollectors <= 0 {
		config.MaxConcurrentCollectors = 4
	}
	if config.Client == nil {
		config.Client = &http.Client{Timeout: 20 * time.Second}
	}
	if config.SpoolDir == "" {
		return nil, errors.New("spool directory is required for the native agent")
	}
	queue, err := openSpool(config.SpoolDir, config.MaxSpoolBytes)
	if err != nil {
		return nil, err
	}
	eventQueue, err := openEventSpool(config.SpoolDir, config.MaxSpoolBytes)
	if err != nil {
		return nil, err
	}
	collectors := config.Collectors
	if len(collectors) == 0 {
		collectors = append(collectors, SystemCollector{Every: configuredCollectorInterval(config.SystemInterval, config.Interval), Labels: config.Labels})
		if config.ProbeConfigPath != "" {
			collectors = append(collectors, ProbeCollector{Every: configuredCollectorInterval(config.ProbeInterval, config.Interval), ConfigPath: config.ProbeConfigPath, Labels: config.Labels})
		}
		if config.LogConfigPath != "" {
			collectors = append(collectors, FileLogCollector{Every: configuredCollectorInterval(config.LogInterval, config.Interval), ConfigPath: config.LogConfigPath, StatePath: filepath.Join(config.SpoolDir, "log-offsets.json")})
		}
		if config.ApplicationConfigPath != "" {
			collectors = append(collectors, ApplicationCollector{Every: configuredCollectorInterval(config.ApplicationInterval, config.Interval), ConfigPath: config.ApplicationConfigPath, Labels: config.Labels})
		}
	}
	seen := map[string]bool{}
	for _, collector := range collectors {
		if collector == nil || collector.ID() == "" || seen[collector.ID()] {
			return nil, errors.New("collector IDs must be non-empty and unique")
		}
		seen[collector.ID()] = true
	}
	return &pipeline{
		config:            config,
		collectors:        collectors,
		next:              map[string]time.Time{},
		latest:            map[string]any{},
		latestByCollector: map[string]map[string]any{},
		spool:             queue,
		eventSpool:        eventQueue,
	}, nil
}

func configuredCollectorInterval(specific, fallback time.Duration) time.Duration {
	if specific >= time.Second {
		return specific
	}
	return fallback
}

func (p *pipeline) cycle(ctx context.Context, force bool) error {
	now := time.Now().UTC()
	points := []telemetry.Point{}
	eventEntries := []events.Entry{}
	collectionErrors := []string{}
	type collectionResult struct {
		collector  Collector
		collection Collection
		err        error
		duration   time.Duration
	}
	due := make([]Collector, 0, len(p.collectors))
	for _, collector := range p.collectors {
		if !force && now.Before(p.next[collector.ID()]) {
			continue
		}
		due = append(due, collector)
		p.next[collector.ID()] = now.Add(collector.Interval() + collectorJitter(p.config.NodeID, collector.ID(), now, p.config.Jitter))
	}
	results := make(chan collectionResult, len(due))
	semaphore := make(chan struct{}, p.config.MaxConcurrentCollectors)
	var wait sync.WaitGroup
	for _, collector := range due {
		wait.Add(1)
		go func(collector Collector) {
			defer wait.Done()
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-ctx.Done():
				results <- collectionResult{collector: collector, err: ctx.Err()}
				return
			}
			collectorCtx, cancel := context.WithTimeout(ctx, p.config.CollectorTimeout)
			defer cancel()
			started := time.Now()
			collection, err := collector.Collect(collectorCtx, now)
			results <- collectionResult{collector: collector, collection: collection, err: err, duration: time.Since(started)}
		}(collector)
	}
	wait.Wait()
	close(results)
	for result := range results {
		collector := result.collector
		points = append(points, collectorHealthPoint(collector.ID(), now, result.err == nil, result.duration)...)
		if result.err != nil {
			collectionErrors = append(collectionErrors, collector.ID()+": "+result.err.Error())
			continue
		}
		points = append(points, result.collection.Points...)
		eventEntries = append(eventEntries, result.collection.Events...)
		p.latestByCollector[collector.ID()] = result.collection.ReportMetrics
	}
	if err := p.rebuildLatestMetrics(); err != nil {
		collectionErrors = append(collectionErrors, err.Error())
	}
	if len(points) > 0 {
		sequence, err := p.spool.nextSequence()
		if err != nil {
			return err
		}
		batch := telemetry.Batch{Schema: telemetry.BatchSchema, NodeID: p.config.NodeID, Source: "native-agent", Sequence: sequence, SentAt: now.Format(time.RFC3339Nano), Points: points}
		if err := p.spool.enqueue(batch); err != nil {
			if !errors.Is(err, errSpoolCapacity) {
				return err
			}
			if _, drainErr := p.spool.drain(ctx, p.sendTelemetry); drainErr != nil {
				return fmt.Errorf("%w; recovery drain failed: %v", err, drainErr)
			}
			if err = p.spool.enqueue(batch); err != nil {
				return err
			}
		}
	}
	if len(eventEntries) > 0 {
		sequence, err := p.eventSpool.nextSequence()
		if err != nil {
			return err
		}
		batch := events.Batch{Schema: events.BatchSchema, NodeID: p.config.NodeID, Source: "native-agent", Sequence: sequence, SentAt: now.Format(time.RFC3339Nano), Events: eventEntries}
		if err := p.eventSpool.enqueue(batch); err != nil {
			if !errors.Is(err, errSpoolCapacity) {
				return err
			}
			if _, drainErr := p.eventSpool.drain(ctx, p.sendEvents); drainErr != nil {
				return fmt.Errorf("%w; recovery drain failed: %v", err, drainErr)
			}
			if err = p.eventSpool.enqueue(batch); err != nil {
				return err
			}
		}
	}
	_, sendErr := p.spool.drain(ctx, p.sendTelemetry)
	_, eventSendErr := p.eventSpool.drain(ctx, p.sendEvents)
	if sendErr == nil {
		sendErr = eventSendErr
	}
	reportDue := force || p.lastReport.IsZero() || now.Sub(p.lastReport) >= p.config.ReportInterval
	if reportDue {
		if err := p.sendReport(ctx, now); err != nil {
			if sendErr == nil {
				sendErr = err
			}
		} else {
			p.lastReport = now
		}
	}
	if sendErr != nil {
		return sendErr
	}
	if force && len(collectionErrors) > 0 {
		return fmt.Errorf("collector errors: %s", strings.Join(collectionErrors, "; "))
	}
	return nil
}

func collectorJitter(nodeID, collectorID string, cycle time.Time, maximum time.Duration) time.Duration {
	if maximum <= 0 {
		return 0
	}
	window := cycle.UnixNano() / int64(maximum)
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%d", nodeID, collectorID, window)))
	return time.Duration(binary.BigEndian.Uint64(sum[:8]) % uint64(maximum))
}

func (p *pipeline) sendEvents(ctx context.Context, batch events.Batch) error {
	body, err := json.Marshal(batch)
	if err != nil {
		return err
	}
	endpoint := strings.TrimRight(p.config.CenterURL, "/") + "/api/v1/events/batches"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	if token := p.writeToken(); token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := p.config.Client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		return fmt.Errorf("event center returned %s", response.Status)
	}
	return nil
}

func (p *pipeline) rebuildLatestMetrics() error {
	merged := map[string]any{}
	for collectorID, metrics := range p.latestByCollector {
		if err := mergeMetrics(merged, metrics); err != nil {
			return fmt.Errorf("collector %s: %w", collectorID, err)
		}
	}
	p.latest = merged
	return nil
}

func (p *pipeline) sendTelemetry(ctx context.Context, batch telemetry.Batch) error {
	body, err := json.Marshal(batch)
	if err != nil {
		return err
	}
	endpoint := strings.TrimRight(p.config.CenterURL, "/") + "/api/v1/telemetry/batches"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	if token := p.writeToken(); token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := p.config.Client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		return fmt.Errorf("telemetry center returned %s", response.Status)
	}
	return nil
}

func (p *pipeline) sendReport(ctx context.Context, observedAt time.Time) error {
	config := p.config
	if config.InfraScoutPath != "" {
		if err := runInfraScout(ctx, &config); err != nil {
			return err
		}
	}
	host, _ := os.Hostname()
	version := config.AgentVersion
	if version == "" {
		version = "dev"
	}
	report := center.Report{NodeID: config.NodeID, ObservedAt: observedAt.Format(time.RFC3339Nano), Agent: center.AgentInfo{Version: version, OS: runtime.GOOS, Arch: runtime.GOARCH, Hostname: host}, Metrics: p.latest, Labels: config.Labels}
	var err error
	if config.InventoryPath != "" {
		if report.Inventory, err = os.ReadFile(config.InventoryPath); err != nil {
			return fmt.Errorf("read inventory: %w", err)
		}
	}
	if config.DriftPath != "" {
		if report.Drift, err = os.ReadFile(config.DriftPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("read drift: %w", err)
		}
	}
	body, err := json.Marshal(report)
	if err != nil {
		return err
	}
	endpoint := strings.TrimRight(config.CenterURL, "/") + "/api/v1/nodes/" + url.PathEscape(config.NodeID) + "/report"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	if token := p.writeToken(); token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := config.Client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		return fmt.Errorf("report center returned %s", response.Status)
	}
	return nil
}

func (p *pipeline) writeToken() string {
	if p.agentToken != "" {
		return p.agentToken
	}
	return p.config.Token
}
