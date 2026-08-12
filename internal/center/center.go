package center

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/GeneJie199/fleet-observability/internal/events"
	"github.com/GeneJie199/fleet-observability/internal/telemetry"
)

//go:embed static
var assets embed.FS

var validID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

type Server struct {
	dir, token, webhookURL string
	mux                    *http.ServeMux
	mu                     sync.RWMutex
	telemetry              *telemetry.Store
	eventStore             *events.Store
	protectReads           bool
	historyMaxEntries      int
	historyMaxBytes        int64
	historyCounts          map[string]int
}

type Options struct {
	TelemetryRetention  time.Duration
	TelemetryMaxSeries  int
	TelemetryMaxBatch   int
	TelemetryMaxSamples int
	EventRetention      time.Duration
	EventMaxEntries     int
	EventMaxBatch       int
	HistoryMaxEntries   int
	HistoryMaxBytes     int64
	ProtectReads        bool
}

func New(dataDir, token string) (*Server, error) {
	return NewWithWebhook(dataDir, token, "")
}

func NewWithWebhook(dataDir, token, webhookURL string) (*Server, error) {
	return NewWithOptions(dataDir, token, webhookURL, Options{})
}

func NewWithOptions(dataDir, token, webhookURL string, options Options) (*Server, error) {
	if dataDir == "" {
		return nil, errors.New("data directory is required")
	}
	for _, name := range []string{"nodes", "history"} {
		if err := os.MkdirAll(filepath.Join(dataDir, name), 0o750); err != nil {
			return nil, err
		}
	}
	if options.HistoryMaxEntries <= 0 {
		options.HistoryMaxEntries = 2000
	}
	if options.HistoryMaxBytes <= 0 {
		options.HistoryMaxBytes = 64 << 20
	}
	historyCounts, err := prepareHistories(filepath.Join(dataDir, "history"), options.HistoryMaxEntries, options.HistoryMaxBytes)
	if err != nil {
		return nil, fmt.Errorf("prepare node report history: %w", err)
	}
	telemetryStore, err := telemetry.OpenStore(filepath.Join(dataDir, "telemetry"), telemetry.StoreOptions{Retention: options.TelemetryRetention, MaxSeries: options.TelemetryMaxSeries, MaxPoints: options.TelemetryMaxBatch, MaxSamples: options.TelemetryMaxSamples})
	if err != nil {
		return nil, fmt.Errorf("open native telemetry store: %w", err)
	}
	if err := telemetryStore.Prune(time.Now().UTC()); err != nil {
		return nil, fmt.Errorf("prune native telemetry store: %w", err)
	}
	eventStore, err := events.OpenStore(filepath.Join(dataDir, "events"), events.Options{Retention: options.EventRetention, MaxEvents: options.EventMaxEntries, MaxBatch: options.EventMaxBatch})
	if err != nil {
		return nil, fmt.Errorf("open native event store: %w", err)
	}
	if err := eventStore.Prune(time.Now().UTC()); err != nil {
		return nil, fmt.Errorf("prune native event store: %w", err)
	}
	s := &Server{dir: dataDir, token: token, webhookURL: webhookURL, telemetry: telemetryStore, eventStore: eventStore, protectReads: options.ProtectReads, historyMaxEntries: options.HistoryMaxEntries, historyMaxBytes: options.HistoryMaxBytes, historyCounts: historyCounts}
	if err := s.ensureDefaultRules(); err != nil {
		return nil, fmt.Errorf("initialize metric rules: %w", err)
	}
	s.mux = s.routes()
	return s, nil
}
func (s *Server) Handler() http.Handler { return securityHeaders(s.authorizeReads(s.mux)) }

