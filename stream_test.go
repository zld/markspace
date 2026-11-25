package mediakernel

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"
)

func buildStream(t *testing.T, frames ...[]byte) *FrameReader {
	t.Helper()
	var buf bytes.Buffer
	for _, f := range frames {
		buf.Write(f)
	}
	return NewFrameReader(&buf)
}

func mustFrame(t *testing.T, seq uint16, codec Codec, samples uint32, payload []byte) []byte {
	t.Helper()
	b, err := BuildFramedPacket(seq, codec, samples, payload, 0)
	if err != nil {
		t.Fatalf("build frame: %v", err)
	}
	return b
}

func TestPacketRoundTrip(t *testing.T) {
	reader := buildStream(t,
		mustFrame(t, 0, CodecOpus, 960, []byte("audio-0")),
		mustFrame(t, 1, CodecOpus, 480, []byte("audio-1")),
		mustFrame(t, 2, CodecAAC, 1024, []byte("audio-2")),
	)
	pkts, err := ReadAll(reader)
	if err != nil {
		t.Fatalf("read all: %v", err)
	}
	if len(pkts) != 3 {
		t.Fatalf("got %d packets, want 3", len(pkts))
	}
	if pkts[0].Seq != 0 || pkts[0].ValidSamples != 960 || pkts[0].PayloadLen != 7 || pkts[0].Offset != 0 {
		t.Fatalf("first packet fields wrong: %+v", pkts[0])
	}
	if pkts[1].Offset <= 0 {
		t.Fatal("second packet must carry a non-zero input offset")
	}
	if pkts[2].Codec != CodecAAC || pkts[2].ValidSamples != 1024 {
		t.Fatalf("aac packet fields wrong: %+v", pkts[2])
	}
}

func TestPacketInvalidDuration(t *testing.T) {
	reader := buildStream(t, mustFrame(t, 3, CodecOpus, 500, []byte("x")))
	_, err := reader.ReadPacket()
	me, ok := AsMediaError(err)
	if !ok {
		t.Fatalf("want MediaError, got %v", err)
	}
	if me.Phase != PhaseParse || me.Seq == nil || *me.Seq != 3 {
		t.Fatalf("error context wrong: %+v", me)
	}
}

func TestPacketTruncatedBody(t *testing.T) {
	good := mustFrame(t, 1, CodecOpus, 960, []byte("payload"))
	// Declare a full-length frame but cut the bytes short.
	cut := make([]byte, len(good)-3)
	copy(cut, good)
	reader := buildStream(t, cut)
	_, err := reader.ReadPacket()
	me, ok := AsMediaError(err)
	if !ok || me.Phase != PhaseParse || me.Offset != 0 {
		t.Fatalf("want offset-0 parse error, got %v", err)
	}
}

func TestPacketTruncatedFlag(t *testing.T) {
	bad, err := BuildFramedPacket(5, CodecAAC, 1024, []byte("p"), FlagTruncated)
	if err != nil {
		t.Fatal(err)
	}
	reader := buildStream(t, bad)
	_, err = reader.ReadPacket()
	me, ok := AsMediaError(err)
	if !ok || me.Seq == nil || *me.Seq != 5 {
		t.Fatalf("want flagged-truncation error with seq, got %v", err)
	}
}

func TestPacketEmptyStream(t *testing.T) {
	reader := buildStream(t)
	pkts, err := ReadAll(reader)
	if err != nil || pkts != nil {
		t.Fatalf("empty stream must EOF cleanly, got %v %v", pkts, err)
	}
	if _, err := reader.ReadPacket(); !errors.Is(err, io.EOF) {
		t.Fatalf("want io.EOF, got %v", err)
	}
}

func TestValidSamplesTable(t *testing.T) {
	if _, ok := ValidSamplesFor(CodecOpus, 960); !ok {
		t.Fatal("opus 960 must be valid")
	}
	if _, ok := ValidSamplesFor(CodecAAC, 480); ok {
		t.Fatal("aac 480 must be invalid")
	}
	if _, ok := ValidSamplesFor(CodecUnknown, 960); ok {
		t.Fatal("unknown codec must reject")
	}
}

func TestGapTrackerBasic(t *testing.T) {
	g := NewGapTracker()
	for _, seq := range []uint16{10, 11, 12} {
		dup, missing, err := g.Observe(seq)
		if dup || len(missing) != 0 || err != nil {
			t.Fatalf("seq %d misclassified", seq)
		}
	}
	dup, missing, err := g.Observe(11)
	if !dup || len(missing) != 0 || err != nil {
		t.Fatal("repeated 11 must be a duplicate")
	}
	dup, missing, err = g.Observe(15)
	if dup || err != nil || len(missing) != 2 || missing[0] != 13 || missing[1] != 14 {
		t.Fatalf("want missing 13,14 got %v err=%v", missing, err)
	}
	if g.GapCount() != 2 || g.DuplicateCount() != 1 {
		t.Fatalf("counts wrong: gaps=%d dups=%d", g.GapCount(), g.DuplicateCount())
	}
}

