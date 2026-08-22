//go:build !linux && !windows && !darwin

package agent

import (
	"context"
	"errors"
)

func discoverListeners(context.Context) ([]discoveredListener, error) {
	return []discoveredListener{}, errors.New("native listener discovery is currently available on Linux")
}

func discoverProcesses(context.Context, int) ([]discoveredProcess, error) {
	return []discoveredProcess{}, errors.New("native process discovery is currently available on Linux")
}

func defaultDockerSocket() string { return "" }