func (s *Server) authorizeReads(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.protectReads && s.token != "" && r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/") && r.URL.Path != "/api/v1/health" {
			header := r.Header.Get("Authorization")
			if !strings.HasPrefix(header, "Bearer ") || subtle.ConstantTimeCompare([]byte(strings.TrimPrefix(header, "Bearer ")), []byte(s.token)) != 1 {
				writeError(w, http.StatusUnauthorized, "unauthorized")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) routes() *http.ServeMux {
	m := http.NewServeMux()
	m.HandleFunc("GET /api/v1/health", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, map[string]string{"status": "ok"}) })
	m.HandleFunc("GET /api/v1/overview", s.overview)
	m.HandleFunc("GET /api/v1/nodes", s.listNodes)
	m.HandleFunc("GET /api/v1/nodes/{id}", s.getNode)
	m.HandleFunc("GET /api/v1/nodes/{id}/history", s.nodeHistory)
	m.HandleFunc("POST /api/v1/nodes/{id}/report", s.authorizeAgent(s.putReport))
	m.HandleFunc("GET /api/v1/agents", s.listAgents)
	m.HandleFunc("POST /api/v1/agents/enroll", s.authorize(s.enrollAgent))
	m.HandleFunc("DELETE /api/v1/agents/{id}", s.authorize(s.revokeAgent))
	m.HandleFunc("GET /api/v1/alerts", s.listAlerts)
	m.HandleFunc("PATCH /api/v1/alerts/{id}", s.authorize(s.updateAlert))
	m.HandleFunc("GET /api/v1/rules", s.listRules)
	m.HandleFunc("POST /api/v1/rules", s.authorize(s.createRule))
	m.HandleFunc("PATCH /api/v1/rules/{id}", s.authorize(s.updateRule))
	m.HandleFunc("DELETE /api/v1/rules/{id}", s.authorize(s.deleteRule))
	m.HandleFunc("GET /api/v1/changes", s.listChanges)
	m.HandleFunc("PATCH /api/v1/changes/{id}", s.authorize(s.updateChange))
	m.HandleFunc("GET /api/v1/groups", s.listGroups)
	m.HandleFunc("POST /api/v1/groups", s.authorize(s.createGroup))
	m.HandleFunc("PATCH /api/v1/groups/{id}", s.authorize(s.updateGroup))
	m.HandleFunc("DELETE /api/v1/groups/{id}", s.authorize(s.deleteGroup))
	m.HandleFunc("GET /api/v1/topology", s.topology)
	m.HandleFunc("PATCH /api/v1/topology/relationships/{id}", s.authorize(s.updateRelationship))
	m.HandleFunc("GET /api/v1/coverage", s.coverage)
	m.HandleFunc("GET /api/v1/databases", s.databases)
	m.HandleFunc("POST /api/v1/telemetry/batches", s.authorizeAgent(s.ingestTelemetry))
	m.HandleFunc("POST /api/v1/ingest/prometheus", s.authorize(s.ingestCompatibility("prometheus")))
	m.HandleFunc("POST /api/v1/ingest/influx", s.authorize(s.ingestCompatibility("influx")))
	m.HandleFunc("POST /api/v1/ingest/otlp", s.authorize(s.ingestCompatibility("otlp")))
	m.HandleFunc("POST /v1/metrics", s.authorize(s.ingestCompatibility("otlp")))
	m.HandleFunc("GET /api/v1/telemetry/catalog", s.telemetryCatalog)
	m.HandleFunc("GET /api/v1/telemetry/query", s.queryTelemetry)
	m.HandleFunc("GET /api/v1/telemetry/sources", s.telemetrySources)
	m.HandleFunc("POST /api/v1/events/batches", s.authorizeAgent(s.ingestEvents))
	m.HandleFunc("GET /api/v1/events", s.queryEvents)
	m.HandleFunc("GET /api/v1/events/catalog", s.eventCatalog)
	m.HandleFunc("GET /api/v1/events/sources", s.eventSources)
	static, _ := fs.Sub(assets, "static")
	m.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(static))))
	m.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		b, _ := assets.ReadFile("static/index.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(b)
	})
	return m
}
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self'; script-src 'self'; connect-src 'self'")
		next.ServeHTTP(w, r)
	})
}
func (s *Server) authorize(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.token != "" {
			header := r.Header.Get("Authorization")
			if !strings.HasPrefix(header, "Bearer ") || subtle.ConstantTimeCompare([]byte(strings.TrimPrefix(header, "Bearer ")), []byte(s.token)) != 1 {
				writeError(w, http.StatusUnauthorized, "unauthorized")
				return
			}
		}
		next(w, r)
	}
}

func (s *Server) putReport(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !validID.MatchString(id) {
		writeError(w, 400, "invalid node id")
		return
	}
	if !s.requireNodeIdentity(w, r, id) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<20)
	var report Report
	dec := json.NewDecoder(r.Body)
	dec.UseNumber()
	if err := dec.Decode(&report); err != nil {
		writeError(w, 400, "invalid report: "+err.Error())
		return
	}
	if report.NodeID != "" && report.NodeID != id {
		writeError(w, 400, "node id mismatch")
		return
	}
	if len(report.Inventory) == 0 && len(report.Drift) == 0 && len(report.Metrics) == 0 {
		writeError(w, 400, "empty report")
		return
	}
	if !validJSON(report.Inventory) || !validJSON(report.Drift) {
		writeError(w, 400, "inventory or drift is invalid JSON")
		return
	}
	report.NodeID = id
	if observedTime(report.ObservedAt).IsZero() {
		report.ObservedAt = time.Now().UTC().Format(time.RFC3339)
	}
	b, _ := json.MarshalIndent(report, "", "  ")
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := atomicWrite(filepath.Join(s.dir, "nodes", id+".json"), append(b, '\n')); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if err := s.appendHistory(id, report); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	ruleAlerts := []Alert{}
	if points := telemetry.NumericPoints(report.Metrics, observedTime(report.ObservedAt).UnixMilli(), report.Labels); len(points) > 0 && !s.telemetry.HasSource(id, "native-agent") {
		batch := telemetry.Batch{Schema: telemetry.BatchSchema, NodeID: id, Source: "legacy-report", SentAt: report.ObservedAt, Points: points}
		_, _ = s.telemetry.Append(batch)
		ruleAlerts, _ = s.evaluateRulesLocked(batch)
	}
	alerts, _ := s.readAlerts()
	freshAlerts := deriveAlerts(report)
	newAlerts := unseenAlerts(alerts, freshAlerts)
	newAlerts = append(newAlerts, ruleAlerts...)
	alerts = mergeAlerts(alerts, freshAlerts, report.NodeID, report.ObservedAt)
	_ = s.writeAlerts(alerts)
	changes, _ := s.readChanges()
	changes = mergeChanges(changes, deriveChanges(report))
	_ = s.writeChanges(changes)
	if s.webhookURL != "" && len(newAlerts) > 0 {
		go s.notifyWebhook(newAlerts)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "accepted", "node_id": id})
}
func validJSON(raw json.RawMessage) bool { return len(raw) == 0 || json.Valid(raw) }

