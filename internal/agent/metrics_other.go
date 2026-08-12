//go:build !linux

package agent

import (
	"context"
	"time"
)

func CollectMetrics(_ context.Context) (map[string]any, error) {
	metrics := map[string]any{"collected_at": time.Now().UTC().Format(time.RFC3339)}
	collectRuntimeMetrics(metrics)
	return metrics, nil
}
