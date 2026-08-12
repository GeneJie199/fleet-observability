//go:build linux

package agent

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func CollectMetrics(ctx context.Context) (map[string]any, error) {
	a, err := cpuSample()
	if err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(200 * time.Millisecond):
	}
	b, err := cpuSample()
	if err != nil {
		return nil, err
	}
	total := b.total - a.total
	idle := b.idle - a.idle
	cpu := 0.0
	if total > 0 {
		cpu = 100 * float64(total-idle) / float64(total)
	}
	m := map[string]any{"cpu_percent": round(cpu), "collected_at": time.Now().UTC().Format(time.RFC3339)}
	collectRuntimeMetrics(m)
	collectMemory(m)
	collectDisk(m)
	collectLoad(m)
	collectNetwork(m)
	collectDocker(ctx, m)
	return m, nil
}

type cpuStat struct{ total, idle uint64 }

func cpuSample() (cpuStat, error) {
	b, e := os.ReadFile("/proc/stat")
	if e != nil {
		return cpuStat{}, e
	}
	line := strings.SplitN(string(b), "\n", 2)[0]
	f := strings.Fields(line)
	if len(f) < 5 {
		return cpuStat{}, fmt.Errorf("invalid /proc/stat")
	}
	var s cpuStat
	for i, x := range f[1:] {
		v, _ := strconv.ParseUint(x, 10, 64)
		s.total += v
		if i == 3 || i == 4 {
			s.idle += v
		}
	}
	return s, nil
}
func collectMemory(m map[string]any) {
	b, e := os.ReadFile("/proc/meminfo")
	if e != nil {
		return
	}
	vals := map[string]uint64{}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 {
			v, _ := strconv.ParseUint(f[1], 10, 64)
			vals[strings.TrimSuffix(f[0], ":")] = v * 1024
		}
	}
	total, avail := vals["MemTotal"], vals["MemAvailable"]
	m["memory_total_bytes"] = total
	m["memory_available_bytes"] = avail
	if total > 0 {
		m["memory_percent"] = round(100 * float64(total-avail) / float64(total))
	}
}
func collectDisk(m map[string]any) {
	var s syscall.Statfs_t
	if syscall.Statfs("/", &s) != nil {
		return
	}
	total := s.Blocks * uint64(s.Bsize)
	free := s.Bavail * uint64(s.Bsize)
	m["disk_total_bytes"] = total
	m["disk_free_bytes"] = free
	if total > 0 {
		m["disk_percent"] = round(100 * float64(total-free) / float64(total))
	}
}
func collectLoad(m map[string]any) {
	b, e := os.ReadFile("/proc/loadavg")
	if e == nil {
		f := strings.Fields(string(b))
		if len(f) >= 3 {
			m["load_1"], _ = strconv.ParseFloat(f[0], 64)
			m["load_5"], _ = strconv.ParseFloat(f[1], 64)
			m["load_15"], _ = strconv.ParseFloat(f[2], 64)
		}
	}
	b, e = os.ReadFile("/proc/uptime")
	if e == nil {
		f := strings.Fields(string(b))
		if len(f) > 0 {
			m["uptime_seconds"], _ = strconv.ParseFloat(f[0], 64)
		}
	}
}
func collectNetwork(m map[string]any) {
	f, e := os.Open("/proc/net/dev")
	if e != nil {
		return
	}
	defer f.Close()
	var rx, tx uint64
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if !strings.Contains(line, ":") {
			continue
		}
		p := strings.Fields(strings.Replace(line, ":", " ", 1))
		if len(p) >= 10 {
			a, _ := strconv.ParseUint(p[1], 10, 64)
			z, _ := strconv.ParseUint(p[9], 10, 64)
			rx += a
			tx += z
		}
	}
	m["network_receive_bytes_total"] = rx
	m["network_transmit_bytes_total"] = tx
}
func round(v float64) float64 { x, _ := strconv.ParseFloat(fmt.Sprintf("%.2f", v), 64); return x }
