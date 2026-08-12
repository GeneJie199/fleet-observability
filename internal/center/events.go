package center

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/GeneJie199/fleet-observability/internal/events"
)

func (s *Server) ingestEvents(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 8<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var batch events.Batch
	if err := decoder.Decode(&batch); err != nil {
		writeError(w, http.StatusBadRequest, "invalid event batch: "+err.Error())
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "event request must contain one JSON object")
		return
	}
	if !s.requireNodeIdentity(w, r, batch.NodeID) {
		return
	}
	duplicate, err := s.eventStore.Append(batch)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]any{"status": "accepted", "duplicate": duplicate, "events": len(batch.Events)})
}

func (s *Server) queryEvents(w http.ResponseWriter, r *http.Request) {
	nodes, ok := s.requestGroupNodeSet(w, r)
	if !ok {
		return
	}
	values := r.URL.Query()
	end, err := queryTime(values.Get("end"), time.Now().UTC())
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid end: "+err.Error())
		return
	}
	start, err := queryTime(values.Get("start"), end.Add(-24*time.Hour))
	if err != nil || !start.Before(end) || end.Sub(start) > 31*24*time.Hour {
		writeError(w, http.StatusBadRequest, "event query range must be positive and at most 31 days")
		return
	}
	before := int64(0)
	if raw := strings.TrimSpace(values.Get("before")); raw != "" {
		before, err = strconv.ParseInt(raw, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "before must be Unix milliseconds")
			return
		}
	}
	limit := 200
	if raw := values.Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 1000 {
			writeError(w, http.StatusBadRequest, "limit must be between 1 and 1000")
			return
		}
	}
	writeJSON(w, s.eventStore.Query(events.Query{
		NodeID: values.Get("node"), Source: values.Get("source"), Kind: values.Get("kind"), Severity: values.Get("severity"), Service: values.Get("service"), Search: values.Get("search"),
		StartMS: start.UnixMilli(), EndMS: end.UnixMilli(), BeforeMS: before, Limit: limit, NodeIDs: nodes,
	}))
}

func (s *Server) eventCatalog(w http.ResponseWriter, r *http.Request) {
	nodes, ok := s.requestGroupNodeSet(w, r)
	if !ok {
		return
	}
	writeJSON(w, s.eventStore.CatalogForNodes(nodes))
}

func (s *Server) eventSources(w http.ResponseWriter, r *http.Request) {
	nodes, ok := s.requestGroupNodeSet(w, r)
	if !ok {
		return
	}
	writeJSON(w, s.eventStore.SourcesForNodes(nodes))
}
