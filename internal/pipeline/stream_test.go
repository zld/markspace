package pipeline

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestBackpressureReachesProducer(t *testing.T) {
	r := NewRunner[int](3)
	var produced int64
	blocker := make(chan struct{})

	err := r.Run(context.Background(),
		func(ctx context.Context) error {
			// The fourth Put cannot complete: three slots fill and the
			// consumer has not started reading yet.
			for i := 0; i < 4; i++ {
				if err := r.Queue().Put(ctx, i); err != nil {
					return err
				}
				atomic.StoreInt64(&produced, int64(i+1))
			}
			return nil
		},
		func(ctx context.Context, in *Queue[int]) error {
			// Hold the consumer until the producer is provably blocked.
			time.Sleep(50 * time.Millisecond)
			if got := atomic.LoadInt64(&produced); got != 3 {
				t.Fatalf("producer advanced past full queue: produced=%d", got)
			}
			if in.Len() != 3 {
				t.Fatalf("queue fill = %d, want capacity 3", in.Len())
			}
			close(blocker)
			n := 0
			for {
				item, ok, err := in.Get(ctx)
				if err != nil {
					return err
				}
				if !ok {
					break
				}
				_ = item
				n++
			}
			if n != 4 {
				t.Fatalf("consumed %d items, want 4", n)
			}
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-blocker:
	default:
		t.Fatal("consumer checkpoint never reached")
	}
	if r.Peak() != 3 {
		t.Fatalf("peak = %d, want 3 (must never exceed capacity)", r.Peak())
	}
}

func TestCancellationStopsProducer(t *testing.T) {
	r := NewRunner[int](2)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	go func() {
		done <- r.Run(ctx,
			func(ctx context.Context) error {
				i := 0
				for {
					if err := r.Queue().Put(ctx, i); err != nil {
						return err
					}
					i++
				}
			},
			func(ctx context.Context, in *Queue[int]) error {
				cancel() // cancel before reading anything
				_, _, err := in.Get(ctx)
				return err
			},
		)
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancellation did not unwind the pipeline")
	}
}

func TestDownstreamErrorStopsProduction(t *testing.T) {
	r := NewRunner[int](2)
	stopErr := errors.New("render disk full")
	var produced int64

	err := r.Run(context.Background(),
		func(ctx context.Context) error {
			for i := 0; ; i++ {
				if err := r.Queue().Put(ctx, i); err != nil {
					return err // must be unblocked once the stage fails
				}
				atomic.StoreInt64(&produced, int64(i+1))
			}
		},
		func(ctx context.Context, in *Queue[int]) error {
			if _, _, err := in.Get(ctx); err != nil {
				return err
			}
			return stopErr
		},
	)
	if !errors.Is(err, stopErr) {
		t.Fatalf("err = %v, want downstream failure", err)
	}
	// Producer must have terminated; give the scheduler a moment to settle.
	time.Sleep(20 * time.Millisecond)
	if got := atomic.LoadInt64(&produced); got > 10 {
		t.Fatalf("producer kept running after stage failure: %d", got)
	}
}

func TestQueueCloseDrainsBufferedItems(t *testing.T) {
	q := NewQueue[string](2)
	if err := q.Put(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}
	if err := q.Put(context.Background(), "b"); err != nil {
		t.Fatal(err)
	}
	q.Close()
	var got []string
	for {
		item, ok, err := q.Get(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			break
		}
		got = append(got, item)
	}
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("drained = %v", got)
	}
}
