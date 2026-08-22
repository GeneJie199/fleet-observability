package center

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const maxTopologySnapshots = 512

type topologySnapshotInput struct {
	GroupID    string `json:"group_id"`
	ReleaseID  string `json:"release_id"`
	IncidentID string `json:"incident_id"`
	Note       string `json:"note"`
}

func (s *Server) readTopologySnapshots() ([]TopologySnapshot, error) {
	var snapshots []TopologySnapshot
	err := readJSONFile(filepath.Join(s.dir, "topology-snapshots.json"), &snapshots)
	if errors.Is(err, os.ErrNotExist) {
		return []TopologySnapshot{}, nil
	}
	return snapshots, err
}

func (s *Server) writeTopologySnapshots(snapshots []TopologySnapshot) error {
	if len(snapshots) > maxTopologySnapshots {
		snapshots = snapshots[len(snapshots)-maxTopologySnapshots:]
	}
	return writeJSONFile(filepath.Join(s.dir, "topology-snapshots.json"), snapshots)
}

func topologySnapshotSummary(snapshot TopologySnapshot) TopologySnapshotSummary {
	return TopologySnapshotSummary{ID: snapshot.ID, CapturedAt: snapshot.CapturedAt, Fingerprint: snapshot.Fingerprint, Trigger: snapshot.Trigger, GroupID: snapshot.GroupID, ReleaseID: snapshot.ReleaseID, IncidentID: snapshot.IncidentID, Note: snapshot.Note, Nodes: len(snapshot.Topology.Nodes), Edges: len(snapshot.Topology.Edges)}
}

func topologyFingerprint(topology Topology) string {
	nodes := append([]TopologyNode(nil), topology.Nodes...)
	edges := append([]TopologyEdge(nil), topology.Edges...)
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })
	sort.Slice(edges, func(i, j int) bool { return edges[i].ID < edges[j].ID })
	type stableNode struct {
		ID, NodeID, Type, Label string
		Metadata                any
	}
	type stableEdge struct {
		ID, Source, Target, Type, NodeID string
		Confidence                       float64
		Evidence                         []string
	}
	stableNodes := make([]stableNode, 0, len(nodes))
	for _, node := range nodes {
		stableNodes = append(stableNodes, stableNode{ID: node.ID, NodeID: node.NodeID, Type: node.Type, Label: node.Label, Metadata: stableTopologyMetadata(node.Metadata)})
	}
	stableEdges := make([]stableEdge, 0, len(edges))
	for _, edge := range edges {
		evidence := append([]string(nil), edge.Evidence...)
		sort.Strings(evidence)
		stableEdges = append(stableEdges, stableEdge{ID: edge.ID, Source: edge.Source, Target: edge.Target, Type: edge.Type, NodeID: edge.NodeID, Confidence: edge.Confidence, Evidence: evidence})
	}
	b, _ := json.Marshal(struct {
		Nodes []stableNode
		Edges []stableEdge
	}{stableNodes, stableEdges})
	return stableID("topology", string(b))
}

func stableTopologyMetadata(value any) any {
	m, ok := value.(map[string]any)
	if !ok {
		return value
	}
	out := make(map[string]any, len(m))
	for key, item := range m {
		switch strings.ToLower(key) {
		case "pid", "observed_at", "started_at", "uptime", "uptime_seconds", "last_seen_at":
			continue
		default:
			out[key] = item
		}
	}
	return out
}

