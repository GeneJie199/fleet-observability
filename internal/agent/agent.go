package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/GeneJie199/fleet-observability/internal/center"
)

type Config struct {
	CenterURL, NodeID, Token, InventoryPath, DriftPath, InfraScoutPath, StateDir, ProbeConfigPath, LogConfigPath, ApplicationConfigPath, AgentVersion, CredentialPath string
	Labels                                                                                                                                                            map[string]string
	Interval, SystemInterval, ProbeInterval, LogInterval, ApplicationInterval, ReportInterval, CollectorTimeout, Jitter                                               time.Duration
	SpoolDir                                                                                                                                                          string
	MaxSpoolBytes                                                                                                                                                     int64
	MaxConcurrentCollectors                                                                                                                                           int
	Reenroll                                                                                                                                                          bool
	Collectors                                                                                                                                                        []Collector
	Client                                                                                                                                                            *http.Client
}

func Run(ctx context.Context, c Config, once bool) error {
	pipeline, err := newPipeline(c)
	if err != nil {
		return err
	}
	if err := pipeline.ensureIdentity(ctx); err != nil {
		return err
	}
	for {
		if err := pipeline.cycle(ctx, once); err != nil {
			if once {
				return err
			}
			fmt.Fprintln(os.Stderr, "fleet agent report:", err)
		}
		if once {
			return nil
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}

func Push(ctx context.Context, c Config) error {
	if c.InfraScoutPath != "" {
		if err := runInfraScout(ctx, &c); err != nil {
			return err
		}
	}
	metrics, err := CollectMetrics(ctx)
	if err != nil {
		return fmt.Errorf("collect metrics: %w", err)
	}
	if c.ProbeConfigPath != "" {
		results, probeErr := RunProbes(ctx, c.ProbeConfigPath)
		if probeErr != nil {
			return fmt.Errorf("run probes: %w", probeErr)
		}
		metrics["checks"] = results
	}
	host, _ := os.Hostname()
	version := c.AgentVersion
	if version == "" {
		version = "dev"
	}
	r := center.Report{NodeID: c.NodeID, ObservedAt: time.Now().UTC().Format(time.RFC3339), Agent: center.AgentInfo{Version: version, OS: runtime.GOOS, Arch: runtime.GOARCH, Hostname: host}, Metrics: metrics, Labels: c.Labels}
	if c.InventoryPath != "" {
		if r.Inventory, err = os.ReadFile(c.InventoryPath); err != nil {
			return fmt.Errorf("read inventory: %w", err)
		}
	}
	if c.DriftPath != "" {
		if r.Drift, err = os.ReadFile(c.DriftPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("read drift: %w", err)
		}
	}
	b, _ := json.Marshal(r)
	endpoint := strings.TrimRight(c.CenterURL, "/") + "/api/v1/nodes/" + url.PathEscape(c.NodeID) + "/report"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("center returned %s", resp.Status)
	}
	return nil
}
func runInfraScout(ctx context.Context, c *Config) error {
	if c.StateDir == "" {
		return errors.New("state directory is required with infrascout")
	}
	command := "check"
	if _, err := os.Stat(c.StateDir + string(os.PathSeparator) + "baseline.json"); errors.Is(err, os.ErrNotExist) {
		command = "baseline"
	}
	args := []string{command, "--state-dir", c.StateDir}
	if command == "check" {
		args = append(args, "--fail-on", "never")
	}
	cmd := exec.CommandContext(ctx, c.InfraScoutPath, args...)
	if b, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("infrascout check: %w: %s", err, strings.TrimSpace(string(b)))
	}
	if c.InventoryPath == "" {
		c.InventoryPath = c.StateDir + string(os.PathSeparator) + "inventory.json"
	}
	if c.DriftPath == "" {
		c.DriftPath = c.StateDir + string(os.PathSeparator) + "drift.json"
	}
	return nil
}

func ParseLabels(values []string) (map[string]string, error) {
	out := map[string]string{}
	for _, v := range values {
		k, x, ok := strings.Cut(v, "=")
		if !ok || strings.TrimSpace(k) == "" {
			return nil, fmt.Errorf("invalid label %q, want key=value", v)
		}
		out[strings.TrimSpace(k)] = strings.TrimSpace(x)
	}
	return out, nil
}
