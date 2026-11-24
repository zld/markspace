package stream

import (
	"context"
	"testing"
	"time"

	"mediad/internal/model"
)

func TestHardLimitsEnforced(t *testing.T) {
	s, err := New(Config{MaxBytes: 100, MaxItems: 2})
	if err != nil {
		t.Fatal(err)
	}
	// Item cap reached first: two items totaling 60 bytes.
	if err := s.TryPut(Item{Size: 30}); err != nil {
		t.Fatal(err)
	}
	if err := s.TryPut(Item{Size: 30}); err != nil {
		t.Fatal(err)
	}
	if err := s.TryPut(Item{Size: 10}); !model.HasCode(err, model.ErrBufferFull) {
		t.Fatalf("third item must hit item cap, got %v", err)
	}
	// Drain one, then the byte cap blocks an oversized single item.
	if _, ok, err := s.Get(context.Background()); !ok || err != nil {
		t.Fatalf("get failed: ok=%v err=%v", ok, err)
	}
	if err := s.TryPut(Item{Size: 200}); !model.HasCode(err, model.ErrBufferFull) {
		t.Fatalf("oversized item must be rejected, got %v", err)
	}
	st := s.Snapshot()
	if st.PeakItems > 2 || st.PeakBytes > 100 {
		t.Fatalf("limits breached: %+v", st)
	}
	if st.Rejected != 2 {
		t.Fatalf("rejected = %d, want 2", st.Rejected)
	}
}

func TestSlowConsumerBackPressure(t *testing.T) {
	s, _ := New(Config{MaxBytes: 64, MaxItems: 4})
	ctx := context.Background()

	// Fill to the cap without blocking.
	for i := 0; i < 4; i++ {
		if err := s.Put(ctx, Item{Size: 16}); err != nil {
			t.Fatal(err)
		}
	}

	done := make(chan error, 1)
	go func() {
		// This Put must block until the consumer makes room.
		done <- s.Put(ctx, Item{Size: 16})
	}()

	select {
	case <-done:
		t.Fatal("producer must block under a full buffer")
	case <-time.After(20 * time.Millisecond):
	}

	if _, _, err := s.Get(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("blocked producer should resume: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("producer never resumed after drain")
	}

	st := s.Snapshot()
	if st.PeakBytes > 64 || st.BlockedNS <= 0 || st.Enqueued != 5 {
		t.Fatalf("back-pressure stats wrong: %+v", st)
	}
}

func TestCancellationReleasesProducer(t *testing.T) {
	s, _ := New(Config{MaxBytes: 10, MaxItems: 1})
	_ = s.TryPut(Item{Size: 10})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Put(ctx, Item{Size: 1}) }()

	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !model.HasCode(err, model.ErrContextCanceled) {
			t.Fatalf("want CONTEXT_CANCELED, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancel did not release producer")
	}
}

func TestConsumerCloseReleasesProducer(t *testing.T) {
	s, _ := New(Config{MaxBytes: 10, MaxItems: 1})
	_ = s.TryPut(Item{Size: 10})

	done := make(chan error, 1)
	go func() { done <- s.Put(context.Background(), Item{Size: 1}) }()
	time.Sleep(20 * time.Millisecond)
	s.Close()

	select {
	case err := <-done:
		if !model.HasCode(err, model.ErrStreamClosed) {
			t.Fatalf("want STREAM_CLOSED, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("close did not release producer")
	}

	// Consumer drains remaining then sees clean end-of-stream.
	item, ok, err := s.Get(context.Background())
	if err != nil || !ok || item.Size != 10 {
		t.Fatalf("drain after close wrong: item=%+v ok=%v err=%v", item, ok, err)
	}
	if _, ok, err := s.Get(context.Background()); ok || err != nil {
		t.Fatalf("closed+drained must return (false,nil), got ok=%v err=%v", ok, err)
	}
}

func TestInvalidConfig(t *testing.T) {
	if _, err := New(Config{MaxBytes: 0, MaxItems: 1}); !model.HasCode(err, model.ErrBufferFull) {
		t.Fatalf("zero byte cap must error, got %v", err)
	}
	if _, err := New(Config{MaxBytes: 1, MaxItems: 0}); !model.HasCode(err, model.ErrBufferFull) {
		t.Fatalf("zero item cap must error, got %v", err)
	}
}

func TestNonPositiveSizeRejected(t *testing.T) {
	for _, size := range []int{0, -1, -100} {
		s, _ := New(Config{MaxBytes: 100, MaxItems: 8})
		if err := s.TryPut(Item{Size: size}); !model.HasCode(err, model.ErrBufferFull) {
			t.Fatalf("TryPut size=%d must be rejected, got %v", size, err)
		}
		if err := s.Put(context.Background(), Item{Size: size}); !model.HasCode(err, model.ErrBufferFull) {
			t.Fatalf("Put size=%d must be rejected, got %v", size, err)
		}
		st := s.Snapshot()
		if st.Enqueued != 0 || st.PeakBytes != 0 {
			t.Fatalf("rejected item must not affect accounting: %+v", st)
		}
	}

	// A negative item must not let subsequent positive items bypass the cap.
	s, _ := New(Config{MaxBytes: 10, MaxItems: 8})
	if err := s.TryPut(Item{Size: -100}); err == nil {
		t.Fatal("negative size accepted")
	}
	if err := s.TryPut(Item{Size: 10}); err != nil {
		t.Fatalf("first valid item should fit: %v", err)
	}
	if err := s.TryPut(Item{Size: 1}); !model.HasCode(err, model.ErrBufferFull) {
		t.Fatalf("byte cap must still hold after the rejected negative item: %v", err)
	}
}
