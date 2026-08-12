//go:build linux

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/GeneJie199/fleet-observability/internal/telemetry"
)

type dockerContainer struct {
	ID     string   `json:"Id"`
	Names  []string `json:"Names"`
	State  string   `json:"State"`
	Status string   `json:"Status"`
}

type dockerStats struct {
	CPUStats struct {
		CPUUsage struct {
			TotalUsage uint64 `json:"total_usage"`
		} `json:"cpu_usage"`
		SystemCPUUsage uint64 `json:"system_cpu_usage"`
		OnlineCPUs     uint64 `json:"online_cpus"`
	} `json:"cpu_stats"`
	PreCPUStats struct {
		CPUUsage struct {
			TotalUsage uint64 `json:"total_usage"`
		} `json:"cpu_usage"`
		SystemCPUUsage uint64 `json:"system_cpu_usage"`
	} `json:"precpu_stats"`
	MemoryStats struct {
		Usage uint64            `json:"usage"`
		Limit uint64            `json:"limit"`
		Stats map[string]uint64 `json:"stats"`
	} `json:"memory_stats"`
	Networks map[string]struct {
		RXBytes uint64 `json:"rx_bytes"`
		TXBytes uint64 `json:"tx_bytes"`
	} `json:"networks"`
	PidsStats struct {
		Current uint64 `json:"current"`
	} `json:"pids_stats"`
}

func collectDockerTarget(ctx context.Context, target DockerTarget, observedAt time.Time, baseLabels map[string]string) applicationResult {
	result := applicationResult{name: target.Name, kind: "docker", required: target.Required}
	socket := target.Socket
	if socket == "" {
		socket = "/var/run/docker.sock"
	}
	socket = strings.TrimPrefix(socket, "unix://")
	if _, err := os.Stat(socket); err != nil {
		result.err = errors.New("docker engine socket is unavailable")
		return result
	}
	client := dockerHTTPClient(socket, target.TimeoutSeconds)
	maximum := target.MaxContainers
	if maximum <= 0 {
		maximum = 100
	}
	containers, err := listDockerContainers(ctx, client, maximum)
	if err != nil {
		result.err = err
		return result
	}
	labels := applicationLabels(baseLabels, target.Labels, target.Name, "docker", target.Required)
	aggregate := map[string]float64{"containers": float64(len(containers))}
	for _, container := range containers {
		state := strings.ToLower(container.State)
		if state == "running" {
			aggregate["containers_running"]++
		}
		if strings.Contains(strings.ToLower(container.Status), "unhealthy") {
			aggregate["containers_unhealthy"]++
		}
		containerName := strings.TrimPrefix(firstDockerName(container), "/")
		containerLabels := copyMetricLabels(labels)
		containerLabels["container"] = containerName
		up := 0.0
		if state == "running" {
			up = 1
		}
		result.points = append(result.points, telemetry.Point{Metric: "docker_container_up", Labels: containerLabels, TimestampMS: observedAt.UnixMilli(), Value: up, Kind: telemetry.Gauge})
		if state != "running" {
			continue
		}
		stats, err := getDockerStats(ctx, client, container.ID)
		if err != nil {
			continue
		}
		metrics := dockerStatsMetrics(stats)
		result.points = append(result.points, pointsFromMetrics("docker_container_", metrics, observedAt, containerLabels, map[string]bool{"network_receive_bytes": true, "network_transmit_bytes": true})...)
	}
	result.points = append(result.points, pointsFromMetrics("docker_", aggregate, observedAt, labels, nil)...)
	result.summary = map[string]any{"containers": aggregate["containers"], "running": aggregate["containers_running"], "unhealthy": aggregate["containers_unhealthy"]}
	return result
}

func dockerHTTPClient(socket string, timeoutSeconds int) *http.Client {
	transport := &http.Transport{DisableCompression: true, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	return &http.Client{Transport: transport, Timeout: timeoutDuration(timeoutSeconds)}
}

func listDockerContainers(ctx context.Context, client *http.Client, maximum int) ([]dockerContainer, error) {
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker/v1.41/containers/json?all=1", nil)
	response, err := client.Do(request)
	if err != nil {
		return nil, errors.New("docker engine list request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("docker engine returned HTTP %d", response.StatusCode)
	}
	var containers []dockerContainer
	decoder := json.NewDecoder(io.LimitReader(response.Body, 4<<20))
	if err := decoder.Decode(&containers); err != nil {
		return nil, errors.New("docker engine returned invalid container data")
	}
	if len(containers) > maximum {
		containers = containers[:maximum]
	}
	return containers, nil
}

func getDockerStats(ctx context.Context, client *http.Client, id string) (dockerStats, error) {
	path := "http://docker/v1.41/containers/" + url.PathEscape(id) + "/stats?stream=false&one-shot=true"
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, path, nil)
	response, err := client.Do(request)
	if err != nil {
		return dockerStats{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return dockerStats{}, fmt.Errorf("stats HTTP %d", response.StatusCode)
	}
	var stats dockerStats
	if err := json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(&stats); err != nil {
		return dockerStats{}, err
	}
	return stats, nil
}

func dockerStatsMetrics(stats dockerStats) map[string]float64 {
	usage := stats.MemoryStats.Usage
	if cache := stats.MemoryStats.Stats["cache"]; cache < usage {
		usage -= cache
	}
	metrics := map[string]float64{"memory_usage_bytes": float64(usage), "memory_limit_bytes": float64(stats.MemoryStats.Limit), "pids": float64(stats.PidsStats.Current)}
	cpuDelta := stats.CPUStats.CPUUsage.TotalUsage - stats.PreCPUStats.CPUUsage.TotalUsage
	systemDelta := stats.CPUStats.SystemCPUUsage - stats.PreCPUStats.SystemCPUUsage
	processors := stats.CPUStats.OnlineCPUs
	if processors == 0 {
		processors = 1
	}
	if cpuDelta > 0 && systemDelta > 0 {
		metrics["cpu_percent"] = float64(cpuDelta) / float64(systemDelta) * float64(processors) * 100
	}
	for _, network := range stats.Networks {
		metrics["network_receive_bytes"] += float64(network.RXBytes)
		metrics["network_transmit_bytes"] += float64(network.TXBytes)
	}
	return metrics
}

func firstDockerName(container dockerContainer) string {
	if len(container.Names) > 0 && container.Names[0] != "" {
		return container.Names[0]
	}
	if len(container.ID) > 12 {
		return container.ID[:12]
	}
	return container.ID
}

func collectDocker(ctx context.Context, metrics map[string]any) {
	if _, err := os.Stat("/var/run/docker.sock"); err != nil {
		return
	}
	containers, err := listDockerContainers(ctx, dockerHTTPClient("/var/run/docker.sock", 5), 500)
	if err != nil {
		return
	}
	running, unhealthy := 0, 0
	for _, container := range containers {
		if strings.EqualFold(container.State, "running") {
			running++
		}
		if strings.Contains(strings.ToLower(container.Status), "unhealthy") {
			unhealthy++
		}
	}
	metrics["docker_containers"] = len(containers)
	metrics["docker_running"] = running
	metrics["docker_unhealthy"] = unhealthy
}