func (s *Server) listNodes(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	nodes, err := s.allNodes()
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	set, found, err := s.groupNodeSet(r.URL.Query().Get("group"))
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if !found {
		writeError(w, 404, "resource group not found")
		return
	}
	out := nodes[:0]
	for _, node := range nodes {
		if includeNode(node.NodeID, set) {
			out = append(out, node)
		}
	}
	writeJSON(w, out)
}
func (s *Server) allNodes() ([]NodeSummary, error) {
	entries, err := os.ReadDir(filepath.Join(s.dir, "nodes"))
	if err != nil {
		return nil, err
	}
	alerts, _ := s.readAlerts()
	counts := map[string]int{}
	for _, a := range alerts {
		if a.Status == "open" {
			counts[a.NodeID]++
		}
	}
	out := []NodeSummary{}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		r, x := readReport(filepath.Join(s.dir, "nodes", e.Name()))
		if x == nil {
			n := summarize(r)
			n.AlertCount = counts[n.NodeID]
			applyFreshness(&n)
			out = append(out, n)
		}
	}
	sortNodeHealth(out)
	return out, nil
}
func (s *Server) getNode(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !validID.MatchString(id) {
		writeError(w, 400, "invalid node id")
		return
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	report, err := readReport(filepath.Join(s.dir, "nodes", id+".json"))
	if errors.Is(err, os.ErrNotExist) {
		writeError(w, 404, "node not found")
		return
	}
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	alerts, _ := s.readAlerts()
	changes, _ := s.readChanges()
	na := []Alert{}
	nc := []Change{}
	for _, a := range alerts {
		if a.NodeID == id {
			na = append(na, a)
		}
	}
	for _, c := range changes {
		if c.NodeID == id {
			nc = append(nc, c)
		}
	}
	summary := summarize(report)
	for _, alert := range na {
		if alert.Status == "open" {
			summary.AlertCount++
		}
	}
	applyFreshness(&summary)
	writeJSON(w, map[string]any{"summary": summary, "report": report, "alerts": na, "changes": nc})
}
func (s *Server) nodeHistory(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !validID.MatchString(id) {
		writeError(w, 400, "invalid node id")
		return
	}
	limit := 240
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := readHistory(filepath.Join(s.dir, "history", id+".ndjson"), limit)
	if errors.Is(err, os.ErrNotExist) {
		writeJSON(w, []Report{})
		return
	}
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, rows)
}

func (s *Server) overview(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	nodes, err := s.allNodes()
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	alerts, _ := s.readAlerts()
	changes, _ := s.readChanges()
	set, found, err := s.groupNodeSet(r.URL.Query().Get("group"))
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if !found {
		writeError(w, 404, "resource group not found")
		return
	}
	filteredNodes := nodes[:0]
	for _, node := range nodes {
		if includeNode(node.NodeID, set) {
			filteredNodes = append(filteredNodes, node)
		}
	}
	nodes = filteredNodes
	o := Overview{GeneratedAt: time.Now().UTC().Format(time.RFC3339), TotalNodes: len(nodes), ResourceTotals: map[string]int{}, NeedsAttention: []NodeSummary{}}
	for _, n := range nodes {
		switch n.Health {
		case "healthy":
			o.HealthyNodes++
		case "warning":
			o.WarningNodes++
		case "critical":
			o.CriticalNodes++
		case "stale":
			o.StaleNodes++
		}
		for k, v := range n.Summary {
			o.ResourceTotals[k] += v
		}
		if n.Health != "healthy" && len(o.NeedsAttention) < 8 {
			o.NeedsAttention = append(o.NeedsAttention, n)
		}
	}
	for _, a := range alerts {
		if includeNode(a.NodeID, set) && a.Status == "open" {
			o.OpenAlerts++
			if a.Severity == "critical" {
				o.CriticalAlerts++
			}
		}
	}
	for _, c := range changes {
		if includeNode(c.NodeID, set) && c.Classification == "unexpected" {
			o.PendingChanges++
		}
	}
	writeJSON(w, o)
}

