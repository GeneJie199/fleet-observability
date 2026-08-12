package events

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type Options struct {
	Retention time.Duration
	MaxEvents int
	MaxBatch  int
}

type Record struct {
	NodeID string `json:"node_id"`
	Source string `json:"source"`
	Entry
}

type storedRecord struct {
	Record
	Sequence uint64 `json:"sequence,omitempty"`
}

type SourceStatus struct {
	NodeID       string `json:"node_id"`
	Source       string `json:"source"`
	LastSeenAt   string `json:"last_seen_at"`
	LastSequence uint64 `json:"last_sequence"`
	Events       int    `json:"events"`
}

type Query struct {
	NodeID, Source, Kind, Severity, Service, Search string
	StartMS, EndMS, BeforeMS                        int64
	Limit                                           int
	NodeIDs                                         map[string]bool
}

type QueryResult struct {
	Events       []Record `json:"events"`
	NextBeforeMS int64    `json:"next_before_ms,omitempty"`
	TotalMatched int      `json:"total_matched"`
}

type Catalog struct {
	Total      int            `json:"total"`
	ByKind     map[string]int `json:"by_kind"`
	BySeverity map[string]int `json:"by_severity"`
	BySource   map[string]int `json:"by_source"`
	FirstMS    int64          `json:"first_ms,omitempty"`
	LastMS     int64          `json:"last_ms,omitempty"`
}

type Store struct {
	dir       string
	retention time.Duration
	maxEvents int
	maxBatch  int
	mu        sync.RWMutex
	events    []Record
	sequence  map[string]uint64
	sources   map[string]SourceStatus
	lastPrune time.Time
}

func OpenStore(dir string, options Options) (*Store, error) {
	if dir == "" {
		return nil, errors.New("event store directory is required")
	}
	if options.Retention <= 0 {
		options.Retention = 30 * 24 * time.Hour
	}
	if options.MaxEvents <= 0 {
		options.MaxEvents = 500000
	}
	if options.MaxBatch <= 0 || options.MaxBatch > MaxBatchEvents {
		options.MaxBatch = MaxBatchEvents
	}
	if err := os.MkdirAll(filepath.Join(dir, "segments"), 0o750); err != nil {
		return nil, err
	}
	store := &Store{dir: dir, retention: options.Retention, maxEvents: options.MaxEvents, maxBatch: options.MaxBatch, sequence: map[string]uint64{}, sources: map[string]SourceStatus{}, lastPrune: time.Now().UTC()}
	_ = store.readSequences()
	if err := store.load(time.Now().UTC()); err != nil {
		return nil, err
	}
	return store, nil
}

func (store *Store) Append(batch Batch) (bool, error) {
	now := time.Now().UTC()
	batch.Normalize(now)
	if err := ValidateBatch(batch, now, store.maxBatch); err != nil {
		return false, err
	}
	key := batch.NodeID + "\x00" + batch.Source
	store.mu.Lock()
	defer store.mu.Unlock()
	if now.Sub(store.lastPrune) >= time.Hour {
		if err := store.pruneLocked(now); err != nil {
			return false, err
		}
	}
	if batch.Sequence > 0 && batch.Sequence <= store.sequence[key] {
		return true, nil
	}
	if len(store.events)+len(batch.Events) > store.maxEvents {
		return false, fmt.Errorf("event store limit exceeded: %d retained events", store.maxEvents)
	}
	if err := store.appendSegments(batch); err != nil {
		return false, err
	}
	for _, entry := range batch.Events {
		store.events = append(store.events, Record{NodeID: batch.NodeID, Source: batch.Source, Entry: entry})
	}
	sort.Slice(store.events, func(i, j int) bool { return store.events[i].TimestampMS < store.events[j].TimestampMS })
	if batch.Sequence > 0 {
		store.sequence[key] = batch.Sequence
		if err := store.writeSequences(); err != nil {
			return false, err
		}
	}
	store.refreshSource(batch.NodeID, batch.Source, batch.Sequence)
	return false, nil
}

