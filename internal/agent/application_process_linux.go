//go:build linux

package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func collectProcessTarget(ctx context.Context, target ProcessTarget, observedAt time.Time, baseLabels map[string]string) applicationResult {
	result := applicationResult{name: target.Name, kind: "process", required: target.Required}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		result.err = errors.New("read process table failed")
		return result
	}
	match := strings.ToLower(strings.TrimSpace(target.Match))
	metrics := map[string]float64{"processes": 0, "memory_rss_bytes": 0, "threads": 0, "cpu_seconds": 0}
	clockTicks := 100.0
	for _, entry := range entries {
		select {
		case <-ctx.Done():
			result.err = ctx.Err()
			return result
		default:
		}
		if !entry.IsDir() {
			continue
		}
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}
		root := filepath.Join("/proc", entry.Name())
		command, _ := os.ReadFile(filepath.Join(root, "cmdline"))
		comm, _ := os.ReadFile(filepath.Join(root, "comm"))
		identity := strings.ToLower(strings.TrimSpace(string(comm)) + " " + strings.ReplaceAll(string(command), "\x00", " "))
		if !strings.Contains(identity, match) {
			continue
		}
		stat, err := os.ReadFile(filepath.Join(root, "stat"))
		if err != nil {
			continue
		}
		closeParen := strings.LastIndexByte(string(stat), ')')
		if closeParen < 0 {
			continue
		}
		fields := strings.Fields(string(stat)[closeParen+1:])
		if len(fields) < 22 {
			continue
		}
		userTicks, _ := strconv.ParseFloat(fields[11], 64)
		systemTicks, _ := strconv.ParseFloat(fields[12], 64)
		threads, _ := strconv.ParseFloat(fields[17], 64)
		rssPages, _ := strconv.ParseFloat(fields[21], 64)
		metrics["processes"]++
		metrics["threads"] += threads
		metrics["memory_rss_bytes"] += rssPages * float64(os.Getpagesize())
		metrics["cpu_seconds"] += (userTicks + systemTicks) / clockTicks
	}
	if metrics["processes"] == 0 {
		result.err = errors.New("no matching process is running")
		return result
	}
	labels := applicationLabels(baseLabels, target.Labels, target.Name, "process", target.Required)
	result.points = pointsFromMetrics("process_", metrics, observedAt, labels, map[string]bool{"cpu_seconds": true})
	result.summary = map[string]any{"processes": metrics["processes"], "memory_rss_bytes": metrics["memory_rss_bytes"], "threads": metrics["threads"]}
	return result
}
