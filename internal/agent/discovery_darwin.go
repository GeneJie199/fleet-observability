//go:build darwin

package agent

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

func discoverListeners(ctx context.Context) ([]discoveredListener, error) {
	output, err := exec.CommandContext(ctx, "lsof", "-nP", "-iTCP", "-sTCP:LISTEN").Output()
	if err != nil {
		return nil, errors.New("run lsof listener discovery")
	}
	seen := map[string]bool{}
	listeners := []discoveredListener{}
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		for _, field := range fields {
			address, port, ok := parseDarwinEndpoint(field)
			if !ok {
				continue
			}
			key := address + ":" + strconv.Itoa(port)
			if !seen[key] {
				seen[key] = true
				listeners = append(listeners, discoveredListener{Address: address, Port: port})
			}
			break
		}
	}
	return listeners, nil
}

func parseDarwinEndpoint(value string) (string, int, bool) {
	index := strings.LastIndex(value, ":")
	if index < 1 {
		return "", 0, false
	}
	port, err := strconv.Atoi(value[index+1:])
	if err != nil || port < 1 || port > 65535 {
		return "", 0, false
	}
	address := strings.Trim(value[:index], "[]")
	if address == "*" {
		address = "0.0.0.0"
	}
	return address, port, true
}

func discoverProcesses(ctx context.Context, limit int) ([]discoveredProcess, error) {
	output, err := exec.CommandContext(ctx, "ps", "-axo", "comm=").Output()
	if err != nil {
		return nil, errors.New("run ps process discovery")
	}
	processes := []discoveredProcess{}
	for _, line := range strings.Split(string(output), "\n") {
		if len(processes) >= limit {
			break
		}
		name := filepath.Base(strings.TrimSpace(line))
		if name != "" && name != "." {
			processes = append(processes, discoveredProcess{Command: name})
		}
	}
	return processes, nil
}

func defaultDockerSocket() string { return "/var/run/docker.sock" }
