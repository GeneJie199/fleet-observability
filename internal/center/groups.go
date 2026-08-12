package center

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func (s *Server) listGroups(w http.ResponseWriter, _ *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	groups, err := s.readGroups()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, groups)
}

func (s *Server) createGroup(w http.ResponseWriter, r *http.Request) {
	var in ResourceGroup
	if err := decodeBody(w, r, &in); err != nil {
		return
	}
	if in.ID == "" {
		in.ID = stableID("group", strings.TrimSpace(in.Name))
	}
	if err := s.validateGroup(in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	in.Name = strings.TrimSpace(in.Name)
	in.Description = strings.TrimSpace(in.Description)
	in.NodeIDs = uniqueStrings(in.NodeIDs)
	in.CreatedAt, in.UpdatedAt = now, now
	s.mu.Lock()
	defer s.mu.Unlock()
	groups, err := s.readGroups()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, group := range groups {
		if group.ID == in.ID {
			writeError(w, http.StatusConflict, "resource group already exists")
			return
		}
	}
	groups = append(groups, in)
	if err := s.writeGroups(groups); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, in)
}

func (s *Server) updateGroup(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !validID.MatchString(id) {
		writeError(w, http.StatusBadRequest, "invalid resource group id")
		return
	}
	var in ResourceGroup
	if err := decodeBody(w, r, &in); err != nil {
		return
	}
	in.ID = id
	if err := s.validateGroup(in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	groups, err := s.readGroups()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for i := range groups {
		if groups[i].ID != id {
			continue
		}
		groups[i].Name = strings.TrimSpace(in.Name)
		groups[i].Description = strings.TrimSpace(in.Description)
		groups[i].NodeIDs = uniqueStrings(in.NodeIDs)
		groups[i].UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		if err := s.writeGroups(groups); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, groups[i])
		return
	}
	writeError(w, http.StatusNotFound, "resource group not found")
}

