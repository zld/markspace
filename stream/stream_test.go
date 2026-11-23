package stream

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestBoundedQueueBlocksAndAppliesBackpressure(t *testing.T) {
	q := NewBoundedQueue(2, 64)
	ctx := context.Background()
	samples := []Sample{{Index: 0, Payload: []byte("a")}, {Index: 1, Payload: []byte("bb")}}
	for _, s := range samples {
		if err := q.Enqueue(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	if q.Len() != 2 || q.HighWater() != 2 {
		t.Fatalf("occupancy wrong: len=%d high=%d", q.Len(), q.HighWater())
	}
	blocked := make(chan struct{})
	go func() {
		_ = q.Enqueue(ctx, Sample{Index: 2, Payload: []byte("ccc")})
		close(blocked)
	}()
	select {
	case <-blocked:
		t.Fatal("enqueue must block while queue is full")
	case <-time.After(30 * time.Millisecond):
	}
	if _, ok, err := q.Dequeue(ctx); err != nil || !ok {
		t.Fatalf("dequeue failed: %v", err)
	}
	select {
	case <-blocked:
	case <-time.After(time.Second):
		t.Fatal("enqueue did not resume after dequeue")
	}
	if q.HighWater() != 2 {
		t.Fatal("queue must never exceed fixed capacity")
	}
}

func TestQueueCapacityAndPayloadCeiling(t *testing.T) {
	q := NewBoundedQueue(4, 16)
	if err := q.Enqueue(context.Background(), Sample{Payload: make([]byte, 16)}); err != nil {
		t.Fatalf("boundary-size sample must be accepted: %v", err)
	}
	if got := q.MemoryCeilingBytes(256); got != 4*16+256 {
		t.Fatalf("memory ceiling=%d want %d", got, 4*16+256)
	}
	if err := q.Enqueue(context.Background(), Sample{Payload: make([]byte, 17)}); err == nil {
		t.Fatal("oversized sample must be rejected")
	}
	q.Close()
	if err := q.Enqueue(context.Background(), Sample{Payload: []byte("x")}); !errors.Is(err, ErrQueueClosed) {
		t.Fatalf("closed queue must reject enqueue, got %v", err)
	}
	// The buffered boundary-size sample remains drainable after close.
	if _, ok, err := q.Dequeue(context.Background()); err != nil || !ok {
		t.Fatalf("drain after close failed: ok=%v err=%v", ok, err)
	}
}

func TestPipelineCancelPropagatesToQueueAndCheckpoint(t *testing.T) {
	q := NewBoundedQueue(2, 32)
	store := &MemoryStore{}
	p := NewPipeline(q, store)
	ctx, cancel := context.WithCancel(context.Background())
	var blocked int32
	p.SetBlockedHook(func() { atomic.AddInt32(&blocked, 1) })

	prodDone := make(chan struct{})
	producer := func(ctx context.Context, q *BoundedQueue) error {
		defer close(prodDone)
		for i := int64(0); ; i++ {
			if err := p.Produce(ctx, Sample{Index: i, TimeNS: i * 10, Payload: []byte("x")}); err != nil {
				return err
			}
		}
	}
	// Slow consumer: stall until the queue fills and backpressure is observed.
	consume := func(ctx context.Context, s Sample) error {
		time.Sleep(25 * time.Millisecond)
		return nil
	}
	go func() {
		time.Sleep(120 * time.Millisecond)
		cancel()
	}()
	cp, err := p.Run(ctx, func(ctx context.Context, q *BoundedQueue) error {
		err := producer(ctx, q)
		q.Close()
		return err
	}, consume)
	<-prodDone
	if !errors.Is(err, context.Canceled) || !p.Canceled() {
		t.Fatalf("cancellation must propagate: err=%v", err)
	}
	if atomic.LoadInt32(&blocked) == 0 {
		t.Fatal("slow consumer must trigger backpressure")
	}
	if cp.NextIndex == 0 {
		t.Fatal("last complete checkpoint must survive cancellation")
	}
}

func TestPipelineReplaySkipsCompletedFrames(t *testing.T) {
	store := &MemoryStore{}
	// Pretend frames 0..2 already completed before the interruption.
	if err := store.Save(Checkpoint{NextIndex: 3, Completed: 3}); err != nil {
		t.Fatal(err)
	}
	q := NewBoundedQueue(8, 32)
	p := NewPipeline(q, store)
	var processed []int64
	var mu sync.Mutex
	producer := func(ctx context.Context, q *BoundedQueue) error {
		for i := int64(0); i < 6; i++ {
			if err := q.Enqueue(ctx, Sample{Index: i, TimeNS: i * 10}); err != nil {
				return err
			}
		}
		q.Close()
		return nil
	}
	consume := func(ctx context.Context, s Sample) error {
		mu.Lock()
		processed = append(processed, s.Index)
		mu.Unlock()
		return nil
	}
	cp, err := p.Run(context.Background(), producer, consume)
	if err != nil {
		t.Fatal(err)
	}
	want := []int64{3, 4, 5}
	if len(processed) != len(want) {
		t.Fatalf("replay processed %v, want %v", processed, want)
	}
	for i := range want {
		if processed[i] != want[i] {
			t.Fatalf("replay processed %v, want %v", processed, want)
		}
	}
	if cp.NextIndex != 6 || cp.Completed != 6 {
		t.Fatalf("checkpoint after resume wrong: %+v", cp)
	}
}

func TestCheckpointStoreRoundTrip(t *testing.T) {
	s := &MemoryStore{}
	if _, ok, err := s.Load(); err != nil || ok {
		t.Fatal("empty store should report not found")
	}
	cp := Checkpoint{NextIndex: 7, TimeNS: 280, Completed: 7}
	if err := s.Save(cp); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.Load()
	if err != nil || !ok || got != cp {
		t.Fatalf("checkpoint round trip failed: %+v ok=%v", got, ok)
	}
}
