package telemetry

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type StoreOptions struct {
	Retention  time.Duration
	MaxSeries  int
	MaxPoints  int
	MaxSamples int
}

type Store struct {
	dir        string
	retention  time.Duration
	maxSeries  int
	maxPoints  int
	maxSamples int
	samples    int
	mu         sync.RWMutex
	series     map[string]*seriesData
	sequence   map[string]uint64
	sources    map[string]SourceStatus
	lastPrune  time.Time
}

type seriesData struct {
	Metric string
	NodeID string
	Source string
	Labels map[string]string
	Kind   MetricKind
	Unit   string
	Points []Point
}

type storedPoint struct {
	NodeID   string `json:"node_id"`
	Source   string `json:"source"`
	Sequence uint64 `json:"sequence,omitempty"`
	Point
}

type SourceStatus struct {
	NodeID       string `json:"node_id"`
	Source       string `json:"source"`
	LastSeenAt   string `json:"last_seen_at"`
	LastSequence uint64 `json:"last_sequence"`
	Series       int    `json:"series"`
}

type CatalogItem struct {
	Metric      string     `json:"metric"`
	Kind        MetricKind `json:"kind"`
	Unit        string     `json:"unit,omitempty"`
	Series      int        `json:"series"`
	Samples     int        `json:"samples"`
	Nodes       []string   `json:"nodes"`
	Sources     []string   `json:"sources"`
	FirstSeenMS int64      `json:"first_seen_ms"`
	LastSeenMS  int64      `json:"last_seen_ms"`
}

type Query struct {
	Metric    string
	NodeID    string
	Source    string
	Labels    map[string]string
	StartMS   int64
	EndMS     int64
	Step      time.Duration
	Aggregate string
	NodeIDs   map[string]bool
}

type SeriesResult struct {
	Metric string            `json:"metric"`
	NodeID string            `json:"node_id"`
	Source string            `json:"source"`
	Labels map[string]string `json:"labels,omitempty"`
	Kind   MetricKind        `json:"kind"`
	Unit   string            `json:"unit,omitempty"`
	Points []Point           `json:"points"`
}

type QueryResult struct {
	StartMS int64          `json:"start_ms"`
	EndMS   int64          `json:"end_ms"`
	StepMS  int64          `json:"step_ms"`
	Series  []SeriesResult `json:"series"`
}

func OpenStore(dir string, options StoreOptions) (*Store, error) {
	if dir == "" {
		return nil, errors.New("telemetry directory is required")
	}
	if options.Retention <= 0 {
		options.Retention = 30 * 24 * time.Hour
	}
	if options.MaxSeries <= 0 {
		options.MaxSeries = 50000
	}
	if options.MaxPoints <= 0 || options.MaxPoints > MaxBatchPoints {
		options.MaxPoints = MaxBatchPoints
	}
	if options.MaxSamples <= 0 {
		options.MaxSamples = 5000000
	}
	if err := os.MkdirAll(filepath.Join(dir, "segments"), 0o750); err != nil {
		return nil, err
	}
	store := &Store{dir: dir, retention: options.Retention, maxSeries: options.MaxSeries, maxPoints: options.MaxPoints, maxSamples: options.MaxSamples, series: map[string]*seriesData{}, sequence: map[string]uint64{}, sources: map[string]SourceStatus{}, lastPrune: time.Now().UTC()}
	_ = store.readSequences()
	if err := store.loadSegments(time.Now()); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *Store) Append(batch Batch) (bool, error) {
	now := time.Now().UTC()
	batch.Normalize(now)
	if err := ValidateBatch(batch, now, s.maxPoints); err != nil {
		return false, err
	}
	sequenceKey := batch.NodeID + "\x00" + batch.Source
	s.mu.Lock()
	defer s.mu.Unlock()
	if now.Sub(s.lastPrune) >= time.Hour {
		if err := s.pruneLocked(now); err != nil {
			return false, err
		}
	}
	if batch.Sequence > 0 && batch.Sequence <= s.sequence[sequenceKey] {
		return true, nil
	}
	newKeys := map[string]bool{}
	for _, point := range batch.Points {
		key := SeriesKey(batch.NodeID, batch.Source, point)
		if _, exists := s.series[key]; !exists {
			newKeys[key] = true
		}
	}
	if len(s.series)+len(newKeys) > s.maxSeries {
		return false, fmt.Errorf("series cardinality limit exceeded: %d", s.maxSeries)
	}
	newSamples := map[string]map[int64]bool{}
	for _, point := range batch.Points {
		key := SeriesKey(batch.NodeID, batch.Source, point)
		if s.hasPoint(key, point.TimestampMS) {
			continue
		}
		if newSamples[key] == nil {
			newSamples[key] = map[int64]bool{}
		}
		newSamples[key][point.TimestampMS] = true
	}
	newSampleCount := 0
	for _, timestamps := range newSamples {
		newSampleCount += len(timestamps)
	}
	if s.samples+newSampleCount > s.maxSamples {
		return false, fmt.Errorf("retained sample limit exceeded: %d", s.maxSamples)
	}
	if err := s.appendSegments(batch); err != nil {
		return false, err
	}
	for _, point := range batch.Points {
		s.addPoint(batch.NodeID, batch.Source, point)
	}
	if batch.Sequence > 0 {
		s.sequence[sequenceKey] = batch.Sequence
		if err := s.writeSequences(); err != nil {
			return false, err
		}
	}
	s.refreshSource(batch.NodeID, batch.Source, batch.Sequence, batch.SentAt)
	return false, nil
}

