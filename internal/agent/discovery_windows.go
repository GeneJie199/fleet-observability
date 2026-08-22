//go:build windows

package agent

import (
	"context"
	"encoding/csv"
	"errors"
	"net"
	"os/exec"
	"strconv"
	"strings"
)

func discoverListeners(ctx context.Context) ([]discoveredListener, error) {
	output, err := exec.CommandContext(ctx, "netstat", "-ano", "-p", "tcp").Output()
	if err != nil {
		return nil, errors.New("run netstat listener discovery")
	}
	seen := map[string]bool{}
	listeners := []discoveredListener{}
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 || !strings.EqualFold(fields[0], "TCP") || !strings.EqualFold(fields[3], "LISTENING") {
			continue
		}
		address, port, ok := parseWindowsEndpoint(fields[1])
		if !ok {
			continue
		}
		key := net.JoinHostPort(address, strconv.Itoa(port))
		if !seen[key] {
			seen[key] = true
			listeners = append(listeners, discoveredListener{Address: address, Port: port})
		}
	}
	return listeners, nil
}

func parseWindowsEndpoint(value string) (string, int, bool) {
	index := strings.LastIndex(value, ":")
	if index < 1 {
		return "", 0, false
	}
	port, err := strconv.Atoi(value[index+1:])
	if err != nil || port < 1 || port > 65535 {
		return "", 0, false
	}
	address := strings.Trim(value[:index], "[]")
	if address == "" {
		address = "::"
	}
	return address, port, true
}

func discoverProcesses(ctx context.Context, limit int) ([]discoveredProcess, error) {
	output, err := exec.CommandContext(ctx, "tasklist", "/fo", "csv", "/nh").Output()
	if err != nil {
		return nil, errors.New("run tasklist process discovery")
	}
	reader := csv.NewReader(strings.NewReader(string(output)))
	rows, err := reader.ReadAll()
	if err != nil {
		return nil, errors.New("parse tasklist process discovery")
	}
	processes := make([]discoveredProcess, 0, min(limit, len(rows)))
	for _, row := range rows {
		if len(processes) >= limit {
			break
		}
		if len(row) > 0 && strings.TrimSpace(row[0]) != "" {
			processes = append(processes, discoveredProcess{Command: strings.TrimSpace(row[0])})
		}
	}
	return processes, nil
}

func defaultDockerSocket() string { return "" }
