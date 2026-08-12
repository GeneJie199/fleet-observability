package events

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStoreAppendQuerySearchPaginationRestartAndDedup(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC().Truncate(time.Second)
	store, err := OpenStore(dir, Options{MaxEvents: 100})
	if err != nil {
		t.Fatal(err)
	}
	batch := Batch{Schema: BatchSchema, NodeID: "node-a", Source: "file-log", Sequence: 4, SentAt: now.Format(time.RFC3339Nano), Events: []Entry{
		{TimestampMS: now.Add(-2 * time.Minute).UnixMilli(), Kind: "log", Severity: "info", Service: "checkout", Message: "server started"},
		{TimestampMS: now.Add(-time.Minute).UnixMilli(), Kind: "log", Severity: "error", Service: "checkout", Message: "database timeout", Attributes: map[string]string{"request_id": "req-1"}},
	}}
	if duplicate, appendErr := store.Append(batch); appendErr != nil || duplicate {
		t.Fatalf("append = (%v, %v)", duplicate, appendErr)
	}
	if duplicate, appendErr := store.Append(batch); appendErr != nil || !duplicate {
		t.Fatalf("dedup = (%v, %v)", duplicate, appendErr)
	}
	result := store.Query(Query{Search: "DATABASE req-1", StartMS: now.Add(-time.Hour).UnixMilli(), EndMS: now.UnixMilli(), Limit: 1})
	if len(result.Events) != 1 || result.Events[0].Severity != "error" || result.TotalMatched != 1 {
		t.Fatalf("query = %+v", result)
	}
	if err := os.Remove(filepath.Join(dir, "sequences.json")); err != nil {
		t.Fatal(err)
	}
	restarted, err := OpenStore(dir, Options{MaxEvents: 100})
	if err != nil {
		t.Fatal(err)
	}
	result = restarted.Query(Query{StartMS: now.Add(-time.Hour).UnixMilli(), EndMS: now.UnixMilli(), Limit: 1})
	if len(result.Events) != 1 || result.NextBeforeMS == 0 || result.TotalMatched != 2 {
		t.Fatalf("page 1 = %+v", result)
	}
	result = restarted.Query(Query{StartMS: now.Add(-time.Hour).UnixMilli(), EndMS: now.UnixMilli(), BeforeMS: result.NextBeforeMS, Limit: 1})
	if len(result.Events) != 1 || result.Events[0].Message != "server started" {
		t.Fatalf("page 2 = %+v", result)
	}
	if restarted.Catalog().Total != 2 || len(restarted.Sources()) != 1 {
		t.Fatalf("catalog=%+v sources=%+v", restarted.Catalog(), restarted.Sources())
	}
	if duplicate, appendErr := restarted.Append(batch); appendErr != nil || !duplicate {
		t.Fatalf("recovered sequence dedup = (%v, %v)", duplicate, appendErr)
	}
	other := Batch{Schema: BatchSchema, NodeID: "node-b", Source: "native", Sequence: 1, SentAt: now.Format(time.RFC3339Nano), Events: []Entry{{TimestampMS: now.Add(-30 * time.Second).UnixMilli(), Kind: "change", Severity: "warning", Message: "other node"}}}
	if _, err := restarted.Append(other); err != nil {
		t.Fatal(err)
	}
	nodes := map[string]bool{"node-a": true}
	filtered := restarted.Query(Query{NodeIDs: nodes, StartMS: now.Add(-time.Hour).UnixMilli(), EndMS: now.UnixMilli()})
	if filtered.TotalMatched != 2 || restarted.CatalogForNodes(nodes).Total != 2 || len(restarted.SourcesForNodes(nodes)) != 1 {
		t.Fatalf("filtered query=%+v catalog=%+v sources=%+v", filtered, restarted.CatalogForNodes(nodes), restarted.SourcesForNodes(nodes))
	}
}

func TestStoreRejectsInvalidEvent(t *testing.T) {
	store, err := OpenStore(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	batch := Batch{NodeID: "node-a", Source: "native", Events: []Entry{{Kind: "log", Severity: "invalid", Message: "bad"}}}
	if _, err := store.Append(batch); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestValidateBatchRejectsEventIDOutsideProtocol(t *testing.T) {
	now := time.Now().UTC()
	for _, id := range []string{"evt_short", "evt_invalid/value"} {
		batch := Batch{Schema: BatchSchema, NodeID: "node-a", Source: "native", SentAt: now.Format(time.RFC3339Nano), Events: []Entry{{ID: id, TimestampMS: now.UnixMilli(), Kind: "log", Severity: "info", Message: "event"}}}
		if err := ValidateBatch(batch, now, 10); err == nil {
			t.Fatalf("event id %q passed validation", id)
		}
	}
}

func TestConfiguredBatchLimitCannotExceedProtocol(t *testing.T) {
	now := time.Now().UTC()
	entries := make([]Entry, MaxBatchEvents+1)
	for index := range entries {
		entries[index] = Entry{ID: fmt.Sprintf("evt_%08d", index), TimestampMS: now.UnixMilli(), Kind: "log", Severity: "info", Message: "event"}
	}
	batch := Batch{Schema: BatchSchema, NodeID: "node-a", Source: "native", SentAt: now.Format(time.RFC3339Nano), Events: entries}
	if err := ValidateBatch(batch, now, MaxBatchEvents*2); err == nil {
		t.Fatal("oversized protocol batch passed validation")
	}
}
