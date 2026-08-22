package center

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/GeneJie199/fleet-observability/internal/events"
)

const evidencePackageSchema = "fleetscope.evidence-package/v1"

type AIProviderConfig struct {
	Type           string `json:"type"`
	EndpointEnv    string `json:"endpoint_env"`
	APIKeyEnv      string `json:"api_key_env,omitempty"`
	Model          string `json:"model"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
}

type AIProvider struct {
	config AIProviderConfig
	client *http.Client
}

type EvidencePackageInput struct {
	AlertIDs             []string `json:"alert_ids,omitempty"`
	ChangeIDs            []string `json:"change_ids,omitempty"`
	NodeIDs              []string `json:"node_ids,omitempty"`
	GroupID              string   `json:"group_id,omitempty"`
	ReleaseID            string   `json:"release_id,omitempty"`
	IncidentID           string   `json:"incident_id,omitempty"`
	CreatedBy            string   `json:"created_by"`
	LookbackMinutes      int      `json:"lookback_minutes,omitempty"`
	IncludeEventExcerpts bool     `json:"include_event_excerpts,omitempty"`
}

type EvidenceFact struct {
	Kind       string         `json:"kind"`
	Severity   string         `json:"severity,omitempty"`
	Summary    string         `json:"summary"`
	SourceID   string         `json:"source_id,omitempty"`
	ObservedAt string         `json:"observed_at,omitempty"`
	Data       map[string]any `json:"data,omitempty"`
}

type EvidenceNode struct {
	NodeID       string             `json:"node_id"`
	Hostname     string             `json:"hostname,omitempty"`
	Health       string             `json:"health"`
	HealthReason string             `json:"health_reason,omitempty"`
	ObservedAt   string             `json:"observed_at"`
	Labels       map[string]string  `json:"labels,omitempty"`
	Metrics      map[string]float64 `json:"metrics,omitempty"`
}

type EvidenceTopology struct {
	SnapshotID string         `json:"snapshot_id,omitempty"`
	Nodes      []TopologyNode `json:"nodes"`
	Edges      []TopologyEdge `json:"edges"`
}

type EvidenceEventSummary struct {
	Total      int             `json:"total"`
	BySeverity map[string]int  `json:"by_severity"`
	ByKind     map[string]int  `json:"by_kind"`
	Excerpts   []events.Record `json:"excerpts,omitempty"`
}

type EvidencePrivacy struct {
	Included []string `json:"included"`
	Excluded []string `json:"excluded"`
	Redacted []string `json:"redacted"`
}

type EvidenceConfidence struct {
	Score float64  `json:"score"`
	Basis []string `json:"basis"`
}

type EvidencePackage struct {
	Schema      string               `json:"schema"`
	ID          string               `json:"id"`
	CreatedAt   string               `json:"created_at"`
	CreatedBy   string               `json:"created_by"`
	GroupID     string               `json:"group_id,omitempty"`
	ReleaseID   string               `json:"release_id,omitempty"`
	IncidentID  string               `json:"incident_id,omitempty"`
	WindowStart string               `json:"window_start"`
	WindowEnd   string               `json:"window_end"`
	Facts       []EvidenceFact       `json:"facts"`
	Alerts      []Alert              `json:"alerts"`
	Changes     []Change             `json:"changes"`
	Nodes       []EvidenceNode       `json:"nodes"`
	Topology    EvidenceTopology     `json:"topology"`
	Events      EvidenceEventSummary `json:"events"`
	Assumptions []string             `json:"assumptions"`
	Confidence  EvidenceConfidence   `json:"confidence"`
	Privacy     EvidencePrivacy      `json:"privacy"`
}

type EvidencePackageSummary struct {
	ID         string  `json:"id"`
	CreatedAt  string  `json:"created_at"`
	CreatedBy  string  `json:"created_by"`
	GroupID    string  `json:"group_id,omitempty"`
	ReleaseID  string  `json:"release_id,omitempty"`
	IncidentID string  `json:"incident_id,omitempty"`
	Facts      int     `json:"facts"`
	Alerts     int     `json:"alerts"`
	Changes    int     `json:"changes"`
	Nodes      int     `json:"nodes"`
	Confidence float64 `json:"confidence"`
}

type AIRequestPreview struct {
	PackageID          string          `json:"package_id"`
	PreviewHash        string          `json:"preview_hash"`
	ProviderConfigured bool            `json:"provider_configured"`
	ProviderType       string          `json:"provider_type,omitempty"`
	Model              string          `json:"model,omitempty"`
	Privacy            EvidencePrivacy `json:"privacy"`
	Payload            string          `json:"payload"`
}

type AIExplanation struct {
	ID          string             `json:"id"`
	PackageID   string             `json:"package_id"`
	CreatedAt   string             `json:"created_at"`
	Provider    string             `json:"provider"`
	Model       string             `json:"model"`
	Analysis    string             `json:"analysis"`
	Evidence    []EvidenceFact     `json:"evidence"`
	Assumptions []string           `json:"assumptions"`
	Confidence  EvidenceConfidence `json:"confidence"`
}

var (
	secretPairPattern    = regexp.MustCompile(`(?i)(password|passwd|secret|token|api[_-]?key|authorization)\s*[:=]\s*[^\s,;]+`)
	secretFlagPattern    = regexp.MustCompile(`(?i)(^|\s)--(?:password|passwd|secret|token|api[_-]?key|authorization)(?:\s+|=)[^\s,;]+`)
	bearerPattern        = regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._~+/=-]+`)
	urlCredentialPattern = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://)[^/@\s]+@`)
	jwtPattern           = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\b`)
)

func LoadAIProviderConfig(path string) (AIProviderConfig, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return AIProviderConfig{}, fmt.Errorf("read AI provider configuration: %w", err)
	}
	var config AIProviderConfig
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return AIProviderConfig{}, fmt.Errorf("decode AI provider configuration: %w", err)
	}
	if err := validateAIProviderConfig(config); err != nil {
		return AIProviderConfig{}, err
	}
	return config, nil
}

func validateAIProviderConfig(config AIProviderConfig) error {
	if !map[string]bool{"openai-compatible": true, "anthropic": true, "gemini": true, "custom": true}[config.Type] {
		return errors.New("AI provider type must be openai-compatible, anthropic, gemini, or custom")
	}
	if strings.TrimSpace(config.EndpointEnv) == "" || strings.TrimSpace(config.Model) == "" {
		return errors.New("AI provider endpoint_env and model are required")
	}
	if config.TimeoutSeconds < 0 || config.TimeoutSeconds > 300 {
		return errors.New("AI provider timeout_seconds must be between 1 and 300")
	}
	return nil
}

func NewAIProvider(config *AIProviderConfig) (*AIProvider, error) {
	if config == nil {
		return nil, nil
	}
	if err := validateAIProviderConfig(*config); err != nil {
		return nil, err
	}
	timeout := config.TimeoutSeconds
	if timeout == 0 {
		timeout = 60
	}
	return &AIProvider{config: *config, client: &http.Client{Timeout: time.Duration(timeout) * time.Second}}, nil
}

func (s *Server) createEvidencePackage(w http.ResponseWriter, r *http.Request) {
	var input EvidencePackageInput
	if decodeBody(w, r, &input) != nil {
		return
	}
	if strings.TrimSpace(input.CreatedBy) == "" || len(input.CreatedBy) > 80 || len(input.AlertIDs) > 500 || len(input.ChangeIDs) > 500 || len(input.NodeIDs) > 500 || (input.GroupID != "" && !validID.MatchString(input.GroupID)) {
		writeError(w, http.StatusBadRequest, "evidence package scope is invalid")
		return
	}
	if input.LookbackMinutes == 0 {
		input.LookbackMinutes = 60
	}
	if input.LookbackMinutes < 5 || input.LookbackMinutes > 31*24*60 {
		writeError(w, http.StatusBadRequest, "lookback_minutes must be between 5 and 44640")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	pack, err := s.buildEvidencePackageLocked(input)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.writeEvidencePackage(pack); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, pack)
}

func (s *Server) buildEvidencePackageLocked(input EvidencePackageInput) (EvidencePackage, error) {
	now := time.Now().UTC()
	start := now.Add(-time.Duration(input.LookbackMinutes) * time.Minute)
	nodeSet, found, err := s.groupNodeSet(input.GroupID)
	if err != nil {
		return EvidencePackage{}, err
	}
	if !found {
		return EvidencePackage{}, errors.New("resource group not found")
	}
	if len(input.NodeIDs) > 0 {
		requested := map[string]bool{}
		for _, id := range input.NodeIDs {
			if !validID.MatchString(id) {
				return EvidencePackage{}, errors.New("invalid node id")
			}
			requested[id] = true
		}
		if nodeSet == nil {
			nodeSet = requested
		} else {
			for id := range nodeSet {
				if !requested[id] {
					delete(nodeSet, id)
				}
			}
		}
	}
	alertWanted, changeWanted := stringSet(input.AlertIDs), stringSet(input.ChangeIDs)
	alerts, _ := s.readAlerts()
	selectedAlerts := []Alert{}
	for _, alert := range alerts {
		if includeNode(alert.NodeID, nodeSet) && (len(alertWanted) == 0 || alertWanted[alert.ID]) && !observedTime(alert.ObservedAt).Before(start) {
			alert.Title = redactEvidenceText(alert.Title)
			alert.Detail = redactEvidenceText(alert.Detail)
			alert.Evidence = sanitizeEvidenceMap(alert.Evidence)
			alert.Note = redactEvidenceText(alert.Note)
			selectedAlerts = append(selectedAlerts, alert)
		}
	}
	changes, _ := s.readChanges()
	selectedChanges := []Change{}
	for _, change := range changes {
		if includeNode(change.NodeID, nodeSet) && (len(changeWanted) == 0 || changeWanted[change.ID]) && !observedTime(change.ObservedAt).Before(start) {
			change.Summary = redactEvidenceText(change.Summary)
			change.DecisionNote = redactEvidenceText(change.DecisionNote)
			selectedChanges = append(selectedChanges, change)
		}
	}
	allNodes, err := s.allNodes()
	if err != nil {
		return EvidencePackage{}, err
	}
	nodes := []EvidenceNode{}
	for _, node := range allNodes {
		if !includeNode(node.NodeID, nodeSet) {
			continue
		}
		metrics := map[string]float64{}
		for key, value := range node.Metrics {
			if number, ok := numberAny(value); ok && safeEvidenceKey(key) {
				metrics[key] = number
			}
		}
		nodes = append(nodes, EvidenceNode{NodeID: node.NodeID, Hostname: redactEvidenceText(node.Hostname), Health: node.Health, HealthReason: node.HealthReason, ObservedAt: node.ObservedAt, Labels: sanitizeLabels(node.Labels), Metrics: metrics})
	}
	topology := s.buildTopology()
	if nodeSet != nil {
		filterTopologyNodes(&topology, nodeSet)
	}
	evidenceTopology := sanitizeTopology(topology)
	snapshots, _ := s.readTopologySnapshots()
	if len(snapshots) > 0 {
		evidenceTopology.SnapshotID = snapshots[len(snapshots)-1].ID
	}
	result := s.eventStore.Query(events.Query{StartMS: start.UnixMilli(), EndMS: now.Add(time.Second).UnixMilli(), Limit: 200, NodeIDs: nodeSet})
	eventSummary := EvidenceEventSummary{Total: result.TotalMatched, BySeverity: map[string]int{}, ByKind: map[string]int{}}
	for _, event := range result.Events {
		eventSummary.BySeverity[event.Severity]++
		eventSummary.ByKind[event.Kind]++
		if input.IncludeEventExcerpts && len(eventSummary.Excerpts) < 50 && (event.Severity == "error" || event.Severity == "critical" || event.Severity == "warning") {
			event.Message = redactEvidenceText(event.Message)
			event.Attributes = sanitizeLabels(event.Attributes)
			eventSummary.Excerpts = append(eventSummary.Excerpts, event)
		}
	}
	facts := evidenceFacts(selectedAlerts, selectedChanges, nodes, eventSummary, evidenceTopology)
	assumptions := []string{"Temporal correlation does not prove causation.", "Missing collectors or stale nodes can reduce evidence completeness.", "AI output, when requested, is advisory and cannot approve or modify production."}
	confidence := evidenceConfidence(selectedAlerts, selectedChanges, nodes, eventSummary, evidenceTopology)
	privacy := EvidencePrivacy{Included: []string{"structured alerts and rule evidence", "change classifications and release links", "numeric metric summaries", "sanitized topology relationships", "event counts by kind and severity"}, Excluded: []string{"API keys, passwords, tokens and authorization headers", "private keys and complete environment variables", "database business rows", "source code and raw inventory documents"}, Redacted: []string{"secret-like key/value pairs", "Bearer tokens and URL credentials", "free-text fields longer than 500 characters"}}
	if input.IncludeEventExcerpts {
		privacy.Included = append(privacy.Included, "up to 50 redacted warning/error event excerpts")
	} else {
		privacy.Excluded = append(privacy.Excluded, "raw log and event message text")
	}
	createdAt := now.Format(time.RFC3339Nano)
	pack := EvidencePackage{Schema: evidencePackageSchema, CreatedAt: createdAt, CreatedBy: strings.TrimSpace(input.CreatedBy), GroupID: input.GroupID, ReleaseID: input.ReleaseID, IncidentID: input.IncidentID, WindowStart: start.Format(time.RFC3339Nano), WindowEnd: createdAt, Facts: facts, Alerts: selectedAlerts, Changes: selectedChanges, Nodes: nodes, Topology: evidenceTopology, Events: eventSummary, Assumptions: assumptions, Confidence: confidence, Privacy: privacy}
	fingerprint, _ := json.Marshal(struct {
		CreatedAt string
		Alerts    []Alert
		Changes   []Change
		Nodes     []EvidenceNode
		Topology  EvidenceTopology
	}{createdAt, selectedAlerts, selectedChanges, nodes, evidenceTopology})
	pack.ID = stableID("evidence", string(fingerprint))
	return pack, nil
}

func evidenceFacts(alerts []Alert, changes []Change, nodes []EvidenceNode, summary EvidenceEventSummary, topology EvidenceTopology) []EvidenceFact {
	facts := []EvidenceFact{}
	for _, alert := range alerts {
		facts = append(facts, EvidenceFact{Kind: "alert", Severity: alert.Severity, Summary: alert.Title + ": " + alert.Detail, SourceID: alert.ID, ObservedAt: alert.ObservedAt, Data: map[string]any{"node_id": alert.NodeID, "status": alert.Status, "value": alert.Value, "threshold": alert.Threshold}})
	}
	for _, change := range changes {
		facts = append(facts, EvidenceFact{Kind: "change", Severity: change.Severity, Summary: change.Summary, SourceID: change.ID, ObservedAt: change.ObservedAt, Data: map[string]any{"node_id": change.NodeID, "classification": change.Classification, "release_id": change.ReleaseID, "resource_type": change.ResourceType}})
	}
	for _, node := range nodes {
		if node.Health != "healthy" {
			facts = append(facts, EvidenceFact{Kind: "node_health", Severity: node.Health, Summary: node.NodeID + ": " + node.HealthReason, SourceID: node.NodeID, ObservedAt: node.ObservedAt})
		}
	}
	if summary.Total > 0 {
		facts = append(facts, EvidenceFact{Kind: "event_summary", Summary: fmt.Sprintf("%d events observed in the selected window", summary.Total), Data: map[string]any{"by_severity": summary.BySeverity, "by_kind": summary.ByKind}})
	}
	if len(topology.Edges) > 0 {
		facts = append(facts, EvidenceFact{Kind: "topology", Summary: fmt.Sprintf("%d nodes and %d relationships are in scope", len(topology.Nodes), len(topology.Edges))})
	}
	sort.Slice(facts, func(i, j int) bool {
		if facts[i].ObservedAt == facts[j].ObservedAt {
			return facts[i].SourceID < facts[j].SourceID
		}
		return facts[i].ObservedAt > facts[j].ObservedAt
	})
	return facts
}

func evidenceConfidence(alerts []Alert, changes []Change, nodes []EvidenceNode, summary EvidenceEventSummary, topology EvidenceTopology) EvidenceConfidence {
	score := .3
	basis := []string{"deterministic FleetScope rule and state data"}
	if len(alerts) > 0 {
		score += .15
		basis = append(basis, "alert evidence present")
	}
	if len(changes) > 0 {
		score += .15
		basis = append(basis, "change evidence present")
	}
	if len(nodes) > 0 {
		score += .1
		basis = append(basis, "node metrics present")
	}
	if summary.Total > 0 {
		score += .1
		basis = append(basis, "event correlation present")
	}
	if len(topology.Edges) > 0 {
		score += .1
		basis = append(basis, "topology relationships present")
	}
	if score > .9 {
		score = .9
	}
	return EvidenceConfidence{Score: score, Basis: basis}
}

func sanitizeTopology(topology Topology) EvidenceTopology {
	out := EvidenceTopology{Nodes: []TopologyNode{}, Edges: []TopologyEdge{}}
	for _, node := range topology.Nodes {
		node.Metadata = nil
		node.Label = redactEvidenceText(node.Label)
		out.Nodes = append(out.Nodes, node)
	}
	for _, edge := range topology.Edges {
		edge.Evidence = sanitizeStringSlice(edge.Evidence)
		edge.ReviewNote = redactEvidenceText(edge.ReviewNote)
		out.Edges = append(out.Edges, edge)
	}
	return out
}

func safeEvidenceKey(name string) bool {
	lower := strings.ToLower(name)
	return !strings.Contains(lower, "secret") && !strings.Contains(lower, "token") && !strings.Contains(lower, "password") && !strings.Contains(lower, "key") && !strings.Contains(lower, "authorization")
}

func sanitizeLabels(labels map[string]string) map[string]string {
	out := map[string]string{}
	for key, value := range labels {
		if safeEvidenceKey(key) {
			out[key] = redactEvidenceText(value)
		}
	}
	return out
}

func sanitizeEvidenceMap(input map[string]any) map[string]any {
	out := map[string]any{}
	for key, value := range input {
		if !safeEvidenceKey(key) {
			continue
		}
		switch typed := value.(type) {
		case string:
			out[key] = redactEvidenceText(typed)
		case map[string]any:
			out[key] = sanitizeEvidenceMap(typed)
		case []any:
			values := make([]any, 0, len(typed))
			for _, item := range typed {
				if text, ok := item.(string); ok {
					values = append(values, redactEvidenceText(text))
				}
			}
			out[key] = values
		case float64, json.Number, bool, nil:
			out[key] = value
		}
	}
	return out
}

func redactEvidenceText(value string) string {
	value = redactEvidenceSecrets(value)
	if len(value) > 500 {
		value = value[:500] + "..."
	}
	return value
}

func redactEvidenceSecrets(value string) string {
	if strings.Contains(strings.ToUpper(value), "PRIVATE KEY") {
		return "[REDACTED PRIVATE KEY]"
	}
	value = bearerPattern.ReplaceAllString(value, "Bearer [REDACTED]")
	value = secretFlagPattern.ReplaceAllString(value, "$1credential-flag=[REDACTED]")
	value = secretPairPattern.ReplaceAllString(value, "$1=[REDACTED]")
	value = urlCredentialPattern.ReplaceAllString(value, "$1[REDACTED]@")
	return jwtPattern.ReplaceAllString(value, "[REDACTED JWT]")
}

func sanitizeStringSlice(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, redactEvidenceText(value))
	}
	return out
}

func stringSet(values []string) map[string]bool {
	out := map[string]bool{}
	for _, value := range values {
		out[value] = true
	}
	return out
}

func (s *Server) evidencePackagePath(id string) string {
	return filepath.Join(s.dir, "evidence-packages", id+".json")
}

func (s *Server) writeEvidencePackage(pack EvidencePackage) error {
	if err := os.MkdirAll(filepath.Join(s.dir, "evidence-packages"), 0o750); err != nil {
		return err
	}
	b, err := json.MarshalIndent(pack, "", "  ")
	if err != nil {
		return err
	}
	if err = atomicWrite(s.evidencePackagePath(pack.ID), append(b, '\n')); err != nil {
		return err
	}
	return s.pruneEvidencePackages()
}

func (s *Server) pruneEvidencePackages() error {
	directory := filepath.Join(s.dir, "evidence-packages")
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	type retainedFile struct {
		name    string
		modTime time.Time
	}
	files := []retainedFile{}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return infoErr
		}
		files = append(files, retainedFile{name: entry.Name(), modTime: info.ModTime()})
	}
	if len(files) <= s.evidenceMaxPackages {
		return nil
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].modTime.Equal(files[j].modTime) {
			return files[i].name < files[j].name
		}
		return files[i].modTime.Before(files[j].modTime)
	})
	for _, file := range files[:len(files)-s.evidenceMaxPackages] {
		if err := os.Remove(filepath.Join(directory, file.name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		packageID := strings.TrimSuffix(file.name, ".json")
		if err := os.Remove(s.explanationPath(packageID)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func (s *Server) readEvidencePackage(id string) (EvidencePackage, error) {
	var pack EvidencePackage
	err := readJSONFile(s.evidencePackagePath(id), &pack)
	return pack, err
}

func evidencePackageSummary(pack EvidencePackage) EvidencePackageSummary {
	return EvidencePackageSummary{ID: pack.ID, CreatedAt: pack.CreatedAt, CreatedBy: pack.CreatedBy, GroupID: pack.GroupID, ReleaseID: pack.ReleaseID, IncidentID: pack.IncidentID, Facts: len(pack.Facts), Alerts: len(pack.Alerts), Changes: len(pack.Changes), Nodes: len(pack.Nodes), Confidence: pack.Confidence.Score}
}

func (s *Server) listEvidencePackages(w http.ResponseWriter, _ *http.Request) {
	entries, err := os.ReadDir(filepath.Join(s.dir, "evidence-packages"))
	if errors.Is(err, os.ErrNotExist) {
		writeJSON(w, []EvidencePackageSummary{})
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := []EvidencePackageSummary{}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		pack, err := s.readEvidencePackage(strings.TrimSuffix(entry.Name(), ".json"))
		if err == nil {
			out = append(out, evidencePackageSummary(pack))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	writeJSON(w, out)
}

func (s *Server) getEvidencePackage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !validID.MatchString(id) {
		writeError(w, http.StatusBadRequest, "invalid evidence package id")
		return
	}
	pack, err := s.readEvidencePackage(id)
	if errors.Is(err, os.ErrNotExist) {
		writeError(w, http.StatusNotFound, "evidence package not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, pack)
}

func evidencePrompt(pack EvidencePackage) string {
	b, _ := json.MarshalIndent(pack, "", "  ")
	return "You are analyzing a FleetScope evidence package. Treat only Evidence Facts as facts. Clearly separate observations, hypotheses, assumptions, confidence, investigation steps, and rollback-safe recommendations. Never claim a root cause is certain without direct evidence. Do not recommend autonomous production changes. Respond in the operator's language when inferable.\n\n" + string(b)
}

func previewForPackage(pack EvidencePackage, provider *AIProvider) AIRequestPreview {
	payload := evidencePrompt(pack)
	sum := sha256.Sum256([]byte(payload))
	preview := AIRequestPreview{PackageID: pack.ID, PreviewHash: hex.EncodeToString(sum[:]), ProviderConfigured: provider != nil, Privacy: pack.Privacy, Payload: payload}
	if provider != nil {
		preview.ProviderType, preview.Model = provider.config.Type, provider.config.Model
	}
	return preview
}

func (s *Server) getAIRequestPreview(w http.ResponseWriter, r *http.Request) {
	pack, err := s.readEvidencePackage(r.PathValue("id"))
	if errors.Is(err, os.ErrNotExist) {
		writeError(w, http.StatusNotFound, "evidence package not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, previewForPackage(pack, s.ai))
}

func (s *Server) explainEvidencePackage(w http.ResponseWriter, r *http.Request) {
	if s.ai == nil {
		writeError(w, http.StatusPreconditionFailed, "AI provider is not configured; the evidence package remains fully usable")
		return
	}
	var input struct {
		PreviewHash string `json:"preview_hash"`
		Confirmed   bool   `json:"confirmed"`
	}
	if decodeBody(w, r, &input) != nil {
		return
	}
	pack, err := s.readEvidencePackage(r.PathValue("id"))
	if errors.Is(err, os.ErrNotExist) {
		writeError(w, http.StatusNotFound, "evidence package not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	preview := previewForPackage(pack, s.ai)
	if !input.Confirmed || input.PreviewHash != preview.PreviewHash {
		writeError(w, http.StatusPreconditionFailed, "confirm the current AI request preview hash before sending")
		return
	}
	analysis, err := s.ai.Explain(r.Context(), preview.Payload)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	createdAt := time.Now().UTC().Format(time.RFC3339Nano)
	explanation := AIExplanation{ID: stableID("explanation", pack.ID, createdAt), PackageID: pack.ID, CreatedAt: createdAt, Provider: s.ai.config.Type, Model: s.ai.config.Model, Analysis: redactEvidenceTextLong(analysis, 65536), Evidence: pack.Facts, Assumptions: append(pack.Assumptions, "The AI analysis is an unverified advisory interpretation of the previewed evidence."), Confidence: pack.Confidence}
	if err := s.appendEvidenceExplanation(explanation); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, explanation)
}

func (p *AIProvider) Explain(ctx context.Context, prompt string) (string, error) {
	endpoint := os.Getenv(p.config.EndpointEnv)
	if endpoint == "" {
		return "", fmt.Errorf("required AI endpoint environment variable %s is empty", p.config.EndpointEnv)
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", errors.New("AI endpoint must be an absolute HTTP(S) URL")
	}
	key := ""
	if p.config.APIKeyEnv != "" {
		key = os.Getenv(p.config.APIKeyEnv)
		if key == "" {
			return "", fmt.Errorf("required AI API key environment variable %s is empty", p.config.APIKeyEnv)
		}
	}
	var body any
	switch p.config.Type {
	case "openai-compatible":
		body = map[string]any{"model": p.config.Model, "messages": []map[string]string{{"role": "user", "content": prompt}}, "temperature": 0.1}
	case "anthropic":
		body = map[string]any{"model": p.config.Model, "max_tokens": 4096, "temperature": 0.1, "messages": []map[string]string{{"role": "user", "content": prompt}}}
	case "gemini":
		body = map[string]any{"contents": []map[string]any{{"role": "user", "parts": []map[string]string{{"text": prompt}}}}, "generationConfig": map[string]any{"temperature": 0.1}}
	default:
		body = map[string]any{"model": p.config.Model, "prompt": prompt}
	}
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(b))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		switch p.config.Type {
		case "anthropic":
			req.Header.Set("x-api-key", key)
			req.Header.Set("anthropic-version", "2023-06-01")
		case "gemini":
			req.Header.Set("x-goog-api-key", key)
		default:
			req.Header.Set("Authorization", "Bearer "+key)
		}
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return "", errors.New("AI provider request failed")
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("AI provider returned HTTP %d", resp.StatusCode)
	}
	return parseAIResponse(p.config.Type, responseBody)
}

func parseAIResponse(providerType string, body []byte) (string, error) {
	var value map[string]any
	if json.Unmarshal(body, &value) != nil {
		if strings.TrimSpace(string(body)) == "" {
			return "", errors.New("AI provider returned an empty response")
		}
		return string(body), nil
	}
	if providerType == "anthropic" {
		if content, ok := value["content"].([]any); ok {
			for _, item := range content {
				if part, ok := item.(map[string]any); ok {
					if text, ok := part["text"].(string); ok && text != "" {
						return text, nil
					}
				}
			}
		}
	}
	if providerType == "gemini" {
		if candidates, ok := value["candidates"].([]any); ok && len(candidates) > 0 {
			if candidate, ok := candidates[0].(map[string]any); ok {
				if content, ok := candidate["content"].(map[string]any); ok {
					if parts, ok := content["parts"].([]any); ok && len(parts) > 0 {
						if part, ok := parts[0].(map[string]any); ok {
							if text, ok := part["text"].(string); ok {
								return text, nil
							}
						}
					}
				}
			}
		}
	}
	if choices, ok := value["choices"].([]any); ok && len(choices) > 0 {
		if choice, ok := choices[0].(map[string]any); ok {
			if message, ok := choice["message"].(map[string]any); ok {
				if content, ok := message["content"].(string); ok {
					return content, nil
				}
			}
			if text, ok := choice["text"].(string); ok {
				return text, nil
			}
		}
	}
	if text, ok := value["text"].(string); ok {
		return text, nil
	}
	return "", errors.New("AI provider response did not contain text")
}

func redactEvidenceTextLong(value string, limit int) string {
	value = redactEvidenceSecrets(value)
	if len(value) > limit {
		value = value[:limit]
	}
	return value
}

func (s *Server) explanationPath(packageID string) string {
	return filepath.Join(s.dir, "evidence-explanations", packageID+".json")
}

func (s *Server) readEvidenceExplanations(packageID string) ([]AIExplanation, error) {
	var explanations []AIExplanation
	err := readJSONFile(s.explanationPath(packageID), &explanations)
	if errors.Is(err, os.ErrNotExist) {
		return []AIExplanation{}, nil
	}
	return explanations, err
}

func (s *Server) appendEvidenceExplanation(explanation AIExplanation) error {
	if err := os.MkdirAll(filepath.Join(s.dir, "evidence-explanations"), 0o750); err != nil {
		return err
	}
	explanations, err := s.readEvidenceExplanations(explanation.PackageID)
	if err != nil {
		return err
	}
	explanations = append(explanations, explanation)
	if len(explanations) > 100 {
		explanations = explanations[len(explanations)-100:]
	}
	return writeJSONFile(s.explanationPath(explanation.PackageID), explanations)
}

func (s *Server) listEvidenceExplanations(w http.ResponseWriter, r *http.Request) {
	explanations, err := s.readEvidenceExplanations(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, explanations)
}