func (s *Server) captureAutomaticTopologyLocked(_ string) error {
	topology := s.buildTopology()
	// A snapshot records when Center observed the fleet state. Individual
	// resources retain their own report timestamps, including late reports.
	topology.GeneratedAt = time.Now().UTC().Format(time.RFC3339Nano)
	_, _, err := s.captureTopologyLocked(topology, topologySnapshotInput{}, "report")
	if err != nil {
		return err
	}
	groups, err := s.readGroups()
	if err != nil {
		return err
	}
	for _, group := range groups {
		set := make(map[string]bool, len(group.NodeIDs))
		for _, nodeID := range group.NodeIDs {
			set[nodeID] = true
		}
		filtered := Topology{GeneratedAt: topology.GeneratedAt, View: topology.View, Nodes: append([]TopologyNode(nil), topology.Nodes...), Edges: append([]TopologyEdge(nil), topology.Edges...)}
		filterTopologyNodes(&filtered, set)
		if _, _, err := s.captureTopologyLocked(filtered, topologySnapshotInput{GroupID: group.ID}, "report"); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) captureTopologyLocked(topology Topology, input topologySnapshotInput, trigger string) (TopologySnapshot, bool, error) {
	fingerprint := topologyFingerprint(topology)
	snapshots, err := s.readTopologySnapshots()
	if err != nil {
		return TopologySnapshot{}, false, err
	}
	previousIndex := -1
	for index := len(snapshots) - 1; index >= 0; index-- {
		if snapshots[index].GroupID == input.GroupID {
			previousIndex = index
			break
		}
	}
	if trigger == "report" && previousIndex >= 0 && snapshots[previousIndex].Fingerprint == fingerprint {
		return snapshots[previousIndex], false, nil
	}
	capturedAt := topology.GeneratedAt
	if observedTime(capturedAt).IsZero() {
		capturedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	snapshot := TopologySnapshot{ID: stableID("topology_snapshot", capturedAt, fingerprint, input.GroupID, input.ReleaseID, input.IncidentID), CapturedAt: capturedAt, Fingerprint: fingerprint, Trigger: trigger, GroupID: input.GroupID, ReleaseID: input.ReleaseID, IncidentID: input.IncidentID, Note: input.Note, Topology: topology}
	if previousIndex >= 0 && input.GroupID == "" {
		previous := snapshots[previousIndex]
		diff := diffTopologies(previous, snapshot)
		if err := s.recordTopologyChangesLocked(diff, input.ReleaseID); err != nil {
			return TopologySnapshot{}, false, err
		}
	}
	snapshots = append(snapshots, snapshot)
	if err := s.writeTopologySnapshots(snapshots); err != nil {
		return TopologySnapshot{}, false, err
	}
	return snapshot, true, nil
}

func (s *Server) recordTopologyChangesLocked(diff TopologyDiff, releaseID string) error {
	changes, err := s.readChanges()
	if err != nil {
		return err
	}
	observedAt := diff.To.CapturedAt
	fresh := make([]Change, 0, len(diff.AddedNodes)+len(diff.RemovedNodes)+len(diff.ChangedNodes)+len(diff.AddedEdges)+len(diff.RemovedEdges)+len(diff.ChangedEdges))
	add := func(resourceID, nodeID, resourceType, kind, summary string) {
		fresh = append(fresh, Change{ID: stableID("change", "topology", diff.From.ID, diff.To.ID, resourceID, kind), NodeID: nodeID, ResourceID: resourceID, ResourceType: resourceType, Kind: kind, Severity: topologyChangeSeverity(resourceType, kind), Summary: summary, Classification: "unexpected", ReleaseID: releaseID, ObservedAt: observedAt, UpdatedAt: observedAt})
	}
	for _, node := range diff.AddedNodes {
		add(node.ID, node.NodeID, "topology.node", "added", "Topology node added: "+node.Label)
	}
	for _, node := range diff.RemovedNodes {
		add(node.ID, node.NodeID, "topology.node", "removed", "Topology node removed: "+node.Label)
	}
	for _, pair := range diff.ChangedNodes {
		add(pair.After.ID, pair.After.NodeID, "topology.node", "changed", "Topology node changed: "+pair.After.Label)
	}
	for _, edge := range diff.AddedEdges {
		add(edge.ID, edge.NodeID, "topology.edge", "added", "Topology relationship added: "+edge.Source+" -> "+edge.Target)
	}
	for _, edge := range diff.RemovedEdges {
		add(edge.ID, edge.NodeID, "topology.edge", "removed", "Topology relationship removed: "+edge.Source+" -> "+edge.Target)
	}
	for _, pair := range diff.ChangedEdges {
		add(pair.After.ID, pair.After.NodeID, "topology.edge", "changed", "Topology relationship changed: "+pair.After.Source+" -> "+pair.After.Target)
	}
	if len(fresh) == 0 {
		return nil
	}
	return s.writeChanges(mergeChanges(changes, fresh))
}

func topologyChangeSeverity(resourceType, kind string) string {
	if resourceType == "topology.edge" && kind == "removed" {
		return "high"
	}
	return "medium"
}

func diffTopologies(from, to TopologySnapshot) TopologyDiff {
	diff := TopologyDiff{From: topologySnapshotSummary(from), To: topologySnapshotSummary(to), AddedNodes: []TopologyNode{}, RemovedNodes: []TopologyNode{}, ChangedNodes: []TopologyNodeChange{}, AddedEdges: []TopologyEdge{}, RemovedEdges: []TopologyEdge{}, ChangedEdges: []TopologyEdgeChange{}, Summary: map[string]int{}}
	fromNodes, toNodes := map[string]TopologyNode{}, map[string]TopologyNode{}
	fromEdges, toEdges := map[string]TopologyEdge{}, map[string]TopologyEdge{}
	for _, node := range from.Topology.Nodes {
		fromNodes[node.ID] = node
	}
	for _, node := range to.Topology.Nodes {
		toNodes[node.ID] = node
	}
	for _, edge := range from.Topology.Edges {
		fromEdges[edge.ID] = edge
	}
	for _, edge := range to.Topology.Edges {
		toEdges[edge.ID] = edge
	}
	for id, node := range toNodes {
		previous, ok := fromNodes[id]
		if !ok {
			diff.AddedNodes = append(diff.AddedNodes, node)
		} else if nodeFingerprint(previous) != nodeFingerprint(node) {
			a, b := previous, node
			diff.ChangedNodes = append(diff.ChangedNodes, TopologyNodeChange{Before: &a, After: &b})
		}
	}
	for id, node := range fromNodes {
		if _, ok := toNodes[id]; !ok {
			diff.RemovedNodes = append(diff.RemovedNodes, node)
		}
	}
	for id, edge := range toEdges {
		previous, ok := fromEdges[id]
		if !ok {
			diff.AddedEdges = append(diff.AddedEdges, edge)
		} else if edgeFingerprint(previous) != edgeFingerprint(edge) {
			a, b := previous, edge
			diff.ChangedEdges = append(diff.ChangedEdges, TopologyEdgeChange{Before: &a, After: &b})
		}
	}
	for id, edge := range fromEdges {
		if _, ok := toEdges[id]; !ok {
			diff.RemovedEdges = append(diff.RemovedEdges, edge)
		}
	}
	sort.Slice(diff.AddedNodes, func(i, j int) bool { return diff.AddedNodes[i].ID < diff.AddedNodes[j].ID })
	sort.Slice(diff.RemovedNodes, func(i, j int) bool { return diff.RemovedNodes[i].ID < diff.RemovedNodes[j].ID })
	sort.Slice(diff.AddedEdges, func(i, j int) bool { return diff.AddedEdges[i].ID < diff.AddedEdges[j].ID })
	sort.Slice(diff.RemovedEdges, func(i, j int) bool { return diff.RemovedEdges[i].ID < diff.RemovedEdges[j].ID })
	diff.Summary = map[string]int{"added_nodes": len(diff.AddedNodes), "removed_nodes": len(diff.RemovedNodes), "changed_nodes": len(diff.ChangedNodes), "added_edges": len(diff.AddedEdges), "removed_edges": len(diff.RemovedEdges), "changed_edges": len(diff.ChangedEdges)}
	return diff
}

func nodeFingerprint(node TopologyNode) string {
	b, _ := json.Marshal(struct {
		ID, NodeID, Type, Label string
		Metadata                any
	}{node.ID, node.NodeID, node.Type, node.Label, stableTopologyMetadata(node.Metadata)})
	return string(b)
}

func edgeFingerprint(edge TopologyEdge) string {
	evidence := append([]string(nil), edge.Evidence...)
	sort.Strings(evidence)
	b, _ := json.Marshal(struct {
		ID, Source, Target, Type, NodeID string
		Confidence                       float64
		Evidence                         []string
	}{edge.ID, edge.Source, edge.Target, edge.Type, edge.NodeID, edge.Confidence, evidence})
	return string(b)
}

func (s *Server) listTopologySnapshots(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	snapshots, err := s.readTopologySnapshots()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	groupID, releaseID, incidentID := r.URL.Query().Get("group"), r.URL.Query().Get("release"), r.URL.Query().Get("incident")
	out := []TopologySnapshotSummary{}
	for i := len(snapshots) - 1; i >= 0; i-- {
		snapshot := snapshots[i]
		if (groupID == "" || snapshot.GroupID == groupID) && (releaseID == "" || snapshot.ReleaseID == releaseID) && (incidentID == "" || snapshot.IncidentID == incidentID) {
			out = append(out, topologySnapshotSummary(snapshot))
		}
	}
	writeJSON(w, out)
}

func (s *Server) getTopologySnapshot(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	snapshots, err := s.readTopologySnapshots()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, snapshot := range snapshots {
		if snapshot.ID == r.PathValue("id") {
			writeJSON(w, snapshot)
			return
		}
	}
	writeError(w, http.StatusNotFound, "topology snapshot not found")
}

func (s *Server) createTopologySnapshot(w http.ResponseWriter, r *http.Request) {
	var input topologySnapshotInput
	if decodeBody(w, r, &input) != nil {
		return
	}
	if len(input.ReleaseID) > 128 || len(input.IncidentID) > 128 || len(input.Note) > 1000 || (input.GroupID != "" && !validID.MatchString(input.GroupID)) {
		writeError(w, http.StatusBadRequest, "snapshot metadata is invalid")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	topology := s.buildTopology()
	if input.GroupID != "" {
		set, found, err := s.groupNodeSet(input.GroupID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if !found {
			writeError(w, http.StatusNotFound, "resource group not found")
			return
		}
		filterTopologyNodes(&topology, set)
	}
	snapshot, _, err := s.captureTopologyLocked(topology, input, "manual")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, snapshot)
}

func filterTopologyNodes(topology *Topology, set map[string]bool) {
	kept := map[string]bool{}
	nodes := topology.Nodes[:0]
	for _, node := range topology.Nodes {
		if includeNode(node.NodeID, set) {
			nodes = append(nodes, node)
			kept[node.ID] = true
		}
	}
	edges := topology.Edges[:0]
	for _, edge := range topology.Edges {
		if kept[edge.Source] && kept[edge.Target] {
			edges = append(edges, edge)
		}
	}
	topology.Nodes, topology.Edges = nodes, edges
}

func (s *Server) topologyDiff(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	snapshots, err := s.readTopologySnapshots()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	fromID, toID := r.URL.Query().Get("from"), r.URL.Query().Get("to")
	var from, to *TopologySnapshot
	for i := range snapshots {
		if snapshots[i].ID == fromID {
			from = &snapshots[i]
		}
		if snapshots[i].ID == toID {
			to = &snapshots[i]
		}
	}
	if toID == "" && len(snapshots) > 0 {
		to = &snapshots[len(snapshots)-1]
	}
	if fromID == "" && len(snapshots) > 1 {
		from = &snapshots[len(snapshots)-2]
	}
	if from == nil || to == nil {
		writeError(w, http.StatusBadRequest, "two valid topology snapshots are required")
		return
	}
	writeJSON(w, diffTopologies(*from, *to))
}
