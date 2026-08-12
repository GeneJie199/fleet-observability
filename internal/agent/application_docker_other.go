//go:build !linux

package agent

import (
	"context"
	"errors"
	"time"
)

func collectDockerTarget(_ context.Context, target DockerTarget, _ time.Time, _ map[string]string) applicationResult {
	return applicationResult{name: target.Name, kind: "docker", required: target.Required, err: errors.New("native Docker collection is currently supported on Linux agents")}
}
