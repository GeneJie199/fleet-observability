package center

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

type Report struct {
	NodeID     string            `json:"node_id"`
	ObservedAt string            `json:"observed_at"`
	Agent      AgentInfo         `json:"agent,omitempty"`
	Inventory  json.RawMessage   `json:"inventory,omitempty"`
	Drift      json.RawMessage   `json:"drift,omitempty"`
	Metrics    map[string]any    `json:"metrics,omitempty"`
	Labels     map[string]string `json:"labels,omitempty"`
}

type AgentInfo struct {
	Version  string `json:"version,omitempty"`
	OS       string `json:"os,omitempty"`
	Arch     string `json:"arch,omitempty"`
	Hostname string `json:"hostname,omitempty"`
}

type NodeSummary struct {
	NodeID       string            `json:"node_id"`
	ObservedAt   string            `json:"observed_at"`
	Hostname     string            `json:"hostname,omitempty"`
	Health       string            `json:"health"`
	HealthReason string            `json:"health_reason,omitempty"`
	HighestRisk  string            `json:"highest_risk,omitempty"`
	Summary      map[string]int    `json:"summary,omitempty"`
	Metrics      map[string]any    `json:"metrics,omitempty"`
	Labels       map[string]string `json:"labels,omitempty"`
	AlertCount   int               `json:"alert_count"`
}

type Alert struct {
	ID         string         `json:"id"`
	NodeID     string         `json:"node_id"`
	Severity   string         `json:"severity"`
	Kind       string         `json:"kind"`
	Title      string         `json:"title"`
	Detail     string         `json:"detail"`
	Status     string         `json:"status"`
	ObservedAt string         `json:"observed_at"`
	UpdatedAt  string         `json:"updated_at"`
	Value      float64        `json:"value,omitempty"`
	Threshold  float64        `json:"threshold,omitempty"`
	Evidence   map[string]any `json:"evidence,omitempty"`
	Assignee   string         `json:"assignee,omitempty"`
	Note       string         `json:"note,omitempty"`
}

type MetricRule struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Metric      string            `json:"metric"`
	NodeID      string            `json:"node_id,omitempty"`
	Source      string            `json:"source,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	Operator    string            `json:"operator"`
	Threshold   float64           `json:"threshold"`
	ForSeconds  int               `json:"for_seconds"`
	Severity    string            `json:"severity"`
	Enabled     bool              `json:"enabled"`
	CreatedAt   string            `json:"created_at"`
	UpdatedAt   string            `json:"updated_at"`
}

type MetricRuleState struct {
	Key            string  `json:"key"`
	RuleID         string  `json:"rule_id"`
	AlertID        string  `json:"alert_id"`
	NodeID         string  `json:"node_id"`
	Source         string  `json:"source"`
	PendingSinceMS int64   `json:"pending_since_ms,omitempty"`
	LastSeenMS     int64   `json:"last_seen_ms"`
	LastValue      float64 `json:"last_value"`
	Firing         bool    `json:"firing"`
}

type Change struct {
	ID             string `json:"id"`
	NodeID         string `json:"node_id"`
	ResourceID     string `json:"resource_id"`
	ResourceType   string `json:"resource_type,omitempty"`
	Kind           string `json:"kind"`
	Severity       string `json:"severity"`
	Summary        string `json:"summary"`
	Classification string `json:"classification"`
	ReleaseID      string `json:"release_id,omitempty"`
	ExpiresAt      string `json:"expires_at,omitempty"`
	ObservedAt     string `json:"observed_at"`
	UpdatedAt      string `json:"updated_at"`
	ReviewedBy     string `json:"reviewed_by,omitempty"`
	DecisionNote   string `json:"decision_note,omitempty"`
}

type TopologyNode struct {
	ID       string `json:"id"`
	NodeID   string `json:"node_id"`
	Type     string `json:"type"`
	Label    string `json:"label"`
	Health   string `json:"health,omitempty"`
	Metadata any    `json:"metadata,omitempty"`
}

type TopologyEdge struct {
	ID           string   `json:"id"`
	Source       string   `json:"source"`
	Target       string   `json:"target"`
	Type         string   `json:"type"`
	Confidence   float64  `json:"confidence"`
	Evidence     []string `json:"evidence,omitempty"`
	NodeID       string   `json:"node_id"`
	FirstSeenAt  string   `json:"first_seen_at,omitempty"`
	LastSeenAt   string   `json:"last_seen_at,omitempty"`
	Confirmation string   `json:"confirmation"`
	ReviewedBy   string   `json:"reviewed_by,omitempty"`
	ReviewNote   string   `json:"review_note,omitempty"`
	ReviewedAt   string   `json:"reviewed_at,omitempty"`
}

type Topology struct {
	GeneratedAt string         `json:"generated_at"`
	View        string         `json:"view"`
	Nodes       []TopologyNode `json:"nodes"`
	Edges       []TopologyEdge `json:"edges"`
}

type ResourceGroup struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	NodeIDs     []string `json:"node_ids"`
	CreatedAt   string   `json:"created_at"`
	UpdatedAt   string   `json:"updated_at"`
}

type RelationshipReview struct {
	ID           string `json:"id"`
	Confirmation string `json:"confirmation"`
	ReviewedBy   string `json:"reviewed_by,omitempty"`
	Note         string `json:"note,omitempty"`
	UpdatedAt    string `json:"updated_at"`
}

type CoverageItem struct {
	NodeID     string `json:"node_id"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	Covered    bool   `json:"covered"`
	Status     string `json:"status"`
	Detail     string `json:"detail"`
	ObservedAt string `json:"observed_at"`
}

