package worker

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestPoolRunsAllJobs(t *testing.T) {
	ctx := context.Background()
	p := New(ctx, 2, 4)
	var done atomic.Int32
	for i := 0; i < 10; i++ {
		id := i
		if !p.Submit(Job{ID: id, Label: "job", Run: func(context.Context) error {
			done.Add(1)
			return nil
		}}) {
			t.Fatalf("job %d rejected before cancel", id)
		}
	}
	results, stats := p.Close()
	if done.Load() != 10 || len(results) != 10 {
		t.Fatalf("done=%d results=%d", done.Load(), len(results))
	}
	if stats.Completed != 10 || stats.NotReceived != 0 || stats.Canceled {
		t.Fatalf("stats wrong: %+v", stats)
	}
	if stats.MaxQueue > 4 {
		t.Fatalf("queue depth %d exceeded capacity 4", stats.MaxQueue)
	}
}

func TestPoolBoundedQueueDepth(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	release := make(chan struct{})
	started := make(chan struct{}, 8)
	p := New(ctx, 2, 2)
	producerExited := make(chan struct{})
	// Submit from a goroutine: 2 jobs occupy workers, 2 fill the queue and
	// further submissions block on backpressure instead of growing memory.
	go func() {
		defer close(producerExited)
		for i := 0; i < 8; i++ {
			if !p.Submit(Job{ID: i, Run: func(context.Context) error {
				select {
				case started <- struct{}{}:
				default:
				}
				<-release
				return nil
			}}) {
				return
			}
		}
	}()
	<-started
	<-started
	// Both workers are blocked; the bounded queue must not exceed depth 2.
	time.Sleep(30 * time.Millisecond)
	if depth := p.QueueDepth(); depth > 2 {
		t.Fatalf("queue grew beyond capacity: %d", depth)
	}
	// Cancellation unblocks the producer; wait for it to stop submitting
	// before Close closes the jobs channel.
	cancel()
	<-producerExited
	close(release)
	_, stats := p.Close()
	if stats.MaxQueue > 2 {
		t.Fatalf("max queue depth %d > 2", stats.MaxQueue)
	}
}

func TestPoolCancelStopsAccepting(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	release := make(chan struct{})
	started := make(chan struct{}, 8)
	p := New(ctx, 2, 2)
	producerExited := make(chan struct{})
	go func() {
		defer close(producerExited)
		for i := 0; i < 4; i++ {
			if !p.Submit(Job{ID: i, Run: func(context.Context) error {
				select {
				case started <- struct{}{}:
				default:
				}
				<-release
				return nil
			}}) {
				return
			}
		}
	}()
	<-started
	<-started
	cancel()
	<-producerExited
	// After cancellation every further submission is rejected immediately.
	if p.Submit(Job{ID: 99, Run: func(context.Context) error { return nil }}) {
		t.Fatal("job accepted after cancellation")
	}
	close(release)
	results, stats := p.Close()
	if !stats.Canceled {
		t.Fatal("stats must report cancellation")
	}
	if stats.Rejected < 1 {
		t.Fatalf("post-cancel submission not counted: %+v", stats)
	}
	if stats.NotReceived != stats.Submitted-stats.Completed {
		t.Fatalf("not received accounting wrong: %+v", stats)
	}
	if len(results) != stats.Completed {
		t.Fatal("returned results must equal completed count")
	}
}

func TestPoolJobErrorIsReturned(t *testing.T) {
	p := New(context.Background(), 1, 1)
	myErr := errors.New("boom")
	p.Submit(Job{ID: 1, Run: func(context.Context) error { return myErr }})
	results, _ := p.Close()
	if len(results) != 1 || !errors.Is(results[0].Err, myErr) {
		t.Fatalf("job error not propagated: %+v", results)
	}
}

func TestPoolCancelAfterN(t *testing.T) {
	// Mirrors the --cancel-after acceptance path: cancel once 5 jobs finish.
	ctx, cancel := context.WithCancel(context.Background())
	var finished atomic.Int32
	p := New(ctx, 2, 2)
	producerDone := make(chan struct{})
	go func() {
		defer close(producerDone)
		for i := 0; i < 20; i++ {
			id := i
			ok := p.Submit(Job{ID: id, Run: func(context.Context) error {
				time.Sleep(2 * time.Millisecond)
				if finished.Add(1) == 5 {
					cancel()
				}
				return nil
			}})
			if !ok {
				return // producer stops as soon as the pool rejects
			}
		}
	}()
	<-producerDone
	_, stats := p.Close()
	if !stats.Canceled || stats.Completed < 5 {
		t.Fatalf("cancel-after semantics wrong: %+v", stats)
	}
	if stats.MaxQueue > 2 {
		t.Fatalf("queue depth exceeded: %d", stats.MaxQueue)
	}
	if stats.Completed+stats.NotReceived != stats.Submitted {
		t.Fatalf("every submitted job must be accounted for: %+v", stats)
	}
}