func (store *Store) Query(query Query) QueryResult {
	if query.EndMS == 0 {
		query.EndMS = time.Now().UnixMilli()
	}
	if query.StartMS == 0 {
		query.StartMS = query.EndMS - int64(24*time.Hour/time.Millisecond)
	}
	if query.BeforeMS == 0 || query.BeforeMS > query.EndMS {
		query.BeforeMS = query.EndMS
	}
	if query.Limit <= 0 {
		query.Limit = 200
	}
	if query.Limit > 1000 {
		query.Limit = 1000
	}
	search := strings.ToLower(strings.TrimSpace(query.Search))
	result := QueryResult{Events: []Record{}}
	store.mu.RLock()
	defer store.mu.RUnlock()
	for index := len(store.events) - 1; index >= 0; index-- {
		record := store.events[index]
		if record.TimestampMS < query.StartMS {
			break
		}
		if record.TimestampMS > query.EndMS || record.TimestampMS >= query.BeforeMS || (query.NodeID != "" && record.NodeID != query.NodeID) || (query.NodeIDs != nil && !query.NodeIDs[record.NodeID]) || (query.Source != "" && record.Source != query.Source) || (query.Kind != "" && record.Kind != query.Kind) || (query.Severity != "" && record.Severity != query.Severity) || (query.Service != "" && record.Service != query.Service) {
			continue
		}
		if search != "" && !matchesSearch(strings.ToLower(record.Message+" "+record.Service+" "+attributesText(record.Attributes)), search) {
			continue
		}
		result.TotalMatched++
		if len(result.Events) < query.Limit {
			result.Events = append(result.Events, record)
			result.NextBeforeMS = record.TimestampMS
		}
	}
	if result.TotalMatched <= len(result.Events) {
		result.NextBeforeMS = 0
	}
	return result
}

func (store *Store) Catalog() Catalog {
	return store.CatalogForNodes(nil)
}

func (store *Store) CatalogForNodes(nodes map[string]bool) Catalog {
	catalog := Catalog{ByKind: map[string]int{}, BySeverity: map[string]int{}, BySource: map[string]int{}}
	store.mu.RLock()
	defer store.mu.RUnlock()
	for _, record := range store.events {
		if nodes != nil && !nodes[record.NodeID] {
			continue
		}
		catalog.Total++
		catalog.ByKind[record.Kind]++
		catalog.BySeverity[record.Severity]++
		catalog.BySource[record.Source]++
		if catalog.FirstMS == 0 || record.TimestampMS < catalog.FirstMS {
			catalog.FirstMS = record.TimestampMS
		}
		if record.TimestampMS > catalog.LastMS {
			catalog.LastMS = record.TimestampMS
		}
	}
	return catalog
}

func (store *Store) Sources() []SourceStatus {
	return store.SourcesForNodes(nil)
}

func (store *Store) SourcesForNodes(nodes map[string]bool) []SourceStatus {
	store.mu.RLock()
	defer store.mu.RUnlock()
	out := make([]SourceStatus, 0, len(store.sources))
	for _, source := range store.sources {
		if nodes != nil && !nodes[source.NodeID] {
			continue
		}
		out = append(out, source)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].NodeID < out[j].NodeID || out[i].NodeID == out[j].NodeID && out[i].Source < out[j].Source
	})
	return out
}

func (store *Store) Prune(now time.Time) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.pruneLocked(now)
}

