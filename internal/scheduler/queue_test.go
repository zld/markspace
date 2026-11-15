package scheduler

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"transcode/internal/model"
)

func mkSegments(n int, statuses ...model.Status) []*model.Segment {
	segs := make([]*model.Segment, n)
	for i := range segs {
		st := model.StatusQueued
		if i < len(statuses) {
			st = statuses[i]
		}
		segs[i] = &model.Segment{Index: i, Status: st, Feasible: true,
			DurationSec: 1, TargetKbps: 100}
	}
	return segs
}

func TestQueueSkipsSucceededAndRunsOthers(t *testing.T) {
	var ran int32
	var progress int32
	task := &model.Task{Segments: mkSegments(6,
		model.StatusSucceeded, model.StatusQueued, model.StatusInterrupted,
		model.StatusFailed, model.StatusSucceeded, model.StatusQueued)}
	q, err := New(2, func(_ context.Context, j Job) error {
		atomic.AddInt32(&ran, 1)
		if j.Segment.Status != model.StatusRunning {
			t.Errorf("segment %d not running when worker invoked", j.Segment.Index)
		}
		return nil
	}, func(*model.Segment) { atomic.AddInt32(&progress, 1) })
	if err != nil {
		t.Fatal(err)
	}
	ok, failed, interrupted := q.Run(context.Background(), task)
	// Four eligible segments run; both pre-existing successes are retained,
	// so six segments end succeeded in total.
	if ran != 4 || ok != 6 || failed != 0 || interrupted != 0 {
		t.Fatalf("ran=%d ok=%d failed=%d interrupted=%d", ran, ok, failed, interrupted)
	}
	if progress != 4 {
		t.Fatalf("progress callbacks = %d, want 4", progress)
	}
	// Pre-existing succeeded segments stay untouched.
	if task.Segments[0].Status != model.StatusSucceeded ||
		task.Segments[4].Status != model.StatusSucceeded {
		t.Fatal("succeeded segments must never be resubmitted")
	}
}

func TestQueueConcurrencyLimit(t *testing.T) {
	var inFlight, maxInFlight int32
	task := &model.Task{Segments: mkSegments(10)}
	q, _ := New(3, func(_ context.Context, _ Job) error {
		cur := atomic.AddInt32(&inFlight, 1)
		for {
			old := atomic.LoadInt32(&maxInFlight)
			if cur <= old || atomic.CompareAndSwapInt32(&maxInFlight, old, cur) {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
		atomic.AddInt32(&inFlight, -1)
		return nil
	}, nil)
	ok, _, _ := q.Run(context.Background(), task)
	if ok != 10 || maxInFlight > 3 {
		t.Fatalf("ok=%d maxInFlight=%d", ok, maxInFlight)
	}
}

func TestQueueWorkerFailureIsRecorded(t *testing.T) {
	task := &model.Task{Segments: mkSegments(4)}
	q, _ := New(2, func(_ context.Context, j Job) error {
		if j.Segment.Index == 1 || j.Segment.Index == 3 {
			return context.DeadlineExceeded
		}
		return nil
	}, nil)
	ok, failed, _ := q.Run(context.Background(), task)
	if ok != 2 || failed != 2 {
		t.Fatalf("ok=%d failed=%d", ok, failed)
	}
	for _, i := range []int{1, 3} {
		if task.Segments[i].Status != model.StatusFailed ||
			task.Segments[i].FailureNote == "" {
			t.Fatalf("segment %d failure not recorded: %+v", i, task.Segments[i])
		}
	}
}

func TestQueueCancellationMarksInterrupted(t *testing.T) {
	task := &model.Task{Segments: mkSegments(8)}
	ctx, cancel := context.WithCancel(context.Background())
	var started int32
	release := make(chan struct{})
	q, _ := New(2, func(_ context.Context, _ Job) error {
		if atomic.AddInt32(&started, 1) == 2 {
			cancel()
		}
		<-release
		return nil
	}, nil)
	go func() {
		time.Sleep(30 * time.Millisecond)
		close(release)
	}()
	ok, failed, interrupted := q.Run(ctx, task)
	if interrupted == 0 {
		t.Fatalf("expected interrupted segments, got ok=%d failed=%d interrupted=%d", ok, failed, interrupted)
	}
	// Every terminal segment must be one of the legal outcomes; no queued
	// or running segment may be left masquerading as finished work.
	for _, seg := range task.Segments {
		switch seg.Status {
		case model.StatusSucceeded, model.StatusFailed, model.StatusInterrupted:
		default:
			t.Fatalf("segment %d left in non-terminal state %s", seg.Index, seg.Status)
		}
		if seg.Status == model.StatusSucceeded && seg.Encode == nil {
			t.Fatalf("segment %d succeeded without an encode result", seg.Index)
		}
	}
}

func TestQueueRejectsBadConfig(t *testing.T) {
	if _, err := New(0, func(context.Context, Job) error { return nil }, nil); err == nil {
		t.Fatal("zero workers must be rejected")
	}
	if _, err := New(2, nil, nil); err == nil {
		t.Fatal("nil worker must be rejected")
	}
}

func TestQueueDeterministicClaimOrder(t *testing.T) {
	var mu sync.Mutex
	var order []int
	task := &model.Task{Segments: mkSegments(5)}
	q, _ := New(1, func(_ context.Context, j Job) error {
		mu.Lock()
		order = append(order, j.Segment.Index)
		mu.Unlock()
		return nil
	}, nil)
	if ok, _, _ := q.Run(context.Background(), task); ok != 5 {
		t.Fatal("all should succeed")
	}
	want := []int{0, 1, 2, 3, 4}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("claim order = %v, want %v", order, want)
		}
	}
}
