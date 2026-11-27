package pipeline

import (
	"context"
	"errors"
	"testing"
	"time"

	"example.com/continuity/internal/fault"
	"example.com/continuity/internal/frame"
)

func recAt(seq uint64, payloadSize int) *frame.Record {
	return &frame.Record{
		Version: frame.Version1, Track: "v", Seq: seq,
		CaptureNS: int64(seq) * 40_000_000, Width: 2, Height: 2,
		Payload: make([]byte, payloadSize), Offset: int64(seq) * 10,
	}
}

func TestQueueBackpressureAndCancel(t *testing.T) {
	q := NewQueue(1)
	ctx := context.Background()
	if err := q.Put(ctx, recAt(0, 1)); err != nil {
		t.Fatal(err)
	}
	if q.Len() != 1 || q.Cap() != 1 {
		t.Fatalf("len=%d cap=%d", q.Len(), q.Cap())
	}
	// Second Put must block because the queue is full.
	blocked := make(chan struct{})
	go func() {
		_ = q.Put(ctx, recAt(1, 1))
		close(blocked)
	}()
	select {
	case <-blocked:
		t.Fatal("Put did not block on a full queue")
	case <-time.After(30 * time.Millisecond):
	}
	// Canceling a blocked Put must unblock it with a pipeline fault.
	cancelCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- q.Put(cancelCtx, recAt(2, 1)) }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled Put returned nil")
		}
	case <-time.After(time.Second):
		t.Fatal("cancel did not unblock Put")
	}
	// Drain the single buffered record; the earlier blocking goroutine then
	// completes and closes its channel.
	if _, err := q.Get(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-blocked:
	case <-time.After(time.Second):
		t.Fatal("drain did not unblock waiting producer")
	}
}

func TestQueueCloseAndGetCancel(t *testing.T) {
	q := NewQueue(2)
	q.Close()
	if _, err := q.Get(context.Background()); !IsClosed(err) {
		t.Fatalf("want closed, got %v", err)
	}
	q2 := NewQueue(1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := q2.Get(ctx); err == nil {
		t.Fatal("canceled Get returned nil")
	}
}

func TestCountersEnforceBudgets(t *testing.T) {
	c := &Counters{MaxFrames: 2, MaxTotalBytes: 100}
	if err := c.Add(recAt(0, 40)); err != nil {
		t.Fatal(err)
	}
	if err := c.Add(recAt(1, 40)); err != nil {
		t.Fatal(err)
	}
	err := c.Add(recAt(2, 1))
	if !errors.Is(err, fault.ErrLimitExceeded) {
		t.Fatalf("frame limit: %v", err)
	}
	c2 := &Counters{MaxTotalBytes: 50}
	if err := c2.Add(recAt(0, 30)); err != nil {
		t.Fatal(err)
	}
	err = c2.Add(recAt(1, 30))
	if !errors.Is(err, fault.ErrLimitExceeded) {
		t.Fatalf("byte limit: %v", err)
	}
}

func TestRunEndToEnd(t *testing.T) {
	const n = 20
	feed := func(ctx context.Context, in *Queue) error {
		for i := 0; i < n; i++ {
			if err := in.Put(ctx, recAt(uint64(i), 4)); err != nil {
				return err
			}
		}
		return nil
	}
	// Stage that passes frames through, and a sink that counts them.
	pass := func(ctx context.Context, in, out *Queue) error {
		for {
			rec, err := in.Get(ctx)
			if IsClosed(err) {
				return nil
			}
			if err != nil {
				return err
			}
			if err := out.Put(ctx, rec); err != nil {
				return err
			}
		}
	}
	sink := func(ctx context.Context, in, out *Queue) error {
		count := 0
		for {
			_, err := in.Get(ctx)
			if IsClosed(err) {
				if count != n {
					return errors.New("sink lost frames")
				}
				return nil
			}
			if err != nil {
				return err
			}
			count++
		}
	}
	if err := Run(context.Background(), 3, feed, pass, sink); err != nil {
		t.Fatalf("run: %v", err)
	}
}

func TestRunPropagatesCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	feed := func(ctx context.Context, in *Queue) error {
		var i uint64
		for {
			if err := in.Put(ctx, recAt(i, 1024)); err != nil {
				return err
			}
			i++
		}
	}
	blockingSink := func(ctx context.Context, in, out *Queue) error {
		<-ctx.Done()
		return ctx.Err()
	}
	cancel()
	err := Run(ctx, 1, feed, blockingSink)
	if err == nil {
		t.Fatal("canceled run returned nil")
	}
}