func (store *Store) pruneLocked(now time.Time) error {
	cutoff := now.Add(-store.retention).UnixMilli()
	index := sort.Search(len(store.events), func(i int) bool { return store.events[i].TimestampMS >= cutoff })
	store.events = append([]Record(nil), store.events[index:]...)
	entries, _ := os.ReadDir(filepath.Join(store.dir, "segments"))
	for _, entry := range entries {
		day, err := time.Parse("20060102", strings.TrimSuffix(entry.Name(), ".ndjson"))
		if err == nil && day.Add(24*time.Hour).Before(now.Add(-store.retention)) {
			if err := os.Remove(filepath.Join(store.dir, "segments", entry.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	store.rebuildSources()
	store.lastPrune = now
	return nil
}

func (store *Store) appendSegments(batch Batch) error {
	byDay := map[string][]Record{}
	for _, entry := range batch.Events {
		day := time.UnixMilli(entry.TimestampMS).UTC().Format("20060102")
		byDay[day] = append(byDay[day], Record{NodeID: batch.NodeID, Source: batch.Source, Entry: entry})
	}
	for day, records := range byDay {
		file, err := os.OpenFile(filepath.Join(store.dir, "segments", day+".ndjson"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		encoder := json.NewEncoder(file)
		for _, record := range records {
			if err := encoder.Encode(storedRecord{Record: record, Sequence: batch.Sequence}); err != nil {
				_ = file.Close()
				return err
			}
		}
		if err := file.Sync(); err != nil {
			_ = file.Close()
			return err
		}
		if err := file.Close(); err != nil {
			return err
		}
	}
	return nil
}

func (store *Store) load(now time.Time) error {
	cutoff := now.Add(-store.retention).UnixMilli()
	entries, err := os.ReadDir(filepath.Join(store.dir, "segments"))
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".ndjson") {
			continue
		}
		file, err := os.Open(filepath.Join(store.dir, "segments", entry.Name()))
		if err != nil {
			return err
		}
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 64<<10), 2<<20)
		for scanner.Scan() {
			var stored storedRecord
			if json.Unmarshal(scanner.Bytes(), &stored) == nil && stored.TimestampMS >= cutoff {
				store.events = append(store.events, stored.Record)
				key := stored.NodeID + "\x00" + stored.Source
				if stored.Sequence > store.sequence[key] {
					store.sequence[key] = stored.Sequence
				}
			}
		}
		scanErr := scanner.Err()
		_ = file.Close()
		if scanErr != nil {
			return scanErr
		}
	}
	sort.Slice(store.events, func(i, j int) bool { return store.events[i].TimestampMS < store.events[j].TimestampMS })
	store.rebuildSources()
	return nil
}

func (store *Store) refreshSource(nodeID, source string, sequence uint64) {
	key := nodeID + "\x00" + source
	status := SourceStatus{NodeID: nodeID, Source: source, LastSequence: sequence}
	for _, record := range store.events {
		if record.NodeID == nodeID && record.Source == source {
			status.Events++
			if seen := time.UnixMilli(record.TimestampMS).UTC().Format(time.RFC3339Nano); seen > status.LastSeenAt {
				status.LastSeenAt = seen
			}
		}
	}
	store.sources[key] = status
}

func (store *Store) rebuildSources() {
	store.sources = map[string]SourceStatus{}
	keys := map[string][2]string{}
	for _, record := range store.events {
		keys[record.NodeID+"\x00"+record.Source] = [2]string{record.NodeID, record.Source}
	}
	for key, identity := range keys {
		store.refreshSource(identity[0], identity[1], store.sequence[key])
	}
}

func (store *Store) readSequences() error {
	data, err := os.ReadFile(filepath.Join(store.dir, "sequences.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(data, &store.sequence)
}

func (store *Store) writeSequences() error {
	data, err := json.MarshalIndent(store.sequence, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(store.dir, "sequences.json"), append(data, '\n'))
}

func atomicWrite(path string, data []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".events-*.tmp")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if err = file.Chmod(0o600); err == nil {
		_, err = file.Write(data)
	}
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, path)
}

func attributesText(attributes map[string]string) string {
	parts := make([]string, 0, len(attributes)*2)
	for key, value := range attributes {
		parts = append(parts, key, value)
	}
	return strings.Join(parts, " ")
}

func matchesSearch(text, query string) bool {
	for _, term := range strings.Fields(query) {
		if !strings.Contains(text, term) {
			return false
		}
	}
	return true
}