func TestGapTrackerWraparound(t *testing.T) {
	g := NewGapTracker()
	for _, seq := range []uint16{65534, 65535, 0, 1} {
		dup, missing, err := g.Observe(seq)
		if dup || err != nil || len(missing) != 0 {
			t.Fatalf("wrap seq %d: dup=%v missing=%v err=%v", seq, dup, missing, err)
		}
	}
	if g.WrapCount() != 1 {
		t.Fatalf("want one wrap, got %d", g.WrapCount())
	}
	// Loss across the boundary: after 1 jump to 4 => 2,3 missing.
	_, missing, err := g.Observe(4)
	if err != nil || len(missing) != 2 || missing[0] != 2 || missing[1] != 3 {
		t.Fatalf("wrap gap wrong: %v err=%v", missing, err)
	}
}

func TestGapTrackerBackwardsAndJump(t *testing.T) {
	g := NewGapTracker()
	_, _, _ = g.Observe(100)
	if _, _, err := g.Observe(99); err == nil {
		t.Fatal("backwards sequence must error")
	}
	if _, _, err := g.Observe(100 + MaxSequenceJump + 1); err == nil {
		t.Fatal("oversized jump must error")
	}
	g2 := NewGapTracker()
	_, _, _ = g2.Observe(100)
	if _, missing, err := g2.Observe(100 + MaxSequenceJump); err != nil || len(missing) != MaxSequenceJump-1 {
		t.Fatalf("boundary jump must be accepted as %d losses, got err=%v", MaxSequenceJump-1, err)
	}
}

func TestBoundedQueueBasicsAndPeak(t *testing.T) {
	q := NewBoundedQueue[int](3)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if err := q.Enqueue(ctx, i); err != nil {
			t.Fatalf("enqueue %d: %v", i, err)
		}
	}
	s := q.Stats()
	if s.Pending != 3 || s.Peak != 3 || s.Capacity != 3 || s.Enqueued != 3 {
		t.Fatalf("full queue stats wrong: %+v", s)
	}
	v, err := q.Dequeue(ctx)
	if err != nil || v != 0 {
		t.Fatalf("dequeue want 0, got %d err=%v", v, err)
	}
	if s := q.Stats(); s.Pending != 2 || s.Dequeued != 1 || s.Peak != 3 {
		t.Fatalf("post-dequeue stats wrong: %+v", s)
	}
}

func TestBoundedQueueBackpressure(t *testing.T) {
	q := NewBoundedQueue[int](2)
	ctx := context.Background()
	// Saturate the queue.
	if err := q.Enqueue(ctx, 1); err != nil {
		t.Fatalf("fill: %v", err)
	}
	if err := q.Enqueue(ctx, 2); err != nil {
		t.Fatalf("fill: %v", err)
	}
	blocked := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		close(blocked)
		done <- q.Enqueue(ctx, 3)
	}()
	<-blocked
	// Give the producer time to park on the full queue, then assert it stayed parked.
	time.Sleep(20 * time.Millisecond)
	select {
	case err := <-done:
		t.Fatalf("producer must be blocked by backpressure, returned %v", err)
	default:
	}
	if s := q.Stats(); s.Pending != 2 {
		t.Fatalf("queue must not grow past capacity: %+v", s)
	}
	if _, err := q.Dequeue(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("producer should proceed after a slot frees: %v", err)
	}
	if s := q.Stats(); s.Peak > s.Capacity || s.Pending != 2 {
		t.Fatalf("invariant violated: %+v", s)
	}
}

func TestBoundedQueueCancellation(t *testing.T) {
	q := NewBoundedQueue[int](1)
	ctx, cancel := context.WithCancel(context.Background())
	if err := q.Enqueue(context.Background(), 99); err != nil {
		t.Fatal(err)
	}
	errc := make(chan error, 1)
	go func() { errc <- q.Enqueue(ctx, 100) }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	if err := <-errc; err == nil {
		t.Fatal("cancelled full-queue enqueue must error")
	} else if me, ok := AsMediaError(err); !ok || me.Phase != PhaseCancelled {
		t.Fatalf("want cancellation media error, got %v", err)
	}
	// A consumer waiting on an empty queue must also exit on cancellation.
	empty := NewBoundedQueue[int](1)
	ctx2, cancel2 := context.WithCancel(context.Background())
	go func() { time.Sleep(10 * time.Millisecond); cancel2() }()
	if _, err := empty.Dequeue(ctx2); err == nil {
		t.Fatal("cancelled dequeue must error")
	}
}

func TestBoundedQueueConcurrentInvariant(t *testing.T) {
	q := NewBoundedQueue[int](4)
	ctx, cancel := context.WithCancel(context.Background())
	const n = 200
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			if err := q.Enqueue(ctx, i); err != nil {
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			if _, err := q.Dequeue(ctx); err != nil {
				return
			}
		}
	}()
	wg.Wait()
	cancel()
	s := q.Stats()
	if s.Peak > s.Capacity {
		t.Fatalf("peak %d exceeded capacity %d", s.Peak, s.Capacity)
	}
	if s.Enqueued != n || s.Dequeued != n || s.Pending != 0 {
		t.Fatalf("counters wrong: %+v", s)
	}
}

func TestCounters(t *testing.T) {
	var c Counters
	c.IncEnqueued()
	c.IncEnqueued()
	c.IncProcessed()
	enq, proc := c.Snapshot()
	if enq != 2 || proc != 1 {
		t.Fatalf("snapshot enq=%d proc=%d", enq, proc)
	}
}