func (s *Server) deleteGroup(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !validID.MatchString(id) {
		writeError(w, http.StatusBadRequest, "invalid resource group id")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	groups, err := s.readGroups()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := groups[:0]
	found := false
	for _, group := range groups {
		if group.ID == id {
			found = true
			continue
		}
		out = append(out, group)
	}
	if !found {
		writeError(w, http.StatusNotFound, "resource group not found")
		return
	}
	if err := s.writeGroups(out); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) validateGroup(group ResourceGroup) error {
	if !validID.MatchString(group.ID) {
		return errors.New("invalid resource group id")
	}
	if name := strings.TrimSpace(group.Name); name == "" || len(name) > 100 {
		return errors.New("resource group name is required and must be at most 100 characters")
	}
	if len(group.Description) > 1000 || len(group.NodeIDs) > 500 {
		return errors.New("resource group description or node list is too large")
	}
	for _, nodeID := range group.NodeIDs {
		if !validID.MatchString(nodeID) {
			return errors.New("resource group contains an invalid node id")
		}
		if _, err := os.Stat(filepath.Join(s.dir, "nodes", nodeID+".json")); err != nil {
			return errors.New("resource group contains an unknown node: " + nodeID)
		}
	}
	return nil
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func (s *Server) groupNodeSet(id string) (map[string]bool, bool, error) {
	if id == "" {
		return nil, true, nil
	}
	groups, err := s.readGroups()
	if err != nil {
		return nil, false, err
	}
	for _, group := range groups {
		if group.ID == id {
			set := make(map[string]bool, len(group.NodeIDs))
			for _, nodeID := range group.NodeIDs {
				set[nodeID] = true
			}
			return set, true, nil
		}
	}
	return nil, false, nil
}

func includeNode(nodeID string, set map[string]bool) bool {
	return set == nil || set[nodeID]
}

func (s *Server) requestGroupNodeSet(w http.ResponseWriter, r *http.Request) (map[string]bool, bool) {
	s.mu.RLock()
	set, found, err := s.groupNodeSet(r.URL.Query().Get("group"))
	s.mu.RUnlock()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return nil, false
	}
	if !found {
		writeError(w, http.StatusNotFound, "resource group not found")
		return nil, false
	}
	return set, true
}

func normalizeTopologyView(view string) string {
	switch view {
	case "deployment", "service", "exposure", "database", "impact":
		return view
	default:
		return "all"
	}
}

func filterTopologyView(topology *Topology, view string) {
	if view == "all" {
		return
	}
	allowedTypes := map[string]map[string]bool{
		"deployment": {"host": true, "service": true, "process": true, "container": true},
		"service":    {"host": true, "service": true, "process": true, "container": true, "endpoint": true, "route": true, "database": true, "postgresql": true, "mysql": true, "redis": true},
		"exposure":   {"host": true, "service": true, "process": true, "endpoint": true, "route": true},
		"database":   {"host": true, "service": true, "process": true, "database": true, "postgresql": true, "mysql": true, "redis": true},
	}
	keep := map[string]bool{}
	if view == "impact" {
		for _, node := range topology.Nodes {
			if node.Health != "" && node.Health != "healthy" {
				keep[node.ID] = true
			}
		}
		for changed := true; changed; {
			changed = false
			for _, edge := range topology.Edges {
				if keep[edge.Source] && !keep[edge.Target] {
					keep[edge.Target] = true
					changed = true
				}
			}
		}
	} else {
		allowed := allowedTypes[view]
		for _, node := range topology.Nodes {
			keep[node.ID] = allowed[strings.ToLower(node.Type)]
		}
	}
	nodes := topology.Nodes[:0]
	for _, node := range topology.Nodes {
		if keep[node.ID] {
			nodes = append(nodes, node)
		}
	}
	edges := topology.Edges[:0]
	for _, edge := range topology.Edges {
		if keep[edge.Source] && keep[edge.Target] {
			edges = append(edges, edge)
		}
	}
	topology.Nodes, topology.Edges = nodes, edges
}

func (s *Server) coverage(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	set, found, err := s.groupNodeSet(r.URL.Query().Get("group"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "resource group not found")
		return
	}
	entries, err := os.ReadDir(filepath.Join(s.dir, "nodes"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := Coverage{GeneratedAt: time.Now().UTC().Format(time.RFC3339), Items: []CoverageItem{}}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		report, err := readReport(filepath.Join(s.dir, "nodes", entry.Name()))
		if err != nil || !includeNode(report.NodeID, set) {
			continue
		}
		hostMetrics := true
		for _, key := range []string{"cpu_percent", "memory_percent", "disk_percent"} {
			if _, ok := metricFloat(report.Metrics, key); !ok {
				hostMetrics = false
			}
		}
		items := []CoverageItem{
			{NodeID: report.NodeID, Kind: "host", Name: "主机容量指标", Covered: hostMetrics, Status: coverageStatus(hostMetrics), Detail: "CPU、内存和磁盘", ObservedAt: report.ObservedAt},
			{NodeID: report.NodeID, Kind: "inventory", Name: "基础设施清单", Covered: len(report.Inventory) > 0, Status: coverageStatus(len(report.Inventory) > 0), Detail: "InfraScout inventory", ObservedAt: report.ObservedAt},
			{NodeID: report.NodeID, Kind: "drift", Name: "基础设施漂移", Covered: len(report.Drift) > 0, Status: coverageStatus(len(report.Drift) > 0), Detail: "InfraScout drift", ObservedAt: report.ObservedAt},
		}
		probes := probeResults(report.Metrics)
		items = append(items, CoverageItem{NodeID: report.NodeID, Kind: "probe", Name: "服务可用性探针", Covered: len(probes) > 0, Status: coverageStatus(len(probes) > 0), Detail: coverageProbeDetail(probes), ObservedAt: report.ObservedAt})
		for _, application := range applicationResults(report.Metrics) {
			detail := "FleetScope 原生 " + application.Kind + " 采集正常"
			if !application.Up {
				detail = application.Error
				if detail == "" {
					detail = "最近一次原生采集失败"
				}
			}
			items = append(items, CoverageItem{NodeID: report.NodeID, Kind: "application/" + application.Kind, Name: application.Name, Covered: application.Up, Status: coverageStatus(application.Up), Detail: detail, ObservedAt: report.ObservedAt})
		}
		for _, item := range items {
			out.Total++
			if item.Covered {
				out.Covered++
			} else {
				out.Missing++
			}
			out.Items = append(out.Items, item)
		}
	}
	writeJSON(w, out)
}

func coverageStatus(covered bool) string {
	if covered {
		return "covered"
	}
	return "missing"
}

func coverageProbeDetail(probes []probeResult) string {
	if len(probes) == 0 {
		return "未配置 HTTP、TCP、TLS 或数据库探针"
	}
	names := make([]string, 0, len(probes))
	for _, probe := range probes {
		names = append(names, probe.Name)
	}
	return strings.Join(names, "、")
}

func (s *Server) updateRelationship(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !validID.MatchString(id) {
		writeError(w, http.StatusBadRequest, "invalid relationship id")
		return
	}
	var in RelationshipReview
	if err := decodeBody(w, r, &in); err != nil {
		return
	}
	if in.Confirmation != "confirmed" && in.Confirmation != "rejected" && in.Confirmation != "unreviewed" {
		writeError(w, http.StatusBadRequest, "confirmation must be confirmed, rejected, or unreviewed")
		return
	}
	if in.Confirmation != "unreviewed" && strings.TrimSpace(in.ReviewedBy) == "" {
		writeError(w, http.StatusBadRequest, "reviewed_by is required")
		return
	}
	if len(in.ReviewedBy) > 80 || len(in.Note) > 1000 {
		writeError(w, http.StatusBadRequest, "reviewed_by or note is too long")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	topology := s.buildTopology()
	found := false
	for _, edge := range topology.Edges {
		if edge.ID == id {
			found = true
			break
		}
	}
	if !found {
		writeError(w, http.StatusNotFound, "relationship not found")
		return
	}
	reviews, err := s.readRelationshipReviews()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	in.ID = id
	in.ReviewedBy = strings.TrimSpace(in.ReviewedBy)
	in.Note = strings.TrimSpace(in.Note)
	in.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	updated := false
	for i := range reviews {
		if reviews[i].ID == id {
			reviews[i] = in
			updated = true
			break
		}
	}
	if !updated {
		reviews = append(reviews, in)
	}
	if err := s.writeRelationshipReviews(reviews); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, in)
}

func (s *Server) readGroups() ([]ResourceGroup, error) {
	var groups []ResourceGroup
	err := readJSONFile(filepath.Join(s.dir, "groups.json"), &groups)
	if errors.Is(err, os.ErrNotExist) {
		return []ResourceGroup{}, nil
	}
	return groups, err
}

func (s *Server) writeGroups(groups []ResourceGroup) error {
	return writeJSONFile(filepath.Join(s.dir, "groups.json"), groups)
}

func (s *Server) readRelationshipReviews() ([]RelationshipReview, error) {
	var reviews []RelationshipReview
	err := readJSONFile(filepath.Join(s.dir, "topology-reviews.json"), &reviews)
	if errors.Is(err, os.ErrNotExist) {
		return []RelationshipReview{}, nil
	}
	return reviews, err
}

func (s *Server) writeRelationshipReviews(reviews []RelationshipReview) error {
	return writeJSONFile(filepath.Join(s.dir, "topology-reviews.json"), reviews)
}