func summarize(report Report) NodeSummary {
	n := NodeSummary{NodeID: report.NodeID, ObservedAt: report.ObservedAt, Metrics: report.Metrics, Labels: report.Labels, Health: "healthy"}
	var inv inventoryDocument
	_ = json.Unmarshal(report.Inventory, &inv)
	n.Hostname = inv.Hostname
	n.Summary = inv.Summary
	if n.Hostname == "" {
		n.Hostname = report.Agent.Hostname
	}
	var drift driftDocument
	_ = json.Unmarshal(report.Drift, &drift)
	n.HighestRisk = strings.ToUpper(drift.HighestRisk)
	if n.HighestRisk == "CRITICAL" {
		n.Health = "critical"
		n.HealthReason = "critical infrastructure drift"
	} else if n.HighestRisk == "WARNING" {
		n.Health = "warning"
		n.HealthReason = "infrastructure drift requires review"
	}
	for _, key := range []string{"cpu_percent", "memory_percent", "disk_percent"} {
		if v, ok := metricFloat(report.Metrics, key); ok {
			if v >= 95 {
				n.Health = "critical"
				n.HealthReason = key + " is critically high"
			} else if v >= 85 && healthRank(n.Health) < healthRank("warning") {
				n.Health = "warning"
				n.HealthReason = key + " is high"
			}
		}
	}
	return n
}
func applyFreshness(n *NodeSummary) {
	t := observedTime(n.ObservedAt)
	if t.IsZero() || time.Since(t) > 5*time.Minute {
		n.Health = "stale"
		n.HealthReason = "agent has not reported within 5 minutes"
	}
}

