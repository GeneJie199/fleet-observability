package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/GeneJie199/fleet-observability/internal/events"
)

func TestEventSpoolRetainsFailureAndDrainsInOrder(t *testing.T) {
	queue, err := openEventSpool(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	for sequence := uint64(1); sequence <= 2; sequence++ {
		if err := queue.enqueue(events.Batch{Schema: events.BatchSchema, NodeID: "node-a", Source: "native-agent", Sequence: sequence}); err != nil {
			t.Fatal(err)
		}
	}
	failure := errors.New("offline")
	if sent, drainErr := queue.drain(context.Background(), func(context.Context, events.Batch) error { return failure }); sent != 0 || !errors.Is(drainErr, failure) {
		t.Fatalf("failed drain = (%d, %v)", sent, drainErr)
	}
	var sequences []uint64
	if sent, drainErr := queue.drain(context.Background(), func(_ context.Context, batch events.Batch) error {
		sequences = append(sequences, batch.Sequence)
		return nil
	}); sent != 2 || drainErr != nil {
		t.Fatalf("recovery drain = (%d, %v)", sent, drainErr)
	}
	if len(sequences) != 2 || sequences[0] != 1 || sequences[1] != 2 {
		t.Fatalf("sequences = %v", sequences)
	}
	if count, size, pendingErr := queue.pending(); pendingErr != nil || count != 0 || size != 0 {
		t.Fatalf("pending = (%d, %d, %v)", count, size, pendingErr)
	}
}