func (s *Store) Query(query Query) QueryResult {
	if query.EndMS == 0 {
		query.EndMS = time.Now().UnixMilli()
	}
	if query.StartMS == 0 {
		query.StartMS = query.EndMS - int64(time.Hour/time.Millisecond)
	}
	if query.Step <= 0 {
		query.Step = 15 * time.Second
	}
	if query.Aggregate == "" {
		query.Aggregate = "avg"
	}
	result := QueryResult{StartMS: query.StartMS, EndMS: query.EndMS, StepMS: query.Step.Milliseconds(), Series: []SeriesResult{}}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, series := range s.series {
		if series.Metric != query.Metric || (query.NodeID != "" && series.NodeID != query.NodeID) || (query.NodeIDs != nil && !query.NodeIDs[series.NodeID]) || (query.Source != "" && series.Source != query.Source) || !labelsMatch(series.Labels, query.Labels) {
			continue
		}
		points := rangePoints(series.Points, query.StartMS, query.EndMS)
		if len(points) == 0 {
			continue
		}
		points = aggregatePoints(points, query.StartMS, query.Step.Milliseconds(), query.Aggregate, series.Kind)
		result.Series = append(result.Series, SeriesResult{Metric: series.Metric, NodeID: series.NodeID, Source: series.Source, Labels: cloneLabels(series.Labels), Kind: series.Kind, Unit: series.Unit, Points: points})
	}
	sort.Slice(result.Series, func(i, j int) bool {
		if result.Series[i].NodeID != result.Series[j].NodeID {
			return result.Series[i].NodeID < result.Series[j].NodeID
		}
		return SeriesKey(result.Series[i].NodeID, result.Series[i].Source, Point{Metric: result.Series[i].Metric, Labels: result.Series[i].Labels}) < SeriesKey(result.Series[j].NodeID, result.Series[j].Source, Point{Metric: result.Series[j].Metric, Labels: result.Series[j].Labels})
	})
	return result
}

func (s *Store) Catalog() []CatalogItem {
	return s.CatalogForNodes(nil)
}

func (s *Store) CatalogForNodes(nodes map[string]bool) []CatalogItem {
	type aggregate struct {
		item    CatalogItem
		nodes   map[string]bool
		sources map[string]bool
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := map[string]*aggregate{}
	for _, series := range s.series {
		if nodes != nil && !nodes[series.NodeID] {
			continue
		}
		entry := items[series.Metric]
		if entry == nil {
			entry = &aggregate{item: CatalogItem{Metric: series.Metric, Kind: series.Kind, Unit: series.Unit}, nodes: map[string]bool{}, sources: map[string]bool{}}
			items[series.Metric] = entry
		}
		entry.item.Series++
		entry.item.Samples += len(series.Points)
		entry.nodes[series.NodeID] = true
		entry.sources[series.Source] = true
		if len(series.Points) > 0 {
			first, last := series.Points[0].TimestampMS, series.Points[len(series.Points)-1].TimestampMS
			if entry.item.FirstSeenMS == 0 || first < entry.item.FirstSeenMS {
				entry.item.FirstSeenMS = first
			}
			if last > entry.item.LastSeenMS {
				entry.item.LastSeenMS = last
			}
		}
	}
	out := make([]CatalogItem, 0, len(items))
	for _, entry := range items {
		entry.item.Nodes = sortedSet(entry.nodes)
		entry.item.Sources = sortedSet(entry.sources)
		out = append(out, entry.item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Metric < out[j].Metric })
	return out
}

func (s *Store) Sources() []SourceStatus {
	return s.SourcesForNodes(nil)
}

func (s *Store) SourcesForNodes(nodes map[string]bool) []SourceStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]SourceStatus, 0, len(s.sources))
	for _, source := range s.sources {
		if nodes != nil && !nodes[source.NodeID] {
			continue
		}
		out = append(out, source)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].NodeID != out[j].NodeID {
			return out[i].NodeID < out[j].NodeID
		}
		return out[i].Source < out[j].Source
	})
	return out
}

