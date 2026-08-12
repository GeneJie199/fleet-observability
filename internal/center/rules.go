package center

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/GeneJie199/fleet-observability/internal/telemetry"
)

func (s *Server) ensureDefaultRules() error {
	path := filepath.Join(s.dir, "metric-rules.json")
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	rules := []MetricRule{
		{ID: "cpu-high", Name: "CPU 使用率过高", Description: "主机 CPU 使用率超过运行阈值。", Metric: "cpu_percent", Operator: "gte", Threshold: 85, Severity: "warning", Enabled: true, CreatedAt: now, UpdatedAt: now},
		{ID: "memory-high", Name: "内存使用率过高", Description: "主机内存使用率超过运行阈值。", Metric: "memory_percent", Operator: "gte", Threshold: 85, Severity: "warning", Enabled: true, CreatedAt: now, UpdatedAt: now},
		{ID: "disk-high", Name: "磁盘使用率过高", Description: "根文件系统使用率超过运行阈值。", Metric: "disk_percent", Operator: "gte", Threshold: 85, Severity: "warning", Enabled: true, CreatedAt: now, UpdatedAt: now},
	}
	return writeJSONFile(path, rules)
}

func (s *Server) listRules(w http.ResponseWriter, _ *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rules, err := s.readRules()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	states, _ := s.readRuleStates()
	counts := map[string]map[string]int{}
	for _, state := range states {
		if counts[state.RuleID] == nil {
			counts[state.RuleID] = map[string]int{"firing": 0, "pending": 0}
		}
		if state.Firing {
			counts[state.RuleID]["firing"]++
		} else if state.PendingSinceMS > 0 {
			counts[state.RuleID]["pending"]++
		}
	}
	writeJSON(w, map[string]any{"rules": rules, "states": counts})
}

func (s *Server) createRule(w http.ResponseWriter, r *http.Request) {
	var input MetricRule
	if err := decodeBody(w, r, &input); err != nil {
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	input.ID = strings.TrimSpace(input.ID)
	if input.ID == "" {
		input.ID = stableID("rule", input.Name, input.Metric, now)
	}
	input.CreatedAt, input.UpdatedAt = now, now
	if err := validateRule(input); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rules, err := s.readRules()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, rule := range rules {
		if rule.ID == input.ID {
			writeError(w, http.StatusConflict, "metric rule already exists")
			return
		}
	}
	rules = append(rules, input)
	if err := s.writeRules(rules); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(input)
}

func (s *Server) updateRule(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var input MetricRule
	if err := decodeBody(w, r, &input); err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rules, err := s.readRules()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for index := range rules {
		if rules[index].ID != id {
			continue
		}
		input.ID = id
		input.CreatedAt = rules[index].CreatedAt
		input.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		if err := validateRule(input); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		rules[index] = input
		if err := s.writeRules(rules); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if err := s.resetRuleLocked(id); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, input)
		return
	}
	writeError(w, http.StatusNotFound, "metric rule not found")
}

