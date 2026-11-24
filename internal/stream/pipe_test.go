package stream_test

import (
	"context"
	"testing"
	"time"

	"medpipe/internal/stream"
)

func TestPipeSlowConsumerBackpressureNeverExceedsBudget(t *testing.T) {
	const limit = int64(4096)
	p := stream.NewPipe(limit)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		for i := 0; i < 20; i++ {
			if err := p.Submit(ctx, stream.Envelope{ByteSize: 1024, Kind: "x"}); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && p.UsedBytes() < limit {
		time.Sleep(time.Millisecond)
	}
	if used := p.UsedBytes(); used != limit {
		t.Fatalf("used=%d want saturated budget %d", used, limit)
	}
	if peak := p.PeakBytes(); peak > limit {
		t.Fatalf("peak=%d exceeds limit %d", peak, limit)
	}
	if p.Budget().WaitCount() == 0 {
		t.Fatal("expected producer backpressure wait")
	}

	select {
	case err := <-done:
		t.Fatalf("producer should be blocked by backpressure, returned err=%v", err)
	case <-time.After(20 * time.Millisecond):
	}

	// Cancelling unblocks the producer; no Receive/Ack happened in this test,
	// and every observed occupancy remained bounded.
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("blocked producer did not exit after cancellation")
	}
	if p.PeakBytes() > limit {
		t.Fatalf("peak=%d exceeds limit after cancel", p.PeakBytes())
	}
}
