package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/GeneJie199/fleet-observability/internal/events"
)

type eventSpool struct {
	dir      string
	maxBytes int64
	mu       sync.Mutex
}

func openEventSpool(dir string, maxBytes int64) (*eventSpool, error) {
	if dir == "" {
		return nil, errors.New("agent spool directory is required")
	}
	if maxBytes <= 0 {
		maxBytes = 64 << 20
	}
	dir = filepath.Join(dir, "events")
	if err := os.MkdirAll(filepath.Join(dir, "queue"), 0o700); err != nil {
		return nil, err
	}
	return &eventSpool{dir: dir, maxBytes: maxBytes}, nil
}

func (spool *eventSpool) nextSequence() (uint64, error) {
	spool.mu.Lock()
	defer spool.mu.Unlock()
	path := filepath.Join(spool.dir, "sequence")
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return 0, err
	}
	current, _ := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
	current++
	return current, atomicSpoolWrite(path, []byte(strconv.FormatUint(current, 10)+"\n"))
}

func (spool *eventSpool) enqueue(batch events.Batch) error {
	spool.mu.Lock()
	defer spool.mu.Unlock()
	data, err := json.Marshal(batch)
	if err != nil {
		return err
	}
	used, err := directorySize(filepath.Join(spool.dir, "queue"))
	if err != nil {
		return err
	}
	if used+int64(len(data)) > spool.maxBytes {
		return fmt.Errorf("%w: event limit is %d bytes", errSpoolCapacity, spool.maxBytes)
	}
	return atomicSpoolWrite(filepath.Join(spool.dir, "queue", fmt.Sprintf("%020d.json", batch.Sequence)), append(data, '\n'))
}

func (spool *eventSpool) drain(ctx context.Context, send func(context.Context, events.Batch) error) (int, error) {
	spool.mu.Lock()
	defer spool.mu.Unlock()
	entries, err := os.ReadDir(filepath.Join(spool.dir, "queue"))
	if err != nil {
		return 0, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	sent := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		select {
		case <-ctx.Done():
			return sent, ctx.Err()
		default:
		}
		path := filepath.Join(spool.dir, "queue", entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return sent, err
		}
		var batch events.Batch
		if err := json.Unmarshal(data, &batch); err != nil {
			return sent, fmt.Errorf("invalid queued events %s: %w", entry.Name(), err)
		}
		if err := send(ctx, batch); err != nil {
			return sent, err
		}
		if err := os.Remove(path); err != nil {
			return sent, err
		}
		sent++
	}
	return sent, nil
}

func (spool *eventSpool) pending() (int, int64, error) {
	spool.mu.Lock()
	defer spool.mu.Unlock()
	entries, err := os.ReadDir(filepath.Join(spool.dir, "queue"))
	if err != nil {
		return 0, 0, err
	}
	count := 0
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
			count++
		}
	}
	size, err := directorySize(filepath.Join(spool.dir, "queue"))
	return count, size, err
}

func directorySize(dir string) (int64, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, err
	}
	var size int64
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return 0, err
		}
		size += info.Size()
	}
	return size, nil
}