func (s *Server) deleteRule(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.mu.Lock()
	defer s.mu.Unlock()
	rules, err := s.readRules()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	found := false
	out := rules[:0]
	for _, rule := range rules {
		if rule.ID == id {
			found = true
			continue
		}
		out = append(out, rule)
	}
	if !found {
		writeError(w, http.StatusNotFound, "metric rule not found")
		return
	}
	if err := s.writeRules(out); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.resetRuleLocked(id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func validateRule(rule MetricRule) error {
	if !validID.MatchString(rule.ID) {
		return errors.New("invalid metric rule id")
	}
	if strings.TrimSpace(rule.Name) == "" || len(rule.Name) > 120 || len(rule.Description) > 1000 {
		return errors.New("rule name is required and must be at most 120 characters")
	}
	if rule.Metric == "" || len(rule.Metric) > 200 {
		return errors.New("metric is required")
	}
	if math.IsNaN(rule.Threshold) || math.IsInf(rule.Threshold, 0) {
		return errors.New("threshold must be finite")
	}
	if !map[string]bool{"gt": true, "gte": true, "lt": true, "lte": true, "eq": true, "neq": true}[rule.Operator] {
		return errors.New("operator must be gt, gte, lt, lte, eq, or neq")
	}
	if rule.Severity != "info" && rule.Severity != "warning" && rule.Severity != "critical" {
		return errors.New("severity must be info, warning, or critical")
	}
	if rule.ForSeconds < 0 || rule.ForSeconds > 31*24*60*60 || len(rule.Labels) > 32 {
		return errors.New("for_seconds or label selector is outside the accepted limit")
	}
	return nil
}

func (s *Server) evaluateRulesLocked(batch telemetry.Batch) ([]Alert, error) {
	rules, err := s.readRules()
	if err != nil {
		return nil, err
	}
	states, err := s.readRuleStates()
	if err != nil {
		return nil, err
	}
	stateByKey := map[string]*MetricRuleState{}
	for index := range states {
		stateByKey[states[index].Key] = &states[index]
	}
	alerts, err := s.readAlerts()
	if err != nil {
		return nil, err
	}
	alertByID := map[string]int{}
	for index := range alerts {
		alertByID[alerts[index].ID] = index
	}
	newAlerts := []Alert{}
	for _, rule := range rules {
		if !rule.Enabled || (rule.NodeID != "" && rule.NodeID != batch.NodeID) || (rule.Source != "" && rule.Source != batch.Source) {
			continue
		}
		seriesPoints := map[string][]telemetry.Point{}
		for _, point := range batch.Points {
			if point.Metric == rule.Metric && labelsContain(point.Labels, rule.Labels) {
				key := telemetry.SeriesKey(batch.NodeID, batch.Source, point)
				seriesPoints[key] = append(seriesPoints[key], point)
			}
		}
		for seriesKey, points := range seriesPoints {
			sort.Slice(points, func(i, j int) bool { return points[i].TimestampMS < points[j].TimestampMS })
			key := rule.ID + "\x00" + seriesKey
			state := stateByKey[key]
			if state == nil {
				alertID := stableID("alert", "rule", rule.ID, seriesKey)
				states = append(states, MetricRuleState{Key: key, RuleID: rule.ID, AlertID: alertID, NodeID: batch.NodeID, Source: batch.Source})
				state = &states[len(states)-1]
				stateByKey[key] = state
			}
			for _, point := range points {
				state.LastSeenMS, state.LastValue = point.TimestampMS, point.Value
				if compareMetric(point.Value, rule.Operator, rule.Threshold) {
					if state.PendingSinceMS == 0 {
						state.PendingSinceMS = point.TimestampMS
					}
					state.Firing = point.TimestampMS-state.PendingSinceMS >= int64(rule.ForSeconds)*1000
				} else {
					state.PendingSinceMS, state.Firing = 0, false
				}
			}
			labels := points[len(points)-1].Labels
			if state.Firing {
				now := time.UnixMilli(state.LastSeenMS).UTC().Format(time.RFC3339Nano)
				alert := Alert{ID: state.AlertID, NodeID: batch.NodeID, Severity: rule.Severity, Kind: "rule." + rule.ID, Title: rule.Name, Detail: fmt.Sprintf("%s %.4g %s %.4g for %ds", rule.Metric, state.LastValue, rule.Operator, rule.Threshold, rule.ForSeconds), Status: "open", ObservedAt: now, UpdatedAt: now, Value: state.LastValue, Threshold: rule.Threshold, Evidence: map[string]any{"rule_id": rule.ID, "metric": rule.Metric, "source": batch.Source, "labels": labels, "pending_since_ms": state.PendingSinceMS}}
				if index, ok := alertByID[alert.ID]; ok {
					if alerts[index].Status != "resolved" {
						alert.Status, alert.Assignee, alert.Note = alerts[index].Status, alerts[index].Assignee, alerts[index].Note
					} else {
						newAlerts = append(newAlerts, alert)
					}
					alerts[index] = alert
				} else {
					alertByID[alert.ID] = len(alerts)
					alerts = append(alerts, alert)
					newAlerts = append(newAlerts, alert)
				}
			} else if index, ok := alertByID[state.AlertID]; ok && alerts[index].Status != "resolved" {
				alerts[index].Status = "resolved"
				alerts[index].UpdatedAt = time.UnixMilli(state.LastSeenMS).UTC().Format(time.RFC3339Nano)
			}
		}
	}
	if err := s.writeRuleStates(states); err != nil {
		return nil, err
	}
	if err := s.writeAlerts(alerts); err != nil {
		return nil, err
	}
	return newAlerts, nil
}

func compareMetric(value float64, operator string, threshold float64) bool {
	switch operator {
	case "gt":
		return value > threshold
	case "gte":
		return value >= threshold
	case "lt":
		return value < threshold
	case "lte":
		return value <= threshold
	case "eq":
		return value == threshold
	case "neq":
		return value != threshold
	default:
		return false
	}
}

func labelsContain(labels, wanted map[string]string) bool {
	for key, value := range wanted {
		if labels[key] != value {
			return false
		}
	}
	return true
}

func (s *Server) readRules() ([]MetricRule, error) {
	var rules []MetricRule
	err := readJSONFile(filepath.Join(s.dir, "metric-rules.json"), &rules)
	if errors.Is(err, os.ErrNotExist) {
		return []MetricRule{}, nil
	}
	return rules, err
}

func (s *Server) writeRules(rules []MetricRule) error {
	sort.Slice(rules, func(i, j int) bool { return rules[i].ID < rules[j].ID })
	return writeJSONFile(filepath.Join(s.dir, "metric-rules.json"), rules)
}

func (s *Server) readRuleStates() ([]MetricRuleState, error) {
	var states []MetricRuleState
	err := readJSONFile(filepath.Join(s.dir, "metric-rule-states.json"), &states)
	if errors.Is(err, os.ErrNotExist) {
		return []MetricRuleState{}, nil
	}
	return states, err
}

func (s *Server) writeRuleStates(states []MetricRuleState) error {
	return writeJSONFile(filepath.Join(s.dir, "metric-rule-states.json"), states)
}

func (s *Server) resetRuleLocked(ruleID string) error {
	states, err := s.readRuleStates()
	if err != nil {
		return err
	}
	kept := states[:0]
	for _, state := range states {
		if state.RuleID != ruleID {
			kept = append(kept, state)
		}
	}
	if err := s.writeRuleStates(kept); err != nil {
		return err
	}
	alerts, err := s.readAlerts()
	if err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	for index := range alerts {
		if alerts[index].Kind == "rule."+ruleID && alerts[index].Status != "resolved" {
			alerts[index].Status = "resolved"
			alerts[index].UpdatedAt = now
		}
	}
	return s.writeAlerts(alerts)
}
