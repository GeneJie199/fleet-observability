package agent

import "runtime"

func collectRuntimeMetrics(metrics map[string]any) {
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	metrics["agent_memory_bytes"] = memory.Sys
	metrics["agent_goroutines"] = runtime.NumGoroutine()
}