type Coverage struct {
	GeneratedAt string         `json:"generated_at"`
	Total       int            `json:"total"`
	Covered     int            `json:"covered"`
	Missing     int            `json:"missing"`
	Items       []CoverageItem `json:"items"`
}

type Overview struct {
	GeneratedAt    string         `json:"generated_at"`
	TotalNodes     int            `json:"total_nodes"`
	HealthyNodes   int            `json:"healthy_nodes"`
	WarningNodes   int            `json:"warning_nodes"`
	CriticalNodes  int            `json:"critical_nodes"`
	StaleNodes     int            `json:"stale_nodes"`
	OpenAlerts     int            `json:"open_alerts"`
	CriticalAlerts int            `json:"critical_alerts"`
	PendingChanges int            `json:"pending_changes"`
	ResourceTotals map[string]int `json:"resource_totals"`
	NeedsAttention []NodeSummary  `json:"needs_attention"`
}
type DatabaseStatus struct {
	NodeID      string `json:"node_id"`
	Name        string `json:"name"`
	Engine      string `json:"engine"`
	Status      string `json:"status"`
	LatencyMS   int64  `json:"latency_ms"`
	Version     string `json:"version,omitempty"`
	Connections int    `json:"connections,omitempty"`
	ObservedAt  string `json:"observed_at"`
}

type inventoryDocument struct {
	Hostname  string         `json:"hostname"`
	Summary   map[string]int `json:"summary"`
	Resources []struct {
		Type     string         `json:"type"`
		ID       string         `json:"id"`
		Host     map[string]any `json:"host"`
		Process  map[string]any `json:"process"`
		Endpoint map[string]any `json:"endpoint"`
		Service  map[string]any `json:"service"`
		Metadata map[string]any `json:"metadata"`
	} `json:"resources"`
	Relationships []struct {
		Source     string   `json:"source"`
		Target     string   `json:"target"`
		Type       string   `json:"type"`
		Confidence float64  `json:"confidence"`
		Evidence   []string `json:"evidence"`
	} `json:"relationships"`
	DetectedServices []struct {
		ResourceID, Kind, Name, Source string
		Confidence                     float64
		Endpoints                      []string
	} `json:"detected_services"`
	NginxRoutes []struct{ SourceFile, ServerName, Listen, Location, Upstream string } `json:"nginx_routes"`
}

type driftItem struct {
	ID             string `json:"id"`
	Type           string `json:"type"`
	Summary        string `json:"summary"`
	Severity       string `json:"severity"`
	Fingerprint    string `json:"fingerprint"`
	Classification string `json:"classification"`
	Decision       *struct {
		Actor     string `json:"actor"`
		Note      string `json:"note"`
		ExpiresAt string `json:"expires_at"`
	} `json:"decision,omitempty"`
}

type driftDocument struct {
	HighestRisk string      `json:"highest_risk"`
	Added       []driftItem `json:"added"`
	Removed     []driftItem `json:"removed"`
	Changed     []driftItem `json:"changed"`
}

func healthRank(v string) int {
	switch strings.ToLower(v) {
	case "critical":
		return 4
	case "warning":
		return 3
	case "stale":
		return 2
	case "healthy":
		return 1
	default:
		return 0
	}
}
func sortNodeHealth(nodes []NodeSummary) {
	sort.Slice(nodes, func(i, j int) bool {
		a, b := healthRank(nodes[i].Health), healthRank(nodes[j].Health)
		if a == b {
			return nodes[i].NodeID < nodes[j].NodeID
		}
		return a > b
	})
}
func metricFloat(m map[string]any, key string) (float64, bool) {
	v, ok := m[key]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case json.Number:
		x, e := n.Float64()
		return x, e == nil
	default:
		return 0, false
	}
}
func observedTime(v string) time.Time { t, _ := time.Parse(time.RFC3339, v); return t }
func labelForResource(rType, id string, values ...map[string]any) string {
	for _, m := range values {
		for _, k := range []string{"name", "hostname", "image", "address"} {
			if v, ok := m[k].(string); ok && v != "" {
				return v
			}
		}
	}
	if p := strings.LastIndex(id, "/"); p >= 0 && p < len(id)-1 {
		return id[p+1:]
	}
	if id != "" {
		return id
	}
	return fmt.Sprintf("%s resource", rType)
}
