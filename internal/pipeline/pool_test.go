package pipeline

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPoolBoundedConcurrency(t *testing.T) {
	var active, peak int64
	var mu sync.Mutex
	pool := Pool{Workers: 4, Queue: 8}
	i := int64(0)
	res := pool.Run(context.Background(), func(ctx context.Context) (Job, bool, error) {
		if i >= 200 {
			return Job{}, false, nil
		}
		j := Job{Index: i}
		i++
		return j, true, nil
	}, func(ctx context.Context, job Job) error {
		cur := atomic.AddInt64(&active, 1)
		mu.Lock()
		if cur > peak {
			peak = cur
		}
		mu.Unlock()
		time.Sleep(time.Millisecond)
		atomic.AddInt64(&active, -1)
		return nil
	})
	if res.Err != nil {
		t.Fatal(res.Err)
	}
	if res.Processed != 200 {
		t.Fatalf("processed = %d", res.Processed)
	}
	if peak > 4 || peak < 2 {
		t.Fatalf("peak concurrency = %d", peak)
	}
}

func TestPoolPropagatesCancelToProducer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	pool := Pool{Workers: 2, Queue: 1}
	var produced int64
	res := pool.Run(ctx, func(ctx context.Context) (Job, bool, error) {
		n := atomic.AddInt64(&produced, 1)
		if n == 5 {
			cancel()
		}
		return Job{Index: n}, true, nil
	}, func(ctx context.Context, job Job) error {
		return nil
	})
	if res.Err == nil {
		t.Fatal("cancellation did not surface")
	}
	if !errors.Is(res.Err, context.Canceled) && res.Err.Error() == "" {
		t.Fatalf("err = %v", res.Err)
	}
	if produced > 200 {
		t.Fatalf("producer kept running after cancel: %d", produced)
	}
}

func TestPoolStopsOnHandlerError(t *testing.T) {
	pool := Pool{Workers: 3, Queue: 4}
	boom := errors.New("boom")
	i := int64(0)
	res := pool.Run(context.Background(), func(ctx context.Context) (Job, bool, error) {
		if i >= 1000 {
			return Job{}, false, nil
		}
		j := Job{Index: i}
		i++
		return j, true, nil
	}, func(ctx context.Context, job Job) error {
		if job.Index == 10 {
			return boom
		}
		return nil
	})
	if !errors.Is(res.Err, boom) {
		t.Fatalf("err = %v", res.Err)
	}
}

func TestPoolContextAlreadyCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res := Pool{Workers: 2, Queue: 2}.Run(ctx, func(ctx context.Context) (Job, bool, error) {
		t.Fatal("producer ran under canceled context")
		return Job{}, false, nil
	}, func(ctx context.Context, job Job) error { return nil })
	if !errors.Is(res.Err, context.Canceled) {
		t.Fatalf("err = %v", res.Err)
	}
}
