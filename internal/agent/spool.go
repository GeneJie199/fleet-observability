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

	"github.com/GeneJie199/fleet-observability/internal/telemetry"
)

type spool struct {
	dir      string
	maxBytes int64
	mu       sync.Mutex
}

var errSpoolCapacity = errors.New("agent spool capacity exceeded")

func openSpool(dir string, maxBytes int64) (*spool, error) {
	if dir == "" {
		return nil, errors.New("agent spool directory is required")
	}
	if maxBytes <= 0 {
		maxBytes = 64 << 20
	}
	queue := filepath.Join(dir, "queue")
	if err := os.MkdirAll(queue, 0o700); err != nil {
		return nil, err
	}
	return &spool{dir: dir, maxBytes: maxBytes}, nil
}

func (s *spool) nextSequence() (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := filepath.Join(s.dir, "sequence")
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return 0, err
	}
	current, _ := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
	current++
	if err := atomicSpoolWrite(path, []byte(strconv.FormatUint(current, 10)+"\n")); err != nil {
		return 0, err
	}
	return current, nil
}

func (s *spool) enqueue(batch telemetry.Batch) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := json.Marshal(batch)
	if err != nil {
		return err
	}
	used, err := s.size()
	if err != nil {
		return err
	}
	if used+int64(len(data)) > s.maxBytes {
		return fmt.Errorf("%w: telemetry limit is %d bytes", errSpoolCapacity, s.maxBytes)
	}
	name := fmt.Sprintf("%020d.json", batch.Sequence)
	return atomicSpoolWrite(filepath.Join(s.dir, "queue", name), append(data, '\n'))
}

func (s *spool) drain(ctx context.Context, send func(context.Context, telemetry.Batch) error) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(filepath.Join(s.dir, "queue"))
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
		path := filepath.Join(s.dir, "queue", entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return sent, err
		}
		var batch telemetry.Batch
		if err := json.Unmarshal(data, &batch); err != nil {
			return sent, fmt.Errorf("invalid queued telemetry %s: %w", entry.Name(), err)
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

func (s *spool) pending() (int, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(filepath.Join(s.dir, "queue"))
	if err != nil {
		return 0, 0, err
	}
	count := 0
	var size int64
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return 0, 0, err
		}
		count++
		size += info.Size()
	}
	return count, size, nil
}

func (s *spool) size() (int64, error) {
	entries, err := os.ReadDir(filepath.Join(s.dir, "queue"))
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

func atomicSpoolWrite(path string, data []byte) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".spool-*.tmp")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err = temporary.Chmod(0o600); err == nil {
		_, err = temporary.Write(data)
	}
	if err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, path)
}
