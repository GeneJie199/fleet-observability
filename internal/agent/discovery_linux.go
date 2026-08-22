//go:build linux

package agent

import (
	"bufio"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func discoverListeners(ctx context.Context) ([]discoveredListener, error) {
	seen := map[string]bool{}
	out := []discoveredListener{}
	var joined error
	for _, path := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		file, err := os.Open(path)
		if err != nil {
			joined = errors.Join(joined, err)
			continue
		}
		scanner := bufio.NewScanner(file)
		first := true
		for scanner.Scan() {
			if first {
				first = false
				continue
			}
			select {
			case <-ctx.Done():
				_ = file.Close()
				return nil, ctx.Err()
			default:
			}
			fields := strings.Fields(scanner.Text())
			if len(fields) < 4 || fields[3] != "0A" {
				continue
			}
			addressHex, portHex, ok := strings.Cut(fields[1], ":")
			if !ok {
				continue
			}
			port, err := strconv.ParseInt(portHex, 16, 32)
			if err != nil {
				continue
			}
			address := decodeProcAddress(addressHex)
			key := fmt.Sprintf("%s:%d", address, port)
			if !seen[key] {
				seen[key] = true
				out = append(out, discoveredListener{Address: address, Port: int(port)})
			}
		}
		joined = errors.Join(joined, scanner.Err())
		_ = file.Close()
	}
	if len(out) > 0 {
		joined = nil
	}
	return out, joined
}

func decodeProcAddress(value string) string {
	b, err := hex.DecodeString(value)
	if err != nil {
		return "*"
	}
	if len(b) == 4 {
		return fmt.Sprintf("%d.%d.%d.%d", b[3], b[2], b[1], b[0])
	}
	if len(b) == 16 {
		allZero := true
		for _, value := range b {
			if value != 0 {
				allZero = false
				break
			}
		}
		if allZero {
			return "::"
		}
		return "ipv6"
	}
	return "*"
}

func discoverProcesses(ctx context.Context, limit int) ([]discoveredProcess, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	out := []discoveredProcess{}
	for _, entry := range entries {
		if len(out) >= limit {
			break
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}
		b, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "cmdline"))
		if err != nil || len(b) == 0 {
			continue
		}
		command := strings.TrimSpace(strings.ReplaceAll(string(b), "\x00", " "))
		if len(command) > 500 {
			command = command[:500]
		}
		out = append(out, discoveredProcess{Command: command})
	}
	return out, nil
}

func defaultDockerSocket() string { return "/var/run/docker.sock" }
