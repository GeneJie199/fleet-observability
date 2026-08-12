//go:build linux

package agent

import "testing"

func TestDockerStatsMetricsCalculatesCPUAndExcludesCache(t *testing.T) {
	var stats dockerStats
	stats.CPUStats.CPUUsage.TotalUsage = 300
	stats.PreCPUStats.CPUUsage.TotalUsage = 100
	stats.CPUStats.SystemCPUUsage = 1000
	stats.PreCPUStats.SystemCPUUsage = 500
	stats.CPUStats.OnlineCPUs = 2
	stats.MemoryStats.Usage = 4096
	stats.MemoryStats.Limit = 8192
	stats.MemoryStats.Stats = map[string]uint64{"cache": 1024}
	stats.PidsStats.Current = 7
	stats.Networks = map[string]struct {
		RXBytes uint64 `json:"rx_bytes"`
		TXBytes uint64 `json:"tx_bytes"`
	}{"eth0": {RXBytes: 10, TXBytes: 20}, "eth1": {RXBytes: 5, TXBytes: 6}}
	metrics := dockerStatsMetrics(stats)
	if metrics["cpu_percent"] != 80 || metrics["memory_usage_bytes"] != 3072 || metrics["network_receive_bytes"] != 15 || metrics["network_transmit_bytes"] != 26 || metrics["pids"] != 7 {
		t.Fatalf("metrics=%+v", metrics)
	}
}