func (s *Server) listAlerts(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items, err := s.readAlerts()
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	status := r.URL.Query().Get("status")
	node := r.URL.Query().Get("node")
	set, found, err := s.groupNodeSet(r.URL.Query().Get("group"))
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if !found {
		writeError(w, 404, "resource group not found")
		return
	}
	out := []Alert{}
	for _, x := range items {
		if includeNode(x.NodeID, set) && (status == "" || x.Status == status) && (node == "" || x.NodeID == node) {
			out = append(out, x)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ObservedAt > out[j].ObservedAt })
	writeJSON(w, out)
}
func (s *Server) updateAlert(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var in struct {
		Status   string `json:"status"`
		Assignee string `json:"assignee"`
		Note     string `json:"note"`
	}
	if err := decodeBody(w, r, &in); err != nil {
		return
	}
	if in.Status != "acknowledged" && in.Status != "resolved" && in.Status != "open" {
		writeError(w, 400, "status must be open, acknowledged, or resolved")
		return
	}
	if len(in.Assignee) > 80 || len(in.Note) > 1000 {
		writeError(w, 400, "assignee or note is too long")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	items, _ := s.readAlerts()
	for i := range items {
		if items[i].ID == id {
			items[i].Status = in.Status
			items[i].Assignee = strings.TrimSpace(in.Assignee)
			items[i].Note = strings.TrimSpace(in.Note)
			items[i].UpdatedAt = time.Now().UTC().Format(time.RFC3339)
			if err := s.writeAlerts(items); err != nil {
				writeError(w, 500, err.Error())
				return
			}
			writeJSON(w, items[i])
			return
		}
	}
	writeError(w, 404, "alert not found")
}
func (s *Server) listChanges(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items, err := s.readChanges()
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	class := r.URL.Query().Get("classification")
	node := r.URL.Query().Get("node")
	set, found, err := s.groupNodeSet(r.URL.Query().Get("group"))
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if !found {
		writeError(w, 404, "resource group not found")
		return
	}
	out := []Change{}
	now := time.Now()
	for _, x := range items {
		if x.Classification == "temporary" && x.ExpiresAt != "" {
			if t := observedTime(x.ExpiresAt); !t.IsZero() && now.After(t) {
				x.Classification = "unexpected"
			}
		}
		if includeNode(x.NodeID, set) && (class == "" || x.Classification == class) && (node == "" || x.NodeID == node) {
			out = append(out, x)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ObservedAt > out[j].ObservedAt })
	writeJSON(w, out)
}
func (s *Server) updateChange(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var in struct {
		Classification string `json:"classification"`
		ReleaseID      string `json:"release_id"`
		ExpiresAt      string `json:"expires_at"`
		ReviewedBy     string `json:"reviewed_by"`
		DecisionNote   string `json:"decision_note"`
	}
	if err := decodeBody(w, r, &in); err != nil {
		return
	}
	allowed := map[string]bool{"expected": true, "approved": true, "temporary": true, "unexpected": true, "denied": true}
	if !allowed[in.Classification] {
		writeError(w, 400, "invalid classification")
		return
	}
	if in.Classification == "temporary" && observedTime(in.ExpiresAt).IsZero() {
		writeError(w, 400, "temporary classification requires RFC3339 expires_at")
		return
	}
	if len(in.ReviewedBy) > 80 || len(in.DecisionNote) > 1000 {
		writeError(w, 400, "reviewed_by or decision_note is too long")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	items, _ := s.readChanges()
	for i := range items {
		if items[i].ID == id {
			items[i].Classification = in.Classification
			items[i].ReleaseID = in.ReleaseID
			items[i].ExpiresAt = in.ExpiresAt
			items[i].ReviewedBy = strings.TrimSpace(in.ReviewedBy)
			items[i].DecisionNote = strings.TrimSpace(in.DecisionNote)
			items[i].UpdatedAt = time.Now().UTC().Format(time.RFC3339)
			if err := s.writeChanges(items); err != nil {
				writeError(w, 500, err.Error())
				return
			}
			writeJSON(w, items[i])
			return
		}
	}
	writeError(w, 404, "change not found")
}

func (s *Server) topology(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t := s.buildTopology()
	set, found, err := s.groupNodeSet(r.URL.Query().Get("group"))
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if !found {
		writeError(w, 404, "resource group not found")
		return
	}
	if set != nil {
		nodes := t.Nodes[:0]
		kept := map[string]bool{}
		for _, node := range t.Nodes {
			if includeNode(node.NodeID, set) {
				nodes = append(nodes, node)
				kept[node.ID] = true
			}
		}
		edges := t.Edges[:0]
		for _, edge := range t.Edges {
			if kept[edge.Source] && kept[edge.Target] {
				edges = append(edges, edge)
			}
		}
		t.Nodes, t.Edges = nodes, edges
	}
	t.View = normalizeTopologyView(r.URL.Query().Get("view"))
	filterTopologyView(&t, t.View)
	writeJSON(w, t)
}

func (s *Server) buildTopology() Topology {
	entries, _ := os.ReadDir(filepath.Join(s.dir, "nodes"))
	t := Topology{GeneratedAt: time.Now().UTC().Format(time.RFC3339), View: "all", Nodes: []TopologyNode{}, Edges: []TopologyEdge{}}
	seen := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		rep, err := readReport(filepath.Join(s.dir, "nodes", e.Name()))
		if err != nil {
			continue
		}
		summary := summarize(rep)
		applyFreshness(&summary)
		var inv inventoryDocument
		_ = json.Unmarshal(rep.Inventory, &inv)
		actualHostID := ""
		for _, x := range inv.Resources {
			if x.Type == "host" {
				actualHostID = x.ID
				break
			}
		}
		if actualHostID == "" {
			actualHostID = "fleet.host:" + rep.NodeID
		}
		observedRelationships := s.relationshipObservationTimes(rep.NodeID, rep)
		for _, x := range inv.Resources {
			id := x.ID
			if id == "" {
				continue
			}
			health := ""
			if id == actualHostID {
				health = summary.Health
			}
			t.Nodes = append(t.Nodes, TopologyNode{ID: id, NodeID: rep.NodeID, Type: x.Type, Label: labelForResource(x.Type, id, x.Host, x.Service, x.Process, x.Endpoint, x.Metadata), Health: health, Metadata: x.Metadata})
			seen[id] = true
		}
		if !seen[actualHostID] {
			t.Nodes = append(t.Nodes, TopologyNode{ID: actualHostID, NodeID: rep.NodeID, Type: "host", Label: summary.Hostname, Health: summary.Health})
			seen[actualHostID] = true
		}
		for _, route := range inv.NginxRoutes {
			rid := stableID("nginx_route", rep.NodeID, route.ServerName, route.Location, route.Upstream)
			edgeID := stableID("relationship", actualHostID, rid, "routes")
			firstSeenAt, lastSeenAt := relationshipTimes(observedRelationships, edgeID, rep.ObservedAt)
			t.Nodes = append(t.Nodes, TopologyNode{ID: rid, NodeID: rep.NodeID, Type: "route", Label: route.ServerName + route.Location, Metadata: route})
			t.Edges = append(t.Edges, TopologyEdge{ID: edgeID, Source: actualHostID, Target: rid, Type: "routes", Confidence: .85, Evidence: []string{route.SourceFile}, NodeID: rep.NodeID, FirstSeenAt: firstSeenAt, LastSeenAt: lastSeenAt})
		}
		for _, x := range inv.Relationships {
			edgeID := stableID("relationship", x.Source, x.Target, x.Type)
			firstSeenAt, lastSeenAt := relationshipTimes(observedRelationships, edgeID, rep.ObservedAt)
			t.Edges = append(t.Edges, TopologyEdge{ID: edgeID, Source: x.Source, Target: x.Target, Type: x.Type, Confidence: x.Confidence, Evidence: x.Evidence, NodeID: rep.NodeID, FirstSeenAt: firstSeenAt, LastSeenAt: lastSeenAt})
		}
	}
	reviews, _ := s.readRelationshipReviews()
	reviewByID := make(map[string]RelationshipReview, len(reviews))
	for _, review := range reviews {
		reviewByID[review.ID] = review
	}
	for i := range t.Edges {
		edge := &t.Edges[i]
		edge.Confirmation = "unreviewed"
		if review, ok := reviewByID[edge.ID]; ok {
			edge.Confirmation = review.Confirmation
			edge.ReviewedBy = review.ReviewedBy
			edge.ReviewNote = review.Note
			edge.ReviewedAt = review.UpdatedAt
		}
	}
	return t
}

type relationshipObservation struct {
	First string
	Last  string
}

// relationshipObservationTimes derives relationship age from the retained node
// history instead of presenting the latest report time as both first and last seen.
func (s *Server) relationshipObservationTimes(nodeID string, current Report) map[string]relationshipObservation {
	reports, err := readHistory(filepath.Join(s.dir, "history", nodeID+".ndjson"), 10000)
	if err != nil || len(reports) == 0 {
		reports = []Report{current}
	}
	out := map[string]relationshipObservation{}
	for _, report := range reports {
		if report.ObservedAt == "" {
			continue
		}
		for _, id := range relationshipIDs(report) {
			observed := out[id]
			if observed.First == "" || observedTime(report.ObservedAt).Before(observedTime(observed.First)) {
				observed.First = report.ObservedAt
			}
			if observed.Last == "" || observedTime(report.ObservedAt).After(observedTime(observed.Last)) {
				observed.Last = report.ObservedAt
			}
			out[id] = observed
		}
	}
	return out
}

func relationshipIDs(report Report) []string {
	var inv inventoryDocument
	if json.Unmarshal(report.Inventory, &inv) != nil {
		return nil
	}
	hostID := ""
	for _, resource := range inv.Resources {
		if resource.Type == "host" {
			hostID = resource.ID
			break
		}
	}
	if hostID == "" {
		hostID = "fleet.host:" + report.NodeID
	}
	ids := make([]string, 0, len(inv.Relationships)+len(inv.NginxRoutes))
	for _, route := range inv.NginxRoutes {
		routeID := stableID("nginx_route", report.NodeID, route.ServerName, route.Location, route.Upstream)
		ids = append(ids, stableID("relationship", hostID, routeID, "routes"))
	}
	for _, relationship := range inv.Relationships {
		ids = append(ids, stableID("relationship", relationship.Source, relationship.Target, relationship.Type))
	}
	return ids
}

func relationshipTimes(observations map[string]relationshipObservation, id, fallback string) (string, string) {
	if observed, ok := observations[id]; ok {
		return observed.First, observed.Last
	}
	return fallback, fallback
}

func (s *Server) databases(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	set, found, err := s.groupNodeSet(r.URL.Query().Get("group"))
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if !found {
		writeError(w, 404, "resource group not found")
		return
	}
	entries, _ := os.ReadDir(filepath.Join(s.dir, "nodes"))
	out := []DatabaseStatus{}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		rep, err := readReport(filepath.Join(s.dir, "nodes", e.Name()))
		if err != nil || !includeNode(rep.NodeID, set) {
			continue
		}
		for _, probe := range probeResults(rep.Metrics) {
			if probe.Kind != "database" {
				continue
			}
			connections := 0
			if v, ok := numberAny(probe.Values["connections"]); ok {
				connections = int(v)
			}
			out = append(out, DatabaseStatus{NodeID: rep.NodeID, Name: probe.Name, Engine: stringAny(probe.Values["engine"]), Status: probe.Status, LatencyMS: probe.LatencyMS, Version: stringAny(probe.Values["version"]), Connections: connections, ObservedAt: rep.ObservedAt})
		}
		for _, application := range applicationResults(rep.Metrics) {
			if application.Kind != "postgres" && application.Kind != "mysql" {
				continue
			}
			connections, _ := numberAny(application.Summary["connections"])
			status := "error"
			if application.Up {
				status = "ok"
			}
			out = append(out, DatabaseStatus{NodeID: rep.NodeID, Name: application.Name, Engine: application.Kind, Status: status, Connections: int(connections), ObservedAt: rep.ObservedAt})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Status != out[j].Status {
			return out[i].Status > out[j].Status
		}
		return out[i].Name < out[j].Name
	})
	writeJSON(w, out)
}

