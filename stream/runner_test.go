package stream

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/rtp"

	"medpipe/timeline"
)

func TestQueueHardLimitAndReject(t *testing.T) {
	if _, err := NewBoundedQueue(0); err == nil {
		t.Fatal("zero capacity must be rejected")
	}
	if _, err := NewBoundedQueue(MaxQueueBlocks + 1); err == nil {
		t.Fatalf("capacity above %d must be rejected", MaxQueueBlocks)
	}
	q, err := NewBoundedQueue(2)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if err := q.Push(ctx, QueuedBlock{TrackID: "t", Sequence: i}); err != nil {
			t.Fatal(err)
		}
	}
	// Third push must block; prove it by timing out.
	done := make(chan error, 1)
	go func() { done <- q.Push(ctx, QueuedBlock{TrackID: "t", Sequence: 2}) }()
	select {
	case err := <-done:
		t.Fatalf("push past capacity did not block: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	b, ok, err := q.Pop(ctx)
	if err != nil || !ok || b.Sequence != 0 {
		t.Fatalf("pop = %v ok=%v err=%v", b, ok, err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("blocked push should resolve after pop: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked push never resolved")
	}
	if st := q.Stats(); st.PeakDepth > 2 || st.InFlightCap != 4 {
		t.Fatalf("bounded-memory stats wrong: %+v", st)
	}
}

func TestQueuePushAfterCloseAndDrain(t *testing.T) {
	q, _ := NewBoundedQueue(4)
	ctx := context.Background()
	_ = q.Push(ctx, QueuedBlock{Sequence: 0})
	q.Close()
	if err := q.Push(ctx, QueuedBlock{Sequence: 1}); err == nil {
		t.Fatal("push after close must fail")
	}
	if _, ok, _ := q.Pop(ctx); !ok {
		t.Fatal("queued block must still drain after close")
	}
	if _, ok, _ := q.Pop(ctx); ok {
		t.Fatal("empty closed queue must report termination")
	}
}

func TestSlowConsumerBackpressureAndPeak(t *testing.T) {
	const cap = 4
	q, _ := NewBoundedQueue(cap)
	store := NewMemCheckpoint()
	var processed int64
	slow := func(ctx context.Context, b QueuedBlock) (timeline.Interval, error) {
		time.Sleep(2 * time.Millisecond) // simulate a long-running consumer
		atomic.AddInt64(&processed, 1)
		return timeline.Interval{Start: b.Start, End: b.Start + 10}, nil
	}
	r, err := NewRunner(q, store, slow, 1)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	producer := func(ctx context.Context) error {
		for i := 0; i < 100; i++ {
			if err := q.Push(ctx, QueuedBlock{TrackID: "t1", Sequence: i, Start: int64(i * 10)}); err != nil {
				return err
			}
		}
		return nil
	}
	r.StartProducers(ctx, producer)
	r.StartConsumers(ctx)
	if err := r.Wait(); err != nil {
		t.Fatalf("runner failed: %v", err)
	}
	if got := atomic.LoadInt64(&processed); got != 100 {
		t.Fatalf("processed %d blocks, want 100", got)
	}
	st := r.QueueStats()
	if st.PeakDepth > cap {
		t.Fatalf("peak queue depth %d exceeded configured capacity %d", st.PeakDepth, cap)
	}
	if st.Pushed != 100 || st.Popped != 100 {
		t.Fatalf("counters wrong: %+v", st)
	}
	cps := store.All()
	if len(cps) != 1 || cps[0].BlockIndex != 99 || cps[0].Position != 1000 {
		t.Fatalf("checkpoint not advanced: %+v", cps)
	}
}

func TestRunnerCancellationExitsAllGoroutines(t *testing.T) {
	q, _ := NewBoundedQueue(64)
	release := make(chan struct{})
	started := make(chan struct{}, 64)
	proc := func(ctx context.Context, b QueuedBlock) (timeline.Interval, error) {
		started <- struct{}{}
		select {
		case <-release:
			return timeline.Interval{End: b.Start + 1}, nil
		case <-ctx.Done():
			return timeline.Interval{}, ctx.Err()
		}
	}
	r, _ := NewRunner(q, NewMemCheckpoint(), proc, 2)
	ctx, cancel := context.WithCancel(context.Background())
	producer := func(ctx context.Context) error {
		for i := 0; ; i++ {
			if err := q.Push(ctx, QueuedBlock{TrackID: "t", Sequence: i}); err != nil {
				return err
			}
		}
	}
	r.StartProducers(ctx, producer)
	r.StartConsumers(ctx)
	<-started
	cancel()
	done := make(chan struct{})
	go func() { _ = r.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("goroutines did not exit after cancellation")
	}
	close(release)
}

func TestRTPValidatorSequenceAndTimestamp(t *testing.T) {
	v := NewRTPValidator("rt1")
	mk := func(seq uint16, ts uint32) *rtp.Packet {
		return &rtp.Packet{Header: rtp.Header{SequenceNumber: seq, Timestamp: ts}}
	}
	if err := v.Observe(mk(10, 1000)); err != nil {
		t.Fatal(err)
	}
	if err := v.Observe(mk(10, 1000)); err == nil {
		t.Fatal("duplicate sequence must be rejected")
	}
	if err := v.Observe(mk(11, 900)); err == nil {
		t.Fatal("backwards timestamp must be rejected")
	}
	if err := v.Observe(mk(40, 2000)); err == nil {
		t.Fatal("large sequence jump must be rejected")
	}
	// Modular wrap-around: 65535 -> 0 is one step forward.
	v2 := NewRTPValidator("rt2")
	_ = v2.Observe(mk(65535, 5000))
	if err := v2.Observe(mk(0, 5200)); err != nil {
		t.Fatalf("wrap-around step rejected: %v", err)
	}
	// Malformed datagram is rejected at the parse entry point.
	if _, err := v2.CheckPacket([]byte{0x80}); err == nil {
		t.Fatal("truncated RTP packet must be rejected")
	}
}

func TestCheckpointResumeAndMonotonicity(t *testing.T) {
	store := NewMemCheckpoint()
	// Simulated failed run: five blocks commit, then the upstream fails.
	for i := 0; i < 5; i++ {
		if err := store.Commit(timeline.Checkpoint{TrackID: "t1", BlockIndex: i, Position: int64((i + 1) * 10)}); err != nil {
			t.Fatal(err)
		}
	}
	cp, ok := store.Load("t1")
	if !ok || cp.BlockIndex != 4 || cp.Position != 50 {
		t.Fatalf("checkpoint = %+v ok=%v", cp, ok)
	}
	// Committed state survives the failure: a regression is refused...
	if err := store.Commit(timeline.Checkpoint{TrackID: "t1", BlockIndex: 1, Position: 20}); err == nil {
		t.Fatal("checkpoint regression must be rejected")
	}
	// ...while the repaired run resumes at block 5 and advances.
	for i := 5; i < 8; i++ {
		if err := store.Commit(timeline.Checkpoint{TrackID: "t1", BlockIndex: i, Position: int64((i + 1) * 10)}); err != nil {
			t.Fatal(err)
		}
	}
	cp, _ = store.Load("t1")
	if cp.BlockIndex != 7 || cp.Position != 80 {
		t.Fatalf("resumed checkpoint = %+v", cp)
	}
}
