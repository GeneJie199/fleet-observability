package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/GeneJie199/fleet-observability/internal/telemetry"
)

func TestSpoolRetainsFailedBatchAndDrainsInOrder(t *testing.T) {
	queue, err := openSpool(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	for sequence := uint64(1); sequence <= 3; sequence++ {
		if err = queue.enqueue(telemetry.Batch{Schema: telemetry.BatchSchema, NodeID: "node-1", Source: "native-agent", Sequence: sequence}); err != nil {
			t.Fatal(err)
		}
	}

	failure := errors.New("center unavailable")
	if sent, drainErr := queue.drain(context.Background(), func(context.Context, telemetry.Batch) error { return failure }); sent != 0 || !errors.Is(drainErr, failure) {
		t.Fatalf("first drain = (%d, %v)", sent, drainErr)
	}
	if count, _, pendingErr := queue.pending(); pendingErr != nil || count != 3 {
		t.Fatalf("pending after failure = (%d, %v)", count, pendingErr)
	}

	var sequences []uint64
	sent, err := queue.drain(context.Background(), func(_ context.Context, batch telemetry.Batch) error {
		sequences = append(sequences, batch.Sequence)
		return nil
	})
	if err != nil || sent != 3 {
		t.Fatalf("second drain = (%d, %v)", sent, err)
	}
	if len(sequences) != 3 || sequences[0] != 1 || sequences[1] != 2 || sequences[2] != 3 {
		t.Fatalf("send order = %v", sequences)
	}
	if count, size, pendingErr := queue.pending(); pendingErr != nil || count != 0 || size != 0 {
		t.Fatalf("pending after recovery = (%d, %d, %v)", count, size, pendingErr)
	}
}

func TestSpoolSequenceSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	first, err := openSpool(dir, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if sequence, nextErr := first.nextSequence(); nextErr != nil || sequence != 1 {
		t.Fatalf("first sequence = (%d, %v)", sequence, nextErr)
	}
	second, err := openSpool(dir, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if sequence, nextErr := second.nextSequence(); nextErr != nil || sequence != 2 {
		t.Fatalf("restarted sequence = (%d, %v)", sequence, nextErr)
	}
}
