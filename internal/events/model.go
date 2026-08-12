package events

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

const BatchSchema = "event.batch/v1"
const MaxBatchEvents = 5000

var (
	identityPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	eventIDPattern  = regexp.MustCompile(`^evt_[A-Za-z0-9._-]{8,76}$`)
)

type Entry struct {
	ID          string            `json:"id"`
	TimestampMS int64             `json:"timestamp_ms"`
	Kind        string            `json:"kind"`
	Severity    string            `json:"severity"`
	Service     string            `json:"service,omitempty"`
	Message     string            `json:"message"`
	Attributes  map[string]string `json:"attributes,omitempty"`
}

type Batch struct {
	Schema   string  `json:"schema"`
	NodeID   string  `json:"node_id"`
	Source   string  `json:"source"`
	Sequence uint64  `json:"sequence"`
	SentAt   string  `json:"sent_at"`
	Events   []Entry `json:"events"`
}

func (batch *Batch) Normalize(now time.Time) {
	if batch.Schema == "" {
		batch.Schema = BatchSchema
	}
	if batch.Source == "" {
		batch.Source = "native-agent"
	}
	if batch.SentAt == "" {
		batch.SentAt = now.UTC().Format(time.RFC3339Nano)
	}
	for index := range batch.Events {
		entry := &batch.Events[index]
		if entry.TimestampMS == 0 {
			entry.TimestampMS = now.UnixMilli()
		}
		if entry.Kind == "" {
			entry.Kind = "log"
		}
		if entry.Severity == "" {
			entry.Severity = "info"
		}
		if entry.ID == "" {
			hash := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%d\x00%s\x00%s", batch.NodeID, batch.Source, entry.TimestampMS, entry.Service, entry.Message)))
			entry.ID = "evt_" + hex.EncodeToString(hash[:12])
		}
	}
}

func ValidateBatch(batch Batch, now time.Time, maxEvents int) error {
	if batch.Schema != BatchSchema {
		return fmt.Errorf("unsupported event schema %q", batch.Schema)
	}
	if !identityPattern.MatchString(batch.NodeID) || !identityPattern.MatchString(batch.Source) {
		return errors.New("invalid node_id or source")
	}
	if _, err := time.Parse(time.RFC3339Nano, batch.SentAt); err != nil {
		return errors.New("sent_at must be RFC3339")
	}
	if maxEvents <= 0 || maxEvents > MaxBatchEvents {
		maxEvents = MaxBatchEvents
	}
	if len(batch.Events) == 0 || len(batch.Events) > maxEvents {
		return fmt.Errorf("events must contain between 1 and %d items", maxEvents)
	}
	oldest := now.Add(-400 * 24 * time.Hour).UnixMilli()
	newest := now.Add(10 * time.Minute).UnixMilli()
	allowedSeverity := map[string]bool{"debug": true, "info": true, "warning": true, "error": true, "critical": true}
	for index, entry := range batch.Events {
		if !eventIDPattern.MatchString(entry.ID) {
			return fmt.Errorf("event %d has invalid id", index)
		}
		if entry.TimestampMS < oldest || entry.TimestampMS > newest {
			return fmt.Errorf("event %d timestamp is outside the accepted window", index)
		}
		if !identityPattern.MatchString(entry.Kind) || !allowedSeverity[entry.Severity] {
			return fmt.Errorf("event %d has invalid kind or severity", index)
		}
		if strings.TrimSpace(entry.Message) == "" || len(entry.Message) > 16384 || len(entry.Service) > 200 || len(entry.Attributes) > 32 {
			return fmt.Errorf("event %d has invalid message, service, or attributes", index)
		}
		for key, value := range entry.Attributes {
			if !identityPattern.MatchString(key) || len(value) > 1000 {
				return fmt.Errorf("event %d has invalid attribute", index)
			}
		}
	}
	return nil
}
