package center

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
)

func (s *Server) recommendations(w http.ResponseWriter, r *http.Request) {
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
	out := []DiscoveryRecommendation{}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		report, err := readReport(filepath.Join(s.dir, "nodes", entry.Name()))
		if err != nil || !includeNode(report.NodeID, set) {
			continue
		}
		var inventory inventoryDocument
		if json.Unmarshal(report.Inventory, &inventory) != nil {
			continue
		}
		monitored := map[string]bool{}
		for _, application := range applicationResults(report.Metrics) {
			monitored[application.Kind+"\x00"+application.Name] = true
		}
		for _, recommendation := range inventory.Recommendations {
			recommendation.NodeID = report.NodeID
			if recommendation.ObservedAt == "" {
				recommendation.ObservedAt = report.ObservedAt
			}
			if monitored[recommendation.Kind+"\x00"+recommendation.Name] {
				recommendation.Status = "monitored"
				recommendation.RequiredFields = nil
			}
			out = append(out, recommendation)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Status != out[j].Status {
			return out[i].Status < out[j].Status
		}
		if out[i].NodeID != out[j].NodeID {
			return out[i].NodeID < out[j].NodeID
		}
		return out[i].Name < out[j].Name
	})
	writeJSON(w, out)
}
