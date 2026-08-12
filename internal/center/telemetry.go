package center

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/GeneJie199/fleet-observability/internal/compat"
	"github.com/GeneJie199/fleet-observability/internal/telemetry"
)

func (s *Server) ingestTelemetry(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var batch telemetry.Batch
	if err := decoder.Decode(&batch); err != nil {
		writeError(w, http.StatusBadRequest, "invalid telemetry batch: "+err.Error())
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			writeError(w, http.StatusBadRequest, "telemetry request must contain one JSON object")
		} else {
			writeError(w, http.StatusBadRequest, "invalid trailing telemetry data: "+err.Error())
		}
		return
	}
	batch.Normalize(time.Now().UTC())
	if !s.requireNodeIdentity(w, r, batch.NodeID) {
		return
	}
	s.mu.Lock()
	duplicate, err := s.telemetry.Append(batch)
	if err != nil {
		s.mu.Unlock()
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	newAlerts := []Alert{}
	if !duplicate {
		newAlerts, err = s.evaluateRulesLocked(batch)
	}
	s.mu.Unlock()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "evaluate metric rules: "+err.Error())
		return
	}
	if s.webhookURL != "" && len(newAlerts) > 0 {
		go s.notifyWebhook(newAlerts)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]any{"status": "accepted", "duplicate": duplicate, "points": len(batch.Points)})
}

func (s *Server) telemetryCatalog(w http.ResponseWriter, r *http.Request) {
	nodes, ok := s.requestGroupNodeSet(w, r)
	if !ok {
		return
	}
	writeJSON(w, s.telemetry.CatalogForNodes(nodes))
}

func (s *Server) telemetrySources(w http.ResponseWriter, r *http.Request) {
	nodes, ok := s.requestGroupNodeSet(w, r)
	if !ok {
		return
	}
	writeJSON(w, map[string]any{
		"native":  true,
		"storage": "embedded-segment-store",
		"sources": s.telemetry.SourcesForNodes(nodes),
		"adapters": []map[string]any{
			{"id": "native", "mode": "push", "status": "ready", "path": "/api/v1/telemetry/batches"},
			{"id": "prometheus", "mode": "compatibility", "status": "ready", "path": "/api/v1/ingest/prometheus", "format": "text-exposition"},
			{"id": "otlp", "mode": "compatibility", "status": "ready", "path": "/v1/metrics", "format": "otlp-json"},
			{"id": "influx", "mode": "compatibility", "status": "ready", "path": "/api/v1/ingest/influx", "format": "line-protocol"},
		},
	})
}

func (s *Server) ingestCompatibility(format string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		nodeID := strings.TrimSpace(r.URL.Query().Get("node"))
		if nodeID == "" {
			nodeID = strings.TrimSpace(r.Header.Get("X-Node-ID"))
		}
		if !validID.MatchString(nodeID) {
			writeError(w, http.StatusBadRequest, "node query parameter or X-Node-ID header is required")
			return
		}
		now := time.Now().UTC()
		r.Body = http.MaxBytesReader(w, r.Body, 4<<20)
		var points []telemetry.Point
		var err error
		switch format {
		case "prometheus":
			points, err = compat.ParsePrometheus(r.Body, now)
		case "influx":
			points, err = compat.ParseInflux(r.Body, now, r.URL.Query().Get("precision"))
		case "otlp":
			points, err = compat.ParseOTLPJSON(r.Body, now)
		default:
			err = fmt.Errorf("unsupported compatibility format")
		}
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		sequence := uint64(0)
		if raw := strings.TrimSpace(r.Header.Get("X-Telemetry-Sequence")); raw != "" {
			sequence, err = strconv.ParseUint(raw, 10, 64)
			if err != nil {
				writeError(w, http.StatusBadRequest, "X-Telemetry-Sequence must be an unsigned integer")
				return
			}
		}
		batch := telemetry.Batch{Schema: telemetry.BatchSchema, NodeID: nodeID, Source: format, Sequence: sequence, SentAt: now.Format(time.RFC3339Nano), Points: points}
		s.mu.Lock()
		duplicate, err := s.telemetry.Append(batch)
		if err != nil {
			s.mu.Unlock()
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		newAlerts := []Alert{}
		if !duplicate {
			newAlerts, err = s.evaluateRulesLocked(batch)
		}
		s.mu.Unlock()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "evaluate metric rules: "+err.Error())
			return
		}
		if s.webhookURL != "" && len(newAlerts) > 0 {
			go s.notifyWebhook(newAlerts)
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "accepted", "format": format, "duplicate": duplicate, "points": len(points)})
	}
}

func (s *Server) queryTelemetry(w http.ResponseWriter, r *http.Request) {
	nodes, ok := s.requestGroupNodeSet(w, r)
	if !ok {
		return
	}
	values := r.URL.Query()
	metric := strings.TrimSpace(values.Get("metric"))
	if metric == "" {
		writeError(w, http.StatusBadRequest, "metric is required")
		return
	}
	end, err := queryTime(values.Get("end"), time.Now().UTC())
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid end: "+err.Error())
		return
	}
	start, err := queryTime(values.Get("start"), end.Add(-time.Hour))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid start: "+err.Error())
		return
	}
	if !start.Before(end) || end.Sub(start) > 31*24*time.Hour {
		writeError(w, http.StatusBadRequest, "query range must be positive and at most 31 days")
		return
	}
	step := 15 * time.Second
	if raw := values.Get("step"); raw != "" {
		step, err = time.ParseDuration(raw)
		if err != nil || step < time.Second || step > 24*time.Hour {
			writeError(w, http.StatusBadRequest, "step must be between 1s and 24h")
			return
		}
	}
	aggregate := values.Get("aggregate")
	if aggregate == "" {
		aggregate = "avg"
	}
	allowedAggregates := map[string]bool{"avg": true, "min": true, "max": true, "sum": true, "last": true, "rate": true}
	if !allowedAggregates[aggregate] {
		writeError(w, http.StatusBadRequest, "invalid aggregate")
		return
	}
	labels := map[string]string{}
	for _, raw := range values["label"] {
		key, value, ok := strings.Cut(raw, "=")
		if !ok || strings.TrimSpace(key) == "" {
			writeError(w, http.StatusBadRequest, "labels must use key=value")
			return
		}
		labels[strings.TrimSpace(key)] = value
	}
	writeJSON(w, s.telemetry.Query(telemetry.Query{Metric: metric, NodeID: values.Get("node"), Source: values.Get("source"), Labels: labels, StartMS: start.UnixMilli(), EndMS: end.UnixMilli(), Step: step, Aggregate: aggregate, NodeIDs: nodes}))
}

func queryTime(raw string, fallback time.Time) (time.Time, error) {
	if raw == "" {
		return fallback, nil
	}
	if unixMS, err := strconv.ParseInt(raw, 10, 64); err == nil {
		return time.UnixMilli(unixMS), nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("expected Unix milliseconds or RFC3339")
	}
	return parsed, nil
}
