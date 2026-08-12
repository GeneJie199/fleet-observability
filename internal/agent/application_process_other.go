//go:build !linux

package agent

import (
	"context"
	"errors"
	"time"
)

func collectProcessTarget(_ context.Context, target ProcessTarget, _ time.Time, _ map[string]string) applicationResult {
	return applicationResult{name: target.Name, kind: "process", required: target.Required, err: errors.New("native process collection is currently supported on Linux agents")}
}