type probeResult struct {
	Name, Kind, Status, Detail string
	Required                   bool
	LatencyMS                  int64
	Values                     map[string]any
}

type applicationResult struct {
	Name, Kind, Error string
	Up, Required      bool
	Summary           map[string]any
}

func applicationResults(metrics map[string]any) []applicationResult {
	raw, ok := metrics["applications"].([]any)
	if !ok {
		return nil
	}
	out := []applicationResult{}
	for _, item := range raw {
		value, ok := item.(map[string]any)
		if !ok {
			continue
		}
		up, _ := value["up"].(bool)
		required, _ := value["required"].(bool)
		out = append(out, applicationResult{Name: stringAny(value["name"]), Kind: stringAny(value["kind"]), Error: stringAny(value["error"]), Up: up, Required: required, Summary: value})
	}
	return out
}

func probeResults(metrics map[string]any) []probeResult {
	raw, ok := metrics["checks"].([]any)
	if !ok {
		return nil
	}
	out := []probeResult{}
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		latency, _ := numberAny(m["latency_ms"])
		required, _ := m["required"].(bool)
		values, _ := m["values"].(map[string]any)
		out = append(out, probeResult{Name: stringAny(m["name"]), Kind: stringAny(m["kind"]), Status: stringAny(m["status"]), Detail: stringAny(m["detail"]), Required: required, LatencyMS: int64(latency), Values: values})
	}
	return out
}
func numberAny(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case json.Number:
		x, e := n.Float64()
		return x, e == nil
	case int:
		return float64(n), true
	}
	return 0, false
}
func stringAny(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func deriveAlerts(r Report) []Alert {
	now := r.ObservedAt
	out := []Alert{}
	var d driftDocument
	_ = json.Unmarshal(r.Drift, &d)
	if strings.EqualFold(d.HighestRisk, "CRITICAL") {
		out = append(out, Alert{ID: stableID("alert", r.NodeID, "drift"), NodeID: r.NodeID, Severity: "critical", Kind: "infrastructure_drift", Title: "Critical infrastructure drift", Detail: "InfraScout reported a critical unreviewed change", Status: "open", ObservedAt: now, UpdatedAt: now})
	}
	if v, ok := metricFloat(r.Metrics, "docker_unhealthy"); ok && v > 0 {
		out = append(out, Alert{ID: stableID("alert", r.NodeID, "docker_unhealthy"), NodeID: r.NodeID, Severity: "critical", Kind: "docker_unhealthy", Title: "Container is not running", Detail: fmt.Sprintf("%.0f containers are stopped or unhealthy", v), Status: "open", ObservedAt: now, UpdatedAt: now, Value: v})
	}
	for _, p := range probeResults(r.Metrics) {
		if p.Status == "ok" {
			continue
		}
		severity := "warning"
		if p.Required || p.Status == "critical" {
			severity = "critical"
		}
		out = append(out, Alert{ID: stableID("alert", r.NodeID, "probe", p.Kind, p.Name), NodeID: r.NodeID, Severity: severity, Kind: "probe." + p.Kind, Title: p.Name + " check failed", Detail: p.Detail, Status: "open", ObservedAt: now, UpdatedAt: now, Value: float64(p.LatencyMS), Evidence: map[string]any{"probe_kind": p.Kind, "probe_status": p.Status, "values": p.Values}})
	}
	return out
}
func unseenAlerts(old, fresh []Alert) []Alert {
	seen := map[string]bool{}
	for _, x := range old {
		if x.Status != "resolved" {
			seen[x.ID] = true
		}
	}
	out := []Alert{}
	for _, x := range fresh {
		if !seen[x.ID] {
			out = append(out, x)
		}
	}
	return out
}
func (s *Server) notifyWebhook(alerts []Alert) {
	b, _ := json.Marshal(map[string]any{"event": "fleet.alert.opened", "alerts": alerts})
	req, err := http.NewRequest(http.MethodPost, s.webhookURL, strings.NewReader(string(b)))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err == nil {
		_ = resp.Body.Close()
	}
}
func deriveChanges(r Report) []Change {
	var d driftDocument
	_ = json.Unmarshal(r.Drift, &d)
	out := []Change{}
	add := func(kind string, items []driftItem) {
		for _, x := range items {
			identity := x.Fingerprint
			if identity == "" {
				identity = kind + "\x00" + x.ID
			}
			classification := x.Classification
			if !map[string]bool{"expected": true, "approved": true, "temporary": true, "unexpected": true, "denied": true}[classification] {
				classification = "unexpected"
			}
			change := Change{ID: stableID("change", r.NodeID, identity), NodeID: r.NodeID, ResourceID: x.ID, ResourceType: x.Type, Kind: kind, Severity: strings.ToLower(x.Severity), Summary: x.Summary, Classification: classification, ObservedAt: r.ObservedAt, UpdatedAt: r.ObservedAt}
			if x.Decision != nil {
				change.ReviewedBy = x.Decision.Actor
				change.DecisionNote = x.Decision.Note
				change.ExpiresAt = x.Decision.ExpiresAt
			}
			out = append(out, change)
		}
	}
	add("added", d.Added)
	add("removed", d.Removed)
	add("changed", d.Changed)
	return out
}
func mergeAlerts(old, fresh []Alert, nodeID, now string) []Alert {
	idx := map[string]int{}
	freshIDs := map[string]bool{}
	for _, x := range fresh {
		freshIDs[x.ID] = true
	}
	for i, x := range old {
		idx[x.ID] = i
		if x.NodeID == nodeID && !strings.HasPrefix(x.Kind, "rule.") && !freshIDs[x.ID] && x.Status != "resolved" {
			old[i].Status = "resolved"
			old[i].UpdatedAt = now
		}
	}
	for _, x := range fresh {
		if i, ok := idx[x.ID]; ok {
			status := old[i].Status
			if status == "resolved" {
				status = "open"
			}
			x.Status = status
			old[i] = x
		} else {
			old = append(old, x)
		}
	}
	return old
}
func mergeChanges(old, fresh []Change) []Change {
	idx := map[string]int{}
	for i, x := range old {
		idx[x.ID] = i
	}
	for _, x := range fresh {
		if i, ok := idx[x.ID]; ok {
			if x.ReviewedBy == "" {
				x.Classification = old[i].Classification
				x.ReleaseID = old[i].ReleaseID
				x.ExpiresAt = old[i].ExpiresAt
				x.ReviewedBy = old[i].ReviewedBy
				x.DecisionNote = old[i].DecisionNote
			}
			old[i] = x
		} else {
			old = append(old, x)
		}
	}
	return old
}
func stableID(prefix string, parts ...string) string {
	h := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return prefix + "_" + hex.EncodeToString(h[:8])
}

func (s *Server) readAlerts() ([]Alert, error) {
	var v []Alert
	err := readJSONFile(filepath.Join(s.dir, "alerts.json"), &v)
	if errors.Is(err, os.ErrNotExist) {
		return []Alert{}, nil
	}
	return v, err
}
func (s *Server) writeAlerts(v []Alert) error {
	return writeJSONFile(filepath.Join(s.dir, "alerts.json"), v)
}
func (s *Server) readChanges() ([]Change, error) {
	var v []Change
	err := readJSONFile(filepath.Join(s.dir, "changes.json"), &v)
	if errors.Is(err, os.ErrNotExist) {
		return []Change{}, nil
	}
	return v, err
}
func (s *Server) writeChanges(v []Change) error {
	return writeJSONFile(filepath.Join(s.dir, "changes.json"), v)
}
func readJSONFile(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}
func writeJSONFile(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(path, append(b, '\n'))
}
func readReport(path string) (Report, error) { var r Report; return r, readJSONFile(path, &r) }
func appendHistory(path string, r Report) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	return json.NewEncoder(f).Encode(r)
}

