package pipeline

import (
	"context"
	"crypto/sha256"
	"sync/atomic"
	"testing"
	"time"

	"rtpregress/internal/audio"
	"rtpregress/internal/clock"
)

// fakeProcessor returns a deterministic result derived only from the segment,
// and can sleep to expose scheduling nondeterminism across worker counts.
type fakeProcessor struct {
	delay time.Duration
	calls int64
}

func (f *fakeProcessor) Process(_ context.Context, seg audio.Segment) (*audio.Result, error) {
	atomic.AddInt64(&f.calls, 1)
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	// PCM content is a pure function of the segment id
	pcm := []byte{byte(seg.ID), byte(seg.ID >> 8), 0xAA}
	return &audio.Result{
		SegmentID:  seg.ID,
		StartNanos: seg.StartNanos,
		Samples:    3,
		PCM:        pcm,
		PCMSHA256:  sha256.Sum256(pcm),
	}, nil
}

func TestPoolBoundsRejectBadConfig(t *testing.T) {
	fp := &fakeProcessor{}
	if _, err := NewPool(0, 4, fp); err == nil {
		t.Fatal("zero workers must error")
	}
	if _, err := NewPool(MaxWorkers+1, 4, fp); err == nil {
		t.Fatal("over-limit workers must error")
	}
	if _, err := NewPool(2, MaxQueueJobs+1, fp); err == nil {
		t.Fatal("over-limit queue must error")
	}
}

func TestPoolProcessesAllJobs(t *testing.T) {
	fp := &fakeProcessor{delay: time.Millisecond}
	pool, err := NewPool(4, 8, fp)
	if err != nil {
		t.Fatal(err)
	}
	jobs := make(chan Job, 10)
	var got int64
	for i := 0; i < 10; i++ {
		jobs <- Job{
			Segment:   audio.Segment{ID: i, StartNanos: int64(i) * 1000},
			PID:       0x100,
			SegNumber: i,
		}
	}
	close(jobs)
	err = pool.Run(context.Background(), jobs, func(PoolResult) { atomic.AddInt64(&got, 1) })
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got != 10 || fp.calls != 10 {
		t.Fatalf("processed got=%d calls=%d", got, fp.calls)
	}
}

func TestPoolCancellationPublishesNothingHalfDone(t *testing.T) {
	// processor blocks until canceled, guaranteeing no result can complete
	block := make(chan struct{})
	fp := blockingProcessor{release: block}
	pool, _ := NewPool(2, 4, fp)
	ctx, cancel := context.WithCancel(context.Background())
	jobs := make(chan Job, 4)
	for i := 0; i < 4; i++ {
		jobs <- Job{Segment: audio.Segment{ID: i}, PID: 1, SegNumber: i}
	}
	var published int64
	done := make(chan error, 1)
	go func() {
		done <- pool.Run(ctx, jobs, func(PoolResult) { atomic.AddInt64(&published, 1) })
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	close(block)
	err := <-done
	if e := clock.AsError(err); e == nil || e.Code != clock.CodeCanceled {
		t.Fatalf("want CodeCanceled, got %v", err)
	}
	if atomic.LoadInt64(&published) != 0 {
		t.Fatal("canceled run must not publish half-finished results")
	}
}

type blockingProcessor struct{ release chan struct{} }

func (b blockingProcessor) Process(ctx context.Context, seg audio.Segment) (*audio.Result, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-b.release:
		return &audio.Result{SegmentID: seg.ID}, nil
	}
}