func (s *Store) HasSource(nodeID, source string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.sources[nodeID+"\x00"+source]
	return ok
}

func (s *Store) Prune(now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pruneLocked(now)
}

func (s *Store) pruneLocked(now time.Time) error {
	cutoff := now.Add(-s.retention).UnixMilli()
	for key, series := range s.series {
		index := sort.Search(len(series.Points), func(i int) bool { return series.Points[i].TimestampMS >= cutoff })
		s.samples -= index
		series.Points = append([]Point(nil), series.Points[index:]...)
		if len(series.Points) == 0 {
			delete(s.series, key)
		}
	}
	entries, _ := os.ReadDir(filepath.Join(s.dir, "segments"))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".ndjson") {
			continue
		}
		day, err := time.Parse("20060102", strings.TrimSuffix(entry.Name(), ".ndjson"))
		if err == nil && day.Add(24*time.Hour).Before(now.Add(-s.retention)) {
			if err := os.Remove(filepath.Join(s.dir, "segments", entry.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	s.lastPrune = now
	return nil
}

func (s *Store) hasPoint(key string, timestampMS int64) bool {
	series := s.series[key]
	if series == nil {
		return false
	}
	index := sort.Search(len(series.Points), func(i int) bool { return series.Points[i].TimestampMS >= timestampMS })
	return index < len(series.Points) && series.Points[index].TimestampMS == timestampMS
}

func (s *Store) addPoint(nodeID, source string, point Point) {
	key := SeriesKey(nodeID, source, point)
	series := s.series[key]
	if series == nil {
		series = &seriesData{Metric: point.Metric, NodeID: nodeID, Source: source, Labels: cloneLabels(point.Labels), Kind: point.Kind, Unit: point.Unit, Points: []Point{}}
		s.series[key] = series
	}
	index := sort.Search(len(series.Points), func(i int) bool { return series.Points[i].TimestampMS >= point.TimestampMS })
	if index < len(series.Points) && series.Points[index].TimestampMS == point.TimestampMS {
		series.Points[index] = point
		return
	}
	series.Points = append(series.Points, Point{})
	copy(series.Points[index+1:], series.Points[index:])
	series.Points[index] = point
	s.samples++
}

func (s *Store) appendSegments(batch Batch) error {
	byDay := map[string][]storedPoint{}
	for _, point := range batch.Points {
		day := time.UnixMilli(point.TimestampMS).UTC().Format("20060102")
		byDay[day] = append(byDay[day], storedPoint{NodeID: batch.NodeID, Source: batch.Source, Sequence: batch.Sequence, Point: point})
	}
	for day, points := range byDay {
		path := filepath.Join(s.dir, "segments", day+".ndjson")
		file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		encoder := json.NewEncoder(file)
		for _, point := range points {
			if err := encoder.Encode(point); err != nil {
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

func (s *Store) loadSegments(now time.Time) error {
	entries, err := os.ReadDir(filepath.Join(s.dir, "segments"))
	if err != nil {
		return err
	}
	cutoff := now.Add(-s.retention).UnixMilli()
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".ndjson") {
			continue
		}
		file, err := os.Open(filepath.Join(s.dir, "segments", entry.Name()))
		if err != nil {
			return err
		}
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 64<<10), 2<<20)
		for scanner.Scan() {
			var point storedPoint
			if json.Unmarshal(scanner.Bytes(), &point) == nil && point.TimestampMS >= cutoff {
				key := SeriesKey(point.NodeID, point.Source, point.Point)
				if _, exists := s.series[key]; !exists && len(s.series) >= s.maxSeries {
					_ = file.Close()
					return fmt.Errorf("stored telemetry exceeds series cardinality limit: %d", s.maxSeries)
				}
				if !s.hasPoint(key, point.TimestampMS) && s.samples >= s.maxSamples {
					_ = file.Close()
					return fmt.Errorf("stored telemetry exceeds retained sample limit: %d", s.maxSamples)
				}
				s.addPoint(point.NodeID, point.Source, point.Point)
				sequenceKey := point.NodeID + "\x00" + point.Source
				if point.Sequence > s.sequence[sequenceKey] {
					s.sequence[sequenceKey] = point.Sequence
				}
			}
		}
		scanErr := scanner.Err()
		_ = file.Close()
		if scanErr != nil {
			return scanErr
		}
	}
	for _, series := range s.series {
		if len(series.Points) > 0 {
			s.refreshSource(series.NodeID, series.Source, s.sequence[series.NodeID+"\x00"+series.Source], time.UnixMilli(series.Points[len(series.Points)-1].TimestampMS).UTC().Format(time.RFC3339Nano))
		}
	}
	return nil
}

func (s *Store) refreshSource(nodeID, source string, sequence uint64, seenAt string) {
	key := nodeID + "\x00" + source
	status := SourceStatus{NodeID: nodeID, Source: source, LastSeenAt: seenAt, LastSequence: sequence}
	for _, series := range s.series {
		if series.NodeID == nodeID && series.Source == source {
			status.Series++
		}
	}
	s.sources[key] = status
}

func (s *Store) readSequences() error {
	file, err := os.Open(filepath.Join(s.dir, "sequences.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()
	return json.NewDecoder(io.LimitReader(file, 1<<20)).Decode(&s.sequence)
}

func (s *Store) writeSequences() error {
	data, err := json.MarshalIndent(s.sequence, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(s.dir, "sequences.json")
	temporary, err := os.CreateTemp(s.dir, ".sequences-*.tmp")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err = temporary.Chmod(0o600); err == nil {
		_, err = temporary.Write(append(data, '\n'))
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

func labelsMatch(series, wanted map[string]string) bool {
	for key, value := range wanted {
		if series[key] != value {
			return false
		}
	}
	return true
}

func rangePoints(points []Point, startMS, endMS int64) []Point {
	start := sort.Search(len(points), func(i int) bool { return points[i].TimestampMS >= startMS })
	end := sort.Search(len(points), func(i int) bool { return points[i].TimestampMS > endMS })
	return append([]Point(nil), points[start:end]...)
}

func aggregatePoints(points []Point, startMS, stepMS int64, aggregate string, kind MetricKind) []Point {
	if stepMS <= 0 || len(points) < 2 {
		return points
	}
	type bucket struct {
		point Point
		sum   float64
		min   float64
		max   float64
		count int
		first Point
		last  Point
	}
	buckets := map[int64]*bucket{}
	order := []int64{}
	for _, point := range points {
		at := startMS + ((point.TimestampMS-startMS)/stepMS)*stepMS
		current := buckets[at]
		if current == nil {
			current = &bucket{point: point, sum: point.Value, min: point.Value, max: point.Value, count: 1, first: point, last: point}
			current.point.TimestampMS = at
			buckets[at] = current
			order = append(order, at)
			continue
		}
		current.sum += point.Value
		current.count++
		current.last = point
		if point.Value < current.min {
			current.min = point.Value
		}
		if point.Value > current.max {
			current.max = point.Value
		}
	}
	out := make([]Point, 0, len(order))
	for _, at := range order {
		current := buckets[at]
		switch aggregate {
		case "min":
			current.point.Value = current.min
		case "max":
			current.point.Value = current.max
		case "sum":
			current.point.Value = current.sum
		case "last":
			current.point.Value = current.last.Value
		case "rate":
			seconds := float64(current.last.TimestampMS-current.first.TimestampMS) / 1000
			if kind != Counter || seconds <= 0 || current.last.Value < current.first.Value {
				current.point.Value = 0
			} else {
				current.point.Value = (current.last.Value - current.first.Value) / seconds
			}
		default:
			current.point.Value = current.sum / float64(current.count)
		}
		out = append(out, current.point)
	}
	return out
}

func sortedSet(values map[string]bool) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
