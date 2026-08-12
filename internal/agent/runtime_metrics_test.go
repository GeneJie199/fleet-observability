package agent

import "testing"

func TestCollectRuntimeMetricsProvidesCrossPlatformSelfObservability(t *testing.T) {
	metrics := map[string]any{}
	collectRuntimeMetrics(metrics)

	memory, memoryOK := metrics["agent_memory_bytes"].(uint64)
	if !memoryOK || memory == 0 {
		t.Fatalf("agent_memory_bytes = %#v", metrics["agent_memory_bytes"])
	}
	goroutines, goroutinesOK := metrics["agent_goroutines"].(int)
	if !goroutinesOK || goroutines < 1 {
		t.Fatalf("agent_goroutines = %#v", metrics["agent_goroutines"])
	}
}