func (s *Server) appendHistory(nodeID string, report Report) error {
	path := filepath.Join(s.dir, "history", nodeID+".ndjson")
	if err := appendHistory(path, report); err != nil {
		return err
	}
	s.historyCounts[nodeID]++
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if s.historyCounts[nodeID] <= s.historyMaxEntries && info.Size() <= s.historyMaxBytes {
		return nil
	}
	count, err := compactHistoryFile(path, s.historyMaxEntries, s.historyMaxBytes)
	if err == nil {
		s.historyCounts[nodeID] = count
	}
	return err
}

func prepareHistories(dir string, maxEntries int, maxBytes int64) (map[string]int, error) {
	counts := map[string]int{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".ndjson" {
			continue
		}
		count, err := compactHistoryFile(filepath.Join(dir, entry.Name()), maxEntries, maxBytes)
		if err != nil {
			return nil, err
		}
		counts[strings.TrimSuffix(entry.Name(), ".ndjson")] = count
	}
	return counts, nil
}

func compactHistoryFile(path string, maxEntries int, maxBytes int64) (int, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	lines := [][]byte{}
	totalBytes := int64(0)
	changed := false
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), 20<<20)
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		if !json.Valid(line) || int64(len(line)+1) > maxBytes {
			changed = true
			continue
		}
		lines = append(lines, line)
		totalBytes += int64(len(line) + 1)
		for len(lines) > maxEntries || totalBytes > maxBytes {
			totalBytes -= int64(len(lines[0]) + 1)
			lines = lines[1:]
			changed = true
		}
	}
	scanErr := scanner.Err()
	closeErr := file.Close()
	if scanErr != nil {
		return 0, scanErr
	}
	if closeErr != nil {
		return 0, closeErr
	}
	if !changed {
		return len(lines), nil
	}
	data := bytes.Join(lines, []byte{'\n'})
	if len(data) > 0 {
		data = append(data, '\n')
	}
	if err := atomicWrite(path, data); err != nil {
		return 0, err
	}
	return len(lines), nil
}
func readHistory(path string, limit int) ([]Report, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	rows := []Report{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 20<<20)
	for sc.Scan() {
		var x Report
		if json.Unmarshal(sc.Bytes(), &x) == nil {
			rows = append(rows, x)
			if len(rows) > limit {
				rows = rows[1:]
			}
		}
	}
	return rows, sc.Err()
}
func decodeBody(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, 400, "invalid JSON: "+err.Error())
		return err
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			err = errors.New("multiple JSON values are not allowed")
		}
		writeError(w, 400, "invalid JSON: "+err.Error())
		return err
	}
	return nil
}
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}
func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
func atomicWrite(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".fleet-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err = tmp.Chmod(0o600); err == nil {
		_, err = tmp.Write(data)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if x := tmp.Close(); err == nil {
		err = x
	}
	if err != nil {
		return err
	}
	if err = os.Rename(name, path); err != nil {
		return fmt.Errorf("save data: %w", err)
	}
	return nil
}
func DecodeResponse(body io.Reader, into any) error { return json.NewDecoder(body).Decode(into) }
